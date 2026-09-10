// Package sftp is a sink that puts things on a server over SSH.
//
// It is the one a building already has. There is no bucket to stand up, no
// service to run and no credential to mint: there is a machine with a disk, and
// an account on it.
//
// # The host key is checked
//
// [Options.KnownHosts] is required. A host key nobody checks is a sink that
// will one day be somebody else, and the files this carries are the ones nobody
// is watching. Turning it off is possible and the field is called
// [Options.InsecureIgnoreHostKey], so that it is a decision somebody wrote
// down rather than a default nobody noticed.
//
// # It writes beside the name and then renames
//
// The same as the directory sink, for the same reason: anything watching that
// directory -- a person, a script, a second wick carrying onward -- sees the
// whole file or no file, never a growing one.
package sftp

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/heojeongbo/wick/internal/throttle"
	"github.com/heojeongbo/wick/sink"
	"github.com/lesomnus/z"
)

// Client is the part of an SFTP connection this package uses.
//
// Narrowed to these six calls so that a test can make each of them fail without
// a server that has been talked into failing, and so that reading this says
// exactly what is asked of the other end.
type Client interface {
	Create(path string) (io.WriteCloser, error)
	Stat(path string) (os.FileInfo, error)
	Remove(path string) error
	Rename(oldname, newname string) error
	MkdirAll(path string) error
	Close() error
}

type Options struct {
	// Address is the server, host and port. The port is 22 when it is not
	// said.
	Address string

	// User is the account on it.
	User string

	// KeyFile is a private key, and KeyPassphrase is what it is locked with.
	KeyFile       string
	KeyPassphrase string

	// Password is the other way. One of the two, not both.
	Password string

	// KnownHosts is the file the server's key is checked against. Required
	// unless InsecureIgnoreHostKey.
	KnownHosts string

	// InsecureIgnoreHostKey stops the server's key being checked. It is named
	// this way on purpose.
	InsecureIgnoreHostKey bool

	// Path is the directory things go under, and is where the account lands
	// when it is not said.
	Path string

	// Timeout is how long the connection may take to be made. Nothing means
	// [DefaultTimeout].
	Timeout time.Duration

	// RateLimit is bytes per second, and is no limit when it is not said.
	RateLimit int64

	// Dial makes the connection, and is the real one when nil. It is here for
	// the same reason [Client] is an interface.
	Dial func(ctx context.Context, o Options) (Client, error)
}

// DefaultTimeout is how long the connection may take to be made.
//
// It is about reaching the server, not about carrying a file: a transfer of
// half a gigabyte on a slow link is meant to take a long time, and a deadline
// on the whole thing would be a size limit written as a clock.
const DefaultTimeout = 30 * time.Second

type Sink struct {
	root string
	rate int64

	dial func(ctx context.Context, o Options) (Client, error)
	opts Options

	// The connection is made when it is first wanted and kept. A spool
	// carrying a thousand files is a thousand handshakes otherwise, and the
	// handshake is the expensive part of talking to one of these.
	mu     sync.Mutex
	client Client
}

func New(o Options) (*Sink, error) {
	switch {
	case o.Address == "":
		return nil, fmt.Errorf("an sftp sink has to say which server")

	case o.User == "":
		return nil, fmt.Errorf("an sftp sink has to say which account")

	case o.KeyFile == "" && o.Password == "":
		return nil, fmt.Errorf("an sftp sink has to say how to log in: a key file or a password")

	case o.KeyFile != "" && o.Password != "":
		return nil, fmt.Errorf("a key file and a password are two answers to the same question; give one")

	case o.KnownHosts == "" && !o.InsecureIgnoreHostKey:
		// A host key nobody checks is a sink that will one day be somebody
		// else, and these are the files nobody is watching.
		return nil, fmt.Errorf("an sftp sink has to say which known_hosts to check the server against, or say insecure_ignore_host_key")
	}

	if !strings.Contains(o.Address, ":") {
		o.Address += ":22"
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}

	d := o.Dial
	if d == nil {
		d = dial
	}

	return &Sink{
		root: strings.Trim(o.Path, "/"),
		rate: o.RateLimit,
		dial: d,
		opts: o,
	}, nil
}

func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	to, err := s.resolve(name)
	if err != nil {
		return err
	}

	c, err := s.connect(ctx)
	if err != nil {
		return err
	}

	if dir := path.Dir(to); dir != "." && dir != "/" {
		if err := c.MkdirAll(dir); err != nil {
			return z.Err(err, "make the directory %q goes in", name)
		}
	}

	// The dot keeps it out of the way of anything looking for a pattern, and
	// the suffix says what it is to whoever finds one left behind.
	tmp := path.Join(path.Dir(to), "."+path.Base(to)+".partial")

	w, err := c.Create(tmp)
	if err != nil {
		return z.Err(err, "make %q", tmp)
	}

	n, err := io.Copy(w, throttle.Reader(ctx, r, s.rate))
	if err != nil {
		_ = w.Close()
		_ = c.Remove(tmp)

		return z.Err(err, "write %q", tmp)
	}
	if err := w.Close(); err != nil {
		_ = c.Remove(tmp)

		return z.Err(err, "finish %q", tmp)
	}

	// Before the rename, so that a stream which ended early never becomes a
	// file under the name of the whole thing.
	if want.Size >= 0 && n != want.Size {
		_ = c.Remove(tmp)

		return fmt.Errorf("%q was to be %d bytes and %d arrived", name, want.Size, n)
	}

	// A server that already holds the name will refuse the rename, so what is
	// there goes first. The window between the two is the price of SFTP not
	// having a rename that replaces.
	_ = c.Remove(to)

	if err := c.Rename(tmp, to); err != nil {
		_ = c.Remove(tmp)

		return z.Err(err, "put %q in place", name)
	}

	return nil
}

