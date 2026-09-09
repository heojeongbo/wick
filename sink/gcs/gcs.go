// Package gcs is a sink that puts things in Google Cloud Storage.
//
// # The read-back is a hash and not a length
//
// GCS keeps a CRC32C and an MD5 of what it holds. Neither of them is what this
// computes, and asking it for a SHA-256 it never took is not a question it can
// answer -- so the hash goes into the object's own metadata on the way up, and
// comes back out of the attributes on the way down. That makes this read-back
// as strong as the S3 one, and stronger than the ones that can only compare a
// length.
//
// # What credentials it prefers
//
// None written down. On GKE that is workload identity and on a Compute Engine
// instance it is the metadata server; either way the credential is short-lived,
// rotated by something else, and not sitting in a file on a machine somebody
// can walk up to. A key file is supported because not every deployment has the
// alternative, and it is the second choice rather than the first.
package gcs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"

	"github.com/heojeongbo/wick/internal/throttle"
	"github.com/heojeongbo/wick/sink"
	"github.com/lesomnus/z"
)

// DigestKey is the metadata key the content hash is kept under.
//
// Lower case because GCS folds metadata keys, and a name that comes back
// spelled differently from the way it went in is a read-back that never
// matches.
const DigestKey = "sha256"

// API is the part of the storage client this package uses.
//
// Four calls. It is an interface so that a test can make each of them fail
// without a project, a bucket or a network -- and so that reading this says
// exactly what is asked of the other end, which "a *storage.Client" does not.
type API interface {
	// Writer begins an upload. The metadata is set before the first write, as
	// the client requires.
	Writer(ctx context.Context, name string, meta map[string]string) io.WriteCloser
	// Attrs is what the store holds under a name. It answers with
	// [storage.ErrObjectNotExist] when it holds nothing.
	Attrs(ctx context.Context, name string) (Attrs, error)
	Close() error
}

// Attrs is the part of an object's attributes this package reads.
type Attrs struct {
	Size     int64
	Metadata map[string]string
}

// DefaultChunkSize is how much of an object is buffered and sent at a time.
//
// It is the client's own default, written down here so that the configuration
// has a number to compare against. Smaller means less memory per upload and
// more round trips; larger is the other way about.
const DefaultChunkSize = 16 << 20

type Options struct {
	// Bucket is where things go, and Prefix is put in front of every name so
	// that one bucket can hold more than one thing.
	Bucket string
	Prefix string

	// CredentialsFile is a service account key. Left unsaid, the machine's own
	// credentials are used, which is the way round to prefer.
	CredentialsFile string
	// CredentialsJSON is the same thing held in the configuration rather than
	// named by it, for a deployment whose secrets arrive as environment
	// variables.
	CredentialsJSON string

	// Endpoint is the store, and is Google's own when it is not said.
	Endpoint string

	// ChunkSize is how much is buffered and sent at a time. Nothing means
	// [DefaultChunkSize].
	ChunkSize int

	// RateLimit is bytes per second, and is no limit when it is not said.
	RateLimit int64

	// HTTPClient is what the requests go through. It is here for a deployment
	// behind a proxy or with a certificate authority of its own, and it is
	// what a test hands a transport of its own through.
	HTTPClient *http.Client

	// API is the client, and is one of this package's making when nil.
	API API
}

type Sink struct {
	api    API
	prefix string
	chunk  int
	rate   int64

	// owned says whether the client is this sink's to close.
	owned bool
}

func New(ctx context.Context, o Options) (*Sink, error) {
	switch {
	case o.Bucket == "":
		return nil, fmt.Errorf("a gcs sink has to say which bucket")

	case o.CredentialsFile != "" && o.CredentialsJSON != "":
		return nil, fmt.Errorf("a credentials file and credentials written down are two answers to the same question; give one")

	case o.ChunkSize < 0:
		return nil, fmt.Errorf("a chunk of %d bytes is not an amount to send at a time", o.ChunkSize)
	}

	chunk := o.ChunkSize
	if chunk == 0 {
		chunk = DefaultChunkSize
	}

	api, owned := o.API, false
	if api == nil {
		var err error
		if api, err = newClient(ctx, o); err != nil {
			return nil, err
		}
		owned = true
	}

	return &Sink{
		api:    api,
		prefix: strings.Trim(o.Prefix, "/"),
		chunk:  chunk,
		rate:   o.RateLimit,
		owned:  owned,
	}, nil
}

