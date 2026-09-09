// Package sinktest is the contract every [sink.Sink] has to keep.
//
// It is exported because the interface is meant to be implemented elsewhere:
// whatever the building already runs is a legitimate place for these files to
// go, and this is what says whether an implementation of that is finished.
package sinktest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
)

// Open makes a sink for one test.
type Open func(t *testing.T) sink.Sink

// cancelled is a context of this test's that is about to be done.
func cancelled(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithCancel(t.Context())
}

// meta is what a caller would say about b before sending it.
func meta(b []byte) sink.Meta {
	sum := sha256.Sum256(b)

	return sink.Meta{Size: int64(len(b)), Digest: "sha256:" + hex.EncodeToString(sum[:])}
}

func put(t *testing.T, s sink.Sink, name string, b []byte) error {
	t.Helper()

	return s.Put(t.Context(), name, bytes.NewReader(b), meta(b))
}

// Suite runs every test in the contract.
func Suite(t *testing.T, open Open) {
	t.Helper()

	t.Run("what is put is what is held", func(t *testing.T) {
		x := require.New(t)
		s := open(t)

		b := []byte("some bytes")
		x.NoError(put(t, s, "a/b.rec", b))

		st, ok := s.(sink.Stater)
		if !ok {
			t.Skip("this sink cannot be asked what it holds")
		}

		m, err := st.Stat(t.Context(), "a/b.rec")
		x.NoError(err)
		x.Equal(int64(len(b)), m.Size)
		// A hash is optional. One that is given has to be the right one.
		if m.Digest != "" {
			x.Equal(meta(b).Digest, m.Digest)
		}
	})

	// A name that is already taken is, in every case this is built for, the
	// same bytes arriving a second time because the first attempt died between
	// the write and the record of it.
	t.Run("a name that is taken is written over rather than refused", func(t *testing.T) {
		x := require.New(t)
		s := open(t)

		x.NoError(put(t, s, "a.rec", []byte("first")))
		x.NoError(put(t, s, "a.rec", []byte("second and longer")))

		st, ok := s.(sink.Stater)
		if !ok {
			t.Skip("this sink cannot be asked what it holds")
		}

		m, err := st.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(len("second and longer")), m.Size)
	})

	t.Run("a name it does not hold is not-there and not some other failure", func(t *testing.T) {
		s := open(t)

		st, ok := s.(sink.Stater)
		if !ok {
			t.Skip("this sink cannot be asked what it holds")
		}

		x := require.New(t)
		_, err := st.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})

	// Refusing a name that would climb out is not in this contract, because it
	// is not true of every sink: an object store's key is opaque and ".." in
	// one is a key like any other. It is true of the sinks whose names become
	// paths, and those check it themselves. See [Escapes].
	t.Run("a context that is done is refused rather than acted on", func(t *testing.T) {
		x := require.New(t)
		s := open(t)

		ctx, cancel := cancelled(t)
		cancel()

		err := s.Put(ctx, "a.rec", bytes.NewReader([]byte("x")), meta([]byte("x")))
		x.Error(err)

		if st, ok := s.(sink.Stater); ok {
			_, err := st.Stat(ctx, "a.rec")
			x.Error(err)
		}
	})
}

// Escapes is the extra contract for a sink whose names become paths: nothing is
// put anywhere but under the place it was told about.
//
// It is separate from [Suite] because it is not true of a sink whose names are
// opaque keys, and asserting it there would be asserting something false.
func Escapes(t *testing.T, open Open) {
	t.Helper()

	s := open(t)
	for _, name := range []string{"", "..", "../out.rec", "/etc/passwd", "a//b.rec", "./a.rec"} {
		t.Run(name, func(t *testing.T) {
			x := require.New(t)

			x.ErrorContains(put(t, s, name, []byte("x")), "is not a name")

			if st, ok := s.(sink.Stater); ok {
				_, err := st.Stat(t.Context(), name)
				x.ErrorContains(err, "is not a name")
			}
		})
	}
}
