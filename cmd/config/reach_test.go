package config_test

import (
	"context"
	"io"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd/config"
	"github.com/heojeongbo/wick/sink"
	sinkmem "github.com/heojeongbo/wick/sink/mem"
)

// refusing is a sink that is there and will not answer, which is what a bucket
// somebody's credentials cannot see looks like.
type refusing struct{ err error }

func (refusing) Put(context.Context, string, io.Reader, sink.Meta) error { return nil }
func (r refusing) Stat(context.Context, string) (sink.Meta, error)       { return sink.Meta{}, r.err }

// deaf cannot be asked what it holds at all.
type deaf struct{}

func (deaf) Put(context.Context, string, io.Reader, sink.Meta) error { return nil }

func TestReach(t *testing.T) {
	t.Run("one that answers", func(t *testing.T) {
		x := require.New(t)

		w := &config.Wick{Sinks: map[string]sink.Sink{"cloud": sinkmem.New()}}

		reached, err := w.Reach(t.Context())
		x.NoError(err)
		x.Equal([]string{"cloud"}, reached)
	})

	t.Run("one that says it does not hold it, which is an answer", func(t *testing.T) {
		x := require.New(t)

		w := &config.Wick{Sinks: map[string]sink.Sink{
			"cloud": refusing{err: fs.ErrNotExist},
		}}

		reached, err := w.Reach(t.Context())
		x.NoError(err, "not holding a name is how a sink says it is there")
		x.Equal([]string{"cloud"}, reached)
	})

	t.Run("one that cannot say", func(t *testing.T) {
		x := require.New(t)

		w := &config.Wick{Sinks: map[string]sink.Sink{
			"cloud":  refusing{err: errRefused},
			"onsite": sinkmem.New(),
		}}

		reached, err := w.Reach(t.Context())
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, `the sink "cloud"`)
		x.Equal([]string{"onsite"}, reached, "the one that answered is still counted")
	})

	t.Run("one that cannot be asked is not counted as reached", func(t *testing.T) {
		x := require.New(t)

		w := &config.Wick{Sinks: map[string]sink.Sink{"cloud": deaf{}}}

		reached, err := w.Reach(t.Context())
		x.NoError(err, "not being askable is not a failure; it was still built")
		x.Empty(reached, "it claimed to have checked something it did not")
	})

	t.Run("nothing to reach", func(t *testing.T) {
		x := require.New(t)

		reached, err := (&config.Wick{}).Reach(t.Context())
		x.NoError(err)
		x.Empty(reached)
	})
}