func newClient(ctx context.Context, o Options) (API, error) {
	opts := []option.ClientOption{}
	switch {
	case o.CredentialsFile != "":
		opts = append(opts, option.WithCredentialsFile(o.CredentialsFile))

	case o.CredentialsJSON != "":
		opts = append(opts, option.WithCredentialsJSON([]byte(o.CredentialsJSON)))
	}
	if o.Endpoint != "" {
		opts = append(opts, option.WithEndpoint(o.Endpoint))
	}
	if o.HTTPClient != nil {
		// It is used as it is, credentials and all, which is why this and the
		// credential options above are the same decision said twice.
		opts = append(opts, option.WithHTTPClient(o.HTTPClient))
	}

	c, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, z.Err(err, "work out how to reach the store")
	}

	return &client{c: c, bucket: o.Bucket, chunk: chunkOf(o)}, nil
}

func chunkOf(o Options) int {
	if o.ChunkSize == 0 {
		return DefaultChunkSize
	}

	return o.ChunkSize
}

func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	key := s.key(name)

	// Set before the first write, which is what the client requires, and which
	// is also the only moment it can be: the upload begins with them.
	var meta map[string]string
	if want.Digest != "" {
		meta = map[string]string{DigestKey: want.Digest}
	}

	w := s.api.Writer(ctx, key, meta)

	n, err := io.Copy(w, throttle.Reader(ctx, r, s.rate))
	if err != nil {
		_ = w.Close()

		return z.Err(err, "put %q", key)
	}

	// Where the errors surface: the client buffers, so a write that was
	// accepted here may still be refused there.
	if err := w.Close(); err != nil {
		return z.Err(err, "finish %q", key)
	}

	// A stream that ends early looks exactly like a stream that ended, and the
	// length is what tells them apart. Said here rather than left to the
	// read-back, so that a spool which was told not to verify still catches it.
	if want.Size >= 0 && n != want.Size {
		return fmt.Errorf("%q was to be %d bytes and %d arrived", key, want.Size, n)
	}

	return nil
}

func (s *Sink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	if err := ctx.Err(); err != nil {
		return sink.Meta{}, err
	}

	key := s.key(name)

	a, err := s.api.Attrs(ctx, key)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			// Not there is told from cannot-say, so that the engine sends it
			// again rather than reading the answer as a reason to give up.
			return sink.Meta{}, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}

		return sink.Meta{}, z.Err(err, "ask about %q", key)
	}

	// The hash this put there, if it is still there. A store that has lost it
	// answers with the length alone, which is what the engine then checks.
	return sink.Meta{Size: a.Size, Digest: a.Metadata[DigestKey]}, nil
}

// Close lets the client go, if it was this sink that made it.
func (s *Sink) Close() error {
	if !s.owned {
		return nil
	}

	return z.ErrIf(s.api.Close(), "close the connection to the store")
}

func (s *Sink) key(name string) string {
	if s.prefix == "" {
		return name
	}

	return s.prefix + "/" + name
}

// client is [storage.Client] behind [API].
type client struct {
	c      *storage.Client
	bucket string
	chunk  int
}

func (c *client) Writer(ctx context.Context, name string, meta map[string]string) io.WriteCloser {
	w := c.c.Bucket(c.bucket).Object(name).NewWriter(ctx)
	w.ChunkSize = c.chunk
	w.ContentType = "application/octet-stream"
	w.Metadata = meta

	return w
}

func (c *client) Attrs(ctx context.Context, name string) (Attrs, error) {
	a, err := c.c.Bucket(c.bucket).Object(name).Attrs(ctx)
	if err != nil {
		return Attrs{}, err
	}

	return Attrs{Size: a.Size, Metadata: a.Metadata}, nil
}

func (c *client) Close() error { return c.c.Close() }

var (
	_ API         = (*client)(nil)
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
	_ sink.Closer = (*Sink)(nil)
)
