// Package s3 is a sink that puts things in an S3-compatible store.
//
// Compatible is doing real work in that sentence: this is the same code for
// AWS, MinIO, Ceph, R2 and whatever the building already runs, because the only
// things it needs are a bucket, a key, a multipart upload and a HEAD.
//
// # Why the upload is chunked
//
// Because half a gigabyte over a link that drops is not one request. A single
// PutObject that fails at ninety per cent has sent ninety per cent for nothing.
// Multipart sends it in parts, retries the part that failed rather than the
// whole thing, and can send several at once when the link has room for it.
//
// # Why there is no overall deadline
//
// A deadline on the whole upload is a size limit written as a clock: it refuses
// large files on slow links, which is exactly the case this is for. What is
// wanted instead is a limit on *not moving*, and that is what the transport's
// timeouts are -- see [github.com/heojeongbo/wick/sink/http.NewTransport].
package s3

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/heojeongbo/wick/internal/throttle"
	"github.com/heojeongbo/wick/sink"
	wickhttp "github.com/heojeongbo/wick/sink/http"
	"github.com/lesomnus/z"
)

// API is the part of the S3 client this package uses.
//
// It is narrowed to these six calls so that a test can stand in for the store
// without an account, a network or a container -- and so that reading this file
// tells you exactly what is asked of the other end, which "an *s3.Client" does
// not.
type API interface {
	manager.UploadAPIClient

	HeadObject(ctx context.Context, in *awss3.HeadObjectInput, opts ...func(*awss3.Options)) (*awss3.HeadObjectOutput, error)
}

const (
	// MinPartSize is what S3 itself requires of every part but the last.
	MinPartSize = 5 << 20
	// DefaultPartSize is a compromise: small enough that a part lost on a bad
	// link is a small thing to send again, large enough that a large file is
	// not thousands of requests.
	DefaultPartSize = 16 << 20
	// DefaultConcurrency is how many parts are in the air at once. More would
	// take the link away from whatever else is on it, which on these machines
	// is the thing that matters.
	DefaultConcurrency = 4
)

type Options struct {
	// Bucket is where things go.
	Bucket string
	// Prefix is put in front of every name, so that one bucket can hold more
	// than one thing without the naming template having to know about it.
	Prefix string

	// Endpoint is the store, and is AWS itself when it is not said.
	Endpoint string
	// Region is what the store is told it is. Many of the compatible ones do
	// not care and still require one to be said.
	Region string
	// PathStyle puts the bucket in the path rather than in the host name,
	// which is what a store reached by address rather than by name needs.
	PathStyle bool

	// AccessKeyID and SecretAccessKey are the credentials. Left unsaid, the
	// machine's own are used -- an instance role, a profile, the environment --
	// which is the better way round where it is available.
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string

	// PartSize and Concurrency are how the upload is broken up.
	PartSize    int64
	Concurrency int

	// RateLimit is bytes per second, and is no limit when it is not said.
	RateLimit int64

	// TLS is who to believe and what to say we are.
	TLS wickhttp.TLS

	// API is the client, and is one of this package's making when nil.
	API API
}

type Sink struct {
	api      API
	bucket   string
	prefix   string
	partSize int64
	conc     int
	rate     int64
}

func New(ctx context.Context, o Options) (*Sink, error) {
	if o.Bucket == "" {
		return nil, fmt.Errorf("an s3 sink has to say which bucket")
	}

	part := o.PartSize
	if part == 0 {
		part = DefaultPartSize
	}
	if part < MinPartSize {
		return nil, fmt.Errorf("a part of %d bytes is smaller than the %d S3 requires of every part but the last", part, MinPartSize)
	}

	conc := o.Concurrency
	if conc == 0 {
		conc = DefaultConcurrency
	}
	if conc < 1 {
		return nil, fmt.Errorf("%d parts at once is not a number of parts to send", conc)
	}

	api := o.API
	if api == nil {
		var err error
		if api, err = newClient(ctx, o); err != nil {
			return nil, err
		}
	}

	return &Sink{
		api:      api,
		bucket:   o.Bucket,
		prefix:   strings.Trim(o.Prefix, "/"),
		partSize: part,
		conc:     conc,
		rate:     o.RateLimit,
	}, nil
}

