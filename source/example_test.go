package source_test

import (
	"context"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/heojeongbo/wick/source"
	sourcemem "github.com/heojeongbo/wick/source/mem"
)

// Scanning a source, and reading one thing out of it.
//
// Two methods is all a source has to have. Scan is a snapshot: something that
// appears while it runs may or may not be in it, and the next scan has it
// either way.
func Example() {
	ctx := context.Background()

	src := sourcemem.New()
	src.Add("a.rec", []byte("what the robot recorded"), time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC))
	src.Add("b.rec", []byte("and then some more"), time.Date(2026, 9, 15, 5, 0, 0, 0, time.UTC))

	for it, err := range src.Scan(ctx) {
		if err != nil {
			panic(err)
		}

		fmt.Printf("%s %d bytes, written %s\n", it.Key, it.Size, it.ModAt.Format(time.RFC3339))
	}

	rc, err := src.Open(ctx, "a.rec")
	if err != nil {
		panic(err)
	}
	defer rc.Close()

	b, err := io.ReadAll(rc)
	if err != nil {
		panic(err)
	}
	fmt.Println("read back:", string(b))

	// Output:
	// a.rec 23 bytes, written 2026-09-15T04:00:00Z
	// b.rec 18 bytes, written 2026-09-15T05:00:00Z
	// read back: what the robot recorded
}

// readOnly is a source that can be scanned and read and nothing else, which is
// what a directory on a read-only mount is.
type readOnly struct{ inner source.Source }

func (r readOnly) Scan(ctx context.Context) iter.Seq2[source.Item, error] {
	return r.inner.Scan(ctx)
}

func (r readOnly) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	return r.inner.Open(ctx, key)
}

// What an implementation can also do is asked rather than required.
//
// Folding deleting, moving and watching into [source.Source] would make every
// read-only implementation carry methods that answer "no", and would leave the
// engine unable to tell "cannot" from "would not". So they are separate
// interfaces, and the engine upcasts.
//
// This is what `retain.Check` does at startup: a retention that deletes, on a
// source that cannot, is refused there rather than at the first carry.
func ExampleRemover() {
	var (
		full  = sourcemem.New()
		mount = readOnly{sourcemem.New()}
	)

	for what, s := range map[string]source.Source{"a memory source": full, "a read-only mount": mount} {
		_, canDelete := s.(source.Remover)
		_, canMove := s.(source.Mover)
		_, canWatch := s.(source.Watcher)

		fmt.Printf("%-18s delete=%v move=%v watch=%v\n", what+":", canDelete, canMove, canWatch)
	}

	// Unordered output:
	// a memory source:   delete=true move=true watch=true
	// a read-only mount: delete=false move=false watch=false
}

// A source that is somewhere on a filesystem can say where, and that is what
// makes "carry when the disk is nearly full" possible at all.
//
// One that is not says nothing, and the room left reads as "nobody could say"
// rather than as "no room" -- so a trigger that answers to free space does
// nothing rather than firing constantly.
func ExampleRooted() {
	var s source.Source = sourcemem.New()

	_, ok := s.(source.Rooted)
	fmt.Println("a source held in memory has a root:", ok)

	// Output:
	// a source held in memory has a root: false
}
