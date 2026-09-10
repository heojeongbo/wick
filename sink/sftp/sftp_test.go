package sftp_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	wicksftp "github.com/heojeongbo/wick/sink/sftp"
	"github.com/heojeongbo/wick/sink/sinktest"
)

var errRefused = errors.New("refused")

// fake is an SFTP connection kept in memory, for the failures. Each of the six
// calls can be made to refuse; the real server next door is what says the
// interface is one a real connection satisfies.
type fake struct {
	mu    sync.Mutex
	files map[string][]byte
	dirs  []string

	create   map[string]error
	stat     map[string]error
	remove   map[string]error
	rename   map[string]error
	mkdir    map[string]error
	closeErr error

	// createAs hands back a writer of the test's choosing, for the failures
	// that arrive part way through rather than at the start.
	createAs map[string]io.WriteCloser

	closed bool
}

func newFake() *fake { return &fake{files: map[string][]byte{}} }

func (f *fake) Create(p string) (io.WriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.create[p]; ok {
		return nil, err
	}
	if w, ok := f.createAs[p]; ok {
		return w, nil
	}

	return &fakeFile{f: f, path: p}, nil
}

func (f *fake) Stat(p string) (os.FileInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.stat[p]; ok {
		return nil, err
	}

	b, ok := f.files[p]
	if !ok {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}

	return fakeInfo{name: path.Base(p), size: int64(len(b))}, nil
}

func (f *fake) Remove(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.remove[p]; ok {
		return err
	}
	delete(f.files, p)

	return nil
}

func (f *fake) Rename(from, to string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.rename[from]; ok {
		return err
	}

	b, ok := f.files[from]
	if !ok {
		return &fs.PathError{Op: "rename", Path: from, Err: fs.ErrNotExist}
	}
	delete(f.files, from)
	f.files[to] = b

	return nil
}

func (f *fake) MkdirAll(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.mkdir[p]; ok {
		return err
	}
	f.dirs = append(f.dirs, p)

	return nil
}

func (f *fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true

	return f.closeErr
}

func (f *fake) data(p string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.files[p]

	return b, ok
}

type fakeFile struct {
	f    *fake
	path string
	buf  bytes.Buffer
}

func (w *fakeFile) Write(p []byte) (int, error) { return w.buf.Write(p) }

func (w *fakeFile) Close() error {
	w.f.mu.Lock()
	defer w.f.mu.Unlock()

	w.f.files[w.path] = bytes.Clone(w.buf.Bytes())

	return nil
}

type fakeInfo struct {
	name string
	size int64
}

func (i fakeInfo) Name() string     { return i.name }
func (i fakeInfo) Size() int64      { return i.size }
func (fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (fakeInfo) ModTime() time.Time { return time.Time{} }
func (fakeInfo) IsDir() bool        { return false }
func (fakeInfo) Sys() any           { return nil }

// onFake is a sink talking to one of those.
func onFake(t *testing.T, f *fake, o wicksftp.Options) *wicksftp.Sink {
	t.Helper()

	if o.Address == "" {
		o.Address = "files.example.invalid"
	}
	if o.User == "" {
		o.User = "wick"
	}
	if o.Password == "" && o.KeyFile == "" {
		o.Password = "sesame"
	}
	o.InsecureIgnoreHostKey = true
	o.Dial = func(context.Context, wicksftp.Options) (wicksftp.Client, error) { return f, nil }

	s, err := wicksftp.New(o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return onFake(t, newFake(), wicksftp.Options{}) })
}

func TestNames(t *testing.T) {
	sinktest.Escapes(t, func(t *testing.T) sink.Sink { return onFake(t, newFake(), wicksftp.Options{}) })
}

func TestNew(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    wicksftp.Options
		says string
	}{
		{"a sink has to say which server", wicksftp.Options{User: "a", Password: "b", InsecureIgnoreHostKey: true}, "which server"},
		{"and which account", wicksftp.Options{Address: "h", Password: "b", InsecureIgnoreHostKey: true}, "which account"},
		{"and how to log in", wicksftp.Options{Address: "h", User: "a", InsecureIgnoreHostKey: true}, "a key file or a password"},
		{"and only one way", wicksftp.Options{Address: "h", User: "a", KeyFile: "k", Password: "b", InsecureIgnoreHostKey: true}, "two answers to the same question"},
		// A host key nobody checks is a sink that will one day be somebody else.
		{"and who the server is", wicksftp.Options{Address: "h", User: "a", Password: "b"}, "known_hosts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			_, err := wicksftp.New(tc.o)
			x.ErrorContains(err, tc.says)
		})
	}
}

