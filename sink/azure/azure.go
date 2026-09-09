// Package azure is a sink that puts things in Azure Blob Storage.
//
// # The read-back is a hash and not a length
//
// Azure keeps an MD5 of what it holds, which is not what this computes. So the
// SHA-256 goes into the blob's own metadata on the way up and comes back out of
// its properties on the way down, the same way the GCS sink does it -- which
// makes this read-back as strong as the S3 one rather than a comparison of
// lengths.
//
// # What credentials it prefers
//
// None written down. A managed identity is a credential the platform mints,
// rotates and can revoke, and it is not in a file on a machine somebody can
// walk up to. A connection string, an account key and a SAS are all supported
// because not every deployment has the alternative, and each of them is a
// second choice rather than a first.
package azure

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"

	"github.com/heojeongbo/wick/internal/throttle"
	"github.com/heojeongbo/wick/sink"
	"github.com/lesomnus/z"
)

// DigestKey is the metadata key the content hash is kept under.
//
// Azure folds metadata names to a canonical case, so this is written and read
// through [Properties], which lower-cases what it finds. A name that comes back
// spelled differently from the way it went in is a read-back that never
// matches.
const DigestKey = "sha256"

// API is the part of the blob client this package uses.
//
// Three calls. It is an interface so that a test can make each of them fail
// without a subscription, a container or a network, and so that reading this
// says exactly what is asked of the other end.
type API interface {
	// Upload sends the whole of r, with the metadata set on the blob.
	Upload(ctx context.Context, name string, r io.Reader, meta map[string]string) error
	// Properties is what the store holds under a name. It answers with an
	// error [bloberror.BlobNotFound] when it holds nothing.
	Properties(ctx context.Context, name string) (Properties, error)
}

// Properties is the part of a blob's properties this package reads.
type Properties struct {
	Size     int64
	Metadata map[string]string
}

const (
	// DefaultBlockSize is how much is buffered and sent at a time. It is the
	// SDK's minimum, and the size of the buffer each concurrent upload holds.
	DefaultBlockSize = 1 << 20
	// DefaultConcurrency is how many blocks are in the air at once. One,
	// because the link this is usually on is shared with something that
	// matters more.
	DefaultConcurrency = 1
)

type Options struct {
	// Account is the storage account, and Container is the container in it.
	// ServiceURL is worked out from Account when it is not said, which is what
	// a deployment reaching a private endpoint or an emulator sets.
	Account    string
	Container  string
	ServiceURL string

	// Prefix is put in front of every name so that one container can hold more
	// than one thing.
	Prefix string

	// One of these, or none of them. None means a managed identity, which is
	// the way round to prefer: the platform mints it, rotates it and can take
	// it away.
	ConnectionString string
	AccountKey       string
	SAS              string

	// BlockSize and Concurrency are how a large blob is broken up.
	BlockSize   int64
	Concurrency int

	// RateLimit is bytes per second, and is no limit when it is not said.
	RateLimit int64

	// Transport is what the requests go through. It is here for a deployment
	// behind a proxy or with a certificate authority of its own, and it is what
	// a test hands a transport of its own through.
	Transport *http.Client

	// API is the client, and is one of this package's making when nil.
	API API
}

type Sink struct {
	api    API
	prefix string
	rate   int64
}

func New(ctx context.Context, o Options) (*Sink, error) {
	said := 0
	for _, s := range []string{o.ConnectionString, o.AccountKey, o.SAS} {
		if s != "" {
			said++
		}
	}

	switch {
	case o.Container == "":
		return nil, fmt.Errorf("an azure sink has to say which container")

	case o.Account == "" && o.ServiceURL == "" && o.ConnectionString == "":
		return nil, fmt.Errorf("an azure sink has to say which account, or a service url, or a connection string")

	case said > 1:
		return nil, fmt.Errorf("a connection string, an account key and a shared access signature are three answers to the question of who this is; give one")

	case o.AccountKey != "" && o.Account == "":
		return nil, fmt.Errorf("an account key is the key of an account, and no account is named")

	case o.BlockSize < 0:
		return nil, fmt.Errorf("a block of %d bytes is not an amount to send at a time", o.BlockSize)

	case o.Concurrency < 0:
		return nil, fmt.Errorf("%d blocks at once is not a number of blocks to send", o.Concurrency)
	}

	api := o.API
	if api == nil {
		var err error
		if api, err = newClient(o); err != nil {
			return nil, err
		}
	}

	return &Sink{api: api, prefix: strings.Trim(o.Prefix, "/"), rate: o.RateLimit}, nil
}

func newClient(o Options) (API, error) {
	opts := &azblob.ClientOptions{}
	if o.Transport != nil {
		opts.Transport = o.Transport
	}

	url := o.ServiceURL
	if url == "" {
		url = fmt.Sprintf("https://%s.blob.core.windows.net/", o.Account)
	}

	c, err := clientOf(o, url, opts)
	if err != nil {
		return nil, z.Err(err, "work out how to reach the store")
	}

	return &client{
		c:         c,
		container: o.Container,
		block:     blockOf(o),
		conc:      concOf(o),
	}, nil
}

