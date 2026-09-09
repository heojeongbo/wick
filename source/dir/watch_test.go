package dir_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/source/dir"
)

// fakeNotifier is a watcher a test can drive and make fail. The real one's
// interesting states -- the kernel's watch limit, a queue that overflowed --
// are not things a test can arrange on the real thing.
type fakeNotifier struct {
	added  []string
	addErr map[string]error

	events chan struct{}
	errs   chan error
	closed bool
}

func newFake() *fakeNotifier {
	return &fakeNotifier{events: make(chan struct{}, 1), errs: make(chan error, 1)}
}

func (n *fakeNotifier) Add(d string) error {
	if err, ok := n.addErr[d]; ok {
		return err
	}
	n.added = append(n.added, d)

	return nil
}

func (n *fakeNotifier) Events() <-chan struct{} { return n.events }
func (n *fakeNotifier) Errors() <-chan error    { return n.errs }

func (n *fakeNotifier) Close() error {
	n.closed = true

	return nil
}

func TestWatch(t *testing.T) {
	t.Run("a change wakes whoever is watching", func(t *testing.T) {
		x := require.New(t)

		n := newFake()
		s := newSource(t, dir.Options{
			Path:   seed(t, map[string][]byte{"a.rec": []byte("a")}),
			Notify: func() (dir.Notifier, error) { return n, nil },
		})

		c, err := s.Watch(t.Context())
		x.NoError(err)

		// Twice, so that the second finds a wake-up already waiting and adds
		// nothing to it.
		n.events <- struct{}{}
		<-c
		n.events <- struct{}{}
		n.events <- struct{}{}
		<-c
	})

	t.Run("told to look inside directories, it watches them too", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"a.rec": []byte("a"), "deep/er/b.rec": []byte("b")})
		n := newFake()
		s := newSource(t, dir.Options{
			Path: root, Recursive: true,
			Notify: func() (dir.Notifier, error) { return n, nil },
		})

		_, err := s.Watch(t.Context())
		x.NoError(err)
		x.ElementsMatch([]string{
			root,
			filepath.Join(root, "deep"),
			filepath.Join(root, "deep", "er"),
		}, n.added)
	})

	t.Run("not told to, it watches only the one", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"deep/b.rec": []byte("b")})
		n := newFake()
		s := newSource(t, dir.Options{Path: root, Notify: func() (dir.Notifier, error) { return n, nil }})

		_, err := s.Watch(t.Context())
		x.NoError(err)
		x.Equal([]string{root}, n.added)
	})

	t.Run("a watcher that cannot be made is said so", func(t *testing.T) {
		x := require.New(t)

		s := newSource(t, dir.Options{
			Path:   t.TempDir(),
			Notify: func() (dir.Notifier, error) { return nil, errRefused },
		})

		_, err := s.Watch(t.Context())
		x.ErrorIs(err, errRefused)
	})

	t.Run("a directory that cannot be watched is said so, and the watcher is let go of", func(t *testing.T) {
		x := require.New(t)

		root := t.TempDir()
		n := newFake()
		n.addErr = map[string]error{root: errRefused}
		s := newSource(t, dir.Options{Path: root, Notify: func() (dir.Notifier, error) { return n, nil }})

		_, err := s.Watch(t.Context())
		x.ErrorIs(err, errRefused)
		x.True(n.closed)
	})

	t.Run("one directory under it that cannot be watched is said so too", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"deep/b.rec": []byte("b")})
		n := newFake()
		n.addErr = map[string]error{filepath.Join(root, "deep"): errRefused}
		s := newSource(t, dir.Options{Path: root, Recursive: true, Notify: func() (dir.Notifier, error) { return n, nil }})

		_, err := s.Watch(t.Context())
		x.ErrorIs(err, errRefused)
		x.True(n.closed)
	})

	// It is still scanned; it is only slower now. That is the whole reason the
	// watcher is allowed to fail this quietly.
	t.Run("a watcher that fails stops being listened to", func(t *testing.T) {
		x := require.New(t)

		n := newFake()
		s := newSource(t, dir.Options{Path: t.TempDir(), Notify: func() (dir.Notifier, error) { return n, nil }})

		c, err := s.Watch(t.Context())
		x.NoError(err)

		n.errs <- errRefused
		for range c { //nolint:revive // draining until it closes is the assertion
		}
	})

	t.Run("a watcher whose events end is let go of", func(t *testing.T) {
		x := require.New(t)

		n := newFake()
		s := newSource(t, dir.Options{Path: t.TempDir(), Notify: func() (dir.Notifier, error) { return n, nil }})

		c, err := s.Watch(t.Context())
		x.NoError(err)

		close(n.events)
		for range c { //nolint:revive // draining until it closes is the assertion
		}
	})

	t.Run("a watcher whose errors end is let go of", func(t *testing.T) {
		x := require.New(t)

		n := newFake()
		s := newSource(t, dir.Options{Path: t.TempDir(), Notify: func() (dir.Notifier, error) { return n, nil }})

		c, err := s.Watch(t.Context())
		x.NoError(err)

		close(n.errs)
		for range c { //nolint:revive // draining until it closes is the assertion
		}
	})

	t.Run("watching ends when the watching does", func(t *testing.T) {
		x := require.New(t)

		n := newFake()
		s := newSource(t, dir.Options{Path: t.TempDir(), Notify: func() (dir.Notifier, error) { return n, nil }})

		ctx, cancel := context.WithCancel(t.Context())
		c, err := s.Watch(ctx)
		x.NoError(err)

		cancel()
		for range c { //nolint:revive // draining until it closes is the assertion
		}
	})

	// It is still scanned, which is what makes this survivable.
	t.Run("a directory that cannot be read is watched as far as it goes", func(t *testing.T) {
		x := require.New(t)

		root := seed(t, map[string][]byte{"deep/er/b.rec": []byte("b")})
		f := &fakeFS{readDir: map[string]error{filepath.Join(root, "deep"): errRefused}}

		n := newFake()
		s := newSource(t, dir.Options{
			Path: root, Recursive: true, FS: f,
			Notify: func() (dir.Notifier, error) { return n, nil },
		})

		_, err := s.Watch(t.Context())
		x.NoError(err)
		// "deep" was watched; what is under it was never reached.
		x.Equal([]string{root, filepath.Join(root, "deep")}, n.added)
	})
}

// The real one, on a real directory, because an interface that nothing ever
// implements for real is an interface that has not been shown to be
// implementable.
func TestTheRealWatcher(t *testing.T) {
	x := require.New(t)

	root := t.TempDir()
	s := newSource(t, dir.Options{Path: root})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	c, err := s.Watch(ctx)
	x.NoError(err)

	x.NoError(os.WriteFile(filepath.Join(root, "a.rec"), []byte("a"), 0o644))

	select {
	case <-c:
	case <-time.After(10 * time.Second):
		t.Fatal("nothing was said about a file that appeared")
	}

	cancel()
	for range c { //nolint:revive // draining until it closes is the assertion
	}
}