func newClient(ctx context.Context, o Options) (API, error) {
	loads := []func(*awsconfig.LoadOptions) error{}
	if o.Region != "" {
		loads = append(loads, awsconfig.WithRegion(o.Region))
	}
	if o.AccessKeyID != "" {
		loads = append(loads, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(o.AccessKeyID, o.SecretAccessKey, o.SessionToken),
		))
	}

	t, err := wickhttp.NewTransport(o.TLS)
	if err != nil {
		return nil, err
	}
	loads = append(loads, awsconfig.WithHTTPClient(&http.Client{Transport: t}))

	cfg, err := awsconfig.LoadDefaultConfig(ctx, loads...)
	if err != nil {
		return nil, z.Err(err, "work out how to reach the store")
	}

	return awss3.NewFromConfig(cfg, func(so *awss3.Options) {
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
		}
		so.UsePathStyle = o.PathStyle
	}), nil
}

func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	// Asked before anything is started, so that a shutdown does not open a
	// multipart upload it will then abandon.
	if err := ctx.Err(); err != nil {
		return err
	}

	key := s.key(name)

	in := &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   throttle.Reader(ctx, r, s.rate),
	}
	if want.Size >= 0 {
		in.ContentLength = aws.Int64(want.Size)
	}
	// Given to the store so that the store checks it too: something that
	// arrives corrupted is then refused where it lands rather than believed and
	// found out later by the read-back.
	//
	// Only when it goes in one part. S3 checksums each part of a multipart
	// upload separately and the object's own checksum is then a hash of those
	// hashes, which this is not -- sending it anyway is a store that refuses
	// every large file.
	if want.Size >= 0 && want.Size <= s.partSize {
		if b, ok := decodeSHA256(want.Digest); ok {
			in.ChecksumSHA256 = aws.String(base64.StdEncoding.EncodeToString(b))
		}
	}

	u := manager.NewUploader(s.api, func(m *manager.Uploader) {
		m.PartSize = s.partSize
		m.Concurrency = s.conc
		// The parts of an upload that did not finish are cleaned up, so that a
		// daemon which is restarted a lot does not leave a bucket full of
		// pieces that are billed for and never completed.
		m.LeavePartsOnError = false
	})

	if _, err := u.Upload(ctx, in); err != nil {
		return z.Err(err, "put %q", key)
	}

	return nil
}

func (s *Sink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	if err := ctx.Err(); err != nil {
		return sink.Meta{}, err
	}

	key := s.key(name)

	out, err := s.api.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			// Not there is told from cannot-say, so that the engine sends it
			// again rather than reading the answer as a reason to give up.
			return sink.Meta{}, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}

		return sink.Meta{}, z.Err(err, "ask about %q", key)
	}

	m := sink.Meta{Size: -1}
	if out.ContentLength != nil {
		m.Size = *out.ContentLength
	}
	if out.ChecksumSHA256 != nil {
		if b, err := base64.StdEncoding.DecodeString(*out.ChecksumSHA256); err == nil {
			m.Digest = "sha256:" + hex.EncodeToString(b)
		}
	}

	return m, nil
}

func (s *Sink) key(name string) string {
	if s.prefix == "" {
		return name
	}

	return s.prefix + "/" + name
}

// isNotFound reads the several ways this store says a thing is not there. The
// typed one is what a well-behaved SDK gives; the status is what a compatible
// store that has its own opinions gives.
func isNotFound(err error) bool {
	var nk *types.NoSuchKey
	if errors.As(err, &nk) {
		return true
	}

	var nf *types.NotFound
	if errors.As(err, &nf) {
		return true
	}

	var re interface{ HTTPStatusCode() int }
	if errors.As(err, &re) && re.HTTPStatusCode() == http.StatusNotFound {
		return true
	}

	var api smithy.APIError
	if errors.As(err, &api) {
		switch api.ErrorCode() {
		case "NotFound", "NoSuchKey", "404":
			return true
		}
	}

	return false
}

// decodeSHA256 reads "sha256:<hex>" back into the bytes it stands for.
func decodeSHA256(digest string) ([]byte, bool) {
	hexed, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return nil, false
	}

	b, err := hex.DecodeString(hexed)
	if err != nil || len(b) != 32 {
		return nil, false
	}

	return b, true
}

var (
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
)
