package size_test

import (
	"fmt"

	"github.com/heojeongbo/wick/size"
)

// Both spellings are here because both are meant. A disk is sold in GB and
// reports itself in GiB, and a deployment that writes one and gets the other is
// one that fills up at eight per cent less than it thought.
func ExampleParse() {
	for _, s := range []string{"20GiB", "20GB", "1024", "512KiB", "20gib"} {
		n, err := size.Parse(s)
		if err != nil {
			panic(err)
		}

		fmt.Printf("%-8s %d\n", s, n.Int64())
	}

	// Output:
	// 20GiB    21474836480
	// 20GB     20000000000
	// 1024     1024
	// 512KiB   524288
	// 20gib    21474836480
}

// It says what could have been written, rather than only that this was not it.
func ExampleParse_refusal() {
	_, err := size.Parse("a lot")
	fmt.Println(err)

	// Output:
	// "a lot" is not a number of bytes; write it as 1024, or as 20MiB, or as 20MB
}

// And back the way it was most likely written, so that `wick config` shows a
// deployment its own settings rather than a number it has to work out.
func ExampleBytes_String() {
	fmt.Println(size.Bytes(21474836480))
	fmt.Println(size.Bytes(1024))
	fmt.Println(size.Bytes(1000))
	fmt.Println(size.Bytes(0))

	// Output:
	// 20GiB
	// 1KiB
	// 1000
	// 0
}
