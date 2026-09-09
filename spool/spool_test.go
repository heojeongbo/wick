package spool_test

import (
	"context"
	"errors"
	"io"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
	journalmem "github.com/heojeongbo/wick/journal/mem"
	"github.com/heojeongbo/wick/naming"
	"github.com/heojeongbo/wick/retain"
	"github.com/heojeongbo/wick/sink"
	sinkmem "github.com/heojeongbo/wick/sink/mem"
	"github.com/heojeongbo/wick/source"
	sourcemem "github.com/heojeongbo/wick/source/mem"
	"github.com/heojeongbo/wick/spool"
	"github.com/heojeongbo/wick/trigger"
)

var (
	errRefused = errors.New("refused")
	start      = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
)

// clock is a hand-wound one, so that nothing in these tests sleeps.
type clock struct{ at time.Time }

func newClock() *clock { return &clock{at: start} }

func (c *clock) now() time.Time       { return c.at }
func (c *clock) tick(d time.Duration) { c.at = c.at.Add(d) }

// rig is a spool and the things it was made of, so that a test can reach past
// it and say what the source did or what the sink kept.
type rig struct {
	*spool.Spool

	src    *sourcemem.Source
	cloud  *sinkmem.Sink
	onsite *sinkmem.Sink
	jnl    journal.Journal
	clock  *clock
}

type option func(*spool.Config, *rig)

// twoDestinations is the arrangement this is really for: the cloud and a server
// in the building, and the file is only carried when both have answered.
func twoDestinations(c *spool.Config, g *rig) {
	c.Dests = append(c.Dests, spool.Dest{
		Name:   "onsite",
		Sink:   g.onsite,
		Naming: naming.MustParse("{name}"),
		Verify: true,
	})
}

func withRetain(p retain.Policy) option {
	return func(c *spool.Config, _ *rig) { c.Retain = p }
}

func withSettle(d time.Duration) option {
	return func(c *spool.Config, _ *rig) { c.Settle = d }
}

func withWorkers(n int) option {
	return func(c *spool.Config, _ *rig) { c.Workers = n }
}

func withAttempts(n int) option {
	return func(c *spool.Config, _ *rig) { c.Attempts = n }
}

func withNaming(t string) option {
	return func(c *spool.Config, _ *rig) { c.Dests[0].Naming = naming.MustParse(t) }
}

func withFree(n uint64, err error) option {
	return func(c *spool.Config, _ *rig) { c.Free = func() (uint64, error) { return n, err } }
}

func noVerify(c *spool.Config, _ *rig) { c.Dests[0].Verify = false }

// withSink replaces the one destination, so that a test can hand over a sink
// that lies or that gets in the way.
func withSink(k sink.Sink) option {
	return func(c *spool.Config, _ *rig) { c.Dests[0].Sink = k }
}

// withSourceWrapper puts something between the spool and the source.
func withSourceWrapper(f func(source.Source) source.Source) option {
	return func(c *spool.Config, g *rig) { c.Source = f(g.src) }
}

func withTrigger(t trigger.Trigger) option {
	return func(c *spool.Config, _ *rig) { c.Trigger = t }
}

func newRig(t *testing.T, opts ...option) *rig {
	t.Helper()

	g := &rig{
		src:    sourcemem.New(),
		cloud:  sinkmem.New(),
		onsite: sinkmem.New(),
		jnl:    journalmem.New(),
		clock:  newClock(),
	}

	c := spool.Config{
		Name:    "recordings",
		Host:    "thor-top",
		Source:  g.src,
		Journal: g.jnl,
		Now:     g.clock.now,
		Dests: []spool.Dest{{
			Name:   "cloud",
			Sink:   g.cloud,
			Naming: naming.MustParse("{host}/{name}"),
			Verify: true,
		}},
	}
	for _, o := range opts {
		o(&c, g)
	}

	s, err := spool.New(c)
	require.NoError(t, err)
	g.Spool = s

	return g
}

func (g *rig) add(key, data string) {
	g.src.Add(key, []byte(data), start)
}

func (g *rig) record(t *testing.T, key string) (journal.Record, bool) {
	t.Helper()

	r, ok, err := g.jnl.Get(t.Context(), "recordings", key)
	require.NoError(t, err)

	return r, ok
}

// bare is a source that can only be read from.
type bare struct{ inner source.Source }

func (b bare) Scan(ctx context.Context) iter.Seq2[source.Item, error] { return b.inner.Scan(ctx) }
func (b bare) Open(ctx context.Context, k string) (io.ReadCloser, error) {
	return b.inner.Open(ctx, k)
}

// putOnly is a sink that cannot be asked what it holds.
type putOnly struct{}

