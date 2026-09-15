package dir_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/sink/dir"
)

// A disk, a NAS, a drive somebody carries.
//
// The write is atomic -- a temporary name and then a rename -- which is what
// makes a two-step arrangement work: one wick carries into a directory another
// machine can see, and a second wick carries from there onward. Anything
// watching sees the whole file or no file, never half of one.
func Example() {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "wick-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	s, err := dir.New(dir.Options{Path: root})
	if err != nil {
		panic(err)
	}

	b := []byte("what the robot recorded")

	// The directories a name needs are made on the way.
	err = s.Put(ctx, "thor-top/2026/a.rec", bytes.NewReader(b), sink.Meta{Size: int64(len(b))})
	if err != nil {
		panic(err)
	}

	got, err := s.Stat(ctx, "thor-top/2026/a.rec")
	if err != nil {
		panic(err)
	}
	fmt.Println("it holds", got.Size, "bytes")

	// No digest: a filesystem keeps no hash, and reading the file back to take
	// one costs what writing it did. The read-back here is of the length,
	// which catches a stream that ended early and not a file that arrived
	// whole and wrong.
	fmt.Println("and a hash:", got.Digest == "")

	on, err := os.ReadFile(filepath.Join(root, "thor-top", "2026", "a.rec"))
	if err != nil {
		panic(err)
	}
	fmt.Println("on disk:", string(on))

	// Output:
	// it holds 23 bytes
	// and a hash: true
	// on disk: what the robot recorded
}

// A name that would climb out of the directory is refused.
//
// For an object store a key is opaque and ".." in one is a key like any other.
// For a filesystem it is an escape, so this is one of the sinks the escape half
// of the conformance suite applies to.
func ExampleSink_Put_aNameThatClimbsOut() {
	ctx := context.Background()
	root, err := os.MkdirTemp("", "wick-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	s, err := dir.New(dir.Options{Path: root})
	if err != nil {
		panic(err)
	}

	err = s.Put(ctx, "../elsewhere.rec", bytes.NewReader([]byte("x")), sink.Meta{Size: 1})
	fmt.Println(err)

	// Output:
	// "../elsewhere.rec" is not a name this sink can put anything under
}
