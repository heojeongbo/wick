package spool_test

import (
	"context"
	"io"
	"iter"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
	journalmem "github.com/heojeongbo/wick/journal/mem"
	"github.com/heojeongbo/wick/naming"
	"github.com/heojeongbo/wick/retain"
	"github.com/heojeongbo/wick/sink"
	sinkmem "github.com/heojeongbo/wick/sink/mem"
	"github.com/heojeongbo/wick/source"
	sourcedir "github.com/heojeongbo/wick/source/dir"
	sourcemem "github.com/heojeongbo/wick/source/mem"
	"github.com/heojeongbo/wick/spool"
	"github.com/heojeongbo/wick/trigger"
)

// The read-back is only worth having if it is believed about what it says, and
// what it says can be wrong in two ways.
func TestAStoreThatHoldsSomethingElse(t *testing.T) {
	t.Run("a different length is not the file that was sent", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withSink(&lyingSink{Sink: sinkmem.New(), size: 99}))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorContains(r.Err(), "99 bytes")
	})
	t.Run("a different hash is not the file that was sent either", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withSink(&lyingSink{Sink: sinkmem.New(), digest: "sha256:beef"}))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorContains(r.Err(), "holds something else")
	})
	t.Run("a store that says nothing about the length is not argued with", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withSink(&lyingSink{Sink: sinkmem.New(), size: -1}))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
	})
}

func TestAReadThatGivesOutPartWay(t *testing.T) {
	t.Run("while it is being hashed for its name", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withNaming("blob/{sha256}"), withSourceWrapper(halfRead))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorIs(r.Err(), errRefused)
	})
	t.Run("while it is being opened for its name", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withNaming("blob/{sha256}"))
		g.add("a.rec", "contents")
		g.src.FailOpen("a.rec", errRefused)

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorIs(r.Err(), errRefused)
	})
}

func TestAJournalThatFailsPartWayThrough(t *testing.T) {
	t.Run("when the second question about a key is the one it refuses", func(t *testing.T) {
		x := require.New(t)

		// The first ask is what decides the item is due; the second is the one
		// the carry itself makes.
		jnl := &failingJournal{Journal: journalmem.New(), getErrAfter: 1, getErr: errRefused}
		g := newRig(t, withJournal(jnl))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Carried)
		x.ErrorIs(r.Err(), errRefused)
		x.Zero(g.cloud.Puts())
	})
	t.Run("when it will not even remember that the carry failed", func(t *testing.T) {
		x := require.New(t)

		jnl := &failingJournal{Journal: journalmem.New(), putErr: errRefused}
		g := newRig(t, withJournal(jnl))
		g.add("a.rec", "contents")
		g.cloud.FailPut("thor-top/a.rec", errRefused)

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.ErrorIs(r.Err(), errRefused)
	})
	t.Run("when it will not remember that the file was dealt with", func(t *testing.T) {
		x := require.New(t)

		// The carry is remembered; the retirement is not.
		jnl := &failingJournal{Journal: journalmem.New(), putErrAfter: 1, putErr: errRefused}
		g := newRig(t, withJournal(jnl), withRetain(retain.Delete()))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		x.Zero(r.Retired)
		x.ErrorIs(r.Err(), errRefused)
	})
	t.Run("when a retirement it comes back to later cannot be recorded", func(t *testing.T) {
		x := require.New(t)

		jnl := &failingJournal{Journal: journalmem.New()}
		g := newRig(t, withJournal(jnl), withRetain(retain.Grace(time.Hour, retain.Delete())))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)

		// An hour later it is due, and now the journal will not have it.
		g.clock.tick(2 * time.Hour)
		jnl.putErr = errRefused

		r, err = g.Once(t.Context())
		x.NoError(err)
		x.Zero(r.Retired)
		x.ErrorIs(r.Err(), errRefused)
	})
}

// A long run must not remember everything that has ever been in the directory.
func TestWhatIsGoneIsForgotten(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withSettle(10*time.Second), withRetain(retain.Delete()))
	g.add("a.rec", "contents")

	_, err := g.Once(t.Context())
	x.NoError(err)

	g.clock.tick(11 * time.Second)
	_, err = g.Once(t.Context())
	x.NoError(err)
	x.Empty(g.src.Keys())

	// A pass over a directory with nothing in it, which is where the
	// forgetting happens.
	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Zero(r.Scanned)

	// And now it has to sit still all over again rather than being carried at
	// once on the strength of a sighting from before.
	g.src.Add("a.rec", []byte("again"), start.Add(time.Hour))
	r, err = g.Once(t.Context())
	x.NoError(err)
	x.Zero(r.Settled)
}

// Handing out work stops when the running does. What is in flight is left to
// finish or to notice the context itself.
func TestAPassThatIsStoppedPartWay(t *testing.T) {
	x := require.New(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	g := newRig(t, withSink(&cancelsAfterOne{Sink: sinkmem.New(), cancel: cancel}))
	for _, k := range []string{"a.rec", "b.rec", "c.rec", "d.rec"} {
		g.add(k, "contents of "+k)
	}

	_, err := g.Once(ctx)
	x.ErrorIs(err, context.Canceled)
}

// A source that is somewhere is asked how much room is left where it is.
func TestHowMuchRoomIsLeft(t *testing.T) {
	t.Run("a source on a filesystem is asked about that filesystem", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		x.NoError(os.WriteFile(filepath.Join(root, "a.rec"), []byte("contents"), 0o644))

		src, err := sourcedir.New(sourcedir.Options{Path: root})
		x.NoError(err)

		dst := sinkmem.New()
		s, err := spool.New(spool.Config{
			Name:    "recordings",
			Source:  src,
			Journal: journalmem.New(),
			// The disk has room, so this deletes nothing -- and the point is
			// that it asked the real filesystem to find that out.
			Retain: retain.WhenFreeBelow(1, retain.Delete()),
			Dests: []spool.Dest{{
				Name: "cloud", Sink: dst, Naming: naming.MustParse("{name}"), Verify: true,
			}},
		})
		x.NoError(err)

		r, err := s.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		x.Zero(r.Retired)
		x.FileExists(filepath.Join(root, "a.rec"))
	})
	t.Run("a source that is nowhere says nothing, which is not the same as no room", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, withRetain(retain.WhenFreeBelow(1<<62, retain.Delete())))
		g.add("a.rec", "contents")

		r, err := g.Once(t.Context())
		x.NoError(err)
		x.Equal(1, r.Carried)
		// Nobody could say, so nothing was deleted on the strength of it.
		x.Zero(r.Retired)
		x.Equal([]string{"a.rec"}, g.src.Keys())
	})
}

