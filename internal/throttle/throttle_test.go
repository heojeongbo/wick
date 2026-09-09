package throttle_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/internal/throttle"
)

var errRefused = errors.New("refused")

// An unwritten setting must cost nothing, not a wrapper that does nothing.
func TestNoLimitIsNoWrapper(t *testing.T) {
	x := require.New(t)

	r := bytes.NewReader([]byte("xyz"))
	x.Equal(io.Reader(r), throttle.Reader(t.Context(), r, 0))
	x.Equal(io.Reader(r), throttle.Reader(t.Context(), r, -1))
}

func TestReader(t *testing.T) {
	t.Run("everything arrives, whatever the rate", func(t *testing.T) {
		x := require.New(t)

		want := bytes.Repeat([]byte("x"), 4096)
		r := throttle.Reader(t.Context(), bytes.NewReader(want), 1<<30)

		got, err := io.ReadAll(r)
		x.NoError(err)
		x.Equal(want, got)
	})

	t.Run("a read larger than a second's worth is cut down to it", func(t *testing.T) {
		x := require.New(t)

		// Ten bytes a second, and a much larger ask: the read comes back
		// short rather than waiting for the whole of it, which is what keeps
		// one read from being a sleep of minutes.
		r := throttle.Reader(t.Context(), bytes.NewReader(bytes.Repeat([]byte("x"), 100)), 10)

		p := make([]byte, 100)
		n, err := r.Read(p)
		x.NoError(err)
		x.Equal(10, n)
	})

	t.Run("it does take the time it was told to", func(t *testing.T) {
		x := require.New(t)

		// Twenty bytes at ten a second is about a second; asserted loosely,
		// since the assertion is that it waits at all.
		want := bytes.Repeat([]byte("x"), 20)
		r := throttle.Reader(t.Context(), bytes.NewReader(want), 10)

		start := time.Now()
		got, err := io.ReadAll(r)
		x.NoError(err)
		x.Equal(want, got)
		x.Greater(time.Since(start), 500*time.Millisecond)
	})

	t.Run("a reader that gives out is what the caller hears about", func(t *testing.T) {
		x := require.New(t)

		r := throttle.Reader(t.Context(), failingReader{}, 1<<20)

		_, err := io.ReadAll(r)
		x.ErrorIs(err, errRefused)
	})

	// The bytes have been taken out of the reader by then, so they are answered
	// with rather than dropped -- dropping them would lose them.
	t.Run("a wait that is given up on still answers with what was read", func(t *testing.T) {
		x := require.New(t)

		ctx, cancel := context.WithCancel(t.Context())

		r := throttle.Reader(ctx, bytes.NewReader(bytes.Repeat([]byte("x"), 100)), 10)
		cancel()

		p := make([]byte, 10)
		n, err := r.Read(p)
		x.ErrorIs(err, context.Canceled)
		x.Positive(n)
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRefused }
