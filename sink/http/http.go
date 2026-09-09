// Package http is a sink that puts things on a server over HTTP.
//
// It is the one to reach for when the destination is something the fleet
// already runs -- a collector on the far side of a VPN, an object store behind
// a gateway that issues its own credentials, a machine in the same building.
// It has no cloud SDK behind it and no notion of a region: a base address, a
// method, whatever headers the other end wants, and mutual TLS if it asks.
//
// # Why the read-back is a HEAD and not a GET
//
// The size is what a HEAD gives, and the size is what tells a stream that ended
// early from a stream that ended. Reading the object back to hash it would cost
// as much as sending it did, on the same link that was the reason for sending
// it slowly. A server that keeps a hash can say so in a header, and then the
// hash is what is checked; see [Options.DigestHeader].
package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/heojeongbo/wick/internal/throttle"
	"github.com/heojeongbo/wick/sink"
	"github.com/lesomnus/z"
)

type Options struct {
	// Endpoint is the address things are put under. The name is joined onto
	// its path, so "https://host/drop" and a name of "a/b.rec" is
	// "https://host/drop/a/b.rec".
	Endpoint string

	// Method is how they are put there, and is PUT when it is not said. POST
	// is the other one servers ask for.
	Method string

	// Headers are sent with every request. This is where a bearer token goes,
	// and anything else the far end wants to be told.
	Headers map[string]string

	// DigestHeader is the header the content hash is sent in, and the one a
	// read-back reads it back out of. Unset means the hash is not sent and the
	// read-back checks the length alone.
	DigestHeader string

	// RateLimit is bytes per second, and is no limit when it is not said.
	RateLimit int64

	// TLS is what the client says it is and who it believes. It is read when
	// Client is nil, and ignored otherwise -- a client that was handed over
	// already has whatever it was given.
	TLS TLS

	// Client is what the requests go through, and is one of this package's
	// making when nil.
	Client *http.Client
}

type Sink struct {
	base    *url.URL
	method  string
	headers map[string]string
	digestH string
	rate    int64
	client  *http.Client
}

func New(o Options) (*Sink, error) {
	if o.Endpoint == "" {
		return nil, fmt.Errorf("an http sink has to say where to put things")
	}

	u, err := url.Parse(o.Endpoint)
	if err != nil {
		return nil, z.Err(err, "read the address %q", o.Endpoint)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%q is not an address to put things at; it has to be http or https", o.Endpoint)
	}

	m := o.Method
	if m == "" {
		m = http.MethodPut
	}
	switch m {
	case http.MethodPut, http.MethodPost:
	default:
		return nil, fmt.Errorf("%q is not a way to put something somewhere; it is PUT or POST", m)
	}

	c := o.Client
	if c == nil {
		t, err := NewTransport(o.TLS)
		if err != nil {
			return nil, err
		}
		c = &http.Client{Transport: t}
	}

	return &Sink{
		base:    u,
		method:  m,
		headers: o.Headers,
		digestH: o.DigestHeader,
		rate:    o.RateLimit,
		client:  c,
	}, nil
}

func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	u, err := s.url(name)
	if err != nil {
		return err
	}

	req := request(ctx, s.method, u)
	req.Body = io.NopCloser(throttle.Reader(ctx, r, s.rate))

	// Said rather than left to be guessed. A body of unknown length is sent
	// chunked, and a server that has to reassemble the chunks cannot refuse a
	// short one until it has taken all of it.
	if want.Size >= 0 {
		req.ContentLength = want.Size
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}
	if s.digestH != "" && want.Digest != "" {
		req.Header.Set(s.digestH, want.Digest)
	}

	res, err := s.client.Do(req)
	if err != nil {
		return z.Err(err, "put %q", name)
	}
	defer drain(res)

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Errorf("putting %q was answered with %s", name, res.Status)
	}

	return nil
}

func (s *Sink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	u, err := s.url(name)
	if err != nil {
		return sink.Meta{}, err
	}

	req := request(ctx, http.MethodHead, u)
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}

	res, err := s.client.Do(req)
	if err != nil {
		return sink.Meta{}, z.Err(err, "ask about %q", name)
	}
	defer drain(res)

	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		// Not there is told from cannot-say, so that the engine can send it
		// again rather than treat the answer as a reason to give up.
		return sink.Meta{}, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}

	case res.StatusCode < 200 || res.StatusCode > 299:
		return sink.Meta{}, fmt.Errorf("asking about %q was answered with %s", name, res.Status)
	}

	m := sink.Meta{Size: res.ContentLength}
	if s.digestH != "" {
		m.Digest = res.Header.Get(s.digestH)
	}

	return m, nil
}

