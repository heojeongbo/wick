package gcs_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/sink/gcs"
	"github.com/heojeongbo/wick/sink/sinktest"
)

var errRefused = errors.New("refused")

// fake is the store kept in memory, for the failures. The test against the real
// client next door is what says the interface is one it satisfies.
type fake struct {
	mu    sync.Mutex
	objs  map[string][]byte
	metas map[string]map[string]string

	writeErr map[string]error
	closeErr map[string]error
	attrsErr map[string]error
	shutErr  error

	closed bool
}

func newFake() *fake {
	return &fake{objs: map[string][]byte{}, metas: map[string]map[string]string{}}
}

func (f *fake) Writer(ctx context.Context, name string, meta map[string]string) io.WriteCloser {
	return &fakeWriter{f: f, name: name, meta: meta}
}

func (f *fake) Attrs(ctx context.Context, name string) (gcs.Attrs, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.attrsErr[name]; ok {
		return gcs.Attrs{}, err
	}

	b, ok := f.objs[name]
	if !ok {
		return gcs.Attrs{}, storage.ErrObjectNotExist
	}

	return gcs.Attrs{Size: int64(len(b)), Metadata: f.metas[name]}, nil
}

func (f *fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true

	return f.shutErr
}

func (f *fake) data(name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.objs[name]

	return b, ok
}

type fakeWriter struct {
	f    *fake
	name string
	meta map[string]string
	buf  bytes.Buffer
}

func (w *fakeWriter) Write(p []byte) (int, error) {
	if err, ok := w.f.writeErr[w.name]; ok {
		return 0, err
	}

	return w.buf.Write(p)
}

// Close is where the errors surface, which is what the real client does too:
// it buffers, so a write that was accepted may still be refused here.
func (w *fakeWriter) Close() error {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()

	if err, ok := w.f.closeErr[w.name]; ok {
		return err
	}

	w.f.objs[w.name] = bytes.Clone(w.buf.Bytes())
	w.f.metas[w.name] = w.meta

	return nil
}

func onFake(t *testing.T, f *fake, o gcs.Options) *gcs.Sink {
	t.Helper()

	if o.Bucket == "" {
		o.Bucket = "recordings"
	}
	o.API = f

	s, err := gcs.New(t.Context(), o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return onFake(t, newFake(), gcs.Options{}) })
}

func TestNew(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    gcs.Options
		says string
	}{
		{"a sink has to say which bucket", gcs.Options{}, "which bucket"},
		{
			"credentials said twice is not saying them",
			gcs.Options{Bucket: "b", CredentialsFile: "f", CredentialsJSON: "{}"},
			"two answers to the same question",
		},
		{
			"a chunk below nothing is not an amount",
			gcs.Options{Bucket: "b", ChunkSize: -1},
			"not an amount to send",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			tc.o.API = newFake()
			_, err := gcs.New(t.Context(), tc.o)
			x.ErrorContains(err, tc.says)
		})
	}

	t.Run("a client of its own is made when it is not handed one", func(t *testing.T) {
		x := require.New(t)

		// No network is touched: the client is built, not used.
		s, err := gcs.New(t.Context(), gcs.Options{
			Bucket:          "b",
			CredentialsJSON: serviceAccount(t),
			Endpoint:        "https://storage.example.invalid",
			ChunkSize:       1 << 20,
		})
		x.NoError(err)
		x.NoError(s.Close())
	})

	t.Run("credentials that are not credentials are refused before anything is sent", func(t *testing.T) {
		x := require.New(t)

		_, err := gcs.New(t.Context(), gcs.Options{Bucket: "b", CredentialsJSON: "not json"})
		x.ErrorContains(err, "work out how to reach the store")

		_, err = gcs.New(t.Context(), gcs.Options{
			Bucket:          "b",
			CredentialsFile: filepath.Join(t.TempDir(), "nope.json"),
		})
		x.ErrorContains(err, "work out how to reach the store")
	})

	t.Run("one that is a file is read from it", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "key.json")
		x.NoError(os.WriteFile(p, []byte(serviceAccount(t)), 0o600))

		s, err := gcs.New(t.Context(), gcs.Options{Bucket: "b", CredentialsFile: p})
		x.NoError(err)
		x.NoError(s.Close())
	})
}

