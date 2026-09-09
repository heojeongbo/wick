// Package mem keeps records in memory.
//
// It is what the tests use, and it is also a legitimate way to run: a spool
// whose retention is `keep` and whose sink is content-addressed re-derives
// everything it needs from a scan, so forgetting on restart costs a few
// metadata requests and no bytes. Anything that deletes wants [bolt] instead.
package mem

import (
	"context"
	"iter"
	"maps"
	"slices"
	"sync"

	"github.com/heojeongbo/wick/journal"
)

type Journal struct {
	mu sync.RWMutex
	// by source, then key. Nested so that Range does not have to walk every
	// record of every spool to answer about one.
	rs map[string]map[string]journal.Record
}

func New() *Journal {
	return &Journal{rs: map[string]map[string]journal.Record{}}
}

func (j *Journal) Get(ctx context.Context, source, key string) (journal.Record, bool, error) {
	j.mu.RLock()
	defer j.mu.RUnlock()

	r, ok := j.rs[source][key]

	return r, ok, ctx.Err()
}

func (j *Journal) Put(ctx context.Context, r journal.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	if _, ok := j.rs[r.Source]; !ok {
		j.rs[r.Source] = map[string]journal.Record{}
	}
	// Copied, so that a caller holding the map it handed over cannot change
	// what is remembered without saying so.
	r.Carried = maps.Clone(r.Carried)
	j.rs[r.Source][r.Key] = r

	return nil
}

func (j *Journal) Delete(ctx context.Context, source, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	delete(j.rs[source], key)

	return nil
}

func (j *Journal) Range(ctx context.Context, source string) iter.Seq2[journal.Record, error] {
	return func(yield func(journal.Record, error) bool) {
		j.mu.RLock()
		rs := maps.Clone(j.rs[source])
		j.mu.RUnlock()

		for _, k := range slices.Sorted(maps.Keys(rs)) {
			if err := ctx.Err(); err != nil {
				yield(journal.Record{}, err)

				return
			}
			if !yield(rs[k], nil) {
				return
			}
		}
	}
}

// Close is here to be a [journal.Journal]. There is nothing to close.
func (j *Journal) Close() error {
	return nil
}

var _ journal.Journal = (*Journal)(nil)
