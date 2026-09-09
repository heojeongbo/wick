// Package dir is a sink that writes into a directory.
//
// It is a local disk, a mounted NAS, or a removable drive somebody walks out to
// the machine with -- which on a robot that has no uplink worth the name is
// still the fastest way to move half a gigabyte. It is also what makes a
// two-step arrangement possible: one wick carries to a directory another
// machine can see, and a second wick carries from there to the cloud.
//
// # What it does not keep
//
// A hash. Reading a file back to compute one costs as much as writing it did,
// and this sink is often the one on the slow disk. So [Sink.Stat] answers with
// the size and nothing else, and a read-back against this sink is a read-back
// of the length: a stream that ended early is caught, and a file that arrived
// whole and wrong is not. That is the trade, and it is why the length is also
// checked before the rename rather than only after it.
package dir

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/heojeongbo/wick/sink"
	"github.com/lesomnus/z"
)

// FS is the part of a filesystem this package uses. It is an interface for the
// same reason the source's is: the failures are the interesting part, and chmod
// cannot produce them in a test stage that runs as root.
type FS interface {
	Create(name string) (io.WriteCloser, error)
	Stat(name string) (fs.FileInfo, error)
	Remove(name string) error
	Rename(oldpath, newpath string) error
	MkdirAll(name string, perm fs.FileMode) error
}

type Options struct {
	// Path is the directory to write into.
	Path string
	// FS is the filesystem, and is the real one when nil.
	FS FS
}

type Sink struct {
	root string
	fs   FS
}

func New(o Options) (*Sink, error) {
	if o.Path == "" {
		return nil, fmt.Errorf("a directory sink has to say which directory")
	}

	f := o.FS
	if f == nil {
		f = OS{}
	}

	return &Sink{root: o.Path, fs: f}, nil
}

// Root is the directory being written into.
func (s *Sink) Root() string {
	return s.root
}

// Put writes to a name of its own and then renames.
//
// The rename is what makes the write atomic: anything watching this directory
// -- including a second wick carrying onward from it -- sees the whole file or
// no file, never a growing one. Writing in place would hand the next stage a
// prefix and call it a recording.
func (s *Sink) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	to, err := s.resolve(name)
	if err != nil {
		return err
	}
	if err := s.fs.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return z.Err(err, "make the directory %q goes in", name)
	}

	// The dot keeps it out of the way of a scan looking for a pattern, and the
	// suffix says what it is to somebody who finds one left behind.
	tmp := filepath.Join(filepath.Dir(to), "."+filepath.Base(to)+".partial")

	w, err := s.fs.Create(tmp)
	if err != nil {
		return z.Err(err, "make %q", tmp)
	}

	n, err := io.Copy(w, r)
	if err != nil {
		_ = w.Close()
		_ = s.fs.Remove(tmp)

		return z.Err(err, "write %q", tmp)
	}
	if err := w.Close(); err != nil {
		_ = s.fs.Remove(tmp)

		return z.Err(err, "finish %q", tmp)
	}

	// Checked before the rename, so that a stream which ended early never
	// becomes a file under the name of the whole thing. A stream that ends
	// early looks exactly like a stream that ended; the length is the only
	// thing that tells them apart.
	if want.Size >= 0 && n != want.Size {
		_ = s.fs.Remove(tmp)

		return fmt.Errorf("%q was to be %d bytes and %d arrived", name, want.Size, n)
	}

	if err := s.fs.Rename(tmp, to); err != nil {
		_ = s.fs.Remove(tmp)

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

	fi, err := s.fs.Stat(p)
	if err != nil {
		// Left as it is, so that a caller can tell "not there" from "cannot
		// say" with [errors.Is]. Wrapping keeps that; replacing would not.
		return sink.Meta{}, z.Err(err, "look at %q", name)
	}

	// No digest: see the note on the package. The size is what there is.
	return sink.Meta{Size: fi.Size()}, nil
}

// resolve refuses a name that would land outside the directory.
func (s *Sink) resolve(name string) (string, error) {
	if name == "" || name != path.Clean(name) || path.IsAbs(name) || strings.HasPrefix(name, "../") || name == ".." {
		return "", fmt.Errorf("%q is not a name this sink can put anything under", name)
	}

	return filepath.Join(s.root, filepath.FromSlash(name)), nil
}

// OS is the real filesystem.
type OS struct{}

func (OS) Stat(name string) (fs.FileInfo, error)  { return os.Stat(name) }
func (OS) Remove(name string) error               { return os.Remove(name) }
func (OS) Rename(from, to string) error           { return os.Rename(from, to) }
func (OS) MkdirAll(n string, p fs.FileMode) error { return os.MkdirAll(n, p) }

// The nil is written out rather than returned as a typed one, since a typed nil
// in an interface is not nil.
func (OS) Create(name string) (io.WriteCloser, error) {
	f, err := os.Create(name)
	if err != nil {
		return nil, err
	}

	return f, nil
}

var (
	_ FS          = OS{}
	_ sink.Sink   = (*Sink)(nil)
	_ sink.Stater = (*Sink)(nil)
)
