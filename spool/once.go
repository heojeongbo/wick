package spool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/naming"
	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/trigger"
)

// Once is one pass: scan, carry what is due, retire what is carried.
//
// It does not consult the trigger. The trigger is about *when* a pass happens
// and [Spool.Run] is what asks it; a caller who has called this has already
// decided. That is what makes `wick once` mean what it says on a machine where
// somebody has walked up to it.
func (s *Spool) Once(ctx context.Context) (Report, error) {
	var r Report

	items, err := s.scan(ctx, &r)
	if err != nil {
		return r, err
	}

	// Anything already carried but not yet retired is picked up first, whether
	// or not it is still in the source. A restart in the window between the
	// record and the retirement is otherwise a file that is never tidied.
	if err := s.retireOutstanding(ctx, &r); err != nil {
		return r, err
	}

	due := s.due(ctx, items, &r)
	r.Settled = len(due)
	if len(due) == 0 {
		return r, nil
	}

	s.carryAll(ctx, due, &r)

	if r.Carried > 0 {
		s.mu.Lock()
		s.last = s.now()
		s.mu.Unlock()
	}

	return r, ctx.Err()
}

// scan is everything the source holds, with the failures collected rather than
// returned: one entry that cannot be read must not hide the rest.
//
// An error yielded against a zero item is about the scan itself and ends it.
func (s *Spool) scan(ctx context.Context, r *Report) ([]source.Item, error) {
	var items []source.Item
	for it, err := range s.src.Scan(ctx) {
		if err != nil {
			if it.Key == "" {
				return nil, z.Err(err, "scan %q", s.name)
			}

			r.Errs = append(r.Errs, z.Err(err, "look at %q", it.Key))

			continue
		}

		items = append(items, it)
		r.Scanned++
	}

	return items, ctx.Err()
}

// due is what has sat still long enough and has not already been carried.
func (s *Spool) due(ctx context.Context, items []source.Item, r *Report) []source.Item {
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Anything that is no longer there is forgotten, so that the memory of a
	// long-running daemon is the size of the directory rather than the size of
	// everything that has ever been in it.
	live := make(map[string]bool, len(items))
	for _, it := range items {
		live[it.Key] = true
	}
	for k := range s.seen {
		if !live[k] {
			delete(s.seen, k)
		}
	}

	var due []source.Item
	for _, it := range items {
		was, known := s.seen[it.Key]
		if !known || was.size != it.Size || !was.modAt.Equal(it.ModAt) {
			// It is new, or it has changed since the last look. Either way
			// what was watched about it starts again from now.
			was = sighting{size: it.Size, modAt: it.ModAt, at: now}
			known = false
			s.seen[it.Key] = was
		}

		if !s.settled(it, was, known, now) {
			continue
		}

		rec, ok, err := s.jnl.Get(ctx, s.name, it.Key)
		if err != nil {
			r.Errs = append(r.Errs, z.Err(err, "read what is known about %q", it.Key))

			continue
		}
		if ok {
			switch {
			case rec.State == journal.Quarantined:
				// Set aside. It is not tried again until somebody says so, or
				// it changes into a different file.
				if rec.SameAs(it.Size, it.ModAt) {
					continue
				}

			case rec.State != journal.Pending && rec.SameAs(it.Size, it.ModAt):
				// Already carried. Retiring it, if that is still outstanding,
				// is [Spool.retireOutstanding]'s business.
				continue
			}
		}

		due = append(due, it)
	}

	return due
}

// settled says whether something has stopped changing.
//
// # Why the file's own time comes first
//
// Because it is the only one that survives a restart. `wick once` is a fresh
// process every time something runs it, and a gate that only knew what this
// process had watched would say "not yet" on every single run -- a one-shot
// that can never carry anything, which is the shape most schedulers use.
//
// # Why what was watched is still kept
//
// For the machine that boots without a network and has its clock put right
// hours later. Until that happens its idea of now is behind every file it
// holds, and the file's own time would say "written in the future" forever.
// What this process has watched happen is still true then, so that is what is
// used -- and it is only reachable in that case, so a clock that is right
// costs nothing for it.
func (s *Spool) settled(it source.Item, was sighting, known bool, now time.Time) bool {
	if s.settle <= 0 {
		return true
	}

	age := now.Sub(it.ModAt)
	switch {
	case age >= s.settle:
		return true

	case age >= 0:
		return false
	}

	return known && now.Sub(was.at) >= s.settle
}

