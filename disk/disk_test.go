//go:build unix

package disk_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/disk"
)

func TestFree(t *testing.T) {
	t.Run("a directory that exists has room in it", func(t *testing.T) {
		x := require.New(t)

		n, err := disk.Free(t.TempDir())
		x.NoError(err)

		// Not a number, since the machine running this decides that. Only that
		// something answered, because zero is what a failure that was swallowed
		// would also look like.
		x.Positive(n)
	})
	t.Run("a path that is not there is refused", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "no-such-directory")
		n, err := disk.Free(p)
		x.ErrorContains(err, "statfs")
		x.ErrorContains(err, p)
		x.Zero(n)
	})
}
