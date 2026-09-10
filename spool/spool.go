// Package spool is the engine: one place watched, and the places what is found
// there goes.
//
// A spool is the unit of configuration and the unit of work. It has one source
// and any number of sinks, and something is only carried when every sink has
// confirmed it -- so "the cloud and the server in the building" is one spool
// with two sinks rather than two spools that each half-know about the file.
//
// # The order everything else follows from
//
//	read -> write to every sink -> read each one back -> record -> retire
//
// Nothing is written to the journal before the read-back has confirmed the
// copy. A false "not carried" costs one upload. A false "carried" loses the
// file. There is no arrangement of those two mistakes in which the second is
// acceptable, so every ordering decision here resolves toward the first.
//
// # Why the settle gate is here and not in the source
//
// A file that is still being written looks exactly like a file that has been
// written. The only way to tell is to look twice and see whether it changed,
// and looking twice is something the engine does anyway -- it scans on a
// schedule. Putting it in the source would mean every source implementing it,
// including the ones where it is the same code.
package spool

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lesomnus/z"

	"github.com/heojeongbo/wick/disk"
	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/naming"
	"github.com/heojeongbo/wick/retain"
	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/trigger"
)

// A Dest is one place things go, under a name.
type Dest struct {
	// Name is what this destination is called, in the configuration, in the
	// journal, and in anything said about it. It is the key a partly-finished
	// fan-out is remembered by, so it has to be stable across restarts.
	Name string

	Sink sink.Sink

	// Naming is what an item is called here. Two destinations may name the
	// same file differently, which is the point of it being per destination:
	// a bucket wants a prefix and a directory on a NAS wants the date.
	Naming naming.Template

	// Verify says to read the copy back before believing it arrived. It wants
	// a [sink.Stater]; [New] refuses the pair if the sink is not one.
	Verify bool
}

// Config is everything a spool is.
type Config struct {
	// Name is what this spool is called. It is the source name in the journal,
	// so two spools watching the same directory keep separate records and one
	// spool keeps its records across a restart.
	Name string

	// Host is what the machine is called, for a naming template that asks for
	// it. It is here rather than looked up because three machines in one robot
	// are three of these, and which one this is has to be a thing that was
	// decided rather than a thing that was guessed at every carry.
	Host string

	Source  source.Source
	Dests   []Dest
	Journal journal.Journal

	// Trigger says when to carry. Nothing means only when asked.
	Trigger trigger.Trigger

	// Retain says what becomes of the local copy. Nothing means [retain.Keep],
	// because a spool that was told nothing about deleting must not delete.
	Retain retain.Policy

	// Settle is how long a file has to have been unchanged before it is
	// eligible. Nothing means eligible at once, which is right for a source
	// whose items appear whole and wrong for a directory somebody is writing
	// into.
	Settle time.Duration

	// Workers is how many items are carried at once. Nothing means one, which
	// is the right default on a link this is usually sharing.
	Workers int

	// Attempts is how many times a failing item is tried before it is set
	// aside. Nothing means [DefaultAttempts]. One that is set aside stops
	// being tried, so that it cannot hold up everything behind it forever.
	Attempts int

	// Now is the clock. Nothing means [time.Now]; a test hands over its own so
	// that nothing here has to sleep.
	Now func() time.Time

	// Free says how much room is left where the source is. Nothing means
	// [disk.Free] of the source's root, when the source has one, and otherwise
	// nothing -- which the triggers and policies read as "nobody could say"
	// rather than as "no room".
	Free func() (uint64, error)
}

// DefaultAttempts is how many times something is tried before it is set aside.
//
// Small, because the thing it is protecting against is one poisoned item
// spending the whole link on itself. A carry that failed for a passing reason
// gets its attempts back on the next success anyway.
const DefaultAttempts = 5

// dest is a [Dest] with the question "can this be asked what it holds" already
// answered, so that it is answered once at startup rather than at every carry
// -- and so that there is no branch here for a case [New] has refused.
type dest struct {
	Dest

	// stat is nil unless Verify is set, and is never nil when it is.
	stat sink.Stater
}

// A Spool carries what accumulates in one place to the places it goes.
type Spool struct {
	name    string
	host    string
	src     source.Source
	dests   []dest
	jnl     journal.Journal
	trig    trigger.Trigger
	keep    retain.Policy
	settle  time.Duration
	workers int
	tries   int
	now     func() time.Time
	free    func() (uint64, error)

	// seen is what the last scan said about each key, so that the settle gate
	// has something to compare against. It is only in memory: after a restart
	// every file has to sit still once more before it is carried, which is the
	// safe way round.
	mu   sync.Mutex
	seen map[string]sighting

	// wake is how something outside asks for a pass. Buffered by one: a second
	// nudge before the first is taken says nothing the first did not.
	wake chan struct{}

	// last is when a carry last happened. It stays zero until one has, which is
	// what keeps `wick.carry.last_success` from reading as healthy on a daemon
	// that has never managed to carry anything -- the exact failure that metric
	// exists to make visible.
	last time.Time

	// born is when this spool was made, and is what the clock trigger measures
	// from until there is a carry to measure from instead.
	//
	// It is a second field rather than a seeded `last` because the two are
	// asked different questions. "How long since something was carried" has no
	// answer before the first one and must not be given a made-up one. "How
	// long has this been waiting" always has one: since it started. Without
	// this, a spool whose link is down never carries, so `last` stays zero, so
	// [trigger.Every] never fires, so it never tries again.
	born time.Time
}

