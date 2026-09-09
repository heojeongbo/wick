package source_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/source"
	_ "github.com/heojeongbo/wick/source/dir"
)

type spec struct {
	source.Typed `yaml:",inline"`

	kind string
}

func (s *spec) New(ctx context.Context) (source.Source, error) { return nil, nil } //nolint:nilnil // nothing to make

func TestRegistry(t *testing.T) {
	t.Run("importing the package that implements a kind is what makes it available", func(t *testing.T) {
		x := require.New(t)

		// dir is imported for its side effect above and for no other reason.
		x.Contains(source.Kinds(), "dir")

		s, err := source.NewSpec("dir")
		x.NoError(err)
		x.NotNil(s)
	})
	t.Run("a kind nothing answers to says what could have been meant", func(t *testing.T) {
		x := require.New(t)

		_, err := source.NewSpec("nope")
		x.ErrorContains(err, `"nope"`)
		x.ErrorContains(err, "source")
		x.ErrorContains(err, "dir")
	})
	t.Run("one that is registered is one that is made", func(t *testing.T) {
		x := require.New(t)

		source.Register("test-kind", func() source.Spec { return &spec{kind: "test-kind"} })

		s, err := source.NewSpec("test-kind")
		x.NoError(err)
		x.Equal("test-kind", s.(*spec).kind)
		x.Contains(source.Kinds(), "test-kind")
	})

	// Which of them a configuration meant would otherwise depend on link order.
	t.Run("two things under one name is not a state to run in", func(t *testing.T) {
		x := require.New(t)

		x.PanicsWithValue(`source: two kinds are called "dir"`, func() {
			source.Register("dir", func() source.Spec { return &spec{} })
		})
	})
}