func (s *Sink) url(name string) (*url.URL, error) {
	if name == "" || name != path.Clean(name) || path.IsAbs(name) || strings.HasPrefix(name, "../") || name == ".." {
		return nil, fmt.Errorf("%q is not a name this sink can put anything under", name)
	}

	u := *s.base
	// The leading slash is put back on by hand. [url.URL.String] adds one when
	// it is missing and [url.URL.RequestURI] does not, so an endpoint written
	// without a path -- which is the ordinary way to write one -- would put a
	// relative path on the request line and be answered with 400.
	u.Path = "/" + strings.TrimPrefix(path.Join(u.Path, name), "/")
	u.RawPath = ""

	return &u, nil
}

// request builds one by hand rather than through [http.NewRequestWithContext].
//
// That function's only job here would be to parse a string this package has
// just finished assembling from a [url.URL] it parsed at startup, and to check
// a method it has already checked. It cannot fail, and a branch that cannot
// fail is a branch nothing ever runs -- so there is no branch.
func request(ctx context.Context, method string, u *url.URL) *http.Request {
	return (&http.Request{
		Method:     method,
		URL:        u,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Host:       u.Host,
	}).WithContext(ctx)
}

// drain reads what is left of a reply and closes it, so that the connection
// goes back to the pool instead of being thrown away. On a machine making one
// request per file this is the difference between one handshake and hundreds.
func drain(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, drainLimit))
	_ = res.Body.Close()
}

// drainLimit is how much of a reply is worth reading in order to keep the
// connection. Past this it is cheaper to make a new one than to read the rest.
const drainLimit = 64 << 10

// TLS is what a client needs in order to be let in.
type TLS struct {
	// CAFile is who to believe about the server. Unset means whoever the
	// machine already believes.
	CAFile string
	// CertFile and KeyFile are what the client says it is, for a server that
	// asks. Both or neither.
	CertFile string
	KeyFile  string
	// Insecure stops the server's name being checked. It is here because a
	// machine on a factory network may have no name to check, and it is named
	// so that turning it on is a decision somebody wrote down.
	Insecure bool
}

// NewTransport is the transport this package uses when it is not handed one.
//
// The timeouts are the reason it is not [http.DefaultTransport]. Every one of
// them is about a connection that is not moving; none of them is about a
// request taking a long time, because a request here is half a gigabyte and
// taking a long time is what it is supposed to do. A deadline on the whole
// thing would be a size limit written as a clock.
func NewTransport(c TLS) (*http.Transport, error) {
	t := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck // it is one
	t.TLSHandshakeTimeout = 10 * time.Second
	t.ResponseHeaderTimeout = 60 * time.Second
	t.ExpectContinueTimeout = 5 * time.Second
	t.IdleConnTimeout = 90 * time.Second

	if (c.CertFile == "") != (c.KeyFile == "") {
		return nil, fmt.Errorf("a client certificate and its key go together, and only one of them was given")
	}
	if c.CAFile == "" && c.CertFile == "" && !c.Insecure {
		return t, nil
	}

	cfg := &tls.Config{MinVersion: tls.VersionTLS12} //nolint:gosec // Insecure is read below
	cfg.InsecureSkipVerify = c.Insecure              //nolint:gosec // it is what the field is for

	if c.CAFile != "" {
		b, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, z.Err(err, "read the authority %q", c.CAFile)
		}

		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("%q holds no certificate this can read", c.CAFile)
		}
		cfg.RootCAs = pool
	}

	if c.CertFile != "" {
		// Read once here so that a certificate which is not there, or is not a
		// certificate, is a refusal at startup rather than a handshake that
		// fails in an hour.
		if _, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile); err != nil {
			return nil, z.Err(err, "read the client certificate %q", c.CertFile)
		}

		// And read again at every handshake, because a client certificate on a
		// machine like this is often a short-lived one that something else
		// renews underneath. Holding the first one would mean working for an
		// hour and then failing until somebody restarts the daemon.
		cfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			pair, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
			if err != nil {
				return nil, z.Err(err, "read the client certificate %q", c.CertFile)
			}

			return &pair, nil
		}
	}

	t.TLSClientConfig = cfg

	return t, nil
}

var (
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
)
