// Package retain says what becomes of the local copy once it is known to be
// somewhere else.
//
// # Why there is no default here
//
// Every policy in this package is one somebody has to have written down. There
// is no "sensible default" for deleting a file, because the sensible default
// depends on what the file is, and a template that guesses is a template that
// deletes something it should not have on a machine nobody was watching.
//
// [Keep] is what a configuration that says nothing gets. It is not a policy so
// much as the absence of one: the carry is recorded, the file is left, and
// somebody else decides.
package retain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/source"
	"github.com/lesomnus/z"
)

// A Policy says what becomes of one item.
//
// It is given a record whose state is [journal.Carried] -- that is, one the
// sinks have confirmed -- and answers with the state it is now in.
type Policy interface {
	// After acts, and says what the record now is. Answering
	// [journal.Carried] means "not yet, ask again", which is how a policy that
	// is waiting for something says so.
	After(ctx context.Context, r journal.Record, s source.Source, now time.Time, free uint64) (journal.State, error)

	// Needs says what the policy has to be able to do to the source, so that a
	// configuration naming a policy the source cannot carry out is refused at
	// startup rather than at the first carry.
	Needs() Capability
}

// A Capability is what a policy needs of a source.
type Capability uint8

const (
	// Nothing is what a policy that only records needs.
	Nothing Capability = iota
	// Removes means the source has to be a [source.Remover].
	Removes
	// Moves means the source has to be a [source.Mover].
	Moves
)

func (c Capability) String() string {
	switch c {
	case Nothing:
		return "nothing"
	case Removes:
		return "deleting"
	case Moves:
		return "moving"
	default:
		return fmt.Sprintf("Capability(%d)", uint8(c))
	}
}

// Keep leaves the file where it is.
//
// The carry is still recorded, so the file is not sent again; it is simply not
// tidied up. This is what a configuration that said nothing about retention
// gets, because a template must not invent a destructive default.
func Keep() Policy { return keep{} }

type keep struct{}

func (keep) After(context.Context, journal.Record, source.Source, time.Time, uint64) (journal.State, error) {
	return journal.Retired, nil
}

func (keep) Needs() Capability { return Nothing }
func (keep) String() string    { return "keep" }

// Delete takes the file away.
func Delete() Policy { return del{} }

type del struct{}

func (del) After(ctx context.Context, r journal.Record, s source.Source, _ time.Time, _ uint64) (journal.State, error) {
	rm, ok := s.(source.Remover)
	if !ok {
		// Refused at startup by [Check], so reaching this means the source was
		// swapped underneath. Saying so beats deleting nothing in silence.
		return journal.Carried, fmt.Errorf("this source cannot delete %q", r.Key)
	}

	if err := rm.Remove(ctx, r.Key); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Somebody else got there first, which is the outcome that was
			// wanted. Retrying it forever would be the only mistake here.
			return journal.Retired, nil
		}

		return journal.Carried, z.Err(err, "delete %q", r.Key)
	}

	return journal.Retired, nil
}

func (del) Needs() Capability { return Removes }
func (del) String() string    { return "delete" }

// Move puts the file under dir, keeping its own name.
//
// Whether that is free or is a copy depends on whether dir is on the same
// filesystem; the source decides, and both work.
func Move(dir string) Policy { return move(dir) }

type move string

func (m move) After(ctx context.Context, r journal.Record, s source.Source, _ time.Time, _ uint64) (journal.State, error) {
	mv, ok := s.(source.Mover)
	if !ok {
		return journal.Carried, fmt.Errorf("this source cannot move %q", r.Key)
	}

	if err := mv.Move(ctx, r.Key, string(m)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return journal.Retired, nil
		}

		return journal.Carried, z.Err(err, "move %q", r.Key)
	}

	return journal.Retired, nil
}

func (m move) Needs() Capability { return Moves }
func (m move) String() string    { return fmt.Sprintf("move to %q", string(m)) }

// Grace holds the file for d after it was carried, and then hands it to p.
//
// This is the one worth reaching for on a machine that is hard to get to. The
// window is long enough that somebody who notices the wrong thing was carried
// can still reach the original, and short enough that the disk does not fill
// with things that are already safe elsewhere.
func Grace(d time.Duration, p Policy) Policy { return grace{d: d, p: p} }

type grace struct {
	d time.Duration
	p Policy
}

