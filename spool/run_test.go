package spool_test

import (
	"context"
	"io"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal/mem"
	"github.com/heojeongbo/wick/naming"
	"github.com/heojeongbo/wick/retain"
	sinkmem "github.com/heojeongbo/wick/sink/mem"
	"github.com/heojeongbo/wick/source"
	sourcemem "github.com/heojeongbo/wick/source/mem"
	"github.com/heojeongbo/wick/spool"
	"github.com/heojeongbo/wick/trigger"
)

// until waits for something to become true, and fails rather than hanging when
// it does not. It is here because Run is the one part of this that is about
// wall-clock time and cannot be wound by hand.
func until(t *testing.T, why string, f func() bool) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}

	t.Fatalf("waited for %s and it did not happen", why)
}

// asked is a trigger that counts how many times it was consulted, wrapping one
// that decides.
//
// It is here so that a test can wait for the loop to have *asked* rather than
// sleeping and hoping. Waiting for something not to happen is otherwise a
// guess at how long is long enough, and the answer on a loaded machine is
// always longer than the guess.
type asked struct {
	trigger.Trigger

	mu sync.Mutex
	n  int
}

func (a *asked) Fire(s trigger.State) bool {
	a.mu.Lock()
	a.n++
	a.mu.Unlock()

	return a.Trigger.Fire(s)
}

func (a *asked) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.n
}

// The trigger is what says whether to carry, and every kind of it has to be
// able to say no.
//
// This is the test that was not here, and its absence is why [trigger.Trigger]
// grew a Fire method that nothing ever called. The loop woke on a timer and
// carried, so `count: 10` carried at one, `every: 15m` carried every minute,
// and a spool with no trigger -- documented as "only when asked" -- carried
// constantly. Every trigger collapsed to the same one-minute poll, on the
// metered link this whole thing exists to be careful with.
func TestRunAsksBeforeItCarries(t *testing.T) {
	for _, tt := range []struct {
		what string
		trig trigger.Trigger
	}{
		{"nothing written down, which means only when asked", trigger.Any()},
		{"a count that has not been reached", trigger.Count(10)},
		{"a size that has not been reached", trigger.Bytes(1 << 30)},
		{"a clock that has not come round", trigger.Every(time.Hour)},
		{"room that is not short", trigger.FreeBelow(1)},
		{"all of them, where one says no", trigger.All(trigger.Count(1), trigger.Count(10))},
	} {
		t.Run(tt.what, func(t *testing.T) {
			x := require.New(t)

			trig := &asked{Trigger: tt.trig}
			g := newRig(t, withTrigger(trig))
			g.add("a.rec", "contents")

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			done := make(chan error, 1)
			go func() { done <- g.Run(ctx) }()

			// The first pass is the one that is not asked, so it happens.
			until(t, "the first pass", func() bool { return g.cloud.Puts() == 1 })

			g.add("b.rec", "more")
			g.Nudge()

			// The nudge makes it look, and looking is what asks. Once it has
			// been asked, whatever it was going to do it has done.
			until(t, "the trigger to be asked", func() bool { return trig.count() > 0 })

			x.Equal(1, g.cloud.Puts(), "it carried something the trigger said no to")

			cancel()
			x.NoError(<-done)
		})
	}
}

// And the other way round: a trigger that says yes is carried out.
func TestRunCarriesWhenTheTriggerSaysSo(t *testing.T) {
	x := require.New(t)

	trig := &asked{Trigger: trigger.Count(1)}
	g := newRig(t, withTrigger(trig))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	// Nothing was there for the first pass, so the one that carries is a later
	// one -- and every later one is asked. The nudge is in the condition
	// because it is the only lever on a loop that is otherwise waiting out
	// [idle], and one that arrives while another is pending says nothing the
	// first did not.
	until(t, "the trigger to be asked", func() bool {
		g.Nudge()

		return trig.count() > 0
	})

	g.add("a.rec", "contents")
	g.Nudge()

	until(t, "the pass the trigger allowed", func() bool { return g.cloud.Puts() == 1 })

	cancel()
	x.NoError(<-done)
}

// A daemon that has just started on a machine holding a week of files should
// not wait for the interval before saying so.
func TestRunCarriesAtOnce(t *testing.T) {
	x := require.New(t)

	g := newRig(t)
	g.add("a.rec", "contents")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	until(t, "the first pass", func() bool { return g.cloud.Puts() == 1 })

	cancel()
	x.NoError(<-done)
}

