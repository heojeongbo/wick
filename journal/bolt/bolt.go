// Package bolt keeps records in a file.
//
// # Why bbolt and not a directory of files
//
// The thing being protected is the ordering: a record must become durable
// before the local file is deleted, and either be there or not be there if the
// power goes out in between. A single-file transactional store gives that for
// nothing. A directory of small files gives it only with fsync on the file and
// then on the directory, which is the same work, written by hand, in the one
// place a mistake is silent.
//
// # Why the open has a timeout
//
// bbolt takes an exclusive lock on the file. Two copies of this daemon on the
// same journal is an operator error or a restart that overlapped the process it
// replaced, and both are much better as a refusal at startup than as two
// processes carrying and deleting the same files.
package bolt

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"time"

	bbolt "go.etcd.io/bbolt"

	"github.com/heojeongbo/wick/journal"
	"github.com/lesomnus/z"
)

// The layout is one top-level bucket per source, keyed by the item's key and
// holding the record as JSON.
//
//	<source>/<key> -> json(Record)
//
// The bucket is named the source and not a prefix of it, so that a source with
// no name is refused by the store rather than by a check here that would then
// be the only thing standing between an unnamed spool and a bucket called "".
// Anything this package ever keeps for itself has to go in a bucket whose name
// a source cannot have; see the name rules a spool is held to in the config.

// openTimeout is how long to wait for the file lock before saying somebody else
// has it. Long enough that a restart overlapping its predecessor by a moment
// succeeds, short enough that one which is not going to succeed says so.
const openTimeout = 3 * time.Second

type Journal struct {
	db *bbolt.DB
}

// Open makes the journal at path, and the directory it sits in.
func Open(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, z.Err(err, "make the directory the journal goes in")
	}

	db, err := bbolt.Open(path, 0o600, &bbolt.Options{Timeout: openTimeout})
	if err != nil {
		return nil, z.Err(err, "open journal %q", path)
	}

	return &Journal{db: db}, nil
}

func (j *Journal) Get(ctx context.Context, source, key string) (journal.Record, bool, error) {
	if err := ctx.Err(); err != nil {
		return journal.Record{}, false, err
	}

	var (
		r  journal.Record
		ok bool
	)
	err := j.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(source))
		if b == nil {
			// Nothing has been written about this source yet, which is not an
			// error; it is the first pass over a directory.
			return nil
		}

		v := b.Get([]byte(key))
		if v == nil {
			return nil
		}
		if err := json.Unmarshal(v, &r); err != nil {
			return z.Err(err, "decode the record of %q", key)
		}
		ok = true

		return nil
	})
	if err != nil {
		return journal.Record{}, false, z.Err(err, "read journal")
	}

	return r, ok, nil
}

func (j *Journal) Put(ctx context.Context, r journal.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	v, err := json.Marshal(r)
	if err != nil {
		return z.Err(err, "encode the record of %q", r.Key)
	}

	err = j.db.Update(func(tx *bbolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(r.Source))
		if err != nil {
			return z.Err(err, "the records of source %q", r.Source)
		}

		return z.ErrIf(b.Put([]byte(r.Key), v), "the record of %q", r.Key)
	})

	return z.ErrIf(err, "write journal")
}

func (j *Journal) Delete(ctx context.Context, source, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	err := j.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(source))
		if b == nil {
			// Forgetting twice is forgetting once.
			return nil
		}

		return b.Delete([]byte(key))
	})

	return z.ErrIf(err, "write journal")
}

func (j *Journal) Range(ctx context.Context, source string) iter.Seq2[journal.Record, error] {
	return func(yield func(journal.Record, error) bool) {
		// The whole walk is one read transaction, so what is yielded is one
		// consistent view rather than a series of glimpses of a moving target.
		err := j.db.View(func(tx *bbolt.Tx) error {
			b := tx.Bucket([]byte(source))
			if b == nil {
				return nil
			}

			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				if err := ctx.Err(); err != nil {
					yield(journal.Record{}, err)

					return nil
				}

				var r journal.Record
				if err := json.Unmarshal(v, &r); err != nil {
					// Yielded rather than returned: one record nothing can read
					// must not hide the rest of them from a `status` that is
					// being run precisely because something is wrong.
					if !yield(journal.Record{}, z.Err(err, "decode the record of %q", k)) {
						return nil
					}

					continue
				}
				if !yield(r, nil) {
					return nil
				}
			}

			return nil
		})
		if err != nil {
			yield(journal.Record{}, z.Err(err, "read journal"))
		}
	}
}

func (j *Journal) Close() error {
	return z.ErrIf(j.db.Close(), "close journal")
}

var _ journal.Journal = (*Journal)(nil)