func TestPut(t *testing.T) {
	t.Run("it writes beside the name and then renames", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wicksftp.Options{Path: "/srv/recordings/"})

		x.NoError(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		// Under the path it was given, and the directory made first.
		b, ok := f.data("srv/recordings/a/b.rec")
		x.True(ok)
		x.Equal("xyz", string(b))
		x.Equal([]string{"srv/recordings/a"}, f.dirs)

		// Nothing left beside it.
		_, ok = f.data("srv/recordings/a/.b.rec.partial")
		x.False(ok)
	})

	t.Run("a name with no directory in it asks for none", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wicksftp.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		x.Empty(f.dirs)
	})

	// A stream that ends early looks exactly like a stream that ended.
	t.Run("a stream that ends early never becomes a file under the whole name", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wicksftp.Options{})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("short")), sink.Meta{Size: 100})
		x.ErrorContains(err, "to be 100 bytes and 5 arrived")

		_, ok := f.data("a.rec")
		x.False(ok)
		_, ok = f.data(".a.rec.partial")
		x.False(ok)
	})

	t.Run("a length nobody claimed is not checked", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wicksftp.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("anything")), sink.Meta{Size: -1}))
		_, ok := f.data("a.rec")
		x.True(ok)
	})

	for _, tc := range []struct {
		name  string
		set   func(*fake)
		says  string
		after func(*require.Assertions, *fake)
	}{
		{
			"a directory that cannot be made",
			func(f *fake) { f.mkdir = map[string]error{"a": errRefused} },
			"make the directory",
			nil,
		},
		{
			"a file that cannot be made",
			func(f *fake) { f.create = map[string]error{"a/.b.rec.partial": errRefused} },
			"make",
			nil,
		},
		{
			"a rename that is refused",
			func(f *fake) { f.rename = map[string]error{"a/.b.rec.partial": errRefused} },
			"put",
			func(x *require.Assertions, f *fake) {
				_, ok := f.data("a/b.rec")
				x.False(ok)
			},
		},
	} {
		t.Run(tc.name+" is said so", func(t *testing.T) {
			x := require.New(t)

			f := newFake()
			tc.set(f)
			s := onFake(t, f, wicksftp.Options{})

			err := s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3})
			x.ErrorIs(err, errRefused)
			x.ErrorContains(err, tc.says)

			if tc.after != nil {
				tc.after(x, f)
			}
		})
	}

	t.Run("a read that gives out part way leaves nothing behind", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wicksftp.Options{})

		x.ErrorIs(s.Put(t.Context(), "a.rec", failingReader{}, sink.Meta{Size: 9}), errRefused)
		_, ok := f.data(".a.rec.partial")
		x.False(ok)
	})

	// The failure arrives at the close, not at the write, which is what a full
	// disk looks like.
	t.Run("a write that is refused at the end leaves nothing behind", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.createAs = map[string]io.WriteCloser{".a.rec.partial": failingCloser{}}
		s := onFake(t, f, wicksftp.Options{})

		x.ErrorIs(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}), errRefused)
		_, ok := f.data("a.rec")
		x.False(ok)
	})

	t.Run("bytes are sent no faster than they were told to be", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wicksftp.Options{RateLimit: 1 << 30})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		b, _ := f.data("a.rec")
		x.Equal("xyz", string(b))
	})
}

func TestStat(t *testing.T) {
	t.Run("it says the length and keeps no hash", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wicksftp.Options{})
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
		x.Empty(m.Digest)
	})
	t.Run("a look that is refused is told from a thing that is not there", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.stat = map[string]error{"a.rec": errRefused}
		s := onFake(t, f, wicksftp.Options{})

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorIs(err, errRefused)
		x.NotErrorIs(err, fs.ErrNotExist)
	})
}

