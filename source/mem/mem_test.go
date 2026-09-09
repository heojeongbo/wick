package mem_test

import (
	"context"
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/source/mem"
	"github.com/heojeongbo/wick/source/sourcetest"
)

var modAt = time.Date(2026, 9, 12, 3, 4, 5, 0, time.UTC)

func seeded(t *testing.T, seed map[string][]byte) *mem.Source {
	t.Helper()

	s := mem.New()
	for k, v := range seed {
		s.Add(k, v, modAt)
	}

	return s
}

func TestSource(t *testing.T) {
	sourcetest.Suite(t, func(t *testing.T, seed map[string][]byte) source.Source {
		return seeded(t, seed)
	})
}

func TestHolds(t *testing.T) {
	x := require.New(t)

	s := seeded(t, map[string][]byte{"b.rec": []byte("bb"), "a.rec": []byte("a")})
	x.Equal([]string{"a.rec", "b.rec"}, s.Keys())

	b, ok := s.Data("a.rec")
	x.True(ok)
	x.Equal("a", string(b))

	_, ok = s.Data("nope")
	x.False(ok)

	// Adding under a key that is taken replaces what was there.
	s.Add("a.rec", []byte("zzz"), modAt)
	b, _ = s.Data("a.rec")
	x.Equal("zzz", string(b))
}

func TestMove(t *testing.T) {
	t.Run("it keeps its own name under the new one", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t, map[string][]byte{"deep/a.rec": []byte("x")})
		x.NoError(s.Move(t.Context(), "deep/a.rec", "done"))
		x.Equal([]string{"done/a.rec"}, s.Keys())
	})
	t.Run("moving what is not there says so", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		x.ErrorIs(s.Move(t.Context(), "nope", "done"), fs.ErrNotExist)
	})
}

// The refusals are the point of this source: they are how the engine's
// interesting paths get run at all.
func TestRefusals(t *testing.T) {
	t.Run("a scan that fails ends the scan", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t, map[string][]byte{"a.rec": []byte("x")})
		s.FailScan(mem.ErrRefused)

		var got []error
		for it, err := range s.Scan(t.Context()) {
			x.Zero(it)
			got = append(got, err)
		}
		x.Len(got, 1)
		x.ErrorIs(got[0], mem.ErrRefused)
	})
	t.Run("one item that cannot be looked at does not hide the rest", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t, map[string][]byte{"a.rec": []byte("x"), "b.rec": []byte("y")})
		s.FailItem("a.rec", mem.ErrRefused)

		var (
			ok   []string
			errs []error
		)
		for it, err := range s.Scan(t.Context()) {
			if err != nil {
				x.Equal("a.rec", it.Key)
				errs = append(errs, err)

				continue
			}
			ok = append(ok, it.Key)
		}
		x.Len(errs, 1)
		x.Equal([]string{"b.rec"}, ok)
	})
	t.Run("a caller that stops after the bad one is not walked further", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t, map[string][]byte{"a.rec": []byte("x"), "b.rec": []byte("y")})
		s.FailItem("a.rec", mem.ErrRefused)

		n := 0
		for range s.Scan(t.Context()) {
			n++

			break
		}
		x.Equal(1, n)
	})
	t.Run("opening, removing and moving can each be refused", func(t *testing.T) {
		x := require.New(t)

		s := seeded(t, map[string][]byte{"a.rec": []byte("x")})
		s.FailOpen("a.rec", mem.ErrRefused)
		s.FailRemove("a.rec", mem.ErrRefused)
		s.FailMove("a.rec", mem.ErrRefused)

		_, err := s.Open(t.Context(), "a.rec")
		x.ErrorIs(err, mem.ErrRefused)
		x.ErrorIs(s.Remove(t.Context(), "a.rec"), mem.ErrRefused)
		x.ErrorIs(s.Move(t.Context(), "a.rec", "done"), mem.ErrRefused)
	})
}

func TestWatch(t *testing.T) {
	t.Run("a nudge wakes everything that is watching", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		a, err := s.Watch(t.Context())
		x.NoError(err)
		b, err := s.Watch(t.Context())
		x.NoError(err)

		// Twice, so that the second one finds a wake-up already waiting and
		// says nothing more.
		s.Nudge()
		s.Nudge()

		<-a
		<-b
	})
	t.Run("watching ends when the watching does", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		ctx, cancel := context.WithCancel(t.Context())
		c, err := s.Watch(ctx)
		x.NoError(err)

		cancel()
		for range c { //nolint:revive // draining until it closes is the assertion
		}

		// And a nudge afterwards reaches nobody, rather than sending on a
		// channel that is closed.
		x.NotPanics(s.Nudge)
	})
	t.Run("watching with a context that is already done is refused", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := s.Watch(ctx)
		x.ErrorIs(err, ctx.Err())
	})
}

// The reader Open hands back is a reader, and closing it is allowed.
func TestOpenIsClosable(t *testing.T) {
	x := require.New(t)

	s := seeded(t, map[string][]byte{"a.rec": []byte("x")})
	r, err := s.Open(t.Context(), "a.rec")
	x.NoError(err)
	x.NoError(r.Close())

	_, err = io.ReadAll(r)
	x.NoError(err)
}
