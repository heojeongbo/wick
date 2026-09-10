// Package journal is what this daemon remembers.
//
// # Why there is one at all
//
// Everything else here is idempotent: a scan finds what is there, a carry
// overwrites what it wrote last time. The journal exists for the one question
// that cannot be answered by looking -- whether the copy that is somewhere else
// is the copy that is here -- and for the one action that cannot be taken back,
// which is deleting the local file.
//
// # The rule the ordering follows
//
// A record is written after the copy has been confirmed, never before. A false
// "not carried" costs one re-upload. A false "carried" loses the file. There is
// no arrangement of these two mistakes in which the second one is acceptable,
// so every ordering decision in this package resolves toward the first.
package journal

import (
	"context"
	"fmt"
	"iter"
	"time"
)

// A State is where an item has got to.
type State uint8

const (
	// Pending is seen and not carried. It is also what a record that has never
	// been written reads as, so the absence of a record and the presence of an
	// unfinished one mean the same thing to a caller.
	Pending State = iota
	// Carried is confirmed at every sink that was asked for. The local copy may
	// still be there; what becomes of it is the retention policy's to say.
	Carried
	// Retired is carried, and the local copy dealt with.
	Retired
	// Quarantined is refused often enough that trying again is only costing
	// time, and would cost it in front of everything queued behind it.
	Quarantined
)

var stateNames = map[State]string{
	Pending:     "pending",
	Carried:     "carried",
	Retired:     "retired",
	Quarantined: "quarantined",
}

// String is the word this state is written as, in the journal and in
// anything a person reads.
func (s State) String() string {
	if n, ok := stateNames[s]; ok {
		return n
	}

	return fmt.Sprintf("State(%d)", uint8(s))
}

// MarshalText is here so that a state in a status listing or a log line is the
// word and not the number.
func (s State) MarshalText() ([]byte, error) {
	if _, ok := stateNames[s]; !ok {
		return nil, fmt.Errorf("%d is not a state", uint8(s))
	}

	return []byte(stateNames[s]), nil
}

// UnmarshalText reads one of those words back.
func (s *State) UnmarshalText(b []byte) error {
	for v, n := range stateNames {
		if n == string(b) {
			*s = v

			return nil
		}
	}

	return fmt.Errorf("%q is not a state", b)
}

// A Carry is one destination's copy.
//
// It is kept per sink because a spool may have several, and a restart in the
// middle of a fan-out must not send again to the one that already answered --
// on a metered link that is the difference between a retry and a bill.
type Carry struct {
	// Name is what it was called there.
	Name string `json:"name"`
	// Digest is what was confirmed, "sha256:<hex>", or empty if the sink keeps
	// none and the confirmation was by size alone.
	Digest string `json:"digest,omitempty"`
	// At is when the read-back succeeded, not when the write was sent.
	At time.Time `json:"at"`
}

// A Record is what is remembered about one item.
type Record struct {
	// Source is the name of the spool's source, and Key is what the item was
	// called there. Together they are the identity of the record.
	Source string `json:"source"`
	Key    string `json:"key"`

	// Size and ModAt are what the item looked like when it was carried. They
	// are what tells a file that has been written again from the one that was
	// carried, which is the difference between skipping it and sending it.
	Size  int64     `json:"size"`
	ModAt time.Time `json:"mod_at"`

	// Digest is the content hash, "sha256:<hex>", once it is known.
	Digest string `json:"digest,omitempty"`

	// Carried is by sink name. A record is [Carried] once every sink the spool
	// was configured with is in here.
	Carried map[string]Carry `json:"carried,omitempty"`

	// State is where in its life this record is. See [Pending].
	State State `json:"state"`

	// Attempts is how many times carrying it has been tried and failed. It is
	// reset by a success, so it counts the current run of bad luck and not the
	// life of the file.
	Attempts int `json:"attempts,omitempty"`
	// Err is why it was last refused, kept so that `status` can say without the
	// operator having to find the log line.
	Err string `json:"err,omitempty"`
}

// IsCarriedTo reports whether the named sink has confirmed this record.
func (r Record) IsCarriedTo(sink string) bool {
	_, ok := r.Carried[sink]

	return ok
}

// SameAs reports whether the item now is the item the record was written about.
//
// A file that has been written again under the same name is a different file,
// and the record about the old one says nothing true about it. Size and
// modification time are what there is to go on without reading the whole thing,
// which is the point: the cheap check is what keeps the expensive one rare.
func (r Record) SameAs(size int64, modAt time.Time) bool {
	return r.Size == size && r.ModAt.Equal(modAt)
}

// A Journal is where records are kept.
//
// Every method takes the source name as well as the key, because one journal
// holds every spool's records and two spools may well be watching for the same
// filename in different places.
type Journal interface {
	// Get is the record, and whether there was one. A key with no record is not
	// an error: it is the ordinary case, on the first pass over a directory.
	Get(ctx context.Context, source, key string) (Record, bool, error)

	// Put writes the record, replacing whatever was there.
	Put(ctx context.Context, r Record) error

	// Delete forgets a record. A key with no record is not an error, so that
	// forgetting twice is the same as forgetting once.
	Delete(ctx context.Context, source, key string) error

	// Range yields every record of one source, in key order. An error ends the
	// iteration; the caller sees it as the second value of the last pair.
	Range(ctx context.Context, source string) iter.Seq2[Record, error]

	// Close lets go of whatever is holding the records. What is written before
	// it is written; there is nothing here that is only made durable by
	// closing, because a journal that lost its last record on a hard reset
	// would lose exactly the record the hard reset made important.
	Close() error
}

// Sum is what a listing says about a source.
type Sum struct {
	// Count is how many records are in each state, and Bytes is what they come
	// to. Every state has an entry, including the ones that are zero, so that
	// a reader does not have to tell "none" from "not counted".
	Count map[State]int
	Bytes map[State]int64

	// Oldest is when the earliest thing still being remembered was last
	// written, and is the zero time when nothing is. It is the number that
	// says a spool has stopped keeping up rather than merely being busy.
	Oldest time.Time
}

// Summarize walks a source's records once and counts them by state.
//
// It is here rather than on the interface so that an implementation only has to
// answer [Journal.Range] correctly to be able to answer `status`.
func Summarize(ctx context.Context, j Journal, source string) (Sum, error) {
	s := Sum{
		Count: map[State]int{},
		Bytes: map[State]int64{},
	}
	for r, err := range j.Range(ctx, source) {
		if err != nil {
			return Sum{}, err
		}

		s.Count[r.State]++
		s.Bytes[r.State] += r.Size
		if !r.ModAt.IsZero() && (s.Oldest.IsZero() || r.ModAt.Before(s.Oldest)) {
			s.Oldest = r.ModAt
		}
	}

	return s, nil
}
