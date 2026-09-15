package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"

	"github.com/lesomnus/z"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/journal/bolt"
	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/spool"
)

// A Wick is everything the configuration describes, opened and wired together.
//
// It is one value rather than several return values because the things in it
// have to be let go of together: closing the journal while a spool is still
// carrying is the one arrangement that can lose a record.
type Wick struct {
	Journal journal.Journal
	Group   *spool.Group

	// Sinks are the ones that were made, by the name the configuration gave
	// them. A spool holds the ones it carries to; this is here so that
	// something can ask each of them about itself without going through a
	// spool -- which is what `wick check` is.
	Sinks map[string]sink.Sink

	// closers are the sinks that had something to let go of, in the order they
	// were opened.
	closers []sink.Closer
}

// Reach asks every sink that can be asked whether it is there.
//
// Building a sink and reaching one are different things, and most of these are
// built without touching the network on purpose -- a role that cannot be
// assumed should fail a carry and be retried, not stop a daemon from starting.
// The cost of that is a configuration which builds perfectly and works not at
// all: a bucket that is not there, a key that is not allowed, a known_hosts the
// sftp sink only reads when it first dials.
//
// [sink.Stater] is what closes the gap. Asking about a name is the cheapest
// thing any of these can be asked -- it reads nothing and writes nothing -- and
// "I do not hold that" is a complete answer, because being able to say it at
// all means the far end was reached and the credentials were accepted.
//
// A sink that cannot be asked is not a failure. It was still built, and
// building is what refuses a key that is not base64 or a certificate without
// its key. It is left out of the answer rather than counted as reached, so that
// nothing claims to have checked something it did not.
func (w *Wick) Reach(ctx context.Context) ([]string, error) {
	// A name nothing is called. If something is, the answer is still that the
	// store was reached, which is the only thing being asked.
	const probe = ".wick-check"

	names := make([]string, 0, len(w.Sinks))
	for name := range w.Sinks {
		names = append(names, name)
	}
	slices.Sort(names)

	var (
		errs    []error
		reached []string
	)
	for _, name := range names {
		err, asked := ask(ctx, w.Sinks[name], probe)
		if !asked {
			continue
		}
		if err != nil {
			errs = append(errs, z.Err(err, "the sink %q", name))

			continue
		}

		reached = append(reached, name)
	}

	return reached, errors.Join(errs...)
}

// ask puts the question to one sink, whichever way it can answer, and says
// whether it could be asked at all.
//
// [sink.Reacher] first, because a sink that has one has it precisely where
// asking about a name is not good enough: S3 answers a HEAD with no body, so a
// bucket that does not exist and a key that does not exist arrive as the same
// bare 404, and the first of those is the one worth hearing about.
//
// [sink.Stater] otherwise, where "I do not hold that" is a complete answer --
// it could only be given by a store that was reached and credentials that were
// taken.
func ask(ctx context.Context, s sink.Sink, probe string) (error, bool) {
	if r, ok := s.(sink.Reacher); ok {
		return r.Reach(ctx), true
	}

	st, ok := s.(sink.Stater)
	if !ok {
		return nil, false
	}

	if _, err := st.Stat(ctx, probe); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err, true
	}

	return nil, true
}

// Close lets go of everything this opened, and of nothing it was handed.
func (w *Wick) Close() error {
	var errs []error
	for _, c := range w.closers {
		errs = append(errs, c.Close())
	}
	if w.Journal != nil {
		errs = append(errs, w.Journal.Close())
	}

	return errors.Join(errs...)
}

// Build opens what the configuration describes.
//
// This is where the questions that are about the machine rather than about the
// document are asked -- whether the directory is there, whether the source can
// delete what the retention says to delete. They are asked here, at startup,
// all of them, before anything is carried.
func (c *Config) Build(ctx context.Context) (*Wick, error) {
	w := &Wick{}

	jnl, err := bolt.Open(c.Journal.Path)
	if err != nil {
		// Named, like every other failure in here. Without this a bad
		// `journal.path` surfaces as bolt's own message with nothing in it
		// saying which of the several paths in a configuration it was about.
		return nil, z.Err(err, "the journal at %q", c.Journal.Path)
	}
	w.Journal = jnl

	// Made once each and shared: two spools that carry to the same sink share
	// its connections, which on a machine with one uplink is the whole point of
	// naming them.
	sinks := make(map[string]sink.Sink, len(c.Sinks))
	w.Sinks = sinks
	for name, sc := range c.Sinks {
		s, err := sc.New(ctx)
		if err != nil {
			_ = w.Close()

			return nil, z.Err(err, "the sink %q", name)
		}

		sinks[name] = s
		if cl, ok := s.(sink.Closer); ok {
			w.closers = append(w.closers, cl)
		}
	}

	// What is being watched is a thing being done, not a thing going wrong, so
	// it is only said when somebody asked -- which for `run` is always.
	l := Say(ctx)

	spools := make([]*spool.Spool, 0, len(c.Spools))
	for _, sc := range c.Spools {
		s, err := c.spool(ctx, sc, jnl, sinks)
		if err != nil {
			_ = w.Close()

			return nil, err
		}

		spools = append(spools, s)
		l.Info("watching",
			slog.String("spool", sc.Name),
			slog.String("source", sc.Source.Type),
			slog.Int("to", len(sc.To)),
			slog.String("when", fmt.Sprint(sc.When())),
			slog.String("then", fmt.Sprint(sc.Policy())),
		)
	}

	w.Group = spool.NewGroup(spools...)

	return w, nil
}

func (c *Config) spool(ctx context.Context, sc SpoolConfig, jnl journal.Journal, sinks map[string]sink.Sink) (*spool.Spool, error) {
	src, err := sc.Source.Spec.New(ctx)
	if err != nil {
		return nil, z.Err(err, "the source of the spool %q", sc.Name)
	}

	dests := make([]spool.Dest, 0, len(sc.To))
	for _, d := range sc.To {
		dests = append(dests, spool.Dest{
			Name:   d.Sink,
			Sink:   sinks[d.Sink],
			Naming: d.name,
			Verify: d.Verifies(),
		})
	}

	return spool.New(spool.Config{
		Name:     sc.Name,
		Host:     c.Identity.Name,
		Source:   src,
		Dests:    dests,
		Journal:  jnl,
		Trigger:  sc.When(),
		Retain:   sc.Policy(),
		Settle:   sc.Carry.SettleFor,
		Workers:  sc.Carry.Workers,
		Attempts: sc.Carry.Attempts,
	})
}
