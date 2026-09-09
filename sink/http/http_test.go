package http_test

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	wickhttp "github.com/heojeongbo/wick/sink/http"
	"github.com/heojeongbo/wick/sink/sinktest"
)

// store is a server that keeps what it is put, which is the least a collector
// on the other end of this has to do.
type store struct {
	mu   sync.Mutex
	objs map[string][]byte
	hdrs map[string]http.Header

	digestHeader string
	status       int
	statusFor    map[string]int
}

func newStore() *store {
	return &store{objs: map[string][]byte{}, hdrs: map[string]http.Header{}}
}

func (s *store) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[1:]

	s.mu.Lock()
	defer s.mu.Unlock()

	if code, ok := s.statusFor[name]; ok {
		w.WriteHeader(code)

		return
	}
	if s.status != 0 {
		w.WriteHeader(s.status)

		return
	}

	switch r.Method {
	case http.MethodPut, http.MethodPost:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)

			return
		}
		s.objs[name] = b
		s.hdrs[name] = r.Header.Clone()
		w.WriteHeader(http.StatusCreated)

	case http.MethodHead:
		b, ok := s.objs[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if s.digestHeader != "" {
			w.Header().Set(s.digestHeader, s.hdrs[name].Get(s.digestHeader))
		}
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func serve(t *testing.T, s *store) string {
	t.Helper()

	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)

	return srv.URL
}

func newSink(t *testing.T, o wickhttp.Options) *wickhttp.Sink {
	t.Helper()

	s, err := wickhttp.New(o)
	require.NoError(t, err)

	return s
}

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink {
		return newSink(t, wickhttp.Options{Endpoint: serve(t, newStore())})
	})
}

func TestNames(t *testing.T) {
	sinktest.Escapes(t, func(t *testing.T) sink.Sink {
		return newSink(t, wickhttp.Options{Endpoint: serve(t, newStore())})
	})
}

func TestNew(t *testing.T) {
	t.Run("a sink has to say where to put things", func(t *testing.T) {
		x := require.New(t)

		_, err := wickhttp.New(wickhttp.Options{})
		x.ErrorContains(err, "where to put things")
	})
	t.Run("an address that is not one is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := wickhttp.New(wickhttp.Options{Endpoint: "://nope"})
		x.ErrorContains(err, "read the address")

		_, err = wickhttp.New(wickhttp.Options{Endpoint: "ftp://host/drop"})
		x.ErrorContains(err, "http or https")
	})
	t.Run("a method that is not a way to put something is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := wickhttp.New(wickhttp.Options{Endpoint: "https://host", Method: "DELETE"})
		x.ErrorContains(err, "PUT or POST")
	})
	t.Run("a client that was handed over is the one that is used", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := newSink(t, wickhttp.Options{Endpoint: serve(t, st), Client: &http.Client{}})
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})
	t.Run("a certificate without its key is refused before anything is sent", func(t *testing.T) {
		x := require.New(t)

		_, err := wickhttp.New(wickhttp.Options{
			Endpoint: "https://host",
			TLS:      wickhttp.TLS{CertFile: "only-the-certificate.pem"},
		})
		x.ErrorContains(err, "go together")
	})
}

func TestPut(t *testing.T) {
	t.Run("the name is joined onto the address", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := newSink(t, wickhttp.Options{Endpoint: serve(t, st) + "/drop"})

		x.NoError(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		x.Equal([]byte("xyz"), st.objs["drop/a/b.rec"])
	})
	t.Run("the headers a server asked for are sent with everything", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		st.digestHeader = "X-Content-Sha256"
		s := newSink(t, wickhttp.Options{
			Endpoint:     serve(t, st),
			Method:       http.MethodPost,
			Auth:         wickhttp.Auth{Headers: map[string]string{"Authorization": "Bearer sesame"}},
			DigestHeader: "X-Content-Sha256",
		})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1, Digest: "sha256:beef"}))
		x.Equal("Bearer sesame", st.hdrs["a.rec"].Get("Authorization"))
		x.Equal("sha256:beef", st.hdrs["a.rec"].Get("X-Content-Sha256"))

		// And read back out again.
		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal("sha256:beef", m.Digest)
	})

	// A body of unknown length is sent chunked, and a server that has to
	// reassemble the chunks cannot refuse a short one until it has taken all
	// of it.
	t.Run("the length is said rather than left to be guessed", func(t *testing.T) {
		x := require.New(t)

		var got int64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.ContentLength
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		s := newSink(t, wickhttp.Options{Endpoint: srv.URL})
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		x.Equal(int64(3), got)

		// And a length nobody claimed is not made up.
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: -1}))
		x.Equal(int64(-1), got)
	})

	t.Run("an answer that is not a yes is not read as one", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		st.status = http.StatusForbidden
		s := newSink(t, wickhttp.Options{Endpoint: serve(t, st)})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "403")
	})
	t.Run("a server that cannot be reached is said so", func(t *testing.T) {
		x := require.New(t)

		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		addr := srv.URL
		srv.Close()

		s := newSink(t, wickhttp.Options{Endpoint: addr})
		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "put")

		_, err = s.Stat(t.Context(), "a.rec")
		x.ErrorContains(err, "ask about")
	})
	t.Run("bytes are sent no faster than they were told to be", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := newSink(t, wickhttp.Options{Endpoint: serve(t, st), RateLimit: 1 << 30})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		x.Equal([]byte("xyz"), st.objs["a.rec"])
	})
}

