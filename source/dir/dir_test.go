package dir_test

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/source/dir"
	"github.com/heojeongbo/wick/source/sourcetest"
)

var errRefused = errors.New("refused")

// fakeFS is the real filesystem with a way to make one call fail.
//
// The failures are what the interesting paths are about, and chmod cannot
// produce them: the test stage of the Docker build runs as root.
type fakeFS struct {
	dir.OS

	readDir map[string]error
	open    map[string]error
	create  map[string]error
	remove  map[string]error
	rename  map[string]error
	mkdir   map[string]error

	// entries replaces what a directory is said to hold, for the shapes a real
	// one is awkward to be made into.
	entries map[string][]fs.DirEntry
	// openAs and createAs hand back a reader or a writer of the test's
	// choosing, for the failures that happen part way through rather than at
	// the start.
	openAs   map[string]io.ReadCloser
	createAs map[string]io.WriteCloser
}

func (f *fakeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err, ok := f.readDir[name]; ok {
		return nil, err
	}
	if es, ok := f.entries[name]; ok {
		return es, nil
	}

	return f.OS.ReadDir(name)
}

func (f *fakeFS) Open(name string) (io.ReadCloser, error) {
	if err, ok := f.open[name]; ok {
		return nil, err
	}
	if r, ok := f.openAs[name]; ok {
		return r, nil
	}

	return f.OS.Open(name)
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

// badEntry is something a directory says it holds but cannot say anything about.
type badEntry struct {
	name string
	mode fs.FileMode
	err  error
}

func (b badEntry) Name() string      { return b.name }
func (b badEntry) IsDir() bool       { return b.mode.IsDir() }
func (b badEntry) Type() fs.FileMode { return b.mode.Type() }
func (b badEntry) Info() (fs.FileInfo, error) {
	if b.err != nil {
		return nil, b.err
	}

	return nil, errRefused
}

// seed writes files into a fresh directory and answers with it.
func seed(t *testing.T, files map[string][]byte) string {
	t.Helper()

	root := t.TempDir()
	for k, v := range files {
		p := filepath.Join(root, filepath.FromSlash(k))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, v, 0o644))
	}

	return root
}

func newSource(t *testing.T, o dir.Options) *dir.Source {
	t.Helper()

	s, err := dir.New(o)
	require.NoError(t, err)

	return s
}

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

func TestSource(t *testing.T) {
	sourcetest.Suite(t, func(t *testing.T, files map[string][]byte) source.Source {
		return newSource(t, dir.Options{Path: seed(t, files)})
	})
}

func TestNew(t *testing.T) {
	t.Run("a source has to say which directory", func(t *testing.T) {
		x := require.New(t)

		_, err := dir.New(dir.Options{})
		x.ErrorContains(err, "which directory")
	})
	t.Run("a pattern with a typo in it is refused rather than left to match nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := dir.New(dir.Options{Path: "/tmp", Include: []string{"[bad"}})
		x.ErrorContains(err, "[bad")
		x.ErrorIs(err, path.ErrBadPattern)

		_, err = dir.New(dir.Options{Path: "/tmp", Exclude: []string{"[bad"}})
		x.ErrorIs(err, path.ErrBadPattern)
	})
	t.Run("a smallest size below nothing means nothing", func(t *testing.T) {
		x := require.New(t)

		_, err := dir.New(dir.Options{Path: "/tmp", MinSize: -1})
		x.ErrorContains(err, "means nothing")
	})
	t.Run("it says which directory it is looking in", func(t *testing.T) {
		x := require.New(t)

		x.Equal("/tmp", newSource(t, dir.Options{Path: "/tmp"}).Root())
	})
}

