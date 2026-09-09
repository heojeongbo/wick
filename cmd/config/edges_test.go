package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd/config"
	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/source"
)

func TestReadingTheFile(t *testing.T) {
	t.Run("one that is not there is said so", func(t *testing.T) {
		x := require.New(t)

		_, err := config.ReadFromFile(filepath.Join(t.TempDir(), "nope.yaml"))
		x.ErrorContains(err, "open")
		x.ErrorIs(err, os.ErrNotExist)
	})

	// Without ":-default" the variable is required: one that is not set is an
	// error rather than an empty string, so a missing secret is noticed at
	// startup instead of at the first request.
	t.Run("a secret that is named and not set is refused", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "wick.yaml")
		x.NoError(os.WriteFile(p, []byte("identity:\n  name: \"${env:WICK_TEST_NOT_SET}\"\n"), 0o600))

		_, err := config.ReadFromFile(p)
		x.ErrorContains(err, "expand")
	})

	t.Run("one that is named and set is used", func(t *testing.T) {
		x := require.New(t)

		t.Setenv("WICK_TEST_SET", "from-the-environment")

		p := filepath.Join(t.TempDir(), "wick.yaml")
		x.NoError(os.WriteFile(p, []byte("identity:\n  name: \"${env:WICK_TEST_SET}\"\n"), 0o600))

		c, err := config.ReadFromFile(p)
		x.NoError(err)
		x.Equal("from-the-environment", c.Identity.Name)
		x.Equal(p, c.Path())
	})

	t.Run("something that is not YAML at all is said so", func(t *testing.T) {
		x := require.New(t)

		p := filepath.Join(t.TempDir(), "wick.yaml")
		x.NoError(os.WriteFile(p, []byte("identity: [this is not\n"), 0o600))

		_, err := config.ReadFromFile(p)
		x.ErrorContains(err, "decode")
	})
}

// A machine whose hostname cannot be read can still carry files perfectly well;
// the name is only how they are told apart.
func TestAMachineWithNoName(t *testing.T) {
	x := require.New(t)

	c := &config.Config{}
	x.NoError(c.Evaluate())
	x.NotEmpty(c.Identity.Name)
}

func TestMoving(t *testing.T) {
	x := require.New(t)

	c := config.RetainConfig{After: "move", MoveTo: "done"}
	p, err := c.Policy()
	x.NoError(err)
	x.Contains(fmt.Sprint(p), `move to "done"`)

	// And with a window in front of it.
	c.Grace = time.Hour
	p, err = c.Policy()
	x.NoError(err)
	x.Contains(fmt.Sprint(p), "after 1h0m0s")
}

// What a sink says about itself has to survive being written out, and a sink
// whose settings cannot be written is a sink `wick config` has to say so about
// rather than print half of.
func TestSettingsThatCannotBeWrittenOut(t *testing.T) {
	x := require.New(t)

	_, err := config.SinkConfig{Type: "unwritable", Spec: &unwritable{}}.MarshalYAML()
	x.Error(err)

	_, err = config.SourceConfig{Type: "unwritable", Spec: &unwritableSource{}}.MarshalYAML()
	x.Error(err)
}

type unwritable struct {
	sink.Typed `yaml:",inline"`
}

func (*unwritable) New(context.Context) (sink.Sink, error) { return nil, errRefused }
func (*unwritable) MarshalYAML() ([]byte, error)           { return nil, errRefused }

type unwritableSource struct {
	source.Typed `yaml:",inline"`
}

func (*unwritableSource) New(context.Context) (source.Source, error) { return nil, errRefused }
func (*unwritableSource) MarshalYAML() ([]byte, error)               { return nil, errRefused }
