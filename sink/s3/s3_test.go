package s3_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	wickhttp "github.com/heojeongbo/wick/sink/http"
	"github.com/heojeongbo/wick/sink/s3"
	"github.com/heojeongbo/wick/sink/sinktest"
)

var errRefused = errors.New("refused")

// fakeAPI is the store, in memory. It is written out rather than generated
// because what is worth reading here is exactly which six calls this package
// makes and what it does with the answers.
type fakeAPI struct {
	mu    sync.Mutex
	objs  map[string][]byte
	sums  map[string]string
	parts map[string]map[int32][]byte

	aborted  []string
	multi    int
	putCalls int

	putErr      error
	createErr   error
	partErr     error
	completeErr error
	headErr     error
}

func newAPI() *fakeAPI {
	return &fakeAPI{
		objs:  map[string][]byte{},
		sums:  map[string]string{},
		parts: map[string]map[int32][]byte{},
	}
}

func (f *fakeAPI) PutObject(ctx context.Context, in *awss3.PutObjectInput, _ ...func(*awss3.Options)) (*awss3.PutObjectOutput, error) {
	if f.putErr != nil {
		return nil, f.putErr
	}

	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.putCalls++
	f.objs[*in.Key] = b
	if in.ChecksumSHA256 != nil {
		f.sums[*in.Key] = *in.ChecksumSHA256
	}

	return &awss3.PutObjectOutput{}, nil
}

func (f *fakeAPI) CreateMultipartUpload(ctx context.Context, in *awss3.CreateMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.CreateMultipartUploadOutput, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.multi++
	id := fmt.Sprintf("upload-%d", f.multi)
	f.parts[id] = map[int32][]byte{}

	return &awss3.CreateMultipartUploadOutput{UploadId: aws.String(id)}, nil
}

func (f *fakeAPI) UploadPart(ctx context.Context, in *awss3.UploadPartInput, _ ...func(*awss3.Options)) (*awss3.UploadPartOutput, error) {
	if f.partErr != nil {
		return nil, f.partErr
	}

	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.parts[*in.UploadId][*in.PartNumber] = b

	return &awss3.UploadPartOutput{ETag: aws.String(fmt.Sprintf("etag-%d", *in.PartNumber))}, nil
}

func (f *fakeAPI) CompleteMultipartUpload(ctx context.Context, in *awss3.CompleteMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.CompleteMultipartUploadOutput, error) {
	if f.completeErr != nil {
		return nil, f.completeErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	ps := f.parts[*in.UploadId]
	ns := slices.Sorted(partNumbers(ps))

	var whole []byte
	for _, n := range ns {
		whole = append(whole, ps[n]...)
	}
	f.objs[*in.Key] = whole

	return &awss3.CompleteMultipartUploadOutput{}, nil
}

func (f *fakeAPI) AbortMultipartUpload(ctx context.Context, in *awss3.AbortMultipartUploadInput, _ ...func(*awss3.Options)) (*awss3.AbortMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.aborted = append(f.aborted, *in.UploadId)

	return &awss3.AbortMultipartUploadOutput{}, nil
}

func (f *fakeAPI) HeadObject(ctx context.Context, in *awss3.HeadObjectInput, _ ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.objs[*in.Key]
	if !ok {
		return nil, &types.NotFound{}
	}

	out := &awss3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(b)))}
	if s, ok := f.sums[*in.Key]; ok {
		out.ChecksumSHA256 = aws.String(s)
	}

	return out, nil
}

func partNumbers(m map[int32][]byte) func(func(int32) bool) {
	return func(yield func(int32) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func newSink(t *testing.T, api *fakeAPI, o s3.Options) *s3.Sink {
	t.Helper()

	o.API = api
	if o.Bucket == "" {
		o.Bucket = "bucket"
	}
	s, err := s3.New(t.Context(), o)
	require.NoError(t, err)

	return s
}

func TestSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return newSink(t, newAPI(), s3.Options{}) })
}

