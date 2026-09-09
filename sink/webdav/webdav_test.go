package webdav_test

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	wickhttp "github.com/heojeongbo/wick/sink/http"
	"github.com/heojeongbo/wick/sink/sinktest"
	"github.com/heojeongbo/wick/sink/webdav"
)

// server is the least a WebDAV server has to be for this: it refuses a PUT into
// a collection that is not there, and it makes one when it is asked to.
type server struct {
	mu    sync.Mutex
	objs  map[string][]byte
	dirs  map[string]bool
	hdrs  map[string]http.Header
	mkcol []string

	// mkcolStatus, when set, is what MKCOL is answered with instead.
	mkcolStatus int
	// putStatus, when set, is what PUT is answered with instead.
	putStatus int
	// headStatus, when set, is what HEAD is answered with instead.
	headStatus int
}

func newServer() *server {
	return &server{
		objs: map[string][]byte{},
		dirs: map[string]bool{".": true},
		hdrs: map[string]http.Header{},
	}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[1:]

	s.mu.Lock()
	defer s.mu.Unlock()

	switch r.Method {
	case "MKCOL":
		s.mkcol = append(s.mkcol, name)
		if s.mkcolStatus != 0 {
			w.WriteHeader(s.mkcolStatus)

			return
		}
		if s.dirs[name] {
			// What a server says about a collection that is already there.
			w.WriteHeader(http.StatusMethodNotAllowed)

			return
		}
		if !s.dirs[path.Dir(name)] {
			w.WriteHeader(http.StatusConflict)

			return
		}
		s.dirs[name] = true
		w.WriteHeader(http.StatusCreated)

	case http.MethodPut:
		if s.putStatus != 0 {
			w.WriteHeader(s.putStatus)

			return
		}
		if !s.dirs[path.Dir(name)] {
			// The whole reason this sink is not the http one.
			w.WriteHeader(http.StatusConflict)

			return
		}

		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}
		s.objs[name] = b
		s.hdrs[name] = r.Header.Clone()
		w.WriteHeader(http.StatusCreated)

	case http.MethodHead:
		if s.headStatus != 0 {
			w.WriteHeader(s.headStatus)

			return
		}
		if s.dirs[name] {
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusOK)

			return
		}

		b, ok := s.objs[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func serve(t *testing.T, s *server) string {
	t.Helper()

	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	return srv.URL
}

func newSink(t *testing.T, o webdav.Options) *webdav.Sink {
	t.Helper()

	s, err := webdav.New(o)
	require.NoError(t, err)

	return s
}

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink {
		return newSink(t, webdav.Options{Endpoint: serve(t, newServer())})
	})
}

func TestNames(t *testing.T) {
	sinktest.Escapes(t, func(t *testing.T) sink.Sink {
		return newSink(t, webdav.Options{Endpoint: serve(t, newServer())})
	})
}

func TestNew(t *testing.T) {
	t.Run("a sink has to say where to put things", func(t *testing.T) {
		x := require.New(t)

		_, err := webdav.New(webdav.Options{})
		x.ErrorContains(err, "where to put things")
	})
	t.Run("an address that is not one is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := webdav.New(webdav.Options{Endpoint: "://nope"})
		x.ErrorContains(err, "read the address")

		_, err = webdav.New(webdav.Options{Endpoint: "ftp://host/dav"})
		x.ErrorContains(err, "http or https")
	})
	t.Run("two answers to the same question is not an answer", func(t *testing.T) {
		x := require.New(t)

		_, err := webdav.New(webdav.Options{
			Endpoint: "https://host",
			Auth:     wickhttp.Auth{Username: "wick", TokenFile: "/tmp/t"},
		})
		x.ErrorContains(err, "two answers to the same question")
	})
	t.Run("a certificate without its key is refused before anything is sent", func(t *testing.T) {
		x := require.New(t)

		_, err := webdav.New(webdav.Options{
			Endpoint: "https://host",
			TLS:      wickhttp.TLS{CertFile: "only.pem"},
		})
		x.ErrorContains(err, "go together")
	})
	t.Run("a client that was handed over is the one that is used", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv), Client: &http.Client{}})
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})
}