// carryAll runs the carries, at most [Config.Workers] at a time.
func (s *Spool) carryAll(ctx context.Context, due []source.Item, r *Report) {
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		work = make(chan source.Item)
	)

	add := func(f func(*Report)) {
		mu.Lock()
		defer mu.Unlock()

		f(r)
	}

	for range min(s.workers, len(due)) {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for it := range work {
				s.carry(ctx, it, add)
			}
		}()
	}

	for _, it := range due {
		select {
		case <-ctx.Done():
			// Stop handing out work. What is in flight is left to finish or to
			// notice the context itself.
			close(work)
			wg.Wait()

			return

		case work <- it:
		}
	}

	close(work)
	wg.Wait()
}

// carry takes one item all the way: read it, write it everywhere, read each
// copy back, record it, retire it.
func (s *Spool) carry(ctx context.Context, it source.Item, add func(func(*Report))) {
	l := log.From(ctx)

	rec, _, err := s.jnl.Get(ctx, s.name, it.Key)
	if err != nil {
		add(func(r *Report) { r.Errs = append(r.Errs, z.Err(err, "read what is known about %q", it.Key)) })

		return
	}

	// A file that has been written again under the same name is a different
	// file, and what was remembered about the old one says nothing true about
	// it.
	if !rec.SameAs(it.Size, it.ModAt) {
		rec = journal.Record{Source: s.name, Key: it.Key}
	}
	rec.Source, rec.Key, rec.Size, rec.ModAt = s.name, it.Key, it.Size, it.ModAt
	rec.State = journal.Pending
	if rec.Carried == nil {
		rec.Carried = map[string]journal.Carry{}
	}

	sent, err := s.toEvery(ctx, it, &rec)
	if err != nil {
		if errors.Is(err, errGone) {
			// It went away while we were looking at it. That is not a failure
			// to count against it; there is simply nothing to carry.
			l.Info("it went away before it could be carried", slog.String("key", it.Key))
			s.forget(ctx, it.Key, add)

			return
		}

		rec.Attempts++
		rec.Err = err.Error()
		if rec.Attempts >= s.tries {
			// Set aside rather than tried forever. One item that cannot be
			// carried must not spend the whole link on itself while everything
			// behind it waits.
			rec.State = journal.Quarantined
			l.Warn("set aside after too many attempts",
				slog.String("key", it.Key),
				slog.Int("attempts", rec.Attempts),
				slog.String("error", err.Error()),
			)
		}

		if perr := s.jnl.Put(ctx, rec); perr != nil {
			err = errors.Join(err, z.Err(perr, "record the attempt at %q", it.Key))
		}

		add(func(r *Report) {
			r.Bytes += sent
			r.Errs = append(r.Errs, z.Err(err, "carry %q", it.Key))
			if rec.State == journal.Quarantined {
				r.SetAside++
			}
		})

		return
	}

	// Every destination has answered and every one of them has been asked
	// again. Only now is it written down.
	rec.State = journal.Carried
	rec.Attempts = 0
	rec.Err = ""
	if err := s.jnl.Put(ctx, rec); err != nil {
		// The copies are there and nothing says so. The next pass will send
		// them again, which costs the bytes and loses nothing.
		add(func(r *Report) {
			r.Bytes += sent
			r.Errs = append(r.Errs, z.Err(err, "record the carry of %q", it.Key))
		})

		return
	}

	add(func(r *Report) {
		r.Carried++
		r.Bytes += sent
	})

	if s.retire(ctx, rec, add) {
		add(func(r *Report) { r.Retired++ })
	}
}