func TestScan(t *testing.T) {
	files := map[string][]byte{
		"a.rec":         []byte("aaa"),
		"b.log":         []byte("bbb"),
		"deep/c.rec":    []byte("ccc"),
		"deep/er/d.rec": []byte("ddd"),
	}

	t.Run("without being told to, it does not look inside directories", func(t *testing.T) {
		x := require.New(t)

		s := newSource(t, dir.Options{Path: seed(t, files)})
		x.Equal([]string{"a.rec", "b.log"}, keys(t, s))
	})
	t.Run("told to, it looks all the way down", func(t *testing.T) {
		x := require.New(t)

		s := newSource(t, dir.Options{Path: seed(t, files), Recursive: true})
		x.Equal([]string{"a.rec", "b.log", "deep/c.rec", "deep/er/d.rec"}, keys(t, s))
	})
	t.Run("a pattern with no separator is about the file's own name", func(t *testing.T) {
		x := require.New(t)

		s := newSource(t, dir.Options{Path: seed(t, files), Recursive: true, Include: []string{"*.rec"}})
		x.Equal([]string{"a.rec", "deep/c.rec", "deep/er/d.rec"}, keys(t, s))
	})
	t.Run("a pattern with one is about the whole key", func(t *testing.T) {
		x := require.New(t)

		s := newSource(t, dir.Options{Path: seed(t, files), Recursive: true, Include: []string{"deep/*.rec"}})
		x.Equal([]string{"deep/c.rec"}, keys(t, s))
	})
	t.Run("what is left is left, whatever else says to take it", func(t *testing.T) {
		x := require.New(t)

		s := newSource(t, dir.Options{
			Path: seed(t, files), Recursive: true,
			Include: []string{"*.rec"}, Exclude: []string{"deep/*"},
		})
		x.Equal([]string{"a.rec", "deep/er/d.rec"}, keys(t, s))
	})
	t.Run("anything smaller than the smallest is left", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"small.rec": []byte("x"), "big.rec": []byte("xxxx")})
		s := newSource(t, dir.Options{Path: root, MinSize: 4})
		x.Equal([]string{"big.rec"}, keys(t, s))
	})

	// Following the first would carry whatever it points at, which is not what
	// the directory holds. Opening the second would block on a read that never
	// returns.
	t.Run("a link is not followed and a pipe is not opened", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("aaa")})
		x.NoError(os.Symlink("/etc/passwd", filepath.Join(root, "link.rec")))

		f := &fakeFS{entries: map[string][]fs.DirEntry{}}
		real, err := f.OS.ReadDir(root)
		x.NoError(err)
		f.entries[root] = append(real, badEntry{name: "pipe.rec", mode: fs.ModeNamedPipe})

		s := newSource(t, dir.Options{Path: root, FS: f})
		x.Equal([]string{"a.rec"}, keys(t, s))
	})

	t.Run("a directory that cannot be read does not hide the others", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a"), "bad/x.rec": []byte("x"), "good/y.rec": []byte("y")})
		f := &fakeFS{readDir: map[string]error{filepath.Join(root, "bad"): errRefused}}

		s := newSource(t, dir.Options{Path: root, Recursive: true, FS: f})

		var (
			ks   []string
			errs []error
		)
		for it, err := range s.Scan(t.Context()) {
			if err != nil {
				errs = append(errs, err)

				continue
			}
			ks = append(ks, it.Key)
		}
		slices.Sort(ks)
		x.Equal([]string{"a.rec", "good/y.rec"}, ks)
		x.Len(errs, 1)
		x.ErrorIs(errs[0], errRefused)
		x.ErrorContains(errs[0], "bad")
	})
	t.Run("something that cannot be looked at is reported and the scan goes on", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a")})
		f := &fakeFS{entries: map[string][]fs.DirEntry{}}
		real, err := f.OS.ReadDir(root)
		x.NoError(err)
		f.entries[root] = append([]fs.DirEntry{badEntry{name: "bad.rec", err: errRefused}}, real...)

		s := newSource(t, dir.Options{Path: root, FS: f})

		var (
			ks   []string
			errs []error
		)
		for it, err := range s.Scan(t.Context()) {
			if err != nil {
				x.Equal("bad.rec", it.Key)
				errs = append(errs, err)

				continue
			}
			ks = append(ks, it.Key)
		}
		x.Equal([]string{"a.rec"}, ks)
		x.Len(errs, 1)
	})
	t.Run("a caller that stops is not walked further, whatever it stopped on", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a")})

		// On a directory it could not read.
		f := &fakeFS{readDir: map[string]error{root: errRefused}}
		s := newSource(t, dir.Options{Path: root, FS: f})
		n := 0
		for range s.Scan(t.Context()) {
			n++

			break
		}
		x.Equal(1, n)

		// And on an entry it could not look at.
		g := &fakeFS{entries: map[string][]fs.DirEntry{root: {badEntry{name: "bad.rec", err: errRefused}}}}
		s = newSource(t, dir.Options{Path: root, FS: g})
		n = 0
		for range s.Scan(t.Context()) {
			n++

			break
		}
		x.Equal(1, n)
	})
}