// The trigger is asked what is waiting, and the answer has to be about what is
// actually waiting rather than about everything that is there.
func TestRunAsksWhatIsWaiting(t *testing.T) {
	x := require.New(t)

	// A rate fast enough that the timer is what wakes it, rather than a nudge.
	g := newRig(t, withTrigger(trigger.Every(time.Millisecond)), withRetain(retain.Keep()))
	g.add("a.rec", "contents")
	g.cloud.FailPut("thor-top/a.rec", errRefused)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	// It keeps trying, and keeps finding one thing waiting, until it is set
	// aside.
	until(t, "it to give up on the one it cannot carry", func() bool {
		rec, ok := g.record(t, "a.rec")

		return ok && rec.State == journal.Quarantined
	})

	cancel()
	x.NoError(<-done)
}

// A pass that could not be made because the running stopped is not a pass that
// went wrong.
func TestRunEndsQuietlyWhenTheRunningStops(t *testing.T) {
	x := require.New(t)

	src := sourcemem.New()
	dst := sinkmem.New()

	ctx, cancel := context.WithCancel(t.Context())

	s, err := spool.New(spool.Config{
		Name:    "recordings",
		Source:  blocksUntilDone{inner: src, cancel: cancel},
		Journal: journalmem.New(),
		Dests: []spool.Dest{{
			Name: "cloud", Sink: dst, Naming: naming.MustParse("{name}"), Verify: true,
		}},
	})
	x.NoError(err)

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	x.NoError(<-done)
}

// The nudge goroutine is what a watcher reaches the loop through.
func TestAWatcherReachesTheLoop(t *testing.T) {
	x := require.New(t)

	g := newRig(t)
	g.add("a.rec", "first")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	// The first pass, which also means the watcher is registered by now.
	until(t, "the first pass", func() bool { return g.cloud.Puts() == 1 })

	g.add("b.rec", "second")
	g.src.Nudge()

	until(t, "the pass the watcher asked for", func() bool { return g.cloud.Puts() == 2 })

	cancel()
	x.NoError(<-done)
}

// lyingSink keeps what it is given and then says something else about it.
type lyingSink struct {
	*sinkmem.Sink

	size   int64
	digest string
}

func (l *lyingSink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	m, err := l.Sink.Stat(ctx, name)
	if err != nil {
		return m, err
	}
	if l.size != 0 {
		m.Size = l.size
	}
	if l.digest != "" {
		m.Digest = l.digest
	}

	return m, nil
}

// cancelsAfterOne stops the running once it has taken one thing, which is what
// a shutdown in the middle of a pass looks like.
type cancelsAfterOne struct {
	*sinkmem.Sink

	cancel context.CancelFunc
	n      int
}

func (c *cancelsAfterOne) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	err := c.Sink.Put(ctx, name, r, want)
	c.n++
	if c.n == 1 {
		c.cancel()
	}

	return err
}

// halfRead hands back a reader that gives out part way through, which is what a
// disk going away under a read looks like.
func halfRead(s source.Source) source.Source { return halfReader{inner: s} }

type halfReader struct{ inner source.Source }

func (h halfReader) Scan(ctx context.Context) iter.Seq2[source.Item, error] {
	return h.inner.Scan(ctx)
}

func (h halfReader) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := h.inner.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	_ = rc.Close()

	return io.NopCloser(failingReader{}), nil
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRefused }

// blocksUntilDone stops the running and then answers the scan with the reason,
// which is the shape of a shutdown that arrives during a pass.
type blocksUntilDone struct {
	inner  source.Source
	cancel context.CancelFunc
}

func (b blocksUntilDone) Scan(ctx context.Context) iter.Seq2[source.Item, error] {
	return func(yield func(source.Item, error) bool) {
		b.cancel()
		<-ctx.Done()
		yield(source.Item{}, ctx.Err())
	}
}

func (b blocksUntilDone) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.inner.Open(ctx, key)
}

var (
	_ sink.Stater   = (*lyingSink)(nil)
	_ sink.Sink     = (*cancelsAfterOne)(nil)
	_ source.Source = halfReader{}
	_ source.Source = blocksUntilDone{}
)

// Working out how long to wait means asking what is still waiting, and that
// asks the journal. One that will not answer must not stop the loop -- the
// worst it can do is make the next wait the wrong length.
func TestWorkingOutTheWaitWhenTheJournalWillNotAnswer(t *testing.T) {
	x := require.New(t)

	jnl := &failingJournal{Journal: journalmem.New(), getErr: errRefused}
	g := newRig(t, withJournal(jnl), withTrigger(trigger.Every(time.Millisecond)))
	g.add("a.rec", "contents")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	// Enough asks that the ones the wait makes are among them.
	until(t, "it to keep asking", func() bool { return jnl.Gets() > 4 })

	cancel()
	x.NoError(<-done)
}
