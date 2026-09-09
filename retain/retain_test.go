package retain_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/retain"
	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/source/mem"
)

var (
	now       = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	errNoRoom = errors.New("no room")
)

// carried is a record of something every sink has confirmed, which is the only
// state a policy is ever given.
func carried(at time.Time) journal.Record {
	return journal.Record{
		Source:  "src",
		Key:     "a.rec",
		Size:    1024,
		State:   journal.Carried,
		Carried: map[string]journal.Carry{"cloud": {At: at}},
	}
}

func seeded(t *testing.T) *mem.Source {
	t.Helper()

	s := mem.New()
	s.Add("a.rec", []byte("contents"), now)

	return s
}

// bare is a source that can only be read from -- a read-only mount, or a
// listing endpoint. It is a legitimate source, and the policies have to say so
// rather than fail on it.
type bare struct{ inner source.Source }

func (b bare) Scan(ctx context.Context) iter.Seq2[source.Item, error] { return b.inner.Scan(ctx) }

func (b bare) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.inner.Open(ctx, key)
}

func TestKeep(t *testing.T) {
	x := require.New(t)

	s := seeded(t)
	st, err := retain.Keep().After(t.Context(), carried(now), s, now, 1<<30)
	x.NoError(err)
	x.Equal(journal.Retired, st)

	// The file is still there. That is the whole of it.
	x.Equal([]string{"a.rec"}, s.Keys())
	x.Equal(retain.Nothing, retain.Keep().Needs())
	x.NoError(retain.Check(retain.Keep(), bare{seeded(t)}))
}

func TestDelete(t *testing.T) {
	t.Run("the file goes", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := retain.Delete().After(t.Context(), carried(now), s, now, 1<<30)
		x.NoError(err)
		x.Equal(journal.Retired, st)
		x.Empty(s.Keys())
	})

	// Somebody else got there first, which is the outcome that was wanted.
	t.Run("one that is already gone is not a failure to retry forever", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		s.FailRemove("a.rec", &fs.PathError{Op: "remove", Path: "a.rec", Err: fs.ErrNotExist})

		st, err := retain.Delete().After(t.Context(), carried(now), s, now, 1<<30)
		x.NoError(err)
		x.Equal(journal.Retired, st)
	})
	t.Run("one that is refused stays carried, to be asked about again", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		s.FailRemove("a.rec", errNoRoom)

		st, err := retain.Delete().After(t.Context(), carried(now), s, now, 1<<30)
		x.ErrorIs(err, errNoRoom)
		x.Equal(journal.Carried, st)
	})
	t.Run("a source that cannot delete is said so rather than deleting nothing in silence", func(t *testing.T) {
		x := require.New(t)

		st, err := retain.Delete().After(t.Context(), carried(now), bare{seeded(t)}, now, 1<<30)
		x.ErrorContains(err, "cannot delete")
		x.Equal(journal.Carried, st)
	})
}

func TestMove(t *testing.T) {
	t.Run("the file goes somewhere else", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := retain.Move("done").After(t.Context(), carried(now), s, now, 1<<30)
		x.NoError(err)
		x.Equal(journal.Retired, st)
		x.Equal([]string{"done/a.rec"}, s.Keys())
	})
	t.Run("one that is already gone is not a failure to retry forever", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		s.FailMove("a.rec", &fs.PathError{Op: "move", Path: "a.rec", Err: fs.ErrNotExist})

		st, err := retain.Move("done").After(t.Context(), carried(now), s, now, 1<<30)
		x.NoError(err)
		x.Equal(journal.Retired, st)
	})
	t.Run("one that is refused stays carried", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		s.FailMove("a.rec", errNoRoom)

		st, err := retain.Move("done").After(t.Context(), carried(now), s, now, 1<<30)
		x.ErrorIs(err, errNoRoom)
		x.Equal(journal.Carried, st)
	})
	t.Run("a source that cannot move is said so", func(t *testing.T) {
		x := require.New(t)

		st, err := retain.Move("done").After(t.Context(), carried(now), bare{seeded(t)}, now, 1<<30)
		x.ErrorContains(err, "cannot move")
		x.Equal(journal.Carried, st)
	})
}

// The window is long enough that somebody who notices the wrong thing was
// carried can still reach the original.
func TestGrace(t *testing.T) {
	g := retain.Grace(24*time.Hour, retain.Delete())

	t.Run("inside the window nothing happens, and it is asked again later", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := g.After(t.Context(), carried(now), s, now.Add(time.Hour), 1<<30)
		x.NoError(err)
		x.Equal(journal.Carried, st)
		x.Equal([]string{"a.rec"}, s.Keys())
	})
	t.Run("outside it, the policy it is holding acts", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := g.After(t.Context(), carried(now), s, now.Add(25*time.Hour), 1<<30)
		x.NoError(err)
		x.Equal(journal.Retired, st)
		x.Empty(s.Keys())
	})

	// The alternative is a file that is never tidied because of a record that
	// lost a timestamp.
	t.Run("a record that never said when is acted on now", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		r := carried(now)
		r.Carried = nil

		st, err := g.After(t.Context(), r, s, now, 1<<30)
		x.NoError(err)
		x.Equal(journal.Retired, st)
	})

	// The window is measured from the last sink to answer, since that is when
	// the file became safe to act on.
	t.Run("the window starts when the last sink answered", func(t *testing.T) {
		x := require.New(t)

		r := carried(now)
		r.Carried["onprem"] = journal.Carry{At: now.Add(10 * time.Hour)}

		st, err := g.After(t.Context(), r, seeded(t), now.Add(25*time.Hour), 1<<30)
		x.NoError(err)
		x.Equal(journal.Carried, st)
	})

	t.Run("it asks for what the policy it holds asks for", func(t *testing.T) {
		x := require.New(t)

		x.Equal(retain.Removes, g.Needs())
		x.ErrorContains(retain.Check(g, bare{seeded(t)}), "can delete")
	})
}

