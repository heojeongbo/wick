package sink_test

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/sink"
	_ "github.com/heojeongbo/wick/sink/dir"
	_ "github.com/heojeongbo/wick/sink/http"
	_ "github.com/heojeongbo/wick/sink/s3"
)

type spec struct {
	sink.Typed `yaml:",inline"`

	kind string
}

func (s *spec) New(ctx context.Context) (sink.Sink, error) { return nil, nil } //nolint:nilnil // nothing to make

func TestRegistry(t *testing.T) {
	// Importing the package that implements a kind is what makes it available;
	// the three above are imported for that and for nothing else. It is also
	// what lets a build that does not want the cloud SDK leave the s3 one out.
	t.Run("every kind this build was given can be made", func(t *testing.T) {
		x := require.New(t)

		x.Equal([]string{"dir", "http", "s3"}, sink.Kinds())

		for _, kind := range sink.Kinds() {
			s, err := sink.NewSpec(kind)
			x.NoError(err)
			x.NotNil(s)
		}
	})
	t.Run("a kind nothing answers to says what could have been meant", func(t *testing.T) {
		x := require.New(t)

		_, err := sink.NewSpec("nope")
		x.ErrorContains(err, `"nope"`)
		x.ErrorContains(err, "sink")
		x.ErrorContains(err, "s3")
	})
	t.Run("one that is registered is one that is made", func(t *testing.T) {
		x := require.New(t)

		sink.Register("test-kind", func() sink.Spec { return &spec{kind: "test-kind"} })

		s, err := sink.NewSpec("test-kind")
		x.NoError(err)
		x.Equal("test-kind", s.(*spec).kind)
	})

	// Which of them a configuration meant would otherwise depend on link order.
	t.Run("two things under one name is not a state to run in", func(t *testing.T) {
		x := require.New(t)

		x.PanicsWithValue(`sink: two kinds are called "s3"`, func() {
			sink.Register("s3", func() sink.Spec { return &spec{} })
		})
	})
}

// A sink holding something worth letting go of says so by having a Close, and
// the engine closes what it made rather than what it was handed.
func TestCloser(t *testing.T) {
	x := require.New(t)

	var s sink.Sink = closable{}
	c, ok := s.(sink.Closer)
	x.True(ok)
	x.NoError(c.Close())
}

type closable struct{}

func (closable) Put(context.Context, string, io.Reader, sink.Meta) error { return nil }
func (closable) Close() error                                            { return nil }
