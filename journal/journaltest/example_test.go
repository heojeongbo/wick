package journaltest_test

import (
	"testing"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/journal/journaltest"
	journalmem "github.com/heojeongbo/wick/journal/mem"
)

// The contract, as a test rather than as a paragraph somebody has to interpret.
//
// Hand it something that makes a fresh journal and it holds that journal to
// what the engine relies on: that a record written is a record read back, that
// a key nothing was written under is absent rather than zero, that a walk sees
// one consistent view, and that forgetting twice is forgetting once.
//
// It is worth running against anything that keeps records, however it keeps
// them. There is the same pair for sinks (sinktest.Suite) and for sources
// (sourcetest.Suite).
func ExampleSuite() {
	// In a package of your own:
	//
	//	func TestMyJournal(t *testing.T) {
	//		journaltest.Suite(t, func(t *testing.T) journal.Journal {
	//			return myjournal.Open(filepath.Join(t.TempDir(), "j.db"))
	//		})
	//	}

	// Output:
}

// And what running it looks like, against the in-memory journal this
// repository ships.
func TestExampleSuiteRuns(t *testing.T) {
	journaltest.Suite(t, func(t *testing.T) journal.Journal { return journalmem.New() })
}