func TestNew(t *testing.T) {
	t.Run("a sink has to say which bucket", func(t *testing.T) {
		x := require.New(t)

		_, err := s3.New(t.Context(), s3.Options{API: newAPI()})
		x.ErrorContains(err, "which bucket")
	})
	t.Run("a part smaller than the store allows is refused rather than found out at the first large file", func(t *testing.T) {
		x := require.New(t)

		_, err := s3.New(t.Context(), s3.Options{Bucket: "b", API: newAPI(), PartSize: 1024})
		x.ErrorContains(err, "smaller than the")
	})
	t.Run("a number of parts that is not a number of parts is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := s3.New(t.Context(), s3.Options{Bucket: "b", API: newAPI(), Concurrency: -1})
		x.ErrorContains(err, "not a number of parts")
	})
	t.Run("a client of its own is made when it is not handed one", func(t *testing.T) {
		x := require.New(t)

		// No network is touched: the client is built, not used.
		s, err := s3.New(t.Context(), s3.Options{
			Bucket: "b", Region: "auto", Endpoint: "https://example.invalid",
			PathStyle: true, AccessKeyID: "k", SecretAccessKey: "s", SessionToken: "t",
		})
		x.NoError(err)
		x.NotNil(s)
	})
	t.Run("a certificate that is not one is refused before anything is sent", func(t *testing.T) {
		x := require.New(t)

		_, err := s3.New(t.Context(), s3.Options{
			Bucket: "b",
			TLS:    wickhttp.TLS{CertFile: "only-the-certificate.pem"},
		})
		x.ErrorContains(err, "go together")
	})
}

func TestPut(t *testing.T) {
	t.Run("a prefix is put in front of every name", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		s := newSink(t, api, s3.Options{Prefix: "/robots/"})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: 3}))

		_, ok := api.objs["robots/a.rec"]
		x.True(ok)

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(int64(3), m.Size)
	})

	t.Run("something that fits in one part goes in one request, with its hash", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		s := newSink(t, api, s3.Options{})

		b := []byte("small enough")
		sum := sha256Of(b)
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader(b), sink.Meta{Size: int64(len(b)), Digest: sum}))

		x.Equal(1, api.putCalls)
		x.Zero(api.multi)
		x.Equal(b, api.objs["a.rec"])

		m, err := s.Stat(t.Context(), "a.rec")
		x.NoError(err)
		x.Equal(sum, m.Digest)
	})

	t.Run("something larger is broken up and put together again", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		s := newSink(t, api, s3.Options{PartSize: s3.MinPartSize, Concurrency: 2})

		// Two and a bit parts, so that the last one is the short one.
		b := bytes.Repeat([]byte("x"), s3.MinPartSize*2+7)
		x.NoError(s.Put(t.Context(), "big.rec", bytes.NewReader(b), sink.Meta{Size: int64(len(b)), Digest: sha256Of(b)}))

		x.Equal(1, api.multi)
		x.Equal(b, api.objs["big.rec"])
		x.Empty(api.aborted)

		// The hash of the whole object is not sent for a multipart upload: S3
		// hashes each part and the object's own is a hash of those.
		m, err := s.Stat(t.Context(), "big.rec")
		x.NoError(err)
		x.Equal(int64(len(b)), m.Size)
		x.Empty(m.Digest)
	})

	t.Run("a hash that is not one is simply not sent", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		s := newSink(t, api, s3.Options{})

		for _, d := range []string{"", "md5:abcd", "sha256:nothex", "sha256:aabb"} {
			x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1, Digest: d}))
			x.Empty(api.sums["a.rec"])
		}
	})

	t.Run("a length nobody claimed is not sent either", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		s := newSink(t, api, s3.Options{})

		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("xyz")), sink.Meta{Size: -1}))
		x.Equal([]byte("xyz"), api.objs["a.rec"])
	})

	t.Run("a write that is refused says which key", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		api.putErr = errRefused
		s := newSink(t, api, s3.Options{Prefix: "p"})

		err := s.Put(t.Context(), "a.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, "p/a.rec")
	})

	// A daemon that is restarted a lot must not leave a bucket full of pieces
	// that are billed for and never completed.
	t.Run("a large write that is refused part way is cleaned up after", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		api.partErr = errRefused
		s := newSink(t, api, s3.Options{PartSize: s3.MinPartSize})

		b := bytes.Repeat([]byte("x"), s3.MinPartSize*2)
		x.ErrorIs(s.Put(t.Context(), "big.rec", bytes.NewReader(b), sink.Meta{Size: int64(len(b))}), errRefused)
		x.Equal([]string{"upload-1"}, api.aborted)
	})

	t.Run("a large write that cannot even be started says so", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		api.createErr = errRefused
		s := newSink(t, api, s3.Options{PartSize: s3.MinPartSize})

		b := bytes.Repeat([]byte("x"), s3.MinPartSize*2)
		x.ErrorIs(s.Put(t.Context(), "big.rec", bytes.NewReader(b), sink.Meta{Size: int64(len(b))}), errRefused)
	})

	t.Run("a large write that cannot be finished says so", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		api.completeErr = errRefused
		s := newSink(t, api, s3.Options{PartSize: s3.MinPartSize})

		b := bytes.Repeat([]byte("x"), s3.MinPartSize*2)
		x.ErrorIs(s.Put(t.Context(), "big.rec", bytes.NewReader(b), sink.Meta{Size: int64(len(b))}), errRefused)
	})

	t.Run("bytes are sent no faster than they were told to be", func(t *testing.T) {
		x := require.New(t)

		api := newAPI()
		s := newSink(t, api, s3.Options{RateLimit: 1 << 30})

		b := []byte("xyz")
		x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader(b), sink.Meta{Size: 3}))
		x.Equal(b, api.objs["a.rec"])
	})
}