func (s *Sink) Stat(ctx context.Context, name string) (sink.Meta, error) {
	if err := ctx.Err(); err != nil {
		return sink.Meta{}, err
	}

	p, err := s.resolve(name)
	if err != nil {
		return sink.Meta{}, err
	}

	c, err := s.connect(ctx)
	if err != nil {
		return sink.Meta{}, err
	}

	fi, err := c.Stat(p)
	if err != nil {
		// Left as it is, so that a caller can tell "not there" from "cannot
		// say" with [errors.Is].
		return sink.Meta{}, z.Err(err, "look at %q", name)
	}

	// No digest: SFTP keeps no hash, and reading the file back to compute one
	// would cost as much as writing it did on the same link. The read-back is
	// a read-back of the length.
	return sink.Meta{Size: fi.Size()}, nil
}

// Close lets the connection go. The engine calls it on the sinks it made.
func (s *Sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client == nil {
		return nil
	}

	c := s.client
	s.client = nil

	return z.ErrIf(c.Close(), "close the connection to %q", s.opts.Address)
}

// connect is the connection, made if there is not one yet.
func (s *Sink) connect(ctx context.Context) (Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.client != nil {
		return s.client, nil
	}

	c, err := s.dial(ctx, s.opts)
	if err != nil {
		// Flattened with %v rather than wrapped with %w, and this is the one
		// place in this package that does it.
		//
		// Dialling reads the private key and the known_hosts, and either of
		// those being absent is an *fs.PathError. Left in the chain, that
		// makes "I could not reach the server" indistinguishable by
		// [errors.Is] from "I do not hold that name" -- which is the one
		// answer a sink gives that the engine acts on. It would read a missing
		// known_hosts as a write that did not stick and send the file again,
		// every pass, until the item was set aside.
		return nil, fmt.Errorf("reach %q: %v", s.opts.Address, err)
	}
	s.client = c

	return c, nil
}

func (s *Sink) resolve(name string) (string, error) {
	if name == "" || name != path.Clean(name) || path.IsAbs(name) || strings.HasPrefix(name, "../") || name == ".." {
		return "", fmt.Errorf("%q is not a name this sink can put anything under", name)
	}
	if s.root == "" {
		return name, nil
	}

	return s.root + "/" + name, nil
}

// dial makes the real connection.
func dial(ctx context.Context, o Options) (Client, error) {
	auth, err := authOf(o)
	if err != nil {
		return nil, err
	}

	hosts, err := hostKeyOf(o)
	if err != nil {
		return nil, err
	}

	d := net.Dialer{Timeout: o.Timeout}
	conn, err := d.DialContext(ctx, "tcp", o.Address)
	if err != nil {
		return nil, err
	}

	cc, chans, reqs, err := ssh.NewClientConn(conn, o.Address, &ssh.ClientConfig{
		User:            o.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hosts,
		Timeout:         o.Timeout,
	})
	if err != nil {
		_ = conn.Close()

		return nil, err
	}

	c, err := sftp.NewClient(ssh.NewClient(cc, chans, reqs))
	if err != nil {
		_ = cc.Close()

		return nil, err
	}

	return client{c}, nil
}

func authOf(o Options) (ssh.AuthMethod, error) {
	if o.Password != "" {
		return ssh.Password(o.Password), nil
	}

	b, err := os.ReadFile(o.KeyFile)
	if err != nil {
		return nil, z.Err(err, "read the key %q", o.KeyFile)
	}

	var signer ssh.Signer
	if o.KeyPassphrase == "" {
		signer, err = ssh.ParsePrivateKey(b)
	} else {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(b, []byte(o.KeyPassphrase))
	}
	if err != nil {
		return nil, z.Err(err, "read the key %q", o.KeyFile)
	}

	return ssh.PublicKeys(signer), nil
}

func hostKeyOf(o Options) (ssh.HostKeyCallback, error) {
	if o.InsecureIgnoreHostKey {
		return ssh.InsecureIgnoreHostKey(), nil //nolint:gosec // it is what the field is named for
	}

	cb, err := knownhosts.New(o.KnownHosts)
	if err != nil {
		return nil, z.Err(err, "read the known hosts %q", o.KnownHosts)
	}

	return cb, nil
}

// client is [sftp.Client] behind [Client].
type client struct{ c *sftp.Client }

func (c client) Stat(p string) (os.FileInfo, error) { return c.c.Stat(p) }
func (c client) Remove(p string) error              { return c.c.Remove(p) }
func (c client) Rename(from, to string) error       { return c.c.Rename(from, to) }
func (c client) MkdirAll(p string) error            { return c.c.MkdirAll(p) }
func (c client) Close() error                       { return c.c.Close() }

// The nil is written out rather than returned as a typed one, since a typed nil
// in an interface is not nil.
func (c client) Create(p string) (io.WriteCloser, error) {
	f, err := c.c.Create(p)
	if err != nil {
		return nil, err
	}

	return f, nil
}

var (
	_ Client      = client{}
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
	_ sink.Closer = (*Sink)(nil)
)