func clientOf(o Options, url string, opts *azblob.ClientOptions) (*azblob.Client, error) {
	switch {
	case o.ConnectionString != "":
		return azblob.NewClientFromConnectionString(o.ConnectionString, opts)

	case o.AccountKey != "":
		cred, err := azblob.NewSharedKeyCredential(o.Account, o.AccountKey)
		if err != nil {
			return nil, err
		}

		return azblob.NewClientWithSharedKeyCredential(url, cred, opts)

	case o.SAS != "":
		// The signature is the credential, and it travels in the address.
		return azblob.NewClientWithNoCredential(url+"?"+strings.TrimPrefix(o.SAS, "?"), opts)
	}

	cred, err := newDefaultCredential(nil)
	if err != nil {
		return nil, err
	}

	return azblob.NewClient(url, cred, opts)
}

// newDefaultCredential is a variable rather than a call because the real one is
// built never to fail: it collects the ways a platform might say who this is
// and puts off deciding until a token is asked for. That makes the refusal here
// unreachable through any configuration, and it is still the branch a machine
// would take if a future version of the SDK did refuse -- so it is one a test
// runs rather than one nobody has.
var newDefaultCredential = func(o *azidentity.DefaultAzureCredentialOptions) (azcore.TokenCredential, error) {
	return azidentity.NewDefaultAzureCredential(o)
}

func blockOf(o Options) int64 {
	if o.BlockSize == 0 {
		return DefaultBlockSize
	}

	return o.BlockSize
}

func concOf(o Options) int {
	if o.Concurrency == 0 {
		return DefaultConcurrency
	}

	return o.Concurrency
}

func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	key := s.key(name)

	var meta map[string]string
	if want.Digest != "" {
		meta = map[string]string{DigestKey: want.Digest}
	}

	// Counted on the way past, so that a stream which ended early is caught
	// here as well as by the read-back -- a spool told not to verify still
	// gets that much.
	counted := &counter{r: throttle.Reader(ctx, r, s.rate)}

	if err := s.api.Upload(ctx, key, counted, meta); err != nil {
		return z.Err(err, "put %q", key)
	}

	if want.Size >= 0 && counted.n != want.Size {
		return fmt.Errorf("%q was to be %d bytes and %d arrived", key, want.Size, counted.n)
	}

	return nil
}

func (s *Sink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	if err := ctx.Err(); err != nil {
		return sink.Meta{}, err
	}

	key := s.key(name)

	p, err := s.api.Properties(ctx, key)
	if err != nil {
		if bloberror.HasCode(err, bloberror.BlobNotFound, bloberror.ContainerNotFound) {
			// Not there is told from cannot-say, so that the engine sends it
			// again rather than reading the answer as a reason to give up.
			return sink.Meta{}, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
		}

		return sink.Meta{}, z.Err(err, "ask about %q", key)
	}

	return sink.Meta{Size: p.Size, Digest: p.Metadata[DigestKey]}, nil
}

func (s *Sink) key(name string) string {
	if s.prefix == "" {
		return name
	}

	return s.prefix + "/" + name
}

// client is [azblob.Client] behind [API].
type client struct {
	c         *azblob.Client
	container string
	block     int64
	conc      int
}

func (c *client) Upload(ctx context.Context, name string, r io.Reader, meta map[string]string) error {
	_, err := c.c.UploadStream(ctx, c.container, name, r, &azblob.UploadStreamOptions{
		BlockSize:   c.block,
		Concurrency: c.conc,
		Metadata:    pointers(meta),
	})

	return err
}

func (c *client) Properties(ctx context.Context, name string) (Properties, error) {
	res, err := c.c.ServiceClient().
		NewContainerClient(c.container).
		NewBlobClient(name).
		GetProperties(ctx, nil)
	if err != nil {
		return Properties{}, err
	}

	p := Properties{Metadata: values(res.Metadata)}
	if res.ContentLength != nil {
		p.Size = *res.ContentLength
	}

	return p, nil
}

// pointers is what the SDK wants metadata as.
func pointers(m map[string]string) map[string]*string {
	if m == nil {
		return nil
	}

	out := make(map[string]*string, len(m))
	for k, v := range m {
		out[k] = &v
	}

	return out
}

// values is the other way, and folds the names to lower case.
//
// Azure canonicalizes metadata names, so what was written as "sha256" comes
// back as "Sha256". A name that comes back spelled differently is a read-back
// that never matches.
func values(m map[string]*string) map[string]string {
	if m == nil {
		return nil
	}

	out := make(map[string]string, len(m))
	for k, v := range m {
		if v != nil {
			out[strings.ToLower(k)] = *v
		}
	}

	return out
}

// counter counts what goes past it.
type counter struct {
	r io.Reader
	n int64
}

func (c *counter) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err
}

var (
	_ API         = (*client)(nil)
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
)