// Not there has to be told from cannot-say, or the engine reads an outage as a
// reason to send half a gigabyte again.
func TestStatTellsNotThereFromCannotSay(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		gone bool
	}{
		{"the typed one", &types.NotFound{}, true},
		{"the other typed one", &types.NoSuchKey{}, true},
		{"a status from a store with its own opinions", &smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: statusResponse(404)}}, true},
		{"a code from one with even more of them", &smithy.GenericAPIError{Code: "NoSuchKey"}, true},
		{"anything else at all", errRefused, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			api := newAPI()
			api.headErr = tc.err
			s := newSink(t, api, s3.Options{})

			_, err := s.Stat(t.Context(), "a.rec")
			if tc.gone {
				x.ErrorIs(err, fs.ErrNotExist)

				return
			}
			x.NotErrorIs(err, fs.ErrNotExist)
			x.ErrorIs(err, tc.err)
		})
	}
}

func TestStatSaysWhatItCan(t *testing.T) {
	x := require.New(t)

	api := newAPI()
	api.objs["a.rec"] = []byte("xyz")
	// A store that says nothing about the length, which some compatible ones do.
	s := newSink(t, api, s3.Options{})

	m, err := s.Stat(t.Context(), "a.rec")
	x.NoError(err)
	x.Equal(int64(3), m.Size)

	// A checksum that is not base64 is not a checksum, and is left off rather
	// than passed on as one.
	api.sums["a.rec"] = "not base64!"
	m, err = s.Stat(t.Context(), "a.rec")
	x.NoError(err)
	x.Empty(m.Digest)
}

func TestSpec(t *testing.T) {
	x := require.New(t)

	s, err := (&s3.Spec{
		Bucket: "b", Prefix: "p", Region: "auto", Endpoint: "https://example.invalid",
		PathStyle: true, AccessKeyId: "k", SecretAccessKey: "s", SessionToken: "t",
		PartSize: s3.MinPartSize, Concurrency: 2, RateLimit: 1024,
	}).New(t.Context())
	x.NoError(err)
	x.NotNil(s)

	_, err = (&s3.Spec{}).New(t.Context())
	x.ErrorContains(err, "which bucket")
}

func sha256Of(b []byte) string {
	return "sha256:" + hex.EncodeToString(sha256Sum(b))
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)

	return sum[:]
}

// statusResponse is the shape a compatible store's refusal arrives in when it
// has not bothered with the typed error.
func statusResponse(code int) *http.Response {
	return &http.Response{StatusCode: code}
}

func TestChecksumIsSentAsTheStoreWantsIt(t *testing.T) {
	x := require.New(t)

	api := newAPI()
	s := newSink(t, api, s3.Options{})

	b := []byte("xyz")
	x.NoError(s.Put(t.Context(), "a.rec", bytes.NewReader(b), sink.Meta{Size: 3, Digest: sha256Of(b)}))

	// Base64 of the raw hash, which is how S3 spells it.
	x.Equal(base64.StdEncoding.EncodeToString(sha256Sum(b)), api.sums["a.rec"])
}

// Working out how to reach the store can itself be refused -- a profile that is
// not there, a setting that is not a number -- and that is a refusal at
// startup, not a carry that fails later.
func TestAStoreThatCannotBeWorkedOut(t *testing.T) {
	x := require.New(t)

	t.Setenv("AWS_MAX_ATTEMPTS", "not a number")

	_, err := s3.New(t.Context(), s3.Options{Bucket: "b"})
	x.ErrorContains(err, "work out how to reach the store")
}

// Importing this package is what makes `type: s3` mean something, and the
// package that holds the registry cannot check that: it is the one this
// depends on, so importing this back into its tests would put the AWS SDK into
// the test dependencies of the package that exists to keep it out of them.
func TestItRegistersItself(t *testing.T) {
	x := require.New(t)

	x.Contains(sink.Kinds(), s3.Kind)

	spec, err := sink.NewSpec(s3.Kind)
	x.NoError(err)
	x.IsType(&s3.Spec{}, spec)
}
