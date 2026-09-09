package dir

import (
	"errors"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/require"
)

// The descriptors a watcher needs can run out, on a machine watching many
// directories. Reaching that from outside the package is not possible, so it is
// reached from inside it.
func TestAWatcherThatCannotBeMade(t *testing.T) {
	x := require.New(t)

	want := errors.New("too many open files")
	newWatcher = func() (*fsnotify.Watcher, error) { return nil, want }
	t.Cleanup(func() { newWatcher = fsnotify.NewWatcher })

	n, err := newFsnotify()
	x.ErrorIs(err, want)
	x.Nil(n)

	s, err := New(Options{Path: t.TempDir()})
	x.NoError(err)

	_, err = s.Watch(t.Context())
	x.ErrorIs(err, want)
}