type sighting struct {
	size  int64
	modAt time.Time
	// at is when it was first seen looking like this.
	at time.Time
}

func New(c Config) (*Spool, error) {
	if c.Name == "" {
		return nil, fmt.Errorf("a spool has to have a name; it is what its records are kept under")
	}
	if c.Source == nil {
		return nil, fmt.Errorf("the spool %q has no source, so there is nothing for it to carry", c.Name)
	}
	if c.Journal == nil {
		return nil, fmt.Errorf("the spool %q has no journal, and without one it cannot know what it has already carried", c.Name)
	}
	if len(c.Dests) == 0 {
		return nil, fmt.Errorf("the spool %q has nowhere to carry to", c.Name)
	}

	seen := map[string]bool{}
	dests := make([]dest, 0, len(c.Dests))
	for _, d := range c.Dests {
		switch {
		case d.Name == "":
			return nil, fmt.Errorf("a destination of the spool %q has no name, and the name is how a half-finished carry is remembered", c.Name)

		case seen[d.Name]:
			return nil, fmt.Errorf("the spool %q has two destinations called %q, so a record of one would be read as a record of the other", c.Name, d.Name)

		case d.Sink == nil:
			return nil, fmt.Errorf("the destination %q of the spool %q has no sink", d.Name, c.Name)
		}
		seen[d.Name] = true

		st, ok := d.Sink.(sink.Stater)
		if d.Verify && !ok {
			// Refused here rather than at the first carry, because the whole
			// point of verifying is that a carry which was not verified is not
			// recorded -- and a spool that can never record anything is a
			// spool that carries the same files forever.
			return nil, fmt.Errorf("the destination %q of the spool %q is to be verified, and this kind of sink cannot be asked what it holds", d.Name, c.Name)
		}
		if !d.Verify {
			st = nil
		}

		dests = append(dests, dest{Dest: d, stat: st})
	}

	keep := c.Retain
	if keep == nil {
		keep = retain.Keep()
	}
	if err := retain.Check(keep, c.Source); err != nil {
		return nil, z.Err(err, "the spool %q", c.Name)
	}

	trig := c.Trigger
	if trig == nil {
		trig = trigger.Any()
	}

	if c.Settle < 0 {
		return nil, fmt.Errorf("the spool %q would have things sit still for %s, which is not a length of time to wait", c.Name, c.Settle)
	}

	workers := c.Workers
	if workers == 0 {
		workers = 1
	}
	if workers < 0 {
		return nil, fmt.Errorf("the spool %q would carry %d things at once", c.Name, workers)
	}

	tries := c.Attempts
	if tries == 0 {
		tries = DefaultAttempts
	}
	if tries < 0 {
		return nil, fmt.Errorf("the spool %q would try %d times", c.Name, tries)
	}

	now := c.Now
	if now == nil {
		now = time.Now
	}

	free := c.Free
	if free == nil {
		free = freeOf(c.Source)
	}

	return &Spool{
		name:    c.Name,
		host:    c.Host,
		src:     c.Source,
		dests:   dests,
		jnl:     c.Journal,
		trig:    trig,
		keep:    keep,
		settle:  c.Settle,
		workers: workers,
		tries:   tries,
		now:     now,
		free:    free,
		seen:    map[string]sighting{},
		wake:    make(chan struct{}, 1),
		born:    now(),
	}, nil
}

// Name is what this spool is called.
func (s *Spool) Name() string { return s.name }

// freeOf is how much room is left where a source is, when the source is
// somewhere -- which is only true of the ones that are on a filesystem.
//
// A source that is not says nothing, and a trigger or a policy that asks reads
// that as "nobody could say" rather than as "no room".
func freeOf(src source.Source) func() (uint64, error) {
	r, ok := src.(interface{ Root() string })
	if !ok {
		return func() (uint64, error) { return 0, nil }
	}

	return func() (uint64, error) { return disk.Free(r.Root()) }
}

// Nudge asks for a pass, and is what a watcher calls.
//
// It never blocks. A nudge that arrives while one is already waiting says
// nothing the first did not.
func (s *Spool) Nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Report is what one pass did.
type Report struct {
	// Scanned is everything the source held.
	Scanned int
	// Settled is how many of those had sat still long enough to be eligible.
	Settled int
	// Carried is how many were confirmed at every destination this pass, and
	// Bytes is how much was sent to get there.
	Carried int
	Bytes   int64
	// Retired is how many had their local copy dealt with.
	Retired int
	// SetAside is how many were tried once too often and will not be tried
	// again until somebody says so.
	SetAside int
	// Errs is everything that went wrong, in the order it went wrong. A pass
	// does not stop at the first one: an item that cannot be carried must not
	// hide the ones that can.
	Errs []error
}

// Err is everything that went wrong, as one, or nil.
func (r Report) Err() error { return errors.Join(r.Errs...) }
