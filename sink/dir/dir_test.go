package dir_test

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/sink/dir"
	"github.com/heojeongbo/wick/sink/sinktest"
)

var errRefused = errors.New("refused")

// fakeFS is the real filesystem with a way to make one call fail.
type fakeFS struct {
	dir.OS

	create   map[string]error
	stat     map[string]error
	remove   map[string]error
	rename   map[string]error
	mkdir    map[string]error
	createAs map[string]io.WriteCloser
}

func (f *fakeFS) Create(name string) (io.WriteCloser, error) {
	if err, ok := f.create[name]; ok {
		return nil, err
	}
	if w, ok := f.createAs[name]; ok {
		return w, nil
	}

	return f.OS.Create(name)
}

func (f *fakeFS) Stat(name string) (fs.FileInfo, error) {
	if err, ok := f.stat[name]; ok {
		return nil, err
	}

	return f.OS.Stat(name)
}

func (f *fakeFS) Remove(name string) error {
	if err, ok := f.remove[name]; ok {
		return err
	}

	return f.OS.Remove(name)
}

func (f *fakeFS) Rename(from, to string) error {
	if err, ok := f.rename[from]; ok {
		return err
	}

	return f.OS.Rename(from, to)
}

func (f *fakeFS) MkdirAll(name string, perm fs.FileMode) error {
	if err, ok := f.mkdir[name]; ok {
		return err
	}

	return f.OS.MkdirAll(name, perm)
}

func newSink(t *testing.T, o dir.Options) *dir.Sink {
	t.Helper()

	if o.Path == "" {
		o.Path = t.TempDir()
	}
	s, err := dir.New(o)
	require.NoError(t, err)

	return s
}

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return newSink(t, dir.Options{}) })
}

func TestNames(t *testing.T) {
	sinktest.Escapes(t, func(t *testing.T) sink.Sink { return newSink(t, dir.Options{}) })
}

func TestNew(t *testing.T) {
	t.Run("a sink has to say which directory", func(t *testing.T) {
		x := require.New(t)

		_, err := dir.New(dir.Options{})
		x.ErrorContains(err, "which directory")
	})
	t.Run("it says which directory it is writing into", func(t *testing.T) {
		x := require.New(t)

		x.Equal("/tmp", newSink(t, dir.Options{Path: "/tmp"}).Root())
	})
}

func TestPut(t *testing.T) {
	t.Run("the directories a name needs are made", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		s := newSink(t, dir.Options{Path: root})

		x.NoError(s.Put(t.Context(), "a/b/c.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		b, err := os.ReadFile(filepath.Join(root, "a", "b", "c.rec"))
		x.NoError(err)
		x.Equal("xyz", string(b))
	})

	// A stream that ends early looks exactly like a stream that ended, and the
	// length is the only thing that tells them apart.
	t.Run("a stream that ends early never becomes a file under the whole name", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		s := newSink(t, dir.Options{Path: root})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("short")), sink.Meta{Size: 100})
		x.ErrorContains(err, "to be 100 bytes and 5 arrived")
		x.NoFileExists(filepath.Join(root, "a.rec"))

		// Nor is the half-written one left lying about looking like one.
		es, err := os.ReadDir(root)
		x.NoError(err)
		x.Empty(es)
	})
	t.Run("a length nobody claimed is not checked", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		s := newSink(t, dir.Options{Path: root})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("anything")), sink.Meta{Size: -1}))
		x.FileExists(filepath.Join(root, "a.rec"))
	})

	t.Run("a place that cannot be made is said so", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		f := &fakeFS{mkdir: map[string]error{filepath.Join(root, "a"): errRefused}}
		s := newSink(t, dir.Options{Path: root, FS: f})

		x.ErrorIs(s.Put(t.Context(), "a/b.rec", bytes.NewReader(nil), sink.Meta{Size: 0}), errRefused)
	})
	t.Run("a file that cannot be made is said so", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		f := &fakeFS{create: map[string]error{filepath.Join(root, ".a.rec.partial"): errRefused}}
		s := newSink(t, dir.Options{Path: root, FS: f})

		x.ErrorIs(s.Put(t.Context(), "a.rec", bytes.NewReader(nil), sink.Meta{Size: 0}), errRefused)
	})
	t.Run("a read that gives out part way leaves nothing behind", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		s := newSink(t, dir.Options{Path: root})

		x.ErrorIs(s.Put(t.Context(), "a.rec", failingReader{}, sink.Meta{Size: 9}), errRefused)

		es, err := os.ReadDir(root)
		x.NoError(err)
		x.Empty(es)
	})

	// The failure arrives at the close, not at the write, which is what a full
	// disk looks like.
	t.Run("a write that is refused at the end leaves nothing behind", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		landing, err := os.CreateTemp(t.TempDir(), "landing")
		x.NoError(err)
		defer landing.Close()

		f := &fakeFS{createAs: map[string]io.WriteCloser{
			filepath.Join(root, ".a.rec.partial"): failingCloser{Writer: landing},
		}}
		s := newSink(t, dir.Options{Path: root, FS: f})

		x.ErrorIs(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}), errRefused)
		x.NoFileExists(filepath.Join(root, "a.rec"))
	})
	t.Run("a rename that is refused leaves nothing behind", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		f := &fakeFS{rename: map[string]error{filepath.Join(root, ".a.rec.partial"): errRefused}}
		s := newSink(t, dir.Options{Path: root, FS: f})

		x.ErrorIs(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}), errRefused)
		x.NoFileExists(filepath.Join(root, "a.rec"))
	})
}

func TestStat(t *testing.T) {
	t.Run("it says the size and keeps no hash", func(t *testing.T) {
		x := require.New(t)

		s := newSink(t, dir.Options{})
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
		// Reading it back to hash it would cost as much as writing it did.
		x.Empty(m.Digest)
	})
	t.Run("a look that is refused is told from a thing that is not there", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		f := &fakeFS{stat: map[string]error{filepath.Join(root, "a.rec"): errRefused}}
		s := newSink(t, dir.Options{Path: root, FS: f})

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorIs(err, errRefused)
		x.NotErrorIs(err, fs.ErrNotExist)
	})
}

// The real filesystem answers with nothing rather than with a typed nothing,
// which a caller checking the writer instead of the error would read as a
// writer and then panic on.
func TestTheRealFilesystem(t *testing.T) {
	x := require.New(t)

	missing := filepath.Join(t.TempDir(), "no", "such", "file")

	w, err := dir.OS{}.Create(missing)
	x.Error(err)
	x.Nil(w)

	_, err = dir.OS{}.Stat(missing)
	x.ErrorIs(err, fs.ErrNotExist)

	p := filepath.Join(t.TempDir(), "a")
	w, err = dir.OS{}.Create(p)
	x.NoError(err)
	x.NoError(w.Close())

	x.NoError(dir.OS{}.Rename(p, p+"b"))
	x.NoError(dir.OS{}.Remove(p + "b"))
	x.NoError(dir.OS{}.MkdirAll(filepath.Join(t.TempDir(), "x", "y"), 0o755))
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	root := t.TempDir()
	s, err := (&dir.Spec{Path: root}).New(t.Context())
	x.NoError(err)
	x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	x.FileExists(filepath.Join(root, "a.rec"))

	_, err = (&dir.Spec{}).New(t.Context())
	x.ErrorContains(err, "which directory")
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRefused }

type failingCloser struct{ io.Writer }

func (failingCloser) Close() error { return errRefused }
