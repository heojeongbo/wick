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
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
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

	// HeadBucket is asked about the bucket rather than about anything in it,
	// and is how [Sink.Reach] tells a store that is not there from one that
	// simply does not hold a name. HeadObject cannot: S3 answers a HEAD with
	// no body, so both arrive as a bare 404.
	HeadBucket(ctx context.Context, in *awss3.HeadBucketInput, opts ...func(*awss3.Options)) (*awss3.HeadBucketOutput, error)
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

	// AccessKeyID and SecretAccessKey are the credentials, written down.
	//
	// Left unsaid, the machine's own are used: an instance role, the
	// environment, a profile. That is the better way round wherever it is
	// available, because a long-lived key on a machine in a cupboard is a
	// long-lived key on a machine somebody can walk up to.
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string

	// Profile is the one to read out of the shared configuration, for a
	// machine that has more than one.
	Profile string

	// AssumeRoleARN is a role to take on once the credentials above have said
	// who this is. It is how a machine reaches a bucket in an account it does
	// not itself have an identity in.
	AssumeRoleARN string
	// RoleSessionName is what the session is called in the other account's
	// logs. Worth setting: it is the only thing there that says which machine.
	RoleSessionName string
	// ExternalID is the secret the other account's role asks for, and is what
	// stops one caller's role being used by another who happens to know its
	// name.
	ExternalID string

	// WebIdentityTokenFile holds a token from something that already knows who
	// this is -- a Kubernetes service account, a CI runner -- and is exchanged
	// for the role. It replaces the credentials above rather than adding to
	// them, so it needs AssumeRoleARN and nothing else.
	WebIdentityTokenFile string

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
	if err := o.check(); err != nil {
		return nil, err
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

// check says whether the options can be meant.
func (o Options) check() error {
	switch {
	case o.Bucket == "":
		return fmt.Errorf("an s3 sink has to say which bucket")

	case o.AccessKeyID != "" && o.Profile != "":
		return fmt.Errorf("a key and a profile are two answers to the question of who this is; give one")

	case o.AccessKeyID != "" && o.WebIdentityTokenFile != "":
		return fmt.Errorf("a key and a web identity token are two answers to the question of who this is; give one")

	case o.Profile != "" && o.WebIdentityTokenFile != "":
		return fmt.Errorf("a profile and a web identity token are two answers to the question of who this is; give one")

	case o.WebIdentityTokenFile != "" && o.AssumeRoleARN == "":
		// The token is not a credential; it is something to exchange for one,
		// and there is nothing to exchange it for.
		return fmt.Errorf("a web identity token is exchanged for a role, and no role is named; say assume_role_arn")

	case o.AssumeRoleARN == "" && o.ExternalID != "":
		return fmt.Errorf("an external id is what a role asks for, and no role is named")

	case o.AssumeRoleARN == "" && o.RoleSessionName != "":
		return fmt.Errorf("a session name names a session with a role, and no role is named")
	}

	return nil
}

func newClient(ctx context.Context, o Options) (API, error) {
	loads := []func(*awsconfig.LoadOptions) error{}
	if o.Region != "" {
		loads = append(loads, awsconfig.WithRegion(o.Region))
	}
	if o.Profile != "" {
		loads = append(loads, awsconfig.WithSharedConfigProfile(o.Profile))
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

	if o.AssumeRoleARN != "" {
		cfg.Credentials = assume(cfg, o)
	}

	return awss3.NewFromConfig(cfg, func(so *awss3.Options) {
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
		}
		so.UsePathStyle = o.PathStyle
	}), nil
}

// assume is the credentials of the role, taken on with whatever the
// configuration above worked out this machine is.
//
// Nothing is asked of STS here. The provider is built and wrapped in a cache;
// the exchange happens at the first request that needs signing, which is also
// where a role that cannot be taken on is found out about. That is the right
// way round for a daemon: a role that is refused should fail a carry and be
// retried, not stop the process from starting.
func assume(cfg aws.Config, o Options) aws.CredentialsProvider {
	api := sts.NewFromConfig(cfg)

	if o.WebIdentityTokenFile != "" {
		// The token replaces the credentials rather than adding to them: it is
		// already a statement of who this is, from something that knows.
		return aws.NewCredentialsCache(stscreds.NewWebIdentityRoleProvider(
			api, o.AssumeRoleARN, stscreds.IdentityTokenFile(o.WebIdentityTokenFile),
			func(p *stscreds.WebIdentityRoleOptions) {
				p.RoleSessionName = o.RoleSessionName
			},
		))
	}

	return aws.NewCredentialsCache(stscreds.NewAssumeRoleProvider(
		api, o.AssumeRoleARN,
		func(p *stscreds.AssumeRoleOptions) {
			p.RoleSessionName = o.RoleSessionName
			if o.ExternalID != "" {
				p.ExternalID = aws.String(o.ExternalID)
			}
		},
	))
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

// Reach asks about the bucket, and writes nothing.
//
// It is what `wick check` uses. A bucket that is not there, a key that is not
// allowed to see it, an endpoint nothing answers on: all three come back here
// as themselves rather than as the flat "not found" that asking about an object
// would give.
func (s *Sink) Reach(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if _, err := s.api.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return z.Err(err, "reach the bucket %q", s.bucket)
	}

	return nil
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
	// A bucket that is not there is answered with a 404 as well, and it is the
	// opposite kind of news. "I do not hold that name" means the store was
	// reached and the credentials were taken; "there is no such bucket" means
	// nothing was. Reading the second as the first makes a store nobody can
	// write to look like an empty one -- `wick check` calls it reached, and a
	// carry writes, fails, and is retried for ever against a bucket that will
	// never exist.
	var nb *types.NoSuchBucket
	if errors.As(err, &nb) {
		return false
	}

	// The same thing, from a store that is compatible rather than AWS. MinIO
	// answers HeadObject on a missing bucket with this and a 404, which the
	// status check below would otherwise swallow.
	var coded smithy.APIError
	if errors.As(err, &coded) && coded.ErrorCode() == "NoSuchBucket" {
		return false
	}

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
	_ sink.Sink    = (*Sink)(nil)
	_ sink.Stater  = (*Sink)(nil)
	_ sink.Reacher = (*Sink)(nil)
)
