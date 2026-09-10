package trigger_test

import (
	"fmt"
	"time"

	"github.com/heojeongbo/wick/trigger"
)

// The ordinary arrangement: a clock, and a few reasons not to wait for it.
//
// [github.com/heojeongbo/wick/spool.Spool.Run] asks Fire before every pass
// after the first. After says when to look again, so that a spool waiting on
// the clock sleeps rather than spins.
func Example() {
	t := trigger.Any(
		trigger.Every(15*time.Minute),
		trigger.Count(10),
		trigger.FreeBelow(20<<30),
	)

	// Two files, five minutes since the last carry, plenty of room.
	quiet := trigger.State{Pending: 2, Bytes: 4 << 20, FreeBytes: 500 << 30, Since: 5 * time.Minute}
	fmt.Println("two waiting, five minutes in:", t.Fire(quiet))
	fmt.Println("and it will look again in:", t.After(quiet))

	// The same two files, on a disk that is nearly full.
	tight := quiet
	tight.FreeBytes = 1 << 30
	fmt.Println("with the disk getting tight:", t.Fire(tight))

	// Output:
	// two waiting, five minutes in: false
	// and it will look again in: 10m0s
	// with the disk getting tight: true
}

// Nothing waiting is not a reason to carry, whatever else is true.
func ExampleEvery() {
	t := trigger.Every(time.Hour)

	fmt.Println(t.Fire(trigger.State{Pending: 1, Since: 2 * time.Hour}))
	fmt.Println(t.Fire(trigger.State{Pending: 0, Since: 2 * time.Hour}))

	// Output:
	// true
	// false
}

// A filesystem that cannot be measured must not look like one that is full.
func ExampleFreeBelow() {
	t := trigger.FreeBelow(20 << 30)

	fmt.Println("nearly full:", t.Fire(trigger.State{Pending: 1, FreeBytes: 1 << 30}))
	fmt.Println("plenty:", t.Fire(trigger.State{Pending: 1, FreeBytes: 500 << 30}))
	fmt.Println("nobody could say:", t.Fire(trigger.State{Pending: 1, FreeBytes: 0}))

	// Output:
	// nearly full: true
	// plenty: false
	// nobody could say: false
}

// "On the hour, but only if there is enough to be worth it."
func ExampleAll() {
	t := trigger.All(trigger.Every(time.Hour), trigger.Bytes(100<<20))

	fmt.Println(t.Fire(trigger.State{Pending: 1, Bytes: 200 << 20, Since: 2 * time.Hour}))
	fmt.Println(t.Fire(trigger.State{Pending: 1, Bytes: 1 << 20, Since: 2 * time.Hour}))

	// Output:
	// true
	// false
}

// With nothing in it, Any never fires. That is what a spool with no trigger
// written for it does: carry when something asks, and not otherwise.
func ExampleAny_nothing() {
	t := trigger.Any()

	fmt.Println(t.Fire(trigger.State{Pending: 100, Since: 100 * time.Hour}))
	fmt.Println(t)

	// Output:
	// false
	// never
}