func TestPut(t *testing.T) {
	t.Run("a prefix is put in front of every name", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, gcs.Options{Prefix: "/robots/"})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		_, ok := f.data("robots/a.rec")
		x.True(ok)

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
	})

	// GCS keeps a CRC32C and an MD5, neither of which is what this computes.
	t.Run("the hash goes in the metadata and comes back out", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, gcs.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3, Digest: "sha256:beef"}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal("sha256:beef", m.Digest)
	})

	t.Run("a store that kept no hash answers with the length alone", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, gcs.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
		x.Empty(m.Digest)
	})

	// Said here rather than left to the read-back, so that a spool which was
	// told not to verify still catches it.
	t.Run("a stream that ends early is caught here and not only at the read-back", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, gcs.Options{})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("short")), sink.Meta{Size: 100})
		x.ErrorContains(err, "to be 100 bytes and 5 arrived")
	})

	t.Run("a length nobody claimed is not checked", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, gcs.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("anything")), sink.Meta{Size: -1}))
	})

	t.Run("a write that is refused says which object", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.writeErr = map[string]error{"a.rec": errRefused}
		s := onFake(t, f, gcs.Options{})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3})
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, "a.rec")
	})

	// The client buffers, so a write that was accepted may still be refused
	// when it is finished.
	t.Run("one that is refused at the end says so too", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.closeErr = map[string]error{"a.rec": errRefused}
		s := onFake(t, f, gcs.Options{})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3})
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, "finish")
	})

	t.Run("bytes are sent no faster than they were told to be", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, gcs.Options{RateLimit: 1 << 30})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		b, _ := f.data("a.rec")
		x.Equal("xyz", string(b))
	})
}

func TestStat(t *testing.T) {
	t.Run("what the store does not hold is not-there", func(t *testing.T) {
		x := require.New(t)

		s := onFake(t, newFake(), gcs.Options{})

		_, err := s.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})
	t.Run("what it will not say about is not not-there", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.attrsErr = map[string]error{"a.rec": errRefused}
		s := onFake(t, f, gcs.Options{})

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorIs(err, errRefused)
		x.NotErrorIs(err, fs.ErrNotExist)
	})
}

// The engine closes the sinks it made, and does not close what it was handed.
func TestClosing(t *testing.T) {
	t.Run("a client this made is let go of", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.shutErr = errRefused

		// Handed over, so it is not this sink's to close.
		s := onFake(t, f, gcs.Options{})
		x.NoError(s.Close())
		x.False(f.closed)
	})
	t.Run("and one it was handed is not", func(t *testing.T) {
		x := require.New(t)

		s, err := gcs.New(t.Context(), gcs.Options{Bucket: "b", CredentialsJSON: serviceAccount(t)})
		x.NoError(err)
		x.NoError(s.Close())
	})
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	s, err := (&gcs.Spec{
		Bucket: "recordings", Prefix: "robots",
		CredentialsJson: serviceAccount(t),
		Endpoint:        "https://storage.example.invalid",
		ChunkSize:       1 << 20, RateLimit: 1024,
	}).New(t.Context())
	x.NoError(err)
	x.NotNil(s)

	_, err = (&gcs.Spec{}).New(t.Context())
	x.ErrorContains(err, "which bucket")
}

// Importing this package is what makes `type: gcs` mean something.
func TestItRegistersItself(t *testing.T) {
	x := require.New(t)

	x.Contains(sink.Kinds(), gcs.Kind)

	spec, err := sink.NewSpec(gcs.Kind)
	x.NoError(err)
	x.IsType(&gcs.Spec{}, spec)
}