// errGone is what the source saying "that is not here any more" is turned into.
//
// It has to be told from a sink saying "I do not hold that", and both of them
// are [fs.ErrNotExist]. They mean opposite things: the first is nothing to
// carry and the record should be forgotten; the second is a write that did not
// stick and the file must on no account be let go of. Reading one as the other
// is how a store that swallows writes would look exactly like a tidy-up.
var errGone = errors.New("it is no longer there")

// open reads from the source, and marks the one failure that means the item is
// not there to be carried.
func (s *Spool) open(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := s.src.Open(ctx, key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %w", errGone, err)
	}

	return rc, err
}

// toEvery writes the item to every destination that has not already confirmed
// it, and says how many bytes that took.
//
// The destinations are done one after another rather than at once, on purpose.
// They usually share one link, and sending the same half-gigabyte to two of
// them in parallel is the same bytes twice over a pipe that has room for it
// once.
func (s *Spool) toEvery(ctx context.Context, it source.Item, rec *journal.Record) (int64, error) {
	var sent int64

	for _, d := range s.dests {
		if _, ok := rec.Carried[d.Name]; ok {
			// This one answered on an earlier attempt. Sending again would be
			// the same bytes for the same result, on a link that is the reason
			// any of this exists.
			continue
		}

		n, carry, err := s.toOne(ctx, d, it, rec)
		sent += n
		if err != nil {
			return sent, err
		}

		rec.Carried[d.Name] = carry
		if rec.Digest == "" {
			rec.Digest = carry.Digest
		}
	}

	return sent, nil
}

func (s *Spool) toOne(ctx context.Context, d dest, it source.Item, rec *journal.Record) (int64, journal.Carry, error) {
	digest := rec.Digest

	// A name that is made out of the hash cannot be known until the file has
	// been read all the way through, so it is read twice: once to hash, once
	// to send. That is the cost of asking for it, and it is why
	// [naming.Template.NeedsDigest] exists to make it visible.
	if digest == "" && d.Naming.NeedsDigest() {
		var err error
		if digest, err = s.digestOf(ctx, it.Key); err != nil {
			return 0, journal.Carry{}, err
		}
		rec.Digest = digest
	}

	name, err := d.Naming.Expand(naming.Vars{
		Host:   s.host,
		Source: s.name,
		Key:    it.Key,
		Size:   it.Size,
		ModAt:  it.ModAt,
		Digest: digest,
	})
	if err != nil {
		return 0, journal.Carry{}, z.Err(err, "work out what to call %q at %q", it.Key, d.Name)
	}

	rc, err := s.open(ctx, it.Key)
	if err != nil {
		return 0, journal.Carry{}, err
	}
	defer rc.Close()

	// Hashed on the way past, so that the one read pays for both the sending
	// and the checking.
	h := sha256.New()
	counted := &counter{r: io.TeeReader(rc, h)}

	if err := d.Sink.Put(ctx, name, counted, sink.Meta{Size: it.Size, Digest: digest}); err != nil {
		return counted.n, journal.Carry{}, z.Err(err, "put %q at %q", it.Key, d.Name)
	}

	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if digest != "" && got != digest {
		// What was read is not what was expected: the file changed under us
		// between the hash and the send. Refusing here means the next pass
		// treats it as the new file it is.
		return counted.n, journal.Carry{}, fmt.Errorf("%q changed while it was being carried", it.Key)
	}

	if !d.Verify {
		return counted.n, journal.Carry{At: s.now(), Name: name, Digest: got}, nil
	}

	// The only claim worth recording is the one the store makes when it is
	// asked afterwards. A store that answers a write with a success it does not
	// keep is not a hypothetical.
	if err := s.confirm(ctx, d, name, it.Size, got); err != nil {
		return counted.n, journal.Carry{}, err
	}

	return counted.n, journal.Carry{At: s.now(), Name: name, Digest: got}, nil
}

