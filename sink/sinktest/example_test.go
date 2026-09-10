package sinktest_test

import (
	"testing"

	"github.com/heojeongbo/wick/sink"
	sinkmem "github.com/heojeongbo/wick/sink/mem"
	"github.com/heojeongbo/wick/sink/sinktest"
)

// The contract, as a test rather than as a paragraph somebody has to interpret.
//
// Hand it something that makes a fresh sink and it will hold that sink to
// everything the engine relies on: that a write can be read back, that a name
// which is not held answers [fs.ErrNotExist] rather than something else, that a
// second write under the same name replaces rather than refuses.
//
// It probes the optional capabilities and skips what the sink does not have, so
// a sink that cannot be asked what it holds is not failed for it.
func ExampleSuite() {
	// In a package of your own:
	//
	//	func TestMySink(t *testing.T) {
	//		sinktest.Suite(t, func(t *testing.T) sink.Sink {
	//			return mysink.New(mysink.Options{...})
	//		})
	//	}
	//
	// There is the same pair for sources (sourcetest.Suite) and for journals
	// (journaltest.Suite).

	// Output:
}

// Escapes is separate because it is not true of every sink.
//
// A name is a path in a filesystem and an opaque key in an object store, so
// ".." in one is an escape and in the other is a key like any other. Asserting
// it everywhere would be asserting something false.
func ExampleEscapes() {
	// For a sink whose names become paths:
	//
	//	func TestNames(t *testing.T) {
	//		sinktest.Escapes(t, func(t *testing.T) sink.Sink {
	//			return mysink.New(mysink.Options{...})
	//		})
	//	}

	// Output:
}

// And what running it looks like, against the in-memory sink this repository
// ships.
func TestExampleSuiteRuns(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return sinkmem.New() })
}