// A name that has been through a template or a configuration file must not be
// able to reach outside the directory it names.
func TestAKeyThisSourceDidNotGiveOut(t *testing.T) {
	x := require.New(t)

	s := newSource(t, dir.Options{Path: seed(t, map[string][]byte{"a.rec": []byte("a")})})

	for _, key := range []string{"", "..", "../etc/passwd", "/etc/passwd", "a//b", "./a.rec"} {
		t.Run(key, func(t *testing.T) {
			x := require.New(t)

			_, err := s.Open(t.Context(), key)
			x.ErrorContains(err, "is not a key of")

			x.ErrorContains(s.Remove(t.Context(), key), "is not a key of")
			x.ErrorContains(s.Move(t.Context(), key, "done"), "is not a key of")
		})
	}

	// And the one it did give out is fine.
	r, err := s.Open(t.Context(), "a.rec")
	x.NoError(err)
	x.NoError(r.Close())
}

func TestOpenAndRemove(t *testing.T) {
	t.Run("a read that is refused says which file", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a")})
		f := &fakeFS{open: map[string]error{filepath.Join(root, "a.rec"): errRefused}}
		s := newSource(t, dir.Options{Path: root, FS: f})

		_, err := s.Open(t.Context(), "a.rec")
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, "a.rec")
	})
	t.Run("a delete that is refused says which file", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a")})
		f := &fakeFS{remove: map[string]error{filepath.Join(root, "a.rec"): errRefused}}
		s := newSource(t, dir.Options{Path: root, FS: f})

		err := s.Remove(t.Context(), "a.rec")
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, "a.rec")
	})
}

func TestMove(t *testing.T) {
	t.Run("somewhere relative is relative to the directory being watched", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("aaa")})
		s := newSource(t, dir.Options{Path: root})

		x.NoError(s.Move(t.Context(), "a.rec", "done"))
		x.NoFileExists(filepath.Join(root, "a.rec"))
		x.FileExists(filepath.Join(root, "done", "a.rec"))
	})
	t.Run("somewhere absolute is taken as it is", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("aaa")})
		away := filepath.Join(t.TempDir(), "archive")
		s := newSource(t, dir.Options{Path: root})

		x.NoError(s.Move(t.Context(), "a.rec", away))
		x.FileExists(filepath.Join(away, "a.rec"))
	})
	t.Run("nowhere is not somewhere", func(t *testing.T) {
		x := require.New(t)

		s := newSource(t, dir.Options{Path: seed(t, map[string][]byte{"a.rec": []byte("a")})})
		x.ErrorContains(s.Move(t.Context(), "a.rec", ""), "nowhere to move")
	})
	t.Run("a place that cannot be made is said so", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a")})
		f := &fakeFS{mkdir: map[string]error{filepath.Join(root, "done"): errRefused}}
		s := newSource(t, dir.Options{Path: root, FS: f})

		x.ErrorIs(s.Move(t.Context(), "a.rec", "done"), errRefused)
	})
	t.Run("a rename that is refused for any other reason is not worked around", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a")})
		f := &fakeFS{rename: map[string]error{filepath.Join(root, "a.rec"): errRefused}}
		s := newSource(t, dir.Options{Path: root, FS: f})

		err := s.Move(t.Context(), "a.rec", "done")
		x.ErrorIs(err, errRefused)
		// Still there: a move that did not happen must not read as one that did.
		x.FileExists(filepath.Join(root, "a.rec"))
	})
}