func (g grace) After(ctx context.Context, r journal.Record, s source.Source, now time.Time, free uint64) (journal.State, error) {
	at := carriedAt(r)
	if at.IsZero() {
		// Nothing said when it was carried, so there is no window to be
		// inside. Acting now is the safe way round: the alternative is a file
		// that is never tidied because of a record that lost a timestamp.
		return g.p.After(ctx, r, s, now, free)
	}
	if now.Sub(at) < g.d {
		return journal.Carried, nil
	}

	return g.p.After(ctx, r, s, now, free)
}

func (g grace) Needs() Capability { return g.p.Needs() }
func (g grace) Unwrap() Policy    { return g.p }
func (g grace) String() string    { return fmt.Sprintf("%s after %s", g.p, g.d) }

// WhenFreeBelow hands the file to p as soon as there is less than n bytes of
// room, whatever else was going to wait.
//
// It is the escape hatch on a [Grace]: the window is a kindness, and a disk
// that is filling is not the time for one.
func WhenFreeBelow(n uint64, p Policy) Policy { return whenFree{n: n, p: p} }

type whenFree struct {
	n uint64
	p Policy
}

func (w whenFree) After(ctx context.Context, r journal.Record, s source.Source, now time.Time, free uint64) (journal.State, error) {
	// Zero is nobody could say, and a filesystem that cannot be measured must
	// not read as one that is full.
	if free == 0 || free >= w.n {
		return journal.Carried, nil
	}

	return w.p.After(ctx, r, s, now, free)
}

func (w whenFree) Needs() Capability { return w.p.Needs() }
func (w whenFree) Unwrap() Policy    { return w.p }
func (w whenFree) String() string {
	return fmt.Sprintf("%s when less than %d bytes are free", w.p, w.n)
}

// First is the first of them that does something.
//
// A policy that answers [journal.Carried] has done nothing and the next one is
// asked; the first that retires the file, or refuses, is the answer. This is
// how "after a day, or sooner if the disk is filling" is written:
//
//	retain.First(
//		retain.WhenFreeBelow(20<<30, retain.Delete()),
//		retain.Grace(24*time.Hour, retain.Delete()),
//	)
//
// With nothing in it, it keeps the file, which is the harmless thing to do.
func First(ps ...Policy) Policy { return first(ps) }

type first []Policy

func (f first) After(ctx context.Context, r journal.Record, s source.Source, now time.Time, free uint64) (journal.State, error) {
	for _, p := range f {
		st, err := p.After(ctx, r, s, now, free)
		if err != nil {
			return st, err
		}
		if st != journal.Carried {
			return st, nil
		}
	}

	return journal.Carried, nil
}

// Needs is the most a policy in here asks for. Moving and deleting are both
// asked for when both are in the list, and [Check] is given the whole list
// rather than this, so nothing is lost by the ordering here.
func (f first) Needs() Capability {
	var most Capability
	for _, p := range f {
		if n := p.Needs(); n > most {
			most = n
		}
	}

	return most
}

func (f first) Unwrap() []Policy { return f }

func (f first) String() string {
	if len(f) == 0 {
		return "keep"
	}

	s := "first of ["
	for i, p := range f {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprint(p)
	}

	return s + "]"
}

// Check says whether a source can carry out a policy.
//
// It is what turns "this will fail at the first carry, on a machine, at night"
// into "this is refused when the configuration is read". The message names both
// halves, because which one is wrong depends on what somebody meant.
func Check(p Policy, s source.Source) error {
	for _, want := range needs(p) {
		switch want {
		case Removes:
			if _, ok := s.(source.Remover); !ok {
				return fmt.Errorf("the policy %s needs a source that can delete, and this one cannot", p)
			}

		case Moves:
			if _, ok := s.(source.Mover); !ok {
				return fmt.Errorf("the policy %s needs a source that can move things, and this one cannot", p)
			}

		case Nothing:
		}
	}

	return nil
}

// needs is everything a policy asks for, looking inside the ones that hold
// others.
func needs(p Policy) []Capability {
	if u, ok := p.(interface{ Unwrap() []Policy }); ok {
		var cs []Capability
		for _, inner := range u.Unwrap() {
			cs = append(cs, needs(inner)...)
		}

		return cs
	}
	if u, ok := p.(interface{ Unwrap() Policy }); ok {
		return needs(u.Unwrap())
	}

	return []Capability{p.Needs()}
}

// carriedAt is the earliest moment every sink had answered, which is when the
// file became safe to act on. The latest of them is when the last one
// confirmed; that is the one a window should be measured from.
func carriedAt(r journal.Record) time.Time {
	var latest time.Time
	for _, c := range r.Carried {
		if c.At.After(latest) {
			latest = c.At
		}
	}

	return latest
}
