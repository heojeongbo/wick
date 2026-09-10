package naming_test

import (
	"fmt"
	"time"

	"github.com/heojeongbo/wick/naming"
)

// What a thing is called once it is somewhere else.
//
// {host} is what keeps three machines in one robot, all writing the same kind
// of file into the same bucket, from writing over each other.
func Example() {
	t := naming.MustParse("{host}/{yyyy}/{mm}/{dd}/{name}")

	name, err := t.Expand(naming.Vars{
		Host:  "thor-top",
		Key:   "session-41.rec",
		ModAt: time.Date(2026, 9, 10, 4, 13, 0, 0, time.UTC),
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(name)

	// Output:
	// thor-top/2026/09/10/session-41.rec
}

// A template that says nothing means the name it already had.
func ExampleTemplate_zero() {
	var t naming.Template

	name, err := t.Expand(naming.Vars{Key: "rec/a.rec"})
	if err != nil {
		panic(err)
	}

	fmt.Println(name)

	// Output:
	// rec/a.rec
}

// The same file on two machines is two names, which is the whole point.
func ExampleMustParse() {
	t := naming.MustParse("{host}/{name}")

	for _, host := range []string{"thor-top", "thor-bottom"} {
		name, err := t.Expand(naming.Vars{Host: host, Key: "a.rec"})
		if err != nil {
			panic(err)
		}

		fmt.Println(name)
	}

	// Output:
	// thor-top/a.rec
	// thor-bottom/a.rec
}

// Parse says what could have been written instead of only that this was wrong.
func ExampleParse() {
	_, err := naming.Parse("{host}/{nonsuch}/{name}")
	fmt.Println(err)

	// Output:
	// "nonsuch" is not something a name can be made of; it is one of HH, MM, SS, dd, dir, ext, host, key, mm, name, sha256, size, source, stem, unix, yy, yyyy
}

// A template that asks for the hash says so, so that the engine knows to
// compute one on the way past rather than reading the file a second time.
func ExampleTemplate_NeedsDigest() {
	plain := naming.MustParse("{host}/{name}")
	hashed := naming.MustParse("{sha256}/{name}")

	fmt.Println(plain.NeedsDigest())
	fmt.Println(hashed.NeedsDigest())

	// Output:
	// false
	// true
}
