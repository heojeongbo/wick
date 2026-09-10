package retain_test

import (
	"context"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/heojeongbo/wick/retain"
	"github.com/heojeongbo/wick/source"
	sourcemem "github.com/heojeongbo/wick/source/mem"
)

// readOnly is a source that can be scanned and read and nothing else, which is
// a directory on a read-only mount.
type readOnly struct{ inner source.Source }

func (r readOnly) Scan(ctx context.Context) iter.Seq2[source.Item, error] {
	return r.inner.Scan(ctx)
}

func (r readOnly) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return r.inner.Open(ctx, key)
}

// What becomes of the original, once every destination has confirmed it.
//
// There is no default. A configuration that said nothing gets [Keep], and that
// is deliberate: the sensible default for deleting a file depends on what the
// file is, and a policy that guesses is one that deletes something it should
// not have on a machine nobody was watching.
func Example() {
	// Keep it for a day after it arrived, then delete it.
	p := retain.Grace(24*time.Hour, retain.Delete())

	fmt.Println(p)

	// Output:
	// delete after 24h0m0s
}

// A policy that deletes needs a source that can delete, and says so at startup
// rather than at the first carry.
func ExampleCheck() {
	// A source that can be read and nothing else.
	err := retain.Check(retain.Delete(), readOnly{sourcemem.New()})
	fmt.Println(err)

	// One that can.
	fmt.Println(retain.Check(retain.Delete(), sourcemem.New()))

	// Output:
	// the policy delete needs a source that can delete, and this one cannot
	// <nil>
}

// "Delete when the disk gets tight, and keep it otherwise."
//
// A filesystem that cannot be measured reads as "nobody could say" rather than
// as "no room", so a source that cannot say deletes nothing.
func ExampleWhenFreeBelow() {
	p := retain.WhenFreeBelow(20<<30, retain.Delete())

	fmt.Println(p)

	// Output:
	// delete when less than 21474836480 bytes are free
}

// The first one that has something to say wins, so that a special case can be
// written in front of the general one.
func ExampleFirst() {
	p := retain.First(
		retain.WhenFreeBelow(20<<30, retain.Delete()),
		retain.Grace(7*24*time.Hour, retain.Delete()),
	)

	fmt.Println(p)

	// Output:
	// first of [delete when less than 21474836480 bytes are free, delete after 168h0m0s]
}
