// Package sourcetest is the contract every [source.Source] has to keep.
//
// It is exported because the interface is meant to be implemented elsewhere,
// and because the capabilities are optional: a suite that finds out which of
// them an implementation has and then holds it to the ones it claims is the
// only way "optional" stays a promise rather than a shrug.
package sourcetest

import (
	"context"
	"io"
	"io/fs"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/source"
)

// cancelled is a context of this test's that is about to be done.
func cancelled(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithCancel(t.Context())
}

// Open makes a source holding seed, for one test.
type Open func(t *testing.T, seed map[string][]byte) source.Source

func keys(t *testing.T, s source.Source) []string {
	t.Helper()

	var ks []string
	for it, err := range s.Scan(t.Context()) {
		require.NoError(t, err)
		ks = append(ks, it.Key)
	}
	slices.Sort(ks)

	return ks
}

// Suite runs every test in the contract, and the ones for each capability the
// source turns out to have.
func Suite(t *testing.T, open Open) {
	t.Helper()

	t.Run("a scan yields what is there", func(t *testing.T) {
		x := require.New(t)
		s := open(t, map[string][]byte{"a.rec": []byte("aa"), "b.rec": []byte("bbb")})

		var got []source.Item
		for it, err := range s.Scan(t.Context()) {
			x.NoError(err)
			got = append(got, it)
		}
		x.Len(got, 2)

		slices.SortFunc(got, func(a, b source.Item) int { return len(a.Key) - len(b.Key) })
		x.Equal("a.rec", got[0].Key)
		x.Equal(int64(2), got[0].Size)
		x.Equal(int64(3), got[1].Size)
		// Something was said about when it was written. Which instant is the
		// filesystem's business; that there is one is this contract's.
		x.False(got[0].ModAt.IsZero())
	})

	t.Run("a source with nothing in it yields nothing", func(t *testing.T) {
		x := require.New(t)

		x.Empty(keys(t, open(t, nil)))
	})

	t.Run("what was put in is what is read out", func(t *testing.T) {
		x := require.New(t)
		s := open(t, map[string][]byte{"a.rec": []byte("hello")})

		r, err := s.Open(t.Context(), "a.rec")
		x.NoError(err)
		defer r.Close()

		b, err := io.ReadAll(r)
		x.NoError(err)
		x.Equal("hello", string(b))
	})

	// The engine reads this as "it went away" rather than as something to try
	// again, because trying again will not bring it back.
	t.Run("a key that is not there is not-there and not some other failure", func(t *testing.T) {
		x := require.New(t)
		s := open(t, map[string][]byte{"a.rec": []byte("x")})

		_, err := s.Open(t.Context(), "gone.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})

	t.Run("a caller that stops looking is not walked any further", func(t *testing.T) {
		x := require.New(t)
		s := open(t, map[string][]byte{"a.rec": []byte("a"), "b.rec": []byte("b"), "c.rec": []byte("c")})

		n := 0
		for range s.Scan(t.Context()) {
			n++

			break
		}
		x.Equal(1, n)
	})

	t.Run("a context that is done is refused rather than served stale", func(t *testing.T) {
		x := require.New(t)
		s := open(t, map[string][]byte{"a.rec": []byte("x")})

		ctx, cancel := cancelled(t)
		cancel()

		_, err := s.Open(ctx, "a.rec")
		x.ErrorIs(err, ctx.Err())

		for _, err := range s.Scan(ctx) {
			x.ErrorIs(err, ctx.Err())

			break
		}
	})

	t.Run("what it can do, it does", func(t *testing.T) {
		s := open(t, map[string][]byte{"a.rec": []byte("x")})

		if r, ok := s.(source.Remover); ok {
			t.Run("what is removed is gone", func(t *testing.T) {
				x := require.New(t)
				s := open(t, map[string][]byte{"a.rec": []byte("x"), "b.rec": []byte("y")})
				r := s.(source.Remover) //nolint:errcheck // the same kind as the one above

				x.NoError(r.Remove(t.Context(), "a.rec"))
				x.Equal([]string{"b.rec"}, keys(t, s))

				_, err := s.Open(t.Context(), "a.rec")
				x.ErrorIs(err, fs.ErrNotExist)
			})
			t.Run("removing is refused once the context is done", func(t *testing.T) {
				x := require.New(t)

				ctx, cancel := cancelled(t)
				cancel()
				x.ErrorIs(r.Remove(ctx, "a.rec"), ctx.Err())
			})
		}

		if m, ok := s.(source.Mover); ok {
			t.Run("what is moved is no longer where it was", func(t *testing.T) {
				x := require.New(t)
				s := open(t, map[string][]byte{"a.rec": []byte("x")})
				m := s.(source.Mover) //nolint:errcheck // the same kind as the one above

				x.NoError(m.Move(t.Context(), "a.rec", "done"))

				_, err := s.Open(t.Context(), "a.rec")
				x.ErrorIs(err, fs.ErrNotExist)
			})
			t.Run("moving is refused once the context is done", func(t *testing.T) {
				x := require.New(t)

				ctx, cancel := cancelled(t)
				cancel()
				x.ErrorIs(m.Move(ctx, "a.rec", "done"), ctx.Err())
			})
		}

		if w, ok := s.(source.Watcher); ok {
			t.Run("watching ends when the watching does", func(t *testing.T) {
				x := require.New(t)

				ctx, cancel := cancelled(t)
				c, err := w.Watch(ctx)
				x.NoError(err)

				cancel()
				// Closed rather than left open: a caller ranging over it has to
				// be let go of, or the shutdown waits on a goroutine that is
				// waiting on the shutdown.
				for range c { //nolint:revive // draining is the assertion
				}
			})
		}
	})
}
