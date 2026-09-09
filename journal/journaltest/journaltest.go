// Package journaltest is the contract every [journal.Journal] has to keep.
//
// It is exported rather than internal because the interface is meant to be
// implemented elsewhere -- somebody keeping records in Postgres, or in whatever
// their fleet already runs -- and the difference between an interface and a
// contract is a suite that says what the words in the doc comments mean.
//
//	func TestJournal(t *testing.T) {
//		journaltest.Suite(t, func(t *testing.T) journal.Journal { return mem.New() })
//	}
package journaltest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
)

// contextCancelled is a context of this test's that is about to be done.
func contextCancelled(t *testing.T) (context.Context, context.CancelFunc) {
	return context.WithCancel(t.Context())
}

// keys is what a source holds, in the order it was walked.
//
// It is a function rather than a loop written out at each place that needs one
// because a subtest asserting that a source holds *nothing* would otherwise be
// a loop whose body is never entered, and a body nothing enters is a body
// nothing checks.
func keys(t *testing.T, j journal.Journal, source string) []string {
	t.Helper()

	var ks []string
	for r, err := range j.Range(t.Context(), source) {
		require.NoError(t, err)
		ks = append(ks, r.Key)
	}

	return ks
}

// Open makes a journal for one test. It is given the testing.T so it can
// register its own cleanup.
type Open func(t *testing.T) journal.Journal

// modAt is a fixed instant, so that a test about records is not also a test
// about clocks.
var modAt = time.Date(2026, 9, 12, 3, 4, 5, 0, time.UTC)

func rec(source, key string, s journal.State) journal.Record {
	return journal.Record{
		Source: source,
		Key:    key,
		Size:   1024,
		ModAt:  modAt,
		Digest: "sha256:beef",
		State:  s,
	}
}

