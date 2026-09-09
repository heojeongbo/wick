package bolt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	bbolt "go.etcd.io/bbolt"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/journal/bolt"
	"github.com/heojeongbo/wick/journal/journaltest"
)

func open(t *testing.T) *bolt.Journal {
	t.Helper()

	j, err := bolt.Open(filepath.Join(t.TempDir(), "journal.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = j.Close() })

	return j
}

func TestJournal(t *testing.T) {
	journaltest.Suite(t, func(t *testing.T) journal.Journal { return open(t) })
}

func TestOpen(t *testing.T) {
	t.Run("the directory it goes in is made", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "a", "b", "journal.db")
		j, err := bolt.Open(p)
		x.NoError(err)
		defer j.Close()

		x.FileExists(p)
	})
	t.Run("a directory that cannot be made is said so", func(t *testing.T) {
		x := require.New(t)

		// A file where a directory would have to go.
		blocked := filepath.Join(t.TempDir(), "file")
		x.NoError(os.WriteFile(blocked, nil, 0o600))

		_, err := bolt.Open(filepath.Join(blocked, "a", "journal.db"))
		x.ErrorContains(err, "make the directory")
	})
	t.Run("a path that is not a journal is said so", func(t *testing.T) {
		x := require.New(t)

		// A directory where the file would go.
		dir := t.TempDir()

		_, err := bolt.Open(dir)
		x.ErrorContains(err, "open journal")
		x.ErrorContains(err, dir)
	})
	t.Run("a second one on the same file is refused rather than made to wait forever", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "journal.db")
		first, err := bolt.Open(p)
		x.NoError(err)
		defer first.Close()

		// Two of this daemon on one journal is an operator error, and it is
		// much better as a refusal at startup than as two processes deleting
		// the same files.
		_, err = bolt.Open(p)
		x.ErrorContains(err, "open journal")
	})
}

func TestPut(t *testing.T) {
	t.Run("a record with no source has nowhere to be kept", func(t *testing.T) {
		x := require.New(t)

		j := open(t)
		err := j.Put(t.Context(), journal.Record{Key: "a.rec"})
		x.ErrorContains(err, "source")
	})
	t.Run("a record with no key has nothing to be found by", func(t *testing.T) {
		x := require.New(t)

		j := open(t)
		err := j.Put(t.Context(), journal.Record{Source: "src"})
		x.ErrorContains(err, "the record of")
	})
	t.Run("a state that is not one cannot be written", func(t *testing.T) {
		x := require.New(t)

		j := open(t)
		err := j.Put(t.Context(), journal.Record{Source: "src", Key: "a.rec", State: journal.State(99)})
		x.ErrorContains(err, "encode the record")
		x.ErrorContains(err, "99")
	})
	t.Run("a journal that is closed refuses rather than pretends", func(t *testing.T) {
		x := require.New(t)

		j := open(t)
		x.NoError(j.Close())

		x.ErrorContains(j.Put(t.Context(), journal.Record{Source: "src", Key: "a.rec"}), "write journal")
		x.ErrorContains(j.Delete(t.Context(), "src", "a.rec"), "write journal")

		_, _, err := j.Get(t.Context(), "src", "a.rec")
		x.ErrorContains(err, "read journal")

		for _, err := range j.Range(t.Context(), "src") {
			x.ErrorContains(err, "read journal")

			break
		}
	})
}

// A record nothing can read is the case `status` is being run for. It must say
// which one, and it must still say what the others are.
func TestARecordThatCannotBeRead(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "journal.db")

	j, err := bolt.Open(p)
	x.NoError(err)
	x.NoError(j.Put(t.Context(), journal.Record{Source: "src", Key: "a.rec"}))
	x.NoError(j.Put(t.Context(), journal.Record{Source: "src", Key: "b.rec"}))
	x.NoError(j.Close())

	// Reach past the journal and write something it cannot decode, the way a
	// half-written page or a version that moved on would look.
	db, err := bbolt.Open(p, 0o600, nil)
	x.NoError(err)
	x.NoError(db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("src")).Put([]byte("a.rec"), []byte("{not json"))
	}))
	x.NoError(db.Close())

	j, err = bolt.Open(p)
	x.NoError(err)
	defer j.Close()

	_, _, err = j.Get(t.Context(), "src", "a.rec")
	x.ErrorContains(err, "decode the record")
	x.ErrorContains(err, "a.rec")

	var (
		errs []error
		keys []string
	)
	for r, err := range j.Range(t.Context(), "src") {
		if err != nil {
			errs = append(errs, err)

			continue
		}
		keys = append(keys, r.Key)
	}
	x.Len(errs, 1)
	x.ErrorContains(errs[0], "a.rec")
	// The one that is readable is still reported, which is the point.
	x.Equal([]string{"b.rec"}, keys)
}

// A caller that has seen enough is not walked any further, error or not.
func TestRangeStopsWhenTheCallerDoes(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "journal.db")

	j, err := bolt.Open(p)
	x.NoError(err)
	x.NoError(j.Put(t.Context(), journal.Record{Source: "src", Key: "a.rec"}))
	x.NoError(j.Put(t.Context(), journal.Record{Source: "src", Key: "b.rec"}))
	x.NoError(j.Close())

	db, err := bbolt.Open(p, 0o600, nil)
	x.NoError(err)
	x.NoError(db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket([]byte("src")).Put([]byte("a.rec"), []byte("{not json"))
	}))
	x.NoError(db.Close())

	j, err = bolt.Open(p)
	x.NoError(err)
	defer j.Close()

	n := 0
	for range j.Range(t.Context(), "src") {
		n++

		break
	}
	x.Equal(1, n)
}
