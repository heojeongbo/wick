// Package webdav is a sink that puts things on a WebDAV server.
//
// Nextcloud, ownCloud, the web front end of a NAS: things a building already
// runs and can be pointed at without anybody standing up a bucket.
//
// # Why this is not the http sink with a different name
//
// One thing: collections. A WebDAV server will not accept a PUT into a
// directory that does not exist, and the only way to make one is MKCOL. An
// object store has no directories to make and an http endpoint is whatever the
// server says it is; this is the one where the shape of the name is the shape
// of something on the far side, and has to be built before it can be written
// into.
//
// # When it makes them
//
// Before the first file that needs one, and then never again for that
// collection.
//
// Making them after a refusal would be cheaper -- no round trips at all in the
// usual case, where the collection is already there. It is not what happens,
// because the refusal arrives after the body has been read, and the body is a
// file being streamed from somewhere else. There is no asking for it again. So
// the collections a name needs are made first, once, and remembered.
package webdav

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/heojeongbo/wick/internal/throttle"
	"github.com/heojeongbo/wick/sink"
	wickhttp "github.com/heojeongbo/wick/sink/http"
	"github.com/lesomnus/z"
)

type Options struct {
	// Endpoint is the collection things are put under.
	Endpoint string

	// Auth is who the requests say they are from.
	Auth wickhttp.Auth

	// RateLimit is bytes per second, and is no limit when it is not said.
	RateLimit int64

	// TLS is what the client says it is and who it believes. It is read when
	// Client is nil.
	TLS wickhttp.TLS

	// Client is what the requests go through, and is one of this package's
	// making when nil.
	Client *http.Client
}

type Sink struct {
	base   *url.URL
	auth   wickhttp.Auth
	rate   int64
	client *http.Client

	// made is the collections this has already had to make, so that a spool
	// writing a thousand files into one directory makes it once.
	//
	// It is only ever added to, and only ever after the server has said the
	// collection is there. A restart forgets it, which costs one refusal.
	mu   sync.Mutex
	made map[string]bool
}

func New(o Options) (*Sink, error) {
	if o.Endpoint == "" {
		return nil, fmt.Errorf("a webdav sink has to say where to put things")
	}

	u, err := url.Parse(o.Endpoint)
	if err != nil {
		return nil, z.Err(err, "read the address %q", o.Endpoint)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("%q is not an address to put things at; it has to be http or https", o.Endpoint)
	}

	if err := o.Auth.Check(); err != nil {
		return nil, err
	}

	c := o.Client
	if c == nil {
		t, err := wickhttp.NewTransport(o.TLS)
		if err != nil {
			return nil, err
		}
		c = &http.Client{Transport: t}
	}

	return &Sink{base: u, auth: o.Auth, rate: o.RateLimit, client: c, made: map[string]bool{}}, nil
}

// methodMkcol makes a collection. It is not in net/http, which knows only the
// methods of plain HTTP.
const methodMkcol = "MKCOL"

func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	u, err := s.url(name)
	if err != nil {
		return err
	}

	// Before the body is touched. A refusal arrives after it has been read,
	// and the body is a file being streamed from somewhere else -- there is no
	// asking for it again.
	if dirs := s.missing(name); len(dirs) > 0 {
		if err := s.makeAll(ctx, dirs); err != nil {
			return err
		}
	}

	req := wickhttp.Request(ctx, http.MethodPut, u)
	req.Body = io.NopCloser(throttle.Reader(ctx, r, s.rate))
	if want.Size >= 0 {
		req.ContentLength = want.Size
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if err := s.auth.Apply(req); err != nil {
		return err
	}

	res, err := s.client.Do(req)
	if err != nil {
		return z.Err(err, "put %q", name)
	}
	defer drain(res)

	if !ok(res.StatusCode) {
		return fmt.Errorf("putting %q was answered with %s", name, res.Status)
	}

	return nil
}

func (s *Sink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	u, err := s.url(name)
	if err != nil {
		return sink.Meta{}, err
	}

	req := wickhttp.Request(ctx, http.MethodHead, u)
	if err := s.auth.Apply(req); err != nil {
		return sink.Meta{}, err
	}

	res, err := s.client.Do(req)
	if err != nil {
		return sink.Meta{}, z.Err(err, "ask about %q", name)
	}
	defer drain(res)

	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return sink.Meta{}, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}

	case !ok(res.StatusCode):
		return sink.Meta{}, fmt.Errorf("asking about %q was answered with %s", name, res.Status)
	}

	// No digest: WebDAV keeps no hash of its own, so the read-back is a
	// read-back of the length. A stream that ended early is caught; a file that
	// arrived whole and wrong is not.
	return sink.Meta{Size: res.ContentLength}, nil
}

// missing is the collections a name needs that this has not already made,
// outermost first.
func (s *Sink) missing(name string) []string {
	dir := path.Dir(name)
	if dir == "." || dir == "/" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var want []string
	for d := dir; d != "." && d != "/"; d = path.Dir(d) {
		if s.made[d] {
			break
		}
		want = append([]string{d}, want...)
	}

	return want
}

// makeAll makes each collection in turn, outermost first, since a server will
// not make one inside a collection that is not there either.
func (s *Sink) makeAll(ctx context.Context, dirs []string) error {
	for _, d := range dirs {
		if err := s.make(ctx, d); err != nil {
			return err
		}

		s.mu.Lock()
		s.made[d] = true
		s.mu.Unlock()
	}

	return nil
}

func (s *Sink) make(ctx context.Context, dir string) error {
	req := wickhttp.Request(ctx, methodMkcol, s.join(dir))
	if err := s.auth.Apply(req); err != nil {
		return err
	}

	res, err := s.client.Do(req)
	if err != nil {
		return z.Err(err, "make the collection %q", dir)
	}
	defer drain(res)

	switch {
	case ok(res.StatusCode):
		return nil

	// One that is already there is the answer that was wanted. Servers say it
	// two ways: the method is not allowed on something that exists, or the
	// collection is not empty.
	case res.StatusCode == http.StatusMethodNotAllowed,
		res.StatusCode == http.StatusConflict && s.exists(ctx, dir):
		return nil
	}

	return fmt.Errorf("making the collection %q was answered with %s", dir, res.Status)
}

// exists asks whether something is there, for telling "this collection is
// already here" from "the one above it is not".
func (s *Sink) exists(ctx context.Context, name string) bool {
	_, err := s.Stat(ctx, name)

	return err == nil
}

// url is a name checked and then joined onto the address.
func (s *Sink) url(name string) (*url.URL, error) {
	if name == "" || name != path.Clean(name) || path.IsAbs(name) || strings.HasPrefix(name, "../") || name == ".." {
		return nil, fmt.Errorf("%q is not a name this sink can put anything under", name)
	}

	return s.join(name), nil
}

// join is the same without the checking, for the collections of a name that has
// already been checked. A directory of a clean relative path is one too, so
// checking it again would be a branch nothing can take.
func (s *Sink) join(name string) *url.URL {
	u := *s.base
	// The leading slash is put back on by hand; see the note in the http sink.
	u.Path = "/" + strings.TrimPrefix(path.Join(u.Path, name), "/")
	u.RawPath = ""

	return &u
}

func ok(code int) bool { return code >= 200 && code <= 299 }

// drain reads what is left of a reply and closes it, so the connection goes
// back to the pool instead of being thrown away.
func drain(res *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	_ = res.Body.Close()
}

var (
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
)