func (s *Spool) confirm(ctx context.Context, d dest, name string, size int64, digest string) error {
	m, err := d.stat.Stat(ctx, name)
	if err != nil {
		return z.Err(err, "ask %q about %q", d.Name, name)
	}

	switch {
	case m.Size >= 0 && m.Size != size:
		return fmt.Errorf("%q holds %d bytes of %q and %d were sent", d.Name, m.Size, name, size)

	case m.Digest != "" && m.Digest != digest:
		return fmt.Errorf("%q holds something else under %q", d.Name, name)
	}

	return nil
}

// digestOf reads the whole thing to find out what it hashes to.
func (s *Spool) digestOf(ctx context.Context, key string) (string, error) {
	rc, err := s.open(ctx, key)
	if err != nil {
		return "", err
	}
	defer rc.Close()

	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", z.Err(err, "read %q", key)
	}

	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// retire hands a carried record to the retention policy, and says whether the
// local copy was dealt with.
func (s *Spool) retire(ctx context.Context, rec journal.Record, add func(func(*Report))) bool {
	free, err := s.free()
	if err != nil {
		// Nobody could say. The policies read that as "no answer" rather than
		// as "no room", which is what keeps an unmeasurable filesystem from
		// looking like a full one.
		free = 0
	}

	st, err := s.keep.After(ctx, rec, s.src, s.now(), free)
	if err != nil {
		add(func(r *Report) { r.Errs = append(r.Errs, z.Err(err, "retire %q", rec.Key)) })

		return false
	}
	if st == rec.State {
		// The policy is waiting for something. It will be asked again.
		return false
	}

	rec.State = st
	if err := s.jnl.Put(ctx, rec); err != nil {
		add(func(r *Report) { r.Errs = append(r.Errs, z.Err(err, "record the retirement of %q", rec.Key)) })

		return false
	}

	return true
}

// retireOutstanding picks up anything that was carried and not yet retired.
//
// This is what a restart in the window between the record and the retirement
// depends on, and it is also how a [retain.Grace] ever comes due: the file is
// not in the scan's way any more, so nothing else would ever look at it again.
func (s *Spool) retireOutstanding(ctx context.Context, r *Report) error {
	var carried []journal.Record
	for rec, err := range s.jnl.Range(ctx, s.name) {
		if err != nil {
			return z.Err(err, "read what %q has already carried", s.name)
		}
		if rec.State == journal.Carried {
			carried = append(carried, rec)
		}
	}

	add := func(f func(*Report)) { f(r) }
	for _, rec := range carried {
		if s.retire(ctx, rec, add) {
			r.Retired++
		}
	}

	return ctx.Err()
}

// forget drops what is remembered about something that is no longer there.
func (s *Spool) forget(ctx context.Context, key string, add func(func(*Report))) {
	if err := s.jnl.Delete(ctx, s.name, key); err != nil {
		add(func(r *Report) { r.Errs = append(r.Errs, z.Err(err, "forget %q", key)) })
	}

	s.mu.Lock()
	delete(s.seen, key)
	s.mu.Unlock()
}

// state is what the trigger is asked about.
func (s *Spool) state(ctx context.Context) trigger.State {
	st := trigger.State{}

	for it, err := range s.src.Scan(ctx) {
		if err != nil {
			continue
		}

		rec, ok, err := s.jnl.Get(ctx, s.name, it.Key)
		if err != nil {
			continue
		}
		if ok && rec.State != journal.Pending && rec.SameAs(it.Size, it.ModAt) {
			continue
		}

		st.Pending++
		st.Bytes += it.Size
	}

	if free, err := s.free(); err == nil {
		st.FreeBytes = free
	}

	s.mu.Lock()
	last := s.last
	s.mu.Unlock()

	if last.IsZero() {
		// Nothing has been carried yet, so the clock has been running since
		// the process started. Treating it as forever would carry at once on
		// every restart, which on a machine that restarts a lot is a carry
		// that never settles.
		st.Since = 0
	} else {
		st.Since = s.now().Sub(last)
	}

	return st
}

// counter counts what goes past it, so that a report can say how many bytes a
// pass cost without the sink having to be asked.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}
