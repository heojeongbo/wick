package azure_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	wickazure "github.com/heojeongbo/wick/sink/azure"
	"github.com/heojeongbo/wick/sink/sinktest"
)

var errRefused = errors.New("refused")

// fake is the store kept in memory, for the failures. The test against the real
// client next door is what says the interface is one it satisfies.
type fake struct {
	mu    sync.Mutex
	objs  map[string][]byte
	metas map[string]map[string]string

	uploadErr map[string]error
	propsErr  map[string]error
}

func newFake() *fake {
	return &fake{objs: map[string][]byte{}, metas: map[string]map[string]string{}}
}

func (f *fake) Upload(ctx context.Context, name string, r io.Reader, meta map[string]string) error {
	f.mu.Lock()
	err, refuse := f.uploadErr[name]
	f.mu.Unlock()

	if refuse {
		return err
	}

	// Read first, so that a reader which gives out part way through is what
	// the caller hears about -- the way a real one would.
	b, rerr := io.ReadAll(r)
	if rerr != nil {
		return rerr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.objs[name] = b
	f.metas[name] = meta

	return nil
}

func (f *fake) Properties(ctx context.Context, name string) (wickazure.Properties, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err, ok := f.propsErr[name]; ok {
		return wickazure.Properties{}, err
	}

	b, ok := f.objs[name]
	if !ok {
		// The shape the SDK gives it, so that what tells "not there" from
		// "cannot say" is the same code path a real answer takes.
		return wickazure.Properties{}, &azcore.ResponseError{
			ErrorCode:  string(bloberror.BlobNotFound),
			StatusCode: http.StatusNotFound,
		}
	}

	return wickazure.Properties{Size: int64(len(b)), Metadata: f.metas[name]}, nil
}

func (f *fake) data(name string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.objs[name]

	return b, ok
}

func onFake(t *testing.T, f *fake, o wickazure.Options) *wickazure.Sink {
	t.Helper()

	if o.Container == "" {
		o.Container = "recordings"
	}
	if o.Account == "" {
		o.Account = "wick"
	}
	o.API = f

	s, err := wickazure.New(t.Context(), o)
	require.NoError(t, err)

	return s
}

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return onFake(t, newFake(), wickazure.Options{}) })
}

func TestNew(t *testing.T) {
	for _, tc := range []struct {
		name string
		o    wickazure.Options
		says string
	}{
		{"a sink has to say which container", wickazure.Options{Account: "a"}, "which container"},
		{
			"and where the account is",
			wickazure.Options{Container: "c"},
			"which account, or a service url, or a connection string",
		},
		{
			"three answers to one question is not an answer",
			wickazure.Options{Account: "a", Container: "c", AccountKey: "k", SAS: "sig=x"},
			"three answers",
		},
		{
			"a key with no account to be the key of",
			wickazure.Options{Container: "c", ServiceURL: "https://host/", AccountKey: "k"},
			"no account is named",
		},
		{
			"a block below nothing is not an amount",
			wickazure.Options{Account: "a", Container: "c", BlockSize: -1},
			"not an amount to send",
		},
		{
			"a number of blocks that is not a number of blocks",
			wickazure.Options{Account: "a", Container: "c", Concurrency: -1},
			"not a number of blocks",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			tc.o.API = newFake()
			_, err := wickazure.New(t.Context(), tc.o)
			x.ErrorContains(err, tc.says)
		})
	}
}

func TestPut(t *testing.T) {
	t.Run("a prefix is put in front of every name", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wickazure.Options{Prefix: "/robots/"})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		_, ok := f.data("robots/a.rec")
		x.True(ok)

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
	})

	// Azure keeps an MD5, which is not what this computes.
	t.Run("the hash goes in the metadata and comes back out", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wickazure.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3, Digest: "sha256:beef"}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal("sha256:beef", m.Digest)
	})

	t.Run("a store that kept no hash answers with the length alone", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wickazure.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Empty(m.Digest)
	})

	// A spool told not to verify still gets this much.
	t.Run("a stream that ends early is caught here and not only at the read-back", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wickazure.Options{})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("short")), sink.Meta{Size: 100})
		x.ErrorContains(err, "to be 100 bytes and 5 arrived")
	})

	t.Run("a length nobody claimed is not checked", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wickazure.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("anything")), sink.Meta{Size: -1}))
	})

	t.Run("a write that is refused says which blob", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.uploadErr = map[string]error{"a.rec": errRefused}
		s := onFake(t, f, wickazure.Options{})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3})
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, "a.rec")
	})

	t.Run("bytes are sent no faster than they were told to be", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		s := onFake(t, f, wickazure.Options{RateLimit: 1 << 30})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))
		b, _ := f.data("a.rec")
		x.Equal("xyz", string(b))
	})
}

func TestStat(t *testing.T) {
	t.Run("what the store does not hold is not-there", func(t *testing.T) {
		x := require.New(t)

		s := onFake(t, newFake(), wickazure.Options{})

		_, err := s.Stat(t.Context(), "nothing.rec")
		x.ErrorIs(err, fs.ErrNotExist)
	})
	t.Run("what it will not say about is not not-there", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.propsErr = map[string]error{"a.rec": errRefused}
		s := onFake(t, f, wickazure.Options{})

		_, err := s.Stat(t.Context(), "a.rec")
		x.ErrorIs(err, errRefused)
		x.NotErrorIs(err, fs.ErrNotExist)
	})

	// A container that is not there answers with a 404 as well, and means the
	// opposite thing. "I do not hold that blob" says the store was reached and
	// the credentials were taken; "there is no such container" says nothing
	// was. This used to be folded in with BlobNotFound, which made a container
	// nobody can write to look like an empty one -- `wick check` called it
	// reached.
	t.Run("a container that is not there is not a blob that is not there", func(t *testing.T) {
		x := require.New(t)

		f := newFake()
		f.propsErr = map[string]error{"a.rec": &azcore.ResponseError{
			ErrorCode:  string(bloberror.ContainerNotFound),
			StatusCode: http.StatusNotFound,
		}}
		s := onFake(t, f, wickazure.Options{})

		_, err := s.Stat(t.Context(), "a.rec")
		x.Error(err)
		x.NotErrorIs(err, fs.ErrNotExist)
		x.ErrorContains(err, string(bloberror.ContainerNotFound))
	})
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	s, err := (&wickazure.Spec{
		Account: "wick", Container: "recordings", Prefix: "robots",
		AccountKey:  "aGVsbG8gd29ybGQ=",
		BlockSize:   1 << 20,
		Concurrency: 2, RateLimit: 1024,
	}).New(t.Context())
	x.NoError(err)
	x.NotNil(s)

	_, err = (&wickazure.Spec{}).New(t.Context())
	x.ErrorContains(err, "which container")
}

// Importing this package is what makes `type: azure` mean something.
func TestItRegistersItself(t *testing.T) {
	x := require.New(t)

	x.Contains(sink.Kinds(), wickazure.Kind)

	spec, err := sink.NewSpec(wickazure.Kind)
	x.NoError(err)
	x.IsType(&wickazure.Spec{}, spec)
}
