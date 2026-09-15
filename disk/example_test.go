package disk_test

import (
	"fmt"

	"github.com/heojeongbo/wick/disk"
)

// How much room is left where something is.
//
// It is one function because that is all the engine needs: `trigger.FreeBelow`
// and `retain.WhenFreeBelow` both ask the same question, and neither has any
// use for the total or the used.
func Example() {
	free, err := disk.Free(".")
	if err != nil {
		panic(err)
	}

	// Some, on any machine that could have run this at all.
	fmt.Println("there is room:", free > 0)

	// Output:
	// there is room: true
}

// A path that is not there is an error and not a zero.
//
// The difference matters more than it looks. Zero is what the triggers and
// policies read as "no room", so answering zero for a directory that has gone
// would make everything carry at once and then delete. The engine turns this
// error into "nobody could say", which they read as a reason to do nothing.
func ExampleFree_whatIsNotThere() {
	_, err := disk.Free("/there/is/no/such/place")
	fmt.Println("refused:", err != nil)

	// Output:
	// refused: true
}
