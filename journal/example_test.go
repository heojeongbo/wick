package journal_test

import (
	"context"
	"fmt"
	"time"

	"github.com/heojeongbo/wick/journal"
	journalmem "github.com/heojeongbo/wick/journal/mem"
)

// What is remembered about one item, and the order the states go in.
//
// The order is the whole design. Nothing is written before the copy has been
// read back, so a record that says Carried is a record of something that
// arrived — and the local file is not touched until that record exists.
func Example() {
	ctx := context.Background()
	jnl := journalmem.New()

	// Pending: it has been seen and not yet carried.
	r := journal.Record{
		Source: "recordings",
		Key:    "a.rec",
		Size:   1024,
		ModAt:  time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC),
		State:  journal.Pending,
	}
	if err := jnl.Put(ctx, r); err != nil {
		panic(err)
	}

	// Carried, once every destination has confirmed it. Which ones confirmed
	// is kept, so a restart part way through a fan-out does not send again to
	// the one that already had it.
	r.State = journal.Carried
	r.Carried = map[string]journal.Carry{
		"cloud":  {At: time.Now()},
		"onsite": {At: time.Now()},
	}
	if err := jnl.Put(ctx, r); err != nil {
		panic(err)
	}

	got, ok, err := jnl.Get(ctx, "recordings", "a.rec")
	if err != nil {
		panic(err)
	}

	fmt.Println("remembered:", ok)
	fmt.Println("state:", got.State)
	fmt.Println("destinations that confirmed it:", len(got.Carried))

	// Output:
	// remembered: true
	// state: carried
	// destinations that confirmed it: 2
}

// Summarize is what `wick status` is.
//
// It is a function here rather than a method on the interface so that an
// implementation only has to answer Range correctly to be able to answer
// `status` — one fewer thing to get wrong when writing one.
func ExampleSummarize() {
	ctx := context.Background()
	jnl := journalmem.New()

	at := time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC)
	for _, r := range []journal.Record{
		{Source: "recordings", Key: "a.rec", Size: 1000, ModAt: at, State: journal.Retired},
		{Source: "recordings", Key: "b.rec", Size: 2000, ModAt: at.Add(time.Hour), State: journal.Pending},
		{Source: "recordings", Key: "c.rec", Size: 4000, ModAt: at.Add(2 * time.Hour), State: journal.Quarantined},
	} {
		if err := jnl.Put(ctx, r); err != nil {
			panic(err)
		}
	}

	s, err := journal.Summarize(ctx, jnl, "recordings")
	if err != nil {
		panic(err)
	}

	fmt.Println("waiting:", s.Count[journal.Pending], "of", s.Bytes[journal.Pending], "bytes")
	fmt.Println("set aside:", s.Count[journal.Quarantined])
	fmt.Println("oldest:", s.Oldest.UTC().Format(time.RFC3339))

	// Output:
	// waiting: 1 of 2000 bytes
	// set aside: 1
	// oldest: 2026-09-15T04:00:00Z
}

// A file that has been written again is not the file that was carried.
//
// This is what tells "skip it, it has already gone" from "send it, it is
// different now", and getting it wrong in the safe direction costs one upload.
func ExampleRecord_SameAs() {
	at := time.Date(2026, 9, 15, 4, 0, 0, 0, time.UTC)
	r := journal.Record{Key: "a.rec", Size: 1024, ModAt: at}

	fmt.Println("unchanged:", r.SameAs(1024, at))
	fmt.Println("written again:", r.SameAs(2048, at.Add(time.Minute)))
	fmt.Println("same length, later:", r.SameAs(1024, at.Add(time.Minute)))

	// Output:
	// unchanged: true
	// written again: false
	// same length, later: false
}
