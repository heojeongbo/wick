package gcs_test

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/sink/gcs"
)

// The real storage client, through a transport this test answers.
//
// The fake next door is what the failures are tested through. This is what says
// the narrow interface is one the real client satisfies, and -- the part worth
// having -- that the SHA-256 this puts in the object's metadata is the one that
// comes back out of its attributes. Everything else about the read-back rests
// on that.
//
// It is a RoundTripper rather than an httptest server because it sees every
// request the client makes, so the answers here are built from what the client
// actually asks rather than from a guess at the JSON API.
type store struct {
	mu   sync.Mutex
	objs map[string][]byte
	meta map[string]map[string]string

	// seen is every request, so a test can say what the client did.
	seen []string

	// status, when set, is what everything is answered with instead.
	status int
}

func newStore() *store {
	return &store{objs: map[string][]byte{}, meta: map[string]map[string]string{}}
}

func (s *store) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seen = append(s.seen, r.Method+" "+r.URL.Path)

	if s.status != 0 {
		return s.reply(r, s.status, `{"error":{"message":"no"}}`), nil
	}

	switch {
	// An upload: multipart, with the object's metadata as the first part and
	// the bytes as the second.
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/"):
		name := r.URL.Query().Get("name")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}

		meta, data := splitMultipart(body)
		s.objs[name] = data
		s.meta[name] = meta

		return s.reply(r, http.StatusOK, s.object(name)), nil

	// A look at one.
	case r.Method == http.MethodGet:
		name := nameOf(r.URL.Path)
		if _, ok := s.objs[name]; !ok {
			return s.reply(r, http.StatusNotFound, `{"error":{"code":404,"message":"Not Found"}}`), nil
		}

		return s.reply(r, http.StatusOK, s.object(name)), nil
	}

	return s.reply(r, http.StatusMethodNotAllowed, `{"error":{"message":"no"}}`), nil
}

func (s *store) reply(r *http.Request, code int, body string) *http.Response {
	return &http.Response{
		StatusCode:    code,
		Status:        strconv.Itoa(code),
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       r,
	}
}

func (s *store) object(name string) string {
	b, err := json.Marshal(map[string]any{
		"kind":     "storage#object",
		"name":     name,
		"bucket":   "recordings",
		"size":     strconv.Itoa(len(s.objs[name])),
		"metadata": s.meta[name],
	})
	if err != nil {
		panic(err)
	}

	return string(b)
}

// splitMultipart pulls the object's metadata and its bytes out of the body the
// client sends. It is enough of a multipart reader for two parts in a known
// order, which is what the client sends and all this has to understand.
func splitMultipart(body []byte) (map[string]string, []byte) {
	// The parts are separated by a boundary line; the first is JSON and the
	// second is the object.
	parts := bytes.Split(body, []byte("\r\n--"))

	var (
		meta map[string]string
		data []byte
	)
	for _, p := range parts {
		i := bytes.Index(p, []byte("\r\n\r\n"))
		if i < 0 {
			continue
		}

		head, rest := p[:i], p[i+4:]
		rest = bytes.TrimSuffix(rest, []byte("\r\n"))

		if bytes.Contains(head, []byte("application/json")) {
			var attrs struct {
				Metadata map[string]string `json:"metadata"`
			}
			if err := json.Unmarshal(bytes.TrimSpace(rest), &attrs); err == nil {
				meta = attrs.Metadata
			}

			continue
		}
		data = rest
	}

	return meta, data
}

func nameOf(p string) string {
	i := strings.Index(p, "/o/")
	if i < 0 {
		return ""
	}

	return p[i+3:]
}

func onReal(t *testing.T, st *store, o gcs.Options) *gcs.Sink {
	t.Helper()

	if o.Bucket == "" {
		o.Bucket = "recordings"
	}
	o.Endpoint = "https://storage.example.invalid"
	o.HTTPClient = &http.Client{Transport: st}

	s, err := gcs.New(t.Context(), o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	return s
}

func TestAgainstTheRealClient(t *testing.T) {
	t.Run("what is put is what is held, and the hash survives the round trip", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := onReal(t, st, gcs.Options{ChunkSize: 1 << 20})

		x.NoError(s.Put(t.Context(), "a/b.rec", bytes.NewReader([]byte("contents")), sink.Meta{
			Size:   8,
			Digest: "sha256:beef",
		}))

		x.Equal([]byte("contents"), st.objs["a/b.rec"])
		x.Equal("sha256:beef", st.meta["a/b.rec"][gcs.DigestKey])

		m, err := s.Stat(t.Context(), "a/b.rec")
		x.NoError(err)
		x.Equal(int64(8), m.Size)
		x.Equal("sha256:beef", m.Digest)
	})

	t.Run("what it does not hold is not-there", func(t *testing.T) {
		x := require.New(t)

		s := onReal(t, newStore(), gcs.Options{})

		_, err := s.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})

	t.Run("what it will not say about is not not-there", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		st.status = http.StatusForbidden
		s := onReal(t, st, gcs.Options{})

		_, err := s.Stat(t.Context(), "a.rec")
		x.Error(err)
		x.NotErrorIs(err, fs.ErrNotExist)

		x.Error(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
	})

	t.Run("a prefix is put in front of the object it asks for", func(t *testing.T) {
		x := require.New(t)

		st := newStore()
		s := onReal(t, st, gcs.Options{Prefix: "robots"})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1}))
		_, ok := st.objs["robots/a.rec"]
		x.True(ok)
	})
}

// serviceAccount is a key of the shape the client insists on, so that a test
// about building a client is not also a test about having a Google account.
func serviceAccount(t *testing.T) string {
	t.Helper()

	x := require.New(t)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	x.NoError(err)

	der, err := x509.MarshalPKCS8PrivateKey(key)
	x.NoError(err)

	b, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   "wick-test",
		"client_email": "wick@wick-test.iam.gserviceaccount.invalid",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":    "https://oauth2.example.invalid/token",
	})
	x.NoError(err)

	return string(b)
}