// The window is a kindness, and a disk that is filling is not the time for one.
func TestWhenFreeBelow(t *testing.T) {
	w := retain.WhenFreeBelow(1024, retain.Delete())

	t.Run("with room to spare it does nothing", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := w.After(t.Context(), carried(now), s, now, 1<<30)
		x.NoError(err)
		x.Equal(journal.Carried, st)
		x.Equal([]string{"a.rec"}, s.Keys())
	})
	t.Run("without it, the policy it is holding acts", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := w.After(t.Context(), carried(now), s, now, 512)
		x.NoError(err)
		x.Equal(journal.Retired, st)
	})
	t.Run("a filesystem nobody could measure is not one that is full", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := w.After(t.Context(), carried(now), s, now, 0)
		x.NoError(err)
		x.Equal(journal.Carried, st)
		x.Equal([]string{"a.rec"}, s.Keys())
	})
	t.Run("it asks for what the policy it holds asks for", func(t *testing.T) {
		x := require.New(t)

		x.Equal(retain.Removes, w.Needs())
		x.Equal(retain.Moves, retain.WhenFreeBelow(1, retain.Move("done")).Needs())
	})
}

// After a day, or sooner if the disk is filling.
func TestFirst(t *testing.T) {
	f := retain.First(
		retain.WhenFreeBelow(1024, retain.Delete()),
		retain.Grace(24*time.Hour, retain.Delete()),
	)

	t.Run("nothing yet, when neither of them has anything to say", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := f.After(t.Context(), carried(now), s, now.Add(time.Hour), 1<<30)
		x.NoError(err)
		x.Equal(journal.Carried, st)
		x.Equal([]string{"a.rec"}, s.Keys())
	})
	t.Run("the disk filling is enough on its own", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := f.After(t.Context(), carried(now), s, now.Add(time.Hour), 512)
		x.NoError(err)
		x.Equal(journal.Retired, st)
		x.Empty(s.Keys())
	})
	t.Run("and so is the day going by", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := f.After(t.Context(), carried(now), s, now.Add(25*time.Hour), 1<<30)
		x.NoError(err)
		x.Equal(journal.Retired, st)
	})
	t.Run("one that refuses stops the rest being asked", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		s.FailRemove("a.rec", errNoRoom)

		st, err := f.After(t.Context(), carried(now), s, now.Add(25*time.Hour), 512)
		x.ErrorIs(err, errNoRoom)
		x.Equal(journal.Carried, st)
	})
	t.Run("nothing in it keeps the file, which is the harmless thing to do", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t)
		st, err := retain.First().After(t.Context(), carried(now), s, now, 1)
		x.NoError(err)
		x.Equal(journal.Carried, st)
		x.Equal(retain.Nothing, retain.First().Needs())
	})
	t.Run("it asks for the most any of them asks for", func(t *testing.T) {
		x := require.New(t)

		x.Equal(retain.Moves, retain.First(retain.Delete(), retain.Move("done")).Needs())
	})
}

// This turns "this will fail at the first carry, on a machine, at night" into
// "this is refused when the configuration is read".
func TestCheck(t *testing.T) {
	x := require.New(t)

	full := seeded(t)
	readOnly := bare{seeded(t)}

	x.NoError(retain.Check(retain.Delete(), full))
	x.NoError(retain.Check(retain.Move("done"), full))
	x.NoError(retain.Check(retain.First(retain.Delete(), retain.Move("done")), full))

	x.ErrorContains(retain.Check(retain.Delete(), readOnly), "can delete")
	x.ErrorContains(retain.Check(retain.Move("done"), readOnly), "can move things")

	// Looked inside, so that a policy hidden two deep is still checked.
	x.ErrorContains(
		retain.Check(retain.First(retain.Keep(), retain.WhenFreeBelow(1, retain.Move("done"))), readOnly),
		"can move things",
	)
}

// What a policy says about itself is what `wick config` prints and what a
// startup line says, so it has to read as a sentence.
func TestTheySayWhatTheyAre(t *testing.T) {
	x := require.New(t)

	x.Equal("keep", fmt.Sprint(retain.Keep()))
	x.Equal("delete", fmt.Sprint(retain.Delete()))
	x.Equal(`move to "done"`, fmt.Sprint(retain.Move("done")))
	x.Equal("delete after 24h0m0s", fmt.Sprint(retain.Grace(24*time.Hour, retain.Delete())))
	x.Equal("delete when less than 1024 bytes are free", fmt.Sprint(retain.WhenFreeBelow(1024, retain.Delete())))
	x.Equal("first of [keep, delete]", fmt.Sprint(retain.First(retain.Keep(), retain.Delete())))
	x.Equal("keep", fmt.Sprint(retain.First()))

	x.Equal("nothing", retain.Nothing.String())
	x.Equal("deleting", retain.Removes.String())
	x.Equal("moving", retain.Moves.String())
	x.Equal("Capability(9)", retain.Capability(9).String())
}

var _ source.Source = bare{}
