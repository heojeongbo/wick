// Package dir is a directory on a filesystem, which is where things accumulate
// when nobody has decided where else they should go.
package dir

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/heojeongbo/wick/source"
	"github.com/lesomnus/z"
)

// FS is the part of a filesystem this package uses.
//
// # Why it is an interface
//
// Because the failures are the interesting part. A file whose permissions
// changed under the daemon, a directory that went away between the scan and the
// read, a rename refused across a mount -- those are the paths worth having run
// before a machine runs them. The obvious way to provoke them is chmod, and
// that does not work: the test stage of the Docker build runs as root, and root
// is refused nothing. So the seam is here instead, where a test can say "this
// read fails" and mean it on every machine.
type FS interface {
	ReadDir(name string) ([]fs.DirEntry, error)
	Open(name string) (io.ReadCloser, error)
	Create(name string) (io.WriteCloser, error)
	Remove(name string) error
	Rename(oldpath, newpath string) error
	MkdirAll(name string, perm fs.FileMode) error
}

// Options is how a directory source is described.
type Options struct {
	// Path is the directory to look in.
	Path string

	// Include is what to take. Empty means everything. A pattern with a "/" in
	// it is matched against the whole key; one without is matched against the
	// file's own name, which is what somebody writing "*.rec" means.
	Include []string
	// Exclude is what to leave, and is applied first, so that something both
	// lists is left.
	Exclude []string

	// Recursive says whether to look inside directories.
	Recursive bool

	// MinSize leaves anything smaller. A file of zero bytes is usually one that
	// has been created and not yet written, and carrying it would record a
	// carry of the wrong thing under the right name.
	MinSize int64

	// FS is the filesystem, and is the real one when nil.
	FS FS

	// Notify makes the watcher [Source.Watch] listens to, and is fsnotify when
	// nil. See [Notifier] for why it can be handed over.
	Notify func() (Notifier, error)
}

type Source struct {
	root        string
	include     []string
	exclude     []string
	recursive   bool
	minSize     int64
	fs          FS
	newNotifier func() (Notifier, error)
}

func New(o Options) (*Source, error) {
	if o.Path == "" {
		return nil, fmt.Errorf("a directory source has to say which directory")
	}
	// Refused here rather than silently matching nothing. A pattern with a
	// typo in it is a spool that quietly carries no files, and quietly is the
	// part that costs a week.
	for _, ps := range [][]string{o.Include, o.Exclude} {
		for _, p := range ps {
			if _, err := path.Match(p, "x"); err != nil {
				return nil, fmt.Errorf("%q is not a pattern: %w", p, err)
			}
		}
	}
	if o.MinSize < 0 {
		return nil, fmt.Errorf("a smallest size of %d means nothing; leave it unsaid to take everything", o.MinSize)
	}

	f := o.FS
	if f == nil {
		f = OS{}
	}

	return &Source{
		root:        o.Path,
		include:     o.Include,
		exclude:     o.Exclude,
		recursive:   o.Recursive,
		minSize:     o.MinSize,
		fs:          f,
		newNotifier: o.Notify,
	}, nil
}

// Root is the directory being looked in.
func (s *Source) Root() string {
	return s.root
}

func (s *Source) Scan(ctx context.Context) iter.Seq2[source.Item, error] {
	return func(yield func(source.Item, error) bool) {
		// Breadth first, so that the shallow files -- which is all of them, in
		// the usual case -- are yielded before any time is spent descending.
		todo := []string{""}
		for len(todo) > 0 {
			rel := todo[0]
			todo = todo[1:]

			es, err := s.fs.ReadDir(filepath.Join(s.root, filepath.FromSlash(rel)))
			if err != nil {
				// One directory that cannot be read must not hide the others.
				if !yield(source.Item{}, z.Err(err, "read %q", path.Join(s.root, rel))) {
					return
				}

				continue
			}

			for _, e := range es {
				if err := ctx.Err(); err != nil {
					yield(source.Item{}, err)

					return
				}

				key := path.Join(rel, e.Name())
				if e.IsDir() {
					if s.recursive {
						todo = append(todo, key)
					}

					continue
				}
				// A symlink is not followed and a pipe is not opened. Following
				// the first would carry whatever it points at, which is not
				// what the directory holds; opening the second would block on a
				// read that never returns.
				if !e.Type().IsRegular() {
					continue
				}
				if !s.takes(key, e.Name()) {
					continue
				}

				fi, err := e.Info()
				if err != nil {
					if !yield(source.Item{Key: key}, z.Err(err, "look at %q", key)) {
						return
					}

					continue
				}
				if fi.Size() < s.minSize {
					continue
				}

				if !yield(source.Item{Key: key, Size: fi.Size(), ModAt: fi.ModTime()}, nil) {
					return
				}
			}
		}
	}
}