// The watcher only ever nudges. What it says is not believed; it makes the next
// scan sooner.
func TestRunAnswersANudge(t *testing.T) {
	x := require.New(t)

	g := newRig(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	until(t, "the first pass", func() bool { return len(g.cloud.Names()) == 0 })

	g.add("a.rec", "contents")
	g.src.Nudge()

	until(t, "the pass the nudge asked for", func() bool { return g.cloud.Puts() == 1 })

	cancel()
	x.NoError(<-done)
}

// It is still scanned; it is only slower now. That is the whole reason a
// watcher is allowed to fail this quietly.
func TestRunWithoutAWatcher(t *testing.T) {
	x := require.New(t)

	src := sourcemem.New()
	src.Add("a.rec", []byte("contents"), start)

	dst := sinkmem.New()
	s, err := spool.New(spool.Config{
		Name:    "recordings",
		Source:  refusesToWatch{src},
		Journal: mem.New(),
		Dests: []spool.Dest{{
			Name:   "cloud",
			Sink:   dst,
			Naming: naming.MustParse("{name}"),
			Verify: true,
		}},
	})
	x.NoError(err)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	until(t, "the pass that happens anyway", func() bool { return dst.Puts() == 1 })

	cancel()
	x.NoError(<-done)
}

// A pass that could not be made at all is not a reason to stop. The link comes
// back, and the next pass is the one that works.
func TestRunKeepsGoingAfterAPassThatFailed(t *testing.T) {
	x := require.New(t)

	// The first pass is not put to the trigger, so it is the one that fails.
	// The trigger is what lets the second one happen.
	g := newRig(t, withTrigger(trigger.Count(1)))
	g.add("a.rec", "contents")
	g.src.FailScan(errRefused)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	// It is still running. Let it fail once, then let it succeed.
	time.Sleep(20 * time.Millisecond)
	g.src.FailScan(nil)
	g.Nudge()

	until(t, "the pass that worked", func() bool { return g.cloud.Puts() == 1 })

	cancel()
	x.NoError(<-done)
}

// A nudge that arrives while one is already waiting says nothing the first did
// not, and must not block whoever sent it.
func TestNudgeNeverBlocks(t *testing.T) {
	x := require.New(t)

	g := newRig(t)
	for range 100 {
		g.Nudge()
	}
	x.True(true)
}

func TestRunAsksTheTriggerHowLongToWait(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withTrigger(trigger.Every(time.Hour)))
	g.add("a.rec", "contents")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	// The first pass happens whatever the trigger says.
	until(t, "the first pass", func() bool { return g.cloud.Puts() == 1 })

	cancel()
	x.NoError(<-done)
}

func TestGroup(t *testing.T) {
	t.Run("one pass of each, one after another", func(t *testing.T) {
		x := require.New(t)

		left := newRig(t)
		left.add("a.rec", "left")

		right := newRig(t)
		right.add("b.rec", "right")

		g := spool.NewGroup(left.Spool, right.Spool)
		x.Len(g.Spools(), 2)

		rs, err := g.Once(t.Context())
		x.NoError(err)
		x.Len(rs, 1) // both are called "recordings"
		x.Equal(1, left.cloud.Puts())
		x.Equal(1, right.cloud.Puts())
	})

	// An outage in one is not a reason to stop the other.
	t.Run("one that cannot be carried does not stop the rest", func(t *testing.T) {
		x := require.New(t)

		bad := newRig(t)
		bad.add("a.rec", "contents")
		bad.src.FailScan(errRefused)

		good := newRig(t)
		good.add("b.rec", "contents")

		g := spool.NewGroup(bad.Spool, good.Spool)

		_, err := g.Once(t.Context())
		x.ErrorIs(err, errRefused)
		x.Equal(1, good.cloud.Puts())
	})

	t.Run("something that went wrong inside a pass is reported too", func(t *testing.T) {
		x := require.New(t)

		one := newRig(t)
		one.add("a.rec", "contents")
		one.cloud.FailPut("thor-top/a.rec", errRefused)

		_, err := spool.NewGroup(one.Spool).Once(t.Context())
		x.ErrorIs(err, errRefused)
	})

	t.Run("a context that is done stops it going round again", func(t *testing.T) {
		x := require.New(t)

		one := newRig(t)
		two := newRig(t)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := spool.NewGroup(one.Spool, two.Spool).Once(ctx)
		x.Error(err)
	})

	t.Run("they all run until the running stops", func(t *testing.T) {
		x := require.New(t)

		left := newRig(t)
		left.add("a.rec", "left")
		right := newRig(t)
		right.add("b.rec", "right")

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		g := spool.NewGroup(left.Spool, right.Spool)

		done := make(chan error, 1)
		go func() { done <- g.Run(ctx) }()

		until(t, "both of them", func() bool {
			return left.cloud.Puts() == 1 && right.cloud.Puts() == 1
		})

		cancel()
		x.NoError(<-done)
	})
}

// The engine says what it did, once per pass that did something. A pass that
// found nothing says nothing: on a quiet machine that is one line a minute
// forever.
func TestItSaysWhatItDid(t *testing.T) {
	x := require.New(t)

	g := newRig(t, withRetain(retain.Delete()))
	g.add("a.rec", "contents")

	r, err := g.Once(t.Context())
	x.NoError(err)
	x.Equal(1, r.Carried)
	x.Equal(1, r.Retired)
	x.NoError(r.Err())
}

// refusesToWatch is a source that has a watcher and will not give one out,
// which is what a machine at its limit of watches looks like.
type refusesToWatch struct{ inner *sourcemem.Source }

func (r refusesToWatch) Scan(ctx context.Context) iter.Seq2[source.Item, error] {
	return r.inner.Scan(ctx)
}

func (r refusesToWatch) Open(ctx context.Context, k string) (io.ReadCloser, error) {
	return r.inner.Open(ctx, k)
}

func (r refusesToWatch) Watch(context.Context) (<-chan struct{}, error) { return nil, errRefused }

var _ source.Watcher = refusesToWatch{}
