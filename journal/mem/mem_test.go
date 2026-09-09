package mem_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/journal/journaltest"
	"github.com/heojeongbo/wick/journal/mem"
)

func TestJournal(t *testing.T) {
	journaltest.Suite(t, func(t *testing.T) journal.Journal {
		j := mem.New()
		t.Cleanup(func() { _ = j.Close() })

		return j
	})
}

// The map a caller hands over is theirs and may still be theirs afterwards;
// what is remembered must not change under it.
func TestPutCopiesWhatItIsGiven(t *testing.T) {
	x := require.New(t)

	j := mem.New()
	cs := map[string]journal.Carry{"cloud": {Name: "a"}}
	x.NoError(j.Put(t.Context(), journal.Record{Source: "src", Key: "a.rec", Carried: cs}))

	cs["onprem"] = journal.Carry{Name: "b"}

	r, ok, err := j.Get(t.Context(), "src", "a.rec")
	x.NoError(err)
	x.True(ok)
	x.True(r.IsCarriedTo("cloud"))
	x.False(r.IsCarriedTo("onprem"))
}
