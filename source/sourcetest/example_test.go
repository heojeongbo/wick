package sourcetest_test

import (
	"testing"
	"time"

	"github.com/heojeongbo/wick/source"
	sourcemem "github.com/heojeongbo/wick/source/mem"
	"github.com/heojeongbo/wick/source/sourcetest"
)

// The contract, as a test rather than as a paragraph somebody has to interpret.
//
// It probes the optional capabilities and skips what a source does not have, so
// a directory on a read-only mount is not failed for being unable to delete.
// What it does insist on is the part the engine cannot work around: that a key
// which is no longer there answers fs.ErrNotExist, because the engine reads
// that as "it went away" rather than as a failure worth retrying.
func ExampleSuite() {
	// In a package of your own:
	//
	//	func TestMySource(t *testing.T) {
	//		sourcetest.Suite(t, func(t *testing.T, seed map[string][]byte) source.Source {
	//			s := mysource.New(mysource.Options{...})
	//			for k, v := range seed {
	//				s.Add(k, v, time.Now())
	//			}
	//
	//			return s
	//		})
	//	}
	//
	// The seed is handed in rather than written by the suite through the
	// source's own methods, because a source is not required to have any way
	// of putting something into it. Filling it is the one thing only the
	// implementation knows how to do.

	// Output:
}

// And what running it looks like, against the in-memory source this repository
// ships.
func TestExampleSuiteRuns(t *testing.T) {
	sourcetest.Suite(t, func(t *testing.T, seed map[string][]byte) source.Source {
		s := sourcemem.New()
		for k, v := range seed {
			s.Add(k, v, time.Now())
		}

		return s
	})
}
