// Package trigger says when it is time to carry.
//
// # Why there is more than one kind
//
// Because the reasons are different in kind and any of them is enough. A clock
// is what makes a quiet machine hand its files over eventually. A count is what
// stops a busy one accumulating a backlog nobody has looked at. A size is what
// keeps a single enormous file from waiting for the clock. And free space is
// the one that is not about carrying at all -- it is about the disk filling,
// which is the failure that stops the machine doing its actual job.
//
// So they compose, and the ordinary arrangement is [Any]: whichever comes
// first.
package trigger

import (
	"fmt"
	"time"
)

// State is what the spool knows when it asks whether it is time.
type State struct {
	// Pending is how many things are waiting to be carried, and Bytes is how
	// much they come to.
	Pending int
	Bytes   int64

	// FreeBytes is how much room is left where they are. It is zero when
	// nobody could say, which is why [FreeBelow] treats zero as "no answer"
	// rather than as "no space" -- a filesystem that cannot be measured must
	// not read as one that is full.
	FreeBytes uint64

	// Since is how long it has been since the last carry.
	Since time.Duration
}

// A Trigger says whether it is time.
type Trigger interface {
	// Fire says whether to carry now.
	Fire(s State) bool

	// After is how long until Fire could become true on its own, so that the
	// spool sleeps instead of spinning.
	//
	// Zero means there is nothing to wait for: whatever this trigger answers
	// to is not the clock, and it will not change until the source does. The
	// spool reads that as "wait for something to happen", not as "ask again
	// immediately".
	After(s State) time.Duration
}

// Every fires when it has been at least d since the last carry.
//
// This is the one that makes a quiet machine hand its files over. Without it a
// single recording on a robot that is switched off for the weekend waits for
// the weekend.
func Every(d time.Duration) Trigger { return every(d) }

type every time.Duration

func (e every) Fire(s State) bool {
	// Nothing to carry is not a reason to carry.
	return s.Pending > 0 && s.Since >= time.Duration(e)
}

func (e every) After(s State) time.Duration {
	left := time.Duration(e) - s.Since
	if left < 0 {
		return 0
	}

	return left
}

func (e every) String() string { return fmt.Sprintf("every %s", time.Duration(e)) }

// Count fires when at least n things are waiting.
func Count(n int) Trigger { return count(n) }

type count int

func (c count) Fire(s State) bool         { return s.Pending >= int(c) && s.Pending > 0 }
func (c count) After(State) time.Duration { return 0 }
func (c count) String() string            { return fmt.Sprintf("%d or more waiting", int(c)) }

// Bytes fires when what is waiting comes to at least n bytes.
//
// This is the one for a source that produces a few very large things rather
// than many small ones, where a count of ten is a count that never happens.
func Bytes(n int64) Trigger { return bytes(n) }

type bytes int64

func (b bytes) Fire(s State) bool         { return s.Bytes >= int64(b) && s.Pending > 0 }
func (b bytes) After(State) time.Duration { return 0 }
func (b bytes) String() string            { return fmt.Sprintf("%d bytes or more waiting", int64(b)) }

// FreeBelow fires when there is less than n bytes of room left.
//
// It is the one that is not about carrying. The disk filling is what stops the
// machine doing the thing it is for, and carrying is how the room is made -- so
// this fires whether or not anything else would have.
func FreeBelow(n uint64) Trigger { return freeBelow(n) }

type freeBelow uint64

func (f freeBelow) Fire(s State) bool {
	// Zero is nobody could say. A filesystem that cannot be measured must not
	// read as one that is full, or every carry happens at once for no reason.
	return s.FreeBytes > 0 && s.FreeBytes < uint64(f) && s.Pending > 0
}

func (f freeBelow) After(State) time.Duration { return 0 }
func (f freeBelow) String() string            { return fmt.Sprintf("less than %d bytes free", uint64(f)) }

// Any fires when any of them does, which is the ordinary way to arrange these:
// a clock, and a few reasons not to wait for it.
//
// With nothing in it, it never fires. That is what a spool with no trigger
// written for it should do -- carry when something asks it to and not
// otherwise -- rather than carry constantly.
func Any(ts ...Trigger) Trigger { return anyOf(ts) }

type anyOf []Trigger

func (a anyOf) Fire(s State) bool {
	for _, t := range a {
		if t.Fire(s) {
			return true
		}
	}

	return false
}

// After is the soonest any of them could become true on its own. One that
// answers to nothing but the source says zero, and is not what the sleep is
// taken from.
func (a anyOf) After(s State) time.Duration {
	var soonest time.Duration
	for _, t := range a {
		d := t.After(s)
		if d <= 0 {
			continue
		}
		if soonest == 0 || d < soonest {
			soonest = d
		}
	}

	return soonest
}

func (a anyOf) Unwrap() []Trigger { return a }

func (a anyOf) String() string { return join("any of", a) }

// All fires only when every one of them does.
//
// It is here for the arrangement that says "on the hour, but only if there is
// enough to be worth it". With nothing in it, it never fires, for the same
// reason [Any] does not.
func All(ts ...Trigger) Trigger { return allOf(ts) }

type allOf []Trigger

func (a allOf) Fire(s State) bool {
	if len(a) == 0 {
		return false
	}
	for _, t := range a {
		if !t.Fire(s) {
			return false
		}
	}

	return true
}

// After is the longest any of them has to wait, since the first of them to
// come true still leaves the others.
func (a allOf) After(s State) time.Duration {
	var longest time.Duration
	for _, t := range a {
		if d := t.After(s); d > longest {
			longest = d
		}
	}

	return longest
}

func (a allOf) Unwrap() []Trigger { return a }

func (a allOf) String() string { return join("all of", a) }

func join(what string, ts []Trigger) string {
	if len(ts) == 0 {
		return "never"
	}

	s := what + " ["
	for i, t := range ts {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprint(t)
	}

	return s + "]"
}