func (s *Source) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	p, err := s.resolve(key)
	if err != nil {
		return nil, err
	}

	r, err := s.fs.Open(p)

	return r, z.ErrIf(err, "open %q", p)
}

func (s *Source) Remove(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p, err := s.resolve(key)
	if err != nil {
		return err
	}

	return z.ErrIf(s.fs.Remove(p), "remove %q", p)
}

// Move puts the item under dir, keeping its own name.
//
// dir is taken as it is when it is absolute, and relative to the directory
// being watched otherwise.
func (s *Source) Move(ctx context.Context, key string, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if dir == "" {
		return fmt.Errorf("there is nowhere to move %q to", key)
	}

	from, err := s.resolve(key)
	if err != nil {
		return err
	}

	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.root, filepath.FromSlash(dir))
	}
	if err := s.fs.MkdirAll(dir, 0o755); err != nil {
		return z.Err(err, "make %q", dir)
	}

	to := filepath.Join(dir, filepath.Base(from))
	err = s.fs.Rename(from, to)
	if err == nil {
		return nil
	}
	if !isCrossDevice(err) {
		return z.Err(err, "move %q to %q", from, to)
	}

	// The archive is on another filesystem, which a rename cannot cross. Doing
	// it by hand is the only way, and it is worth doing rather than refusing:
	// "keep them, but not here" is exactly what a second disk is for.
	return z.ErrIf(s.copyAcross(from, to), "copy %q to %q", from, to)
}

func (s *Source) copyAcross(from, to string) error {
	r, err := s.fs.Open(from)
	if err != nil {
		return err
	}
	defer r.Close()

	w, err := s.fs.Create(to)
	if err != nil {
		return err
	}

	if _, err := io.Copy(w, r); err != nil {
		_ = w.Close()
		// The half-written copy is taken away. Leaving it would be a file that
		// looks like an archived one and is not.
		_ = s.fs.Remove(to)

		return err
	}
	if err := w.Close(); err != nil {
		_ = s.fs.Remove(to)

		return err
	}

	// Only now. Until the copy is closed there is one copy, and it is this one.
	return s.fs.Remove(from)
}

// resolve is the path a key stands for, and refuses anything that is not a key
// this source would have given out -- which is what keeps a name that has been
// through a template or a config file from reaching outside the directory.
func (s *Source) resolve(key string) (string, error) {
	if key == "" || key != path.Clean(key) || path.IsAbs(key) || strings.HasPrefix(key, "../") || key == ".." {
		return "", fmt.Errorf("%q is not a key of %q", key, s.root)
	}

	return filepath.Join(s.root, filepath.FromSlash(key)), nil
}

// takes says whether the filters let this one through. Exclude is applied
// first, so that something both lists is left.
func (s *Source) takes(key, name string) bool {
	for _, p := range s.exclude {
		if matches(p, key, name) {
			return false
		}
	}
	if len(s.include) == 0 {
		return true
	}
	for _, p := range s.include {
		if matches(p, key, name) {
			return true
		}
	}

	return false
}

// matches reads a pattern with a separator in it as being about the whole key,
// and one without as being about the file's own name -- which is what somebody
// writing "*.rec" means, and what "logs/*.rec" means too.
//
// The error [path.Match] can return is refused by [New], so there is none here.
func matches(pattern, key, name string) bool {
	subject := name
	if strings.Contains(pattern, "/") {
		subject = key
	}
	ok, _ := path.Match(pattern, subject)

	return ok
}

// OS is the real filesystem.
type OS struct{}

func (OS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (OS) Remove(name string) error                   { return os.Remove(name) }
func (OS) Rename(from, to string) error               { return os.Rename(from, to) }
func (OS) MkdirAll(n string, p fs.FileMode) error     { return os.MkdirAll(n, p) }

// The nil is written out rather than returned as a typed one, since a typed nil
// in an interface is not nil, and a caller checking the reader instead of the
// error would get one that panics on the first read.

func (OS) Open(name string) (io.ReadCloser, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}

	return f, nil
}

func (OS) Create(name string) (io.WriteCloser, error) {
	f, err := os.Create(name)
	if err != nil {
		return nil, err
	}

	return f, nil
}

// isCrossDevice reports whether a rename was refused because the two paths are
// not on the same filesystem, which is the one rename failure that has another
// way of getting the job done.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}

var (
	_ FS             = OS{}
	_ source.Source  = (*Source)(nil)
	_ source.Remover = (*Source)(nil)
	_ source.Mover   = (*Source)(nil)
	_ source.Watcher = (*Source)(nil)
)
