package azure_test

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	wickazure "github.com/heojeongbo/wick/sink/azure"
)

// The real blob client, through a transport this test answers.
//
// The fake next door is what the failures are tested through. This is what says
// the narrow interface is one the real client satisfies, and -- the part worth
// having -- that the SHA-256 this puts in the blob's metadata is the one that
// comes back out of its properties. Azure canonicalizes metadata names, so this
// is also what proves the folding is right rather than assumed.
//
// A RoundTripper rather than an httptest server, because it sees every request
// the client makes, so what is answered here is built from what the client
// actually asks.
type store struct {
	mu   sync.Mutex
	objs map[string][]byte
	meta map[string]map[string]string

	// status, when set, is what everything is answered with instead.
	status int
	// code is the Azure error code that goes with it.
	code string
}

func newStore() *store {
	return &store{objs: map[string][]byte{}, meta: map[string]map[string]string{}}
}

func (s *store) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.status != 0 {
		return s.reply(r, s.status, nil, nil), nil
	}

	name := strings.TrimPrefix(r.URL.Path, "/recordings/")

	switch r.Method {
	case http.MethodPut:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		s.objs[name] = b

		// The SDK writes these headers itself, in lower case, rather than
		// through the canonicalizing setter -- which is worth knowing, since a
		// reader that looked for "X-Ms-Meta-" would find nothing.
		m := map[string]string{}
		for k, v := range r.Header {
			if after, ok := strings.CutPrefix(strings.ToLower(k), "x-ms-meta-"); ok && len(v) > 0 {
				m[after] = v[0]
			}
		}
		s.meta[name] = m

		return s.reply(r, http.StatusCreated, nil, nil), nil

	case http.MethodHead:
		b, ok := s.objs[name]
		if !ok {
			return s.reply(r, http.StatusNotFound, nil, map[string]string{
				"x-ms-error-code": "BlobNotFound",
			}), nil
		}

		h := map[string]string{"Content-Length": strconv.Itoa(len(b))}
		for k, v := range s.meta[name] {
			// Answered the way the service does: canonicalized, which is what
			// the folding on the way back in is for.
			h["x-ms-meta-"+strings.ToUpper(k[:1])+k[1:]] = v
		}

		return s.reply(r, http.StatusOK, b, h), nil
	}

	return s.reply(r, http.StatusMethodNotAllowed, nil, nil), nil
}

func (s *store) reply(r *http.Request, code int, body []byte, extra map[string]string) *http.Response {
	h := http.Header{}
	h.Set("x-ms-version", "2025-01-05")
	h.Set("x-ms-request-id", "wick-test")
	if code >= 400 && s.code != "" {
		h.Set("x-ms-error-code", s.code)
	}
	for k, v := range extra {
		h.Set(k, v)
	}

	res := &http.Response{
		StatusCode:    code,
		Status:        strconv.Itoa(code) + " " + http.StatusText(code),
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader(nil)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
	if code == http.StatusOK && r.Method == http.MethodHead {
		// A HEAD carries the length and no body.
		res.ContentLength = int64(len(body))
	}

	return res
}

func onReal(t *testing.T, st *store, o wickazure.Options) *wickazure.Sink {
	t.Helper()

	if o.Container == "" {
		o.Container = "recordings"
	}
	o.ServiceURL = "https://wick.blob.core.windows.invalid/"
	// A shared key, so that nothing goes looking for a managed identity.
	o.Account = "wick"
	o.AccountKey = "aGVsbG8gd29ybGQ="
	o.Transport = &http.Client{Transport: st}

	s, err := wickazure.New(t.Context(), o)
	require.NoError(t, err)

	return s
}

func TestAgainstTheRealClient(t *testing.T) {
	t.Run("what is put is what is held, and the hash survives the round trip", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := onReal(t, st, wickazure.Options{})

		x.NoError(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("contents")), sink.Meta{
			Size:   8,
			Digest: "sha256:beef",
		}))

		x.Equal([]byte("contents"), st.objs["a/b.rec"])

		m, err := s.Stat(t.Context(), "a/b.rec")
		x.NoError(err)
		x.Equal(int64(8), m.Size)
		// Azure canonicalizes the name; what comes back is folded, which is
		// the whole reason this test is here and not only the fake.
		x.Equal("sha256:beef", m.Digest)
	})

	t.Run("one it kept no hash for answers with the length alone", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := onReal(t, st, wickazure.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
		x.Empty(m.Digest)
	})

	t.Run("what it does not hold is not-there", func(t *testing.T) {
		x := require.New(t)

		s := onReal(t, newStore(), wickazure.Options{})

		_, err := s.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})

	t.Run("what it will not say about is not not-there", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		st.status = http.StatusForbidden
		st.code = "AuthenticationFailed"
		s := onReal(t, st, wickazure.Options{})

		_, err := s.Stat(t.Context(), "a.rec")
		x.Error(err)
		x.NotErrorIs(err, fs.ErrNotExist)

		x.Error(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})

	t.Run("a prefix is put in front of the blob it asks for", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := onReal(t, st, wickazure.Options{Prefix: "robots"})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		_, ok := st.objs["robots/a.rec"]
		x.True(ok)
	})
}

// The four ways of saying who this is, each built without anything being sent.
func TestSayingWhoThisIs(t *testing.T) {
	t.Run("an account key", func(t *testing.T) {
		x := require.New(t)

		s, err := wickazure.New(t.Context(), wickazure.Options{
			Account: "wick", Container: "c", AccountKey: "aGVsbG8gd29ybGQ=",
		})
		x.NoError(err)
		x.NotNil(s)
	})
	t.Run("one that is not base64 is refused before anything is sent", func(t *testing.T) {
		x := require.New(t)

		_, err := wickazure.New(t.Context(), wickazure.Options{
			Account: "wick", Container: "c", AccountKey: "not base64!",
		})
		x.ErrorContains(err, "work out how to reach the store")
	})
	t.Run("a connection string", func(t *testing.T) {
		x := require.New(t)

		s, err := wickazure.New(t.Context(), wickazure.Options{
			Container: "c",
			ConnectionString: "DefaultEndpointsProtocol=https;AccountName=wick;" +
				"AccountKey=aGVsbG8gd29ybGQ=;EndpointSuffix=core.windows.net",
		})
		x.NoError(err)
		x.NotNil(s)
	})
	t.Run("one that is not a connection string is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := wickazure.New(t.Context(), wickazure.Options{
			Container: "c", ConnectionString: "not one",
		})
		x.ErrorContains(err, "work out how to reach the store")
	})
	t.Run("a shared access signature, which travels in the address", func(t *testing.T) {
		x := require.New(t)

		for _, sas := range []string{"sv=2025-01-05&sig=x", "?sv=2025-01-05&sig=x"} {
			s, err := wickazure.New(t.Context(), wickazure.Options{
				Account: "wick", Container: "c", SAS: sas,
			})
			x.NoError(err)
			x.NotNil(s)
		}
	})
	t.Run("and nothing at all, which is a managed identity", func(t *testing.T) {
		x := require.New(t)

		// Building the credential does not reach the platform; using it does.
		s, err := wickazure.New(t.Context(), wickazure.Options{Account: "wick", Container: "c"})
		x.NoError(err)
		x.NotNil(s)
	})
}