func (putOnly) Put(context.Context, string, io.Reader, sink.Meta) error { return nil }

func TestNew(t *testing.T) {
	src := sourcemem.New()
	jnl := journalmem.New()
	dst := spool.Dest{Name: "cloud", Sink: sinkmem.New()}

	for _, tc := range []struct {
		name string
		c    spool.Config
		says string
	}{
		{
			"a spool has to have a name",
			spool.Config{Source: src, Journal: jnl, Dests: []spool.Dest{dst}},
			"has to have a name",
		},
		{
			"and something to carry from",
			spool.Config{Name: "a", Journal: jnl, Dests: []spool.Dest{dst}},
			"no source",
		},
		{
			"and something to remember with",
			spool.Config{Name: "a", Source: src, Dests: []spool.Dest{dst}},
			"no journal",
		},
		{
			"and somewhere to carry to",
			spool.Config{Name: "a", Source: src, Journal: jnl},
			"nowhere to carry to",
		},
		{
			"a destination has to have a name, since that is how a half-finished carry is remembered",
			spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{{Sink: sinkmem.New()}}},
			"has no name",
		},
		{
			"two destinations under one name would each be read as the other",
			spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{dst, dst}},
			"two destinations called",
		},
		{
			"a destination has to have a sink",
			spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{{Name: "cloud"}}},
			"has no sink",
		},
		{
			"verifying against a sink that cannot be asked would never record anything",
			spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{{Name: "c", Sink: putOnly{}, Verify: true}}},
			"cannot be asked what it holds",
		},
		{
			"deleting from a source that cannot delete is refused here, not at night on a machine",
			spool.Config{Name: "a", Source: bare{src}, Journal: jnl, Dests: []spool.Dest{dst}, Retain: retain.Delete()},
			"can delete",
		},
		{
			"a length of time to sit still that is not one",
			spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{dst}, Settle: -1},
			"not a length of time",
		},
		{
			"a number of things at once that is not one",
			spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{dst}, Workers: -1},
			"at once",
		},
		{
			"a number of attempts that is not one",
			spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{dst}, Attempts: -1},
			"would try",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			_, err := spool.New(tc.c)
			x.ErrorContains(err, tc.says)
		})
	}

	t.Run("what it was told nothing about, it is careful with", func(t *testing.T) {
		x := require.New(t)

		s, err := spool.New(spool.Config{Name: "a", Source: src, Journal: jnl, Dests: []spool.Dest{dst}})
		x.NoError(err)
		x.Equal("a", s.Name())
	})
}

func withJournal(j journal.Journal) option {
	return func(c *spool.Config, g *rig) {
		c.Journal = j
		g.jnl = j
	}
}

// failingJournal is a journal that can be made to refuse one thing at a time.
//
// The refusals matter more here than anywhere else: the ordering the whole
// design rests on is only worth having if the failure at each step leaves the
// file where it is.
type failingJournal struct {
	journal.Journal

	getErr    error
	putErr    error
	deleteErr error
	rangeErr  error

	// After lets the first n calls through, so that a test can pick which of
	// the several asks about one key is the one that goes wrong.
	getErrAfter int
	putErrAfter int

	// Counted under a lock, since the loop asks from a goroutine of its own.
	mu   sync.Mutex
	gets int
	puts int
}

// Gets is how many times it has been asked about a key.
func (j *failingJournal) Gets() int {
	j.mu.Lock()
	defer j.mu.Unlock()

	return j.gets
}

func (j *failingJournal) Get(ctx context.Context, source, key string) (journal.Record, bool, error) {
	j.mu.Lock()
	j.gets++
	n := j.gets
	j.mu.Unlock()

	if j.getErr != nil && n > j.getErrAfter {
		return journal.Record{}, false, j.getErr
	}

	return j.Journal.Get(ctx, source, key)
}

func (j *failingJournal) Put(ctx context.Context, r journal.Record) error {
	j.mu.Lock()
	j.puts++
	n := j.puts
	j.mu.Unlock()

	if j.putErr != nil && n > j.putErrAfter {
		return j.putErr
	}

	return j.Journal.Put(ctx, r)
}

func (j *failingJournal) Delete(ctx context.Context, source, key string) error {
	if j.deleteErr != nil {
		return j.deleteErr
	}

	return j.Journal.Delete(ctx, source, key)
}

func (j *failingJournal) Range(ctx context.Context, source string) iter.Seq2[journal.Record, error] {
	if j.rangeErr != nil {
		return func(yield func(journal.Record, error) bool) {
			yield(journal.Record{}, j.rangeErr)
		}
	}

	return j.Journal.Range(ctx, source)
}

var _ journal.Journal = (*failingJournal)(nil)