// Suite runs every test in the contract.
func Suite(t *testing.T, open Open) {
	t.Helper()

	t.Run("a key nothing was written about has no record", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		r, ok, err := j.Get(t.Context(), "src", "nope")
		x.NoError(err)
		x.False(ok)
		x.Zero(r)
	})

	t.Run("what was written is what comes back", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		w := rec("src", "a.rec", journal.Carried)
		w.Carried = map[string]journal.Carry{
			"cloud": {Name: "host/a.rec", Digest: "sha256:beef", At: modAt},
		}
		w.Attempts = 2
		w.Err = "the endpoint said no"
		x.NoError(j.Put(t.Context(), w))

		r, ok, err := j.Get(t.Context(), "src", "a.rec")
		x.NoError(err)
		x.True(ok)
		x.Equal(w.Key, r.Key)
		x.Equal(w.Size, r.Size)
		x.Equal(w.Digest, r.Digest)
		x.Equal(w.State, r.State)
		x.Equal(w.Attempts, r.Attempts)
		x.Equal(w.Err, r.Err)
		x.True(r.ModAt.Equal(w.ModAt))
		x.True(r.IsCarriedTo("cloud"))
		x.False(r.IsCarriedTo("onprem"))
		x.Equal("host/a.rec", r.Carried["cloud"].Name)
		x.True(r.Carried["cloud"].At.Equal(modAt))
	})

	t.Run("a second write replaces the first", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		x.NoError(j.Put(t.Context(), rec("src", "a.rec", journal.Pending)))
		x.NoError(j.Put(t.Context(), rec("src", "a.rec", journal.Retired)))

		r, ok, err := j.Get(t.Context(), "src", "a.rec")
		x.NoError(err)
		x.True(ok)
		x.Equal(journal.Retired, r.State)
	})

	t.Run("forgetting twice is forgetting once", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		x.NoError(j.Put(t.Context(), rec("src", "a.rec", journal.Carried)))
		x.NoError(j.Delete(t.Context(), "src", "a.rec"))
		x.NoError(j.Delete(t.Context(), "src", "a.rec"))
		// And one about a source nothing was ever written about.
		x.NoError(j.Delete(t.Context(), "never", "a.rec"))

		_, ok, err := j.Get(t.Context(), "src", "a.rec")
		x.NoError(err)
		x.False(ok)
	})

	t.Run("records come back in the order of their keys", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		for _, k := range []string{"c.rec", "a.rec", "b.rec"} {
			x.NoError(j.Put(t.Context(), rec("src", k, journal.Pending)))
		}

		x.Equal([]string{"a.rec", "b.rec", "c.rec"}, keys(t, j, "src"))
	})

	t.Run("a source nothing was written about yields nothing", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		x.Empty(keys(t, j, "never"))
	})

	t.Run("one source does not see another's", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		// The same key in both, which is the case this separation is for: two
		// spools watching for the same filename in different directories.
		x.NoError(j.Put(t.Context(), rec("left", "a.rec", journal.Carried)))
		x.NoError(j.Put(t.Context(), rec("right", "a.rec", journal.Quarantined)))

		l, _, err := j.Get(t.Context(), "left", "a.rec")
		x.NoError(err)
		x.Equal(journal.Carried, l.State)

		r, _, err := j.Get(t.Context(), "right", "a.rec")
		x.NoError(err)
		x.Equal(journal.Quarantined, r.State)

		x.Equal([]string{"a.rec"}, keys(t, j, "left"))
	})

	t.Run("a caller that stops looking is not walked any further", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		for _, k := range []string{"a.rec", "b.rec", "c.rec"} {
			x.NoError(j.Put(t.Context(), rec("src", k, journal.Pending)))
		}

		n := 0
		for range j.Range(t.Context(), "src") {
			n++

			break
		}
		x.Equal(1, n)
	})

	t.Run("a context that is done is refused rather than served stale", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		x.NoError(j.Put(t.Context(), rec("src", "a.rec", journal.Pending)))

		ctx, cancel := contextCancelled(t)
		cancel()

		_, _, err := j.Get(ctx, "src", "a.rec")
		x.ErrorIs(err, ctx.Err())

		x.ErrorIs(j.Put(ctx, rec("src", "b.rec", journal.Pending)), ctx.Err())
		x.ErrorIs(j.Delete(ctx, "src", "a.rec"), ctx.Err())

		for _, err := range j.Range(ctx, "src") {
			x.ErrorIs(err, ctx.Err())

			break
		}
	})

	t.Run("what a source holds is counted by state", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		older := rec("src", "old.rec", journal.Pending)
		older.ModAt = modAt.Add(-24 * time.Hour)
		x.NoError(j.Put(t.Context(), older))
		x.NoError(j.Put(t.Context(), rec("src", "a.rec", journal.Pending)))
		x.NoError(j.Put(t.Context(), rec("src", "b.rec", journal.Carried)))
		// One with no time on it at all, which must not become the oldest.
		none := rec("src", "c.rec", journal.Retired)
		none.ModAt = time.Time{}
		x.NoError(j.Put(t.Context(), none))

		s, err := journal.Summarize(t.Context(), j, "src")
		x.NoError(err)
		x.Equal(2, s.Count[journal.Pending])
		x.Equal(1, s.Count[journal.Carried])
		x.Equal(1, s.Count[journal.Retired])
		x.Zero(s.Count[journal.Quarantined])
		x.Equal(int64(2048), s.Bytes[journal.Pending])
		x.True(s.Oldest.Equal(older.ModAt))
	})

	t.Run("counting stops at the first thing it cannot read", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		x.NoError(j.Put(t.Context(), rec("src", "a.rec", journal.Pending)))

		ctx, cancel := contextCancelled(t)
		cancel()

		_, err := journal.Summarize(ctx, j, "src")
		x.ErrorIs(err, ctx.Err())
	})

	t.Run("closing it twice is not a crash", func(t *testing.T) {
		x := require.New(t)
		j := open(t)

		x.NoError(j.Close())
		// The second one may refuse; it may not panic, which is what a
		// shutdown that raced its own cleanup would do.
		x.NotPanics(func() { _ = j.Close() })
	})
}
