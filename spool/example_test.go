package spool_test

import (
	"context"
	"fmt"
	"time"

	"github.com/heojeongbo/wick/journal"
	journalmem "github.com/heojeongbo/wick/journal/mem"
	"github.com/heojeongbo/wick/naming"
	"github.com/heojeongbo/wick/retain"
	sinkmem "github.com/heojeongbo/wick/sink/mem"
	sourcemem "github.com/heojeongbo/wick/source/mem"
	"github.com/heojeongbo/wick/spool"
	"github.com/heojeongbo/wick/trigger"
)

// The whole arrangement, in one place: a source, two destinations, a journal,
// and what becomes of the original.
//
// The three `mem` packages are used here so that this runs anywhere. In a real
// deployment they are `source/dir`, one of the sinks, and `journal/bolt`; the
// engine is the same either way, which is the point of them being interfaces.
func Example() {
	ctx := context.Background()

	src := sourcemem.New()
	src.Add("a.rec", []byte("what the robot recorded"), time.Now())

	cloud := sinkmem.New()
	onsite := sinkmem.New()

	s, err := spool.New(spool.Config{
		Name:    "recordings",
		Host:    "thor-top",
		Source:  src,
		Journal: journalmem.New(),

		// Both have to answer before anything is recorded as carried. A
		// restart part way through does not send again to the one that already
		// had it.
		Dests: []spool.Dest{
			{Name: "cloud", Sink: cloud, Naming: naming.MustParse("{host}/{name}"), Verify: true},
			{Name: "onsite", Sink: onsite, Naming: naming.MustParse("{name}"), Verify: true},
		},

		// Carry when a quarter of an hour has passed or the disk is getting
		// tight, whichever comes first. Only Run consults this.
		Trigger: trigger.Any(
			trigger.Every(15*time.Minute),
			trigger.FreeBelow(20<<30),
		),

		// Keep the original for a day after it has arrived everywhere, then
		// delete it.
		Retain: retain.Grace(24*time.Hour, retain.Delete()),
	})
	if err != nil {
		panic(err)
	}

	r, err := s.Once(ctx)
	if err != nil {
		panic(err)
	}

	fmt.Printf("%d carried, %d bytes\n", r.Carried, r.Bytes)
	fmt.Println("cloud has:", cloud.Names())
	fmt.Println("onsite has:", onsite.Names())

	// Output:
	// 1 carried, 46 bytes
	// cloud has: [thor-top/a.rec]
	// onsite has: [a.rec]
}

// A second pass sends nothing, because the journal remembers the first.
//
// This is what makes the thing safe to run from a timer as often as you like:
// the cost of a pass with nothing new in it is a scan, not an upload.
func ExampleSpool_Once() {
	ctx := context.Background()

	src := sourcemem.New()
	src.Add("a.rec", []byte("contents"), time.Now())

	dst := sinkmem.New()

	s, err := spool.New(spool.Config{
		Name:    "recordings",
		Source:  src,
		Journal: journalmem.New(),
		Dests:   []spool.Dest{{Name: "cloud", Sink: dst, Verify: true}},
	})
	if err != nil {
		panic(err)
	}

	first, _ := s.Once(ctx)
	second, _ := s.Once(ctx)

	fmt.Println("first pass carried", first.Carried)
	fmt.Println("second pass carried", second.Carried)
	fmt.Println("and it was sent", dst.Puts(), "time")

	// Output:
	// first pass carried 1
	// second pass carried 0
	// and it was sent 1 time
}

// A file that is only half written is not a file to carry.
//
// A source that hands things over whole -- a queue, an object store -- wants no
// settle time. A directory somebody is writing into wants one, because a file
// still being appended to looks exactly like a file that is finished.
func ExampleConfig_settleFor() {
	ctx := context.Background()

	now := time.Now()

	src := sourcemem.New()
	src.Add("a.rec", []byte("still being written"), now)

	dst := sinkmem.New()

	s, err := spool.New(spool.Config{
		Name:    "recordings",
		Source:  src,
		Journal: journalmem.New(),
		Dests:   []spool.Dest{{Name: "cloud", Sink: dst, Verify: true}},

		Settle: 10 * time.Second,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		panic(err)
	}

	r, _ := s.Once(ctx)
	fmt.Println("written just now:", r.Carried, "carried")

	// The same file, ten seconds older than the clock.
	src.Add("a.rec", []byte("all of it now"), now.Add(-time.Minute))

	r, _ = s.Once(ctx)
	fmt.Println("and once it has sat still:", r.Carried, "carried")

	// Output:
	// written just now: 0 carried
	// and once it has sat still: 1 carried
}

// What a spool did, and what it could not do.
//
// Once answers with both. A pass where one destination was unreachable is a
// pass that carried nothing -- the file is left where it is and tried again --
// and the error says which one.
func ExampleReport() {
	ctx := context.Background()

	src := sourcemem.New()
	src.Add("a.rec", []byte("contents"), time.Now())

	dst := sinkmem.New()
	dst.FailPut("a.rec", fmt.Errorf("the link is down"))

	s, err := spool.New(spool.Config{
		Name:    "recordings",
		Source:  src,
		Journal: journalmem.New(),
		Dests:   []spool.Dest{{Name: "cloud", Sink: dst, Verify: true}},
	})
	if err != nil {
		panic(err)
	}

	r, err := s.Once(ctx)
	fmt.Println("the pass itself:", err)
	fmt.Println("carried:", r.Carried, "of", r.Settled, "that were due")
	fmt.Println("what went wrong:", r.Err())

	// Output:
	// the pass itself: <nil>
	// carried: 0 of 1 that were due
	// what went wrong: carry "a.rec": put "a.rec" at "cloud": the link is down
}

// A destination that cannot be read back cannot be verified against, and saying
// so at startup is the whole point.
//
// A spool that could never record a carry would send the same file for ever.
func ExampleNew_refusesWhatCannotWork() {
	_, err := spool.New(spool.Config{
		Name:    "recordings",
		Source:  sourcemem.New(),
		Journal: journalmem.New(),
		Dests: []spool.Dest{
			{Name: "cloud", Sink: putOnly{}, Verify: true},
		},
	})

	fmt.Println(err)

	// Output:
	// the destination "cloud" of the spool "recordings" is to be verified, and this kind of sink cannot be asked what it holds
}

// The journal is the thing that knows, and it can be asked directly.
func ExampleGroup() {
	ctx := context.Background()

	jnl := journalmem.New()

	src := sourcemem.New()
	src.Add("a.rec", []byte("contents"), time.Now())

	s, err := spool.New(spool.Config{
		Name:    "recordings",
		Source:  src,
		Journal: jnl,
		Dests:   []spool.Dest{{Name: "cloud", Sink: sinkmem.New(), Verify: true}},
		Retain:  retain.Delete(),
	})
	if err != nil {
		panic(err)
	}

	g := spool.NewGroup(s)
	if _, err := g.Once(ctx); err != nil {
		panic(err)
	}

	sum, err := journal.Summarize(ctx, jnl, "recordings")
	if err != nil {
		panic(err)
	}

	fmt.Println("retired:", sum.Count[journal.Retired])

	// Output:
	// retired: 1
}