// Not there has to be told from cannot-say, or the engine reads an outage as a
// reason to send half a gigabyte again.
func TestStat(t *testing.T) {
	t.Run("what the server does not hold is not-there", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		st.statusFor = map[string]int{"gone.rec": http.StatusGone}
		s := newSink(t, wickhttp.Options{Endpoint: serve(t, st)})

		_, err := s.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)

		_, err = s.Stat(t.Context(), "gone.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})
	t.Run("what it will not say about is not not-there", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		st.statusFor = map[string]int{"a.rec": http.StatusInternalServerError}
		s := newSink(t, wickhttp.Options{Endpoint: serve(t, st)})

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorContains(err, "500")
		x.NotErrorIs(err, fs.ErrNotExist)
	})
}

func TestTransport(t *testing.T) {
	t.Run("nothing said means the machine's own trust and no certificate", func(t *testing.T) {
		x := require.New(t)

		tr, err := wickhttp.NewTransport(wickhttp.TLS{})
		x.NoError(err)
		// Whatever the standard transport was already going to do, untouched.
		x.False(tr.TLSClientConfig.InsecureSkipVerify)
		x.Nil(tr.TLSClientConfig.RootCAs)
		x.Nil(tr.TLSClientConfig.GetClientCertificate)
	})
	t.Run("an authority that is not one is refused", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "ca.pem")
		x.NoError(os.WriteFile(p, []byte("not a certificate"), 0o600))

		_, err := wickhttp.NewTransport(wickhttp.TLS{CAFile: p})
		x.ErrorContains(err, "no certificate this can read")

		_, err = wickhttp.NewTransport(wickhttp.TLS{CAFile: filepath.Join(t.TempDir(), "nope.pem")})
		x.ErrorContains(err, "read the authority")
	})
	t.Run("an authority that is one is believed", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		certFile := filepath.Join(dir, "ca.pem")
		writePair(t, certFile, filepath.Join(dir, "ca.key"))

		tr, err := wickhttp.NewTransport(wickhttp.TLS{CAFile: certFile})
		x.NoError(err)
		x.NotNil(tr.TLSClientConfig.RootCAs)
	})
	t.Run("not checking the server's name is a decision somebody wrote down", func(t *testing.T) {
		x := require.New(t)

		tr, err := wickhttp.NewTransport(wickhttp.TLS{Insecure: true})
		x.NoError(err)
		x.True(tr.TLSClientConfig.InsecureSkipVerify)
	})

	// A client certificate on a machine like this is often a short-lived one
	// that something else renews underneath. Holding the first one would mean
	// working for an hour and then failing until somebody restarts the daemon.
	t.Run("a client certificate is read again at every handshake", func(t *testing.T) {
		x := require.New(t)

		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		dir := t.TempDir()
		certFile := filepath.Join(dir, "client.crt")
		keyFile := filepath.Join(dir, "client.key")
		writePair(t, certFile, keyFile)

		tr, err := wickhttp.NewTransport(wickhttp.TLS{CertFile: certFile, KeyFile: keyFile, Insecure: true})
		x.NoError(err)
		x.NotNil(tr.TLSClientConfig.GetClientCertificate)

		pair, err := tr.TLSClientConfig.GetClientCertificate(nil)
		x.NoError(err)
		x.NotNil(pair)

		// And when it is taken away, the next handshake is the one that finds
		// out -- not a startup an hour ago that could not have known.
		x.NoError(os.Remove(certFile))
		_, err = tr.TLSClientConfig.GetClientCertificate(nil)
		x.ErrorContains(err, "read the client certificate")

		s := newSink(t, wickhttp.Options{Endpoint: srv.URL, Client: srv.Client()})
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})
	t.Run("a certificate that is not one is refused at startup", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		certFile := filepath.Join(dir, "client.crt")
		keyFile := filepath.Join(dir, "client.key")
		x.NoError(os.WriteFile(certFile, []byte("not a certificate"), 0o600))
		x.NoError(os.WriteFile(keyFile, []byte("not a key"), 0o600))

		_, err := wickhttp.NewTransport(wickhttp.TLS{CertFile: certFile, KeyFile: keyFile})
		x.ErrorContains(err, "read the client certificate")
	})
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	st := newStore()
	s, err := (&wickhttp.Spec{
		Endpoint: serve(t, st), Method: http.MethodPost,
		Headers:      map[string]string{"X-Robot": "thor-top"},
		Username:     "wick",
		Password:     "sesame",
		DigestHeader: "X-Content-Sha256", RateLimit: 1024,
	}).New(t.Context())
	x.NoError(err)
	x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	x.Equal([]byte("x"), st.objs["a.rec"])
	x.Equal("thor-top", st.hdrs["a.rec"].Get("X-Robot"))

	user, _, _ := basicOf(st.hdrs["a.rec"])
	x.Equal("wick", user)

	_, err = (&wickhttp.Spec{Endpoint: "https://host", Username: "a", TokenFile: "/t"}).New(t.Context())
	x.ErrorContains(err, "two answers")

	_, err = (&wickhttp.Spec{}).New(t.Context())
	x.ErrorContains(err, "where to put things")

	_, err = (&wickhttp.Spec{Endpoint: "https://host", CertFile: "only.pem"}).New(t.Context())
	x.ErrorContains(err, "go together")
}

