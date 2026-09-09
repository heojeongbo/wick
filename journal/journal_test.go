package journal_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/journal"
)

func TestState(t *testing.T) {
	t.Run("a state says its name and not its number", func(t *testing.T) {
		x := require.New(t)

		x.Equal("pending", journal.Pending.String())
		x.Equal("carried", journal.Carried.String())
		x.Equal("retired", journal.Retired.String())
		x.Equal("quarantined", journal.Quarantined.String())
	})
	t.Run("one that is not a state says so rather than nothing", func(t *testing.T) {
		x := require.New(t)

		// Nothing produces this, which is why it has to read as a number: a
		// state that says "" would be a bug that looks like an empty field.
		x.Equal("State(99)", journal.State(99).String())
	})
	t.Run("a state written down is the word", func(t *testing.T) {
		x := require.New(t)

		b, err := json.Marshal(journal.Carried)
		x.NoError(err)
		x.JSONEq(`"carried"`, string(b))
	})
	t.Run("a state that is not one cannot be written down", func(t *testing.T) {
		x := require.New(t)

		_, err := journal.State(99).MarshalText()
		x.ErrorContains(err, "99")
	})
	t.Run("the word is read back as the state", func(t *testing.T) {
		x := require.New(t)

		var s journal.State
		x.NoError(json.Unmarshal([]byte(`"quarantined"`), &s))
		x.Equal(journal.Quarantined, s)
	})
	t.Run("a word that is not a state is refused", func(t *testing.T) {
		x := require.New(t)

		var s journal.State
		x.ErrorContains(json.Unmarshal([]byte(`"nope"`), &s), "nope")
	})
}

func TestRecord(t *testing.T) {
	modAt := time.Date(2026, 9, 12, 3, 4, 5, 0, time.UTC)
	r := journal.Record{
		Size:    1024,
		ModAt:   modAt,
		Carried: map[string]journal.Carry{"cloud": {}},
	}

	t.Run("a record knows which sinks have answered", func(t *testing.T) {
		x := require.New(t)

		x.True(r.IsCarriedTo("cloud"))
		x.False(r.IsCarriedTo("onprem"))
		x.False(journal.Record{}.IsCarriedTo("cloud"))
	})

	// A file written again under the same name is a different file, and the
	// record about the old one says nothing true about it.
	t.Run("the same size at the same time is the same file", func(t *testing.T) {
		x := require.New(t)

		x.True(r.SameAs(1024, modAt))
		// The same instant said in another zone is the same instant.
		x.True(r.SameAs(1024, modAt.In(time.FixedZone("KST", 9*60*60))))
	})
	t.Run("a different size or a different time is not", func(t *testing.T) {
		x := require.New(t)

		x.False(r.SameAs(1025, modAt))
		x.False(r.SameAs(1024, modAt.Add(time.Second)))
	})
}
