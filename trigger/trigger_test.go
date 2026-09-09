package trigger_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/trigger"
)

func TestEvery(t *testing.T) {
	e := trigger.Every(time.Minute)

	t.Run("it fires once enough time has gone by", func(t *testing.T) {
		x := require.New(t)

		x.False(e.Fire(trigger.State{Pending: 1, Since: 59 * time.Second}))
		x.True(e.Fire(trigger.State{Pending: 1, Since: time.Minute}))
		x.True(e.Fire(trigger.State{Pending: 1, Since: time.Hour}))
	})
	t.Run("nothing to carry is not a reason to carry", func(t *testing.T) {
		x := require.New(t)

		x.False(e.Fire(trigger.State{Pending: 0, Since: time.Hour}))
	})
	t.Run("it says how long is left, so the spool sleeps rather than spins", func(t *testing.T) {
		x := require.New(t)

		x.Equal(time.Minute, e.After(trigger.State{}))
		x.Equal(20*time.Second, e.After(trigger.State{Since: 40 * time.Second}))
		// Past due is not a negative sleep.
		x.Zero(e.After(trigger.State{Since: time.Hour}))
	})
}

func TestCount(t *testing.T) {
	x := require.New(t)

	c := trigger.Count(3)
	x.False(c.Fire(trigger.State{Pending: 2}))
	x.True(c.Fire(trigger.State{Pending: 3}))
	x.True(c.Fire(trigger.State{Pending: 4}))

	// Nothing waiting is nothing to do, whatever the number was.
	x.False(trigger.Count(0).Fire(trigger.State{Pending: 0}))

	// It answers to the source and not to the clock, so there is nothing to
	// wait for.
	x.Zero(c.After(trigger.State{Pending: 1}))
}

// The one for a source that produces a few very large things, where a count of
// ten is a count that never happens.
func TestBytes(t *testing.T) {
	x := require.New(t)

	b := trigger.Bytes(1024)
	x.False(b.Fire(trigger.State{Pending: 1, Bytes: 1023}))
	x.True(b.Fire(trigger.State{Pending: 1, Bytes: 1024}))
	x.False(b.Fire(trigger.State{Pending: 0, Bytes: 99999}))
	x.Zero(b.After(trigger.State{}))
}

func TestFreeBelow(t *testing.T) {
	x := require.New(t)

	f := trigger.FreeBelow(1024)
	x.True(f.Fire(trigger.State{Pending: 1, FreeBytes: 1023}))
	x.False(f.Fire(trigger.State{Pending: 1, FreeBytes: 1024}))
	x.False(f.Fire(trigger.State{Pending: 0, FreeBytes: 1}))

	// A filesystem that cannot be measured must not read as one that is full,
	// or every carry happens at once for no reason.
	x.False(f.Fire(trigger.State{Pending: 1, FreeBytes: 0}))

	x.Zero(f.After(trigger.State{}))
}

func TestAny(t *testing.T) {
	a := trigger.Any(
		trigger.Every(time.Minute),
		trigger.Count(3),
		trigger.FreeBelow(1024),
	)

	t.Run("whichever comes first", func(t *testing.T) {
		x := require.New(t)

		x.False(a.Fire(trigger.State{Pending: 1, Since: time.Second, FreeBytes: 1 << 20}))
		x.True(a.Fire(trigger.State{Pending: 1, Since: time.Hour, FreeBytes: 1 << 20}))
		x.True(a.Fire(trigger.State{Pending: 3, Since: time.Second, FreeBytes: 1 << 20}))
		x.True(a.Fire(trigger.State{Pending: 1, Since: time.Second, FreeBytes: 1}))
	})
	t.Run("the sleep is the soonest of the ones that answer to a clock", func(t *testing.T) {
		x := require.New(t)

		x.Equal(time.Minute, a.After(trigger.State{}))

		// The ones that answer only to the source say nothing about waiting,
		// and must not drag the sleep down to zero.
		both := trigger.Any(trigger.Every(time.Hour), trigger.Every(time.Minute))
		x.Equal(time.Minute, both.After(trigger.State{}))
	})

	// A spool with no trigger written for it should carry when something asks
	// it to and not otherwise, rather than carry constantly.
	t.Run("nothing in it never fires", func(t *testing.T) {
		x := require.New(t)

		none := trigger.Any()
		x.False(none.Fire(trigger.State{Pending: 99, Bytes: 1 << 40}))
		x.Zero(none.After(trigger.State{}))
		x.Equal("never", fmt.Sprint(none))
	})
}

func TestAll(t *testing.T) {
	// On the hour, but only if there is enough to be worth it.
	a := trigger.All(trigger.Every(time.Hour), trigger.Count(10))

	t.Run("only when every one of them does", func(t *testing.T) {
		x := require.New(t)

		x.False(a.Fire(trigger.State{Pending: 10, Since: time.Minute}))
		x.False(a.Fire(trigger.State{Pending: 2, Since: 2 * time.Hour}))
		x.True(a.Fire(trigger.State{Pending: 10, Since: 2 * time.Hour}))
	})
	t.Run("the sleep is the longest, since the first to come true leaves the others", func(t *testing.T) {
		x := require.New(t)

		both := trigger.All(trigger.Every(time.Minute), trigger.Every(time.Hour))
		x.Equal(time.Hour, both.After(trigger.State{}))
	})
	t.Run("nothing in it never fires", func(t *testing.T) {
		x := require.New(t)

		none := trigger.All()
		x.False(none.Fire(trigger.State{Pending: 99}))
		x.Zero(none.After(trigger.State{}))
	})
}

// What a trigger says about itself is what `wick config` prints and what a
// startup line says, so it has to read as a sentence.
func TestTheySayWhatTheyAre(t *testing.T) {
	x := require.New(t)

	x.Equal("every 15m0s", fmt.Sprint(trigger.Every(15*time.Minute)))
	x.Equal("10 or more waiting", fmt.Sprint(trigger.Count(10)))
	x.Equal("1024 bytes or more waiting", fmt.Sprint(trigger.Bytes(1024)))
	x.Equal("less than 2048 bytes free", fmt.Sprint(trigger.FreeBelow(2048)))
	x.Equal(
		"any of [every 1m0s, 3 or more waiting]",
		fmt.Sprint(trigger.Any(trigger.Every(time.Minute), trigger.Count(3))),
	)
	x.Equal(
		"all of [every 1m0s, 3 or more waiting]",
		fmt.Sprint(trigger.All(trigger.Every(time.Minute), trigger.Count(3))),
	)
}

// The composites say what is in them, so that a configuration can be checked
// without taking it apart by hand.
func TestTheyCanBeLookedInside(t *testing.T) {
	x := require.New(t)

	for _, tr := range []trigger.Trigger{
		trigger.Any(trigger.Count(1), trigger.Count(2)),
		trigger.All(trigger.Count(1), trigger.Count(2)),
	} {
		u, ok := tr.(interface{ Unwrap() []trigger.Trigger })
		x.True(ok)
		x.Len(u.Unwrap(), 2)
	}
}