// Three ways to say who is asking. They are separate fields rather than one
// header map because a deployment that has to spell "Basic " and base64 by hand
// is a deployment that will one day spell it wrong, and the mistake looks like
// a server refusing rather than a client asking wrongly.
func TestSayingWhoIsAsking(t *testing.T) {
	t.Run("a username and a password are Basic", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := newSink(t, wickhttp.Options{
			Endpoint: serve(t, st),
			Auth:     wickhttp.Auth{Username: "wick", Password: "sesame"},
		})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))

		user, pass, ok := basicOf(st.hdrs["a.rec"])
		x.True(ok)
		x.Equal("wick", user)
		x.Equal("sesame", pass)
	})

	// On a machine like this it is something else's job to renew it, and
	// holding the first one means working until it expires and then failing
	// until somebody restarts the daemon.
	t.Run("a token is read again at every request", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "token")
		x.NoError(os.WriteFile(p, []byte("first\n"), 0o600))

		st := newStore()
		s := newSink(t, wickhttp.Options{
			Endpoint: serve(t, st),
			Auth:     wickhttp.Auth{TokenFile: p},
		})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		// Trimmed: a token in a file is a token with a newline after it, and a
		// server sent one is a server that says no.
		x.Equal("Bearer first", st.hdrs["a.rec"].Get("Authorization"))

		x.NoError(os.WriteFile(p, []byte("second"), 0o600))

		x.NoError(s.Put(t.Context(), "b.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		x.Equal("Bearer second", st.hdrs["b.rec"].Get("Authorization"))
	})

	t.Run("a token that is not there is said so, on the request that needed it", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "gone")
		st := newStore()
		s := newSink(t, wickhttp.Options{
			Endpoint: serve(t, st),
			Auth:     wickhttp.Auth{TokenFile: p},
		})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorContains(err, "read the token")

		_, err = s.Stat(t.Context(), "a.rec")
		x.ErrorContains(err, "read the token")
	})

	t.Run("two answers to the same question is not an answer", func(t *testing.T) {
		x := require.New(t)

		_, err := wickhttp.New(wickhttp.Options{
			Endpoint: "https://host",
			Auth:     wickhttp.Auth{Username: "wick", TokenFile: "/tmp/token"},
		})
		x.ErrorContains(err, "two answers to the same question")

		_, err = wickhttp.New(wickhttp.Options{
			Endpoint: "https://host",
			Auth:     wickhttp.Auth{Password: "sesame"},
		})
		x.ErrorContains(err, "without a username")
	})
}

func basicOf(h http.Header) (string, string, bool) {
	r := &http.Request{Header: h}

	return r.BasicAuth()
}