// "Keep them, but not here" is exactly what a second disk is for, and a rename
// cannot cross to one.
func TestMoveToAnotherFilesystem(t *testing.T) {
	xdev := func(from string) map[string]error {
		return map[string]error{from: &os.LinkError{Op: "rename", Old: from, Err: syscall.EXDEV}}
	}

	t.Run("it is copied and then the original goes", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("contents")})
		f := &fakeFS{rename: xdev(filepath.Join(root, "a.rec"))}
		s := newSource(t, dir.Options{Path: root, FS: f})

		x.NoError(s.Move(t.Context(), "a.rec", "done"))
		x.NoFileExists(filepath.Join(root, "a.rec"))

		b, err := os.ReadFile(filepath.Join(root, "done", "a.rec"))
		x.NoError(err)
		x.Equal("contents", string(b))
	})
	t.Run("a copy that cannot be started leaves the original alone", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("contents")})
		from := filepath.Join(root, "a.rec")
		f := &fakeFS{rename: xdev(from), open: map[string]error{from: errRefused}}
		s := newSource(t, dir.Options{Path: root, FS: f})

		x.ErrorIs(s.Move(t.Context(), "a.rec", "done"), errRefused)
		x.FileExists(from)
	})
	t.Run("a copy that has nowhere to land leaves the original alone", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("contents")})
		from := filepath.Join(root, "a.rec")
		f := &fakeFS{
			rename: xdev(from),
			create: map[string]error{filepath.Join(root, "done", "a.rec"): errRefused},
		}
		s := newSource(t, dir.Options{Path: root, FS: f})

		x.ErrorIs(s.Move(t.Context(), "a.rec", "done"), errRefused)
		x.FileExists(from)
	})
	t.Run("a copy that fails part way leaves neither a half file nor a lost one", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("contents")})
		from := filepath.Join(root, "a.rec")
		to := filepath.Join(root, "done", "a.rec")

		f := &fakeFS{rename: xdev(from)}
		f.openAs = map[string]io.ReadCloser{from: io.NopCloser(failingReader{})}
		s := newSource(t, dir.Options{Path: root, FS: f})

		x.Error(s.Move(t.Context(), "a.rec", "done"))
		x.FileExists(from)
		// The half-written one is taken away: a file that looks archived and is
		// not is worse than no file at all.
		x.NoFileExists(to)
	})
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRefused }

// failingCloser writes fine and then says the bytes never landed, which is what
// a full disk looks like: the failure arrives at the close, not at the write.
type failingCloser struct{ io.Writer }

func (failingCloser) Close() error { return errRefused }

func TestACopyThatIsRefusedAtTheEnd(t *testing.T) {
	x := require.New(t)

	root := seed(t, map[string][]byte{"a.rec": []byte("contents")})
	from := filepath.Join(root, "a.rec")
	to := filepath.Join(root, "done", "a.rec")

	real, err := os.CreateTemp(t.TempDir(), "landing")
	x.NoError(err)
	defer real.Close()

	f := &fakeFS{
		rename:   map[string]error{from: &os.LinkError{Op: "rename", Old: from, Err: syscall.EXDEV}},
		createAs: map[string]io.WriteCloser{to: failingCloser{Writer: real}},
	}
	s := newSource(t, dir.Options{Path: root, FS: f})

	x.ErrorIs(s.Move(t.Context(), "a.rec", "done"), errRefused)
	// The original is still here, which is the only thing that matters.
	x.FileExists(from)
	x.NoFileExists(to)
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	root := seed(t, map[string][]byte{"a.rec": []byte("aaa"), "b.log": []byte("b")})
	spec := &dir.Spec{Path: root, Include: []string{"*.rec"}, Recursive: true, MinSize: 2}

	s, err := spec.New(t.Context())
	x.NoError(err)
	x.Equal([]string{"a.rec"}, keys(t, s))

	// And one a configuration got wrong is refused when it is read, not when
	// it is first carried from.
	_, err = (&dir.Spec{}).New(t.Context())
	x.ErrorContains(err, "which directory")
}

// The real filesystem answers with nothing rather than with a typed nothing,
// which a caller checking the reader instead of the error would read as a
// reader and then panic on.
func TestTheRealFilesystemRefusesWithANilReader(t *testing.T) {
	x := require.New(t)

	missing := filepath.Join(t.TempDir(), "no", "such", "file")

	r, err := dir.OS{}.Open(missing)
	x.Error(err)
	x.Nil(r)

	w, err := dir.OS{}.Create(missing)
	x.Error(err)
	x.Nil(w)

	// And the ordinary way round.
	p := filepath.Join(t.TempDir(), "a")
	w, err = dir.OS{}.Create(p)
	x.NoError(err)
	x.NoError(w.Close())

	r, err = dir.OS{}.Open(p)
	x.NoError(err)
	x.NoError(r.Close())

	x.NoError(dir.OS{}.Rename(p, p+"b"))
	x.NoError(dir.OS{}.Remove(p + "b"))
	x.NoError(dir.OS{}.MkdirAll(filepath.Join(t.TempDir(), "x", "y"), 0o755))
}