// The whole reason this is not the http sink under another name.
func TestCollections(t *testing.T) {
	t.Run("the ones a name needs are made, outermost first", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		x.NoError(s.Put(t.Context(), "a/b/c.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		x.Equal([]string{"a", "a/b"}, srv.mkcol)
		x.Equal([]byte("xyz"), srv.objs["a/b/c.rec"])
	})

	t.Run("and made once, however many files go into them", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		for _, n := range []string{"a/b/one.rec", "a/b/two.rec", "a/b/three.rec"} {
			x.NoError(s.Put(t.Context(), n, bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		}
		// Two, not six: a spool writing a thousand files into one directory
		// makes it once.
		x.Equal([]string{"a", "a/b"}, srv.mkcol)
	})

	t.Run("a name with no collection in it asks for none", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		x.Empty(srv.mkcol)
	})

	t.Run("one that was already there is the answer that was wanted", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		srv.dirs["a"] = true
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		x.NoError(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})

	// The other way a server says it: a conflict, which also means "the one
	// above this is not there". Asking is what tells them apart.
	t.Run("a conflict about one that is there is not a failure", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		srv.dirs["a"] = true
		srv.mkcolStatus = http.StatusConflict
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		x.NoError(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})
	t.Run("and a conflict about one that is not is", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		srv.mkcolStatus = http.StatusConflict
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		err := s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "making the collection")
	})
	t.Run("any other answer to making one is said", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		srv.mkcolStatus = http.StatusForbidden
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		err := s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "403")
	})
}

func TestPut(t *testing.T) {
	t.Run("an answer that is not a yes is not read as one", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		srv.putStatus = http.StatusForbidden
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "403")
	})
	t.Run("a server that cannot be reached is said so", func(t *testing.T) {
		x := require.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		s := newSink(t, webdav.Options{Endpoint: addr})
		x.ErrorContains(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}), "put")
		x.ErrorContains(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}), "make the collection")

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorContains(err, "ask about")
	})
	t.Run("a length nobody claimed is not made up", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: -1}))
		x.Equal([]byte("xyz"), srv.objs["a.rec"])
	})
	t.Run("bytes are sent no faster than they were told to be", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv), RateLimit: 1 << 30})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		x.Equal([]byte("xyz"), srv.objs["a.rec"])
	})
	t.Run("who is asking is said on every request", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "token")
		x.NoError(os.WriteFile(p, []byte("sesame\n"), 0o600))

		srv := newServer()
		s := newSink(t, webdav.Options{
			Endpoint: serve(t, srv),
			Auth:     wickhttp.Auth{TokenFile: p},
		})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		x.Equal("Bearer sesame", srv.hdrs["a.rec"].Get("Authorization"))
	})
	t.Run("a token that is not there is said so, on every request that needed it", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{
			Endpoint: serve(t, srv),
			Auth:     wickhttp.Auth{TokenFile: filepath.Join(t.TempDir(), "gone")},
		})

		x.ErrorContains(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}), "read the token")
		x.ErrorContains(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}), "read the token")

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorContains(err, "read the token")
	})
}

func TestStat(t *testing.T) {
	t.Run("what the server does not hold is not-there", func(t *testing.T) {
		x := require.New(t)

		s := newSink(t, webdav.Options{Endpoint: serve(t, newServer())})

		_, err := s.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})
	t.Run("one that is gone is also not-there", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		srv.headStatus = http.StatusGone
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})
	t.Run("what it will not say about is not not-there", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		srv.headStatus = http.StatusInternalServerError
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorContains(err, "500")
		x.NotErrorIs(err, fs.ErrNotExist)
	})

	// WebDAV keeps no hash of its own, so the read-back is a read-back of the
	// length. Said here so that nobody is left thinking otherwise.
	t.Run("it says the length and keeps no hash", func(t *testing.T) {
		x := require.New(t)

		srv := newServer()
		s := newSink(t, webdav.Options{Endpoint: serve(t, srv)})
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
		x.Empty(m.Digest)
	})
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	srv := newServer()
	s, err := (&webdav.Spec{
		Endpoint: serve(t, srv),
		Headers:  map[string]string{"X-Robot": "thor-top"},
		Username: "wick", Password: "sesame",
		RateLimit: 1024,
	}).New(t.Context())
	x.NoError(err)
	x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	x.Equal("thor-top", srv.hdrs["a.rec"].Get("X-Robot"))

	_, err = (&webdav.Spec{}).New(t.Context())
	x.ErrorContains(err, "where to put things")

	_, err = (&webdav.Spec{Endpoint: "https://host", CertFile: "only.pem"}).New(t.Context())
	x.ErrorContains(err, "go together")
}

// Importing this package is what makes `type: webdav` mean something.
func TestItRegistersItself(t *testing.T) {
	x := require.New(t)

	x.Contains(sink.Kinds(), webdav.Kind)
}

func TestItIsMadeFromTheRegistry(t *testing.T) {
	x := require.New(t)

	spec, err := sink.NewSpec(webdav.Kind)
	x.NoError(err)
	x.IsType(&webdav.Spec{}, spec)
}