// A spool carrying a thousand files is a thousand handshakes otherwise, and the
// handshake is the expensive part of talking to one of these.
func TestTheConnectionIsMadeOnceAndLetGoOf(t *testing.T) {
	x := require.New(t)

	f := newFake()
	dials := 0

	s, err := wicksftp.New(wicksftp.Options{
		Address: "h", User: "a", Password: "b", InsecureIgnoreHostKey: true,
		Dial: func(context.Context, wicksftp.Options) (wicksftp.Client, error) {
			dials++

			return f, nil
		},
	})
	x.NoError(err)

	for _, n := range []string{"a.rec", "b.rec", "c.rec"} {
		x.NoError(s.Put(t.Context(), n, bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	}
	_, err = s.Stat(t.Context(), "a.rec")
	x.NoError(err)
	x.Equal(1, dials)

	x.NoError(s.Close())
	x.True(f.closed)

	// Twice is not a crash, and the second one has nothing to let go of.
	x.NoError(s.Close())
}

func TestAConnectionThatCannotBeMade(t *testing.T) {
	x := require.New(t)

	s, err := wicksftp.New(wicksftp.Options{
		Address: "h", User: "a", Password: "b", InsecureIgnoreHostKey: true,
		Dial: func(context.Context, wicksftp.Options) (wicksftp.Client, error) { return nil, errRefused },
	})
	x.NoError(err)

	// What it said, rather than what it wraps. The chain is flattened here on
	// purpose, and the test below is why.
	err = s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
	x.ErrorContains(err, "reach")
	x.ErrorContains(err, errRefused.Error())

	_, err = s.Stat(t.Context(), "a.rec")
	x.ErrorContains(err, errRefused.Error())
}

// A server that cannot be reached must not be mistaken for one that does not
// hold the name.
//
// Dialling reads the private key and the known_hosts, and either being absent
// is an *fs.PathError. Left in the chain it would make [Sink.Stat] answer
// [fs.ErrNotExist] -- the one answer the engine acts on, which it reads as a
// write that did not stick. It would send the file again every pass until the
// item was set aside, and `wick check` would call the sink reachable.
func TestAConnectionFailureIsNotAMissingName(t *testing.T) {
	for _, tt := range []struct {
		what string
		opts wicksftp.Options
	}{
		{"a known_hosts that is not there", wicksftp.Options{
			Address: "h", User: "a", Password: "b",
			KnownHosts: filepath.Join(t.TempDir(), "nope"),
		}},
		{"a key file that is not there", wicksftp.Options{
			Address: "h", User: "a", InsecureIgnoreHostKey: true,
			KeyFile: filepath.Join(t.TempDir(), "nope"),
		}},
	} {
		t.Run(tt.what, func(t *testing.T) {
			x := require.New(t)

			s, err := wicksftp.New(tt.opts)
			x.NoError(err)

			_, err = s.Stat(t.Context(), "a.rec")
			x.Error(err)
			x.NotErrorIs(err, fs.ErrNotExist,
				"a server that could not be reached looks exactly like one that does not hold the name")
		})
	}
}

func TestAConnectionThatWillNotBeLetGoOf(t *testing.T) {
	x := require.New(t)

	f := newFake()
	f.closeErr = errRefused
	s := onFake(t, f, wicksftp.Options{})

	x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	x.ErrorIs(s.Close(), errRefused)
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	s, err := (&wicksftp.Spec{
		Address: "files.example.invalid:2222", User: "wick", Password: "sesame",
		InsecureIgnoreHostKey: true, Path: "/srv", Timeout: time.Second, RateLimit: 1024,
	}).New(t.Context())
	x.NoError(err)
	x.NotNil(s)

	_, err = (&wicksftp.Spec{}).New(t.Context())
	x.ErrorContains(err, "which server")
}

// Importing this package is what makes `type: sftp` mean something.
func TestItRegistersItself(t *testing.T) {
	x := require.New(t)

	x.Contains(sink.Kinds(), wicksftp.Kind)

	spec, err := sink.NewSpec(wicksftp.Kind)
	x.NoError(err)
	x.IsType(&wicksftp.Spec{}, spec)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errRefused }

type failingCloser struct{}

func (failingCloser) Write(p []byte) (int, error) { return len(p), nil }
func (failingCloser) Close() error                { return errRefused }
