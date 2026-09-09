package dir

import (
	"context"
	"path/filepath"

	"github.com/fsnotify/fsnotify"

	"github.com/lesomnus/z"
)

// A Notifier is the part of a filesystem watcher this package uses.
//
// It is an interface for the same reason [FS] is: what matters about a watcher
// is what it does when it fails, and the ways it fails -- the kernel's watch
// limit, a queue that overflowed, a directory that was replaced -- are not
// things a test can arrange on a real one.
type Notifier interface {
	// Add starts watching a directory. Watching a file is not asked for: the
	// interesting events happen to the directory that holds it.
	Add(dir string) error
	// Events carries a wake-up per change. What changed is not carried,
	// because anything carried here is something a caller might believe
	// instead of scanning, and this is not something to be believed.
	Events() <-chan struct{}
	// Errors carries what went wrong. A watcher that has failed stops being
	// listened to; the scan is what keeps the source correct.
	Errors() <-chan error
	Close() error
}

// Watch says when something in the directory has changed.
//
// # What this is and is not
//
// It is not how the source knows what it holds. The scan is that, and it runs
// on its own schedule whatever this does. Watching only makes the scan sooner,
// which on a link billed by the hour is the difference between carrying a
// recording when it is made and carrying it at the next tick.
//
// So every way this can go wrong ends the same way: the channel stops carrying
// wake-ups and the spool goes on scanning. The kernel has a limit on how many
// directories may be watched at once; a queue that overflows drops events
// without saying which; a directory that is deleted and made again takes its
// watch with it. Each of those would be a bug if this were the source of truth,
// and each of them is a slower carry given that it is not.
func (s *Source) Watch(ctx context.Context) (<-chan struct{}, error) {
	n, err := s.notify()
	if err != nil {
		return nil, z.Err(err, "watch %q", s.root)
	}

	if err := s.addTree(n); err != nil {
		_ = n.Close()

		return nil, err
	}

	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		defer n.Close()

		for {
			select {
			case <-ctx.Done():
				return

			case _, ok := <-n.Events():
				if !ok {
					return
				}
				select {
				case out <- struct{}{}:
				default:
					// One is already waiting, and a second wake-up says
					// nothing the first one did not.
				}

			case _, ok := <-n.Errors():
				if !ok {
					return
				}
				// A watcher that has failed is not worth listening to any
				// more. The scan is still correct; it is only slower now.
				return
			}
		}
	}()

	return out, nil
}

// addTree watches the directory, and everything under it when the source is
// recursive.
//
// A directory made after this is not watched, and the file that appears in it
// is found by the next scan rather than at once. Following the tree as it grows
// would mean re-walking it on every event, which on a machine writing files
// quickly costs more than the wait it saves.
func (s *Source) addTree(n Notifier) error {
	if err := n.Add(s.root); err != nil {
		return z.Err(err, "watch %q", s.root)
	}
	if !s.recursive {
		return nil
	}

	// Walked through the same filesystem the scan uses, rather than through
	// [filepath.WalkDir], so that a source given a filesystem is given it here
	// too and the two do not disagree about what is there.
	todo := []string{s.root}
	for len(todo) > 0 {
		d := todo[0]
		todo = todo[1:]

		es, err := s.fs.ReadDir(d)
		if err != nil {
			// A directory that cannot be read is one that will not be watched.
			// It is still scanned, which is what makes this survivable.
			continue
		}

		for _, e := range es {
			if !e.IsDir() {
				continue
			}

			p := filepath.Join(d, e.Name())
			if err := n.Add(p); err != nil {
				return z.Err(err, "watch %q", p)
			}
			todo = append(todo, p)
		}
	}

	return nil
}

// notify makes the watcher. It is a field so a test can hand over one that
// fails on demand; nil is the real one.
func (s *Source) notify() (Notifier, error) {
	if s.newNotifier != nil {
		return s.newNotifier()
	}

	return newFsnotify()
}

// fsNotifier is [fsnotify] behind [Notifier].
type fsNotifier struct {
	w  *fsnotify.Watcher
	ev chan struct{}
}

// newWatcher is a variable rather than a call so that the one way fsnotify
// refuses -- running out of the descriptors a watcher needs, which on a machine
// watching many directories is a real Tuesday -- can be made to happen. There
// is no other way to reach it.
var newWatcher = fsnotify.NewWatcher

func newFsnotify() (Notifier, error) {
	w, err := newWatcher()
	if err != nil {
		return nil, err
	}

	n := &fsNotifier{w: w, ev: make(chan struct{}, 1)}
	go func() {
		defer close(n.ev)

		for range w.Events {
			select {
			case n.ev <- struct{}{}:
			default:
			}
		}
	}()

	return n, nil
}

func (n *fsNotifier) Add(dir string) error    { return n.w.Add(dir) }
func (n *fsNotifier) Events() <-chan struct{} { return n.ev }
func (n *fsNotifier) Errors() <-chan error    { return n.w.Errors }
func (n *fsNotifier) Close() error            { return n.w.Close() }

var _ Notifier = (*fsNotifier)(nil)
