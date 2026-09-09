package mem_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/sink/mem"
	"github.com/heojeongbo/wick/sink/sinktest"
)

var errRefused = errors.New("refused")

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return mem.New() })
}

func TestHolds(t *testing.T) {
	x := require.New(t)

	s := mem.New()
	x.NoError(s.Put(t.Context(), "b.rec", bytes.NewReader([]byte("bb")), sink.Meta{Size: 2}))
	x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("a")), sink.Meta{Size: 1}))

	x.Equal([]string{"a.rec", "b.rec"}, s.Names())
	x.Equal(2, s.Puts())

	b, ok := s.Data("a.rec")
	x.True(ok)
	x.Equal("a", string(b))

	_, ok = s.Data("nope")
	x.False(ok)

	got, err := io.ReadAll(s.Reader("b.rec"))
	x.NoError(err)
	x.Equal("bb", string(got))
}

// The refusals are the point of this sink: they are how the engine's
// interesting paths get run at all.
func TestRefusals(t *testing.T) {
	t.Run("a write can be refused", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		s.FailPut("a.rec", errRefused)

		x.ErrorIs(s.Put(t.Context(), "a.rec", bytes.NewReader(nil), sink.Meta{}), errRefused)
		x.Zero(s.Puts())
	})
	t.Run("a read-back can be refused, which is not the same as saying it is not there", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		s.FailStat("a.rec", errRefused)

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorIs(err, errRefused)
	})
	t.Run("a reader that gives out part way is what the caller hears about", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		x.ErrorIs(s.Put(t.Context(), "a.rec", failingReader{}, sink.Meta{Size: 9}), errRefused)
		x.Empty(s.Names())
	})

	// This is the failure the read-back exists for: a store that answers a
	// write with a success and does not hold what it was given.
	t.Run("a write can be answered and then swallowed", func(t *testing.T) {
		x := require.New(t)

		s := mem.New()
		s.Swallow("a.rec")

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		x.Equal(1, s.Puts())
		x.Empty(s.Names())

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorContains(err, "file does not exist")

		// And then it can stop swallowing, so a test can have the second
		// attempt be the one that works.
		s.Unswallow("a.rec")
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(1), m.Size)
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRefused }
