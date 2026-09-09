//go:build unix

// Package disk answers the one question a collector has about the filesystem it
// is collecting from: how much room is left on it.
//
// # Why there is one file and not one per system
//
// [unix.Statfs] has the same shape on every system this runs on, and the only
// difference is the width of the block size, which a conversion settles. Per-GOOS
// files would compile the same three lines twice and each one would be invisible
// to the other's test run -- a coverage floor cannot be met by code the machine
// running the tests never builds.
package disk

import (
	"golang.org/x/sys/unix"

	"github.com/lesomnus/z"
)

// Free is how many bytes an unprivileged process could still write at path.
//
// It is the available count and not the free one: the difference is the reserve
// only root may spend, and a daemon that counts it is a daemon that believes it
// has room it will be refused.
func Free(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, z.Err(err, "statfs %q", path)
	}

	// Bsize is signed on some systems and not on others, so it is widened here
	// rather than in a file per system.
	return uint64(st.Bsize) * st.Bavail, nil //nolint:gosec // a block size is never negative
}
