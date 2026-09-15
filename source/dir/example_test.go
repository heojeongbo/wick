package dir_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/source/dir"
)

// Where things accumulate: a directory, with rules about what counts.
//
// Exclude is applied first, so something both lists is left. MinSize is what
// keeps a file that has been created and not yet written from being carried --
// which would record a carry of the wrong thing under the right name.
func Example() {
	ctx := context.Background()

	root, err := os.MkdirTemp("", "wick-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	write := func(name, body string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			panic(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			panic(err)
		}
	}

	write("session-01.rec", "recorded")
	write("session-02.rec", "recorded too")
	write("notes.txt", "not a recording")
	write("empty.rec", "")
	write("old/session-00.rec", "in a subdirectory")

	src, err := dir.New(dir.Options{
		Path:      root,
		Include:   []string{"*.rec"},
		Recursive: true,
		MinSize:   1,
	})
	if err != nil {
		panic(err)
	}

	for it, err := range src.Scan(ctx) {
		if err != nil {
			panic(err)
		}

		fmt.Println(it.Key)
	}

	// The order is not promised and is not relied on: a scan is a snapshot of
	// what is there, and the engine decides per item.
	//
	// Unordered output:
	// session-01.rec
	// session-02.rec
	// old/session-00.rec
}

// A directory source can do more than the two methods a source must have, and
// the engine asks rather than assumes.
//
// Root is the one that is easy to miss: it is what lets "carry when the disk is
// nearly full" mean anything, because measuring free space needs somewhere to
// measure.
func ExampleSource_capabilities() {
	root, err := os.MkdirTemp("", "wick-example-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(root)

	src, err := dir.New(dir.Options{Path: root})
	if err != nil {
		panic(err)
	}

	var s source.Source = src

	_, remover := s.(source.Remover)
	_, mover := s.(source.Mover)
	_, watcher := s.(source.Watcher)
	rooted, ok := s.(source.Rooted)

	fmt.Println("delete:", remover)
	fmt.Println("move:", mover)
	fmt.Println("watch:", watcher)
	fmt.Println("says where it is:", ok && rooted.Root() == root)

	// Output:
	// delete: true
	// move: true
	// watch: true
	// says where it is: true
}
