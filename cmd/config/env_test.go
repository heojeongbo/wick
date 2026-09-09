package config_test

import (
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd/config"
)

// The tests below are about the reading, not about what this app happens to be
// configured with, so they use a struct of their own. It holds every shape a
// configuration is made of, and it does not change when the app does.

type Root struct {
	Name string `yaml:"name"`
	Note *string

	// A value that is either given or not, which is how a switch that is on
	// unless it is turned off is spelled.
	On    *bool `yaml:"on"`
	Level *int  `yaml:"level"`

	Nested Nested `yaml:"nested"`
	Absent *Nested

	Shared `yaml:",inline"`

	Whole Whole `yaml:"whole"`

	Renamed string `yaml:"other_name"`
	Hidden  string `yaml:"-"`
	secret  string //nolint:unused // it is not read, which is the point.
}

type Nested struct {
	Count   int  `yaml:"count"`
	Enabled bool `yaml:"enabled"`

	Deep struct {
		Values []string `yaml:"values"`
	} `yaml:"deep"`
}

type Shared struct {
	Inlined string `yaml:"inlined"`
}

// Whole reads itself, so it is read as one value rather than walked into.
type Whole struct {
	Raw string
}

func (w *Whole) UnmarshalYAML(b []byte) error {
	w.Raw = string(b)
	return nil
}

func TestEnvNames(t *testing.T) {
	t.Run("a name for every field a value fits in", func(t *testing.T) {
		x := require.New(t)

		x.Equal([]string{
			"WICK_NAME",
			"WICK_NOTE",
			"WICK_ON",
			"WICK_LEVEL",
			"WICK_NESTED_COUNT",
			"WICK_NESTED_ENABLED",
			"WICK_NESTED_DEEP_VALUES",
			"WICK_ABSENT_COUNT",
			"WICK_ABSENT_ENABLED",
			"WICK_ABSENT_DEEP_VALUES",
			// Inlined, so it is named as if it were written here.
			"WICK_INLINED",
			// Reads itself, so it is one name rather than many.
			"WICK_WHOLE",
			"WICK_OTHER_NAME",
		}, config.EnvNames(&Root{}))
	})
	t.Run("nothing for what is not a struct", func(t *testing.T) {
		x := require.New(t)

		x.Empty(config.EnvNames(Root{}))
		x.Empty(config.EnvNames(nil))
		x.Empty(config.EnvNames((*Root)(nil)))
	})

	// The one thing said about this app: the reading is wired to its
	// configuration, and to nothing it keeps to itself.
	t.Run("the configuration of the app is read", func(t *testing.T) {
		x := require.New(t)

		vs := config.EnvNames(&config.Config{})
		x.Contains(vs, "WICK_IDENTITY_NAME")
		x.NotContains(vs, "WICK_PATH")
	})
}

func TestOverrideFromEnv(t *testing.T) {
	t.Run("a value is read into the field it is named after", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := config.OverrideFromEnv(&v, []string{
			"WICK_NAME=alice",
			"WICK_NESTED_COUNT=12",
			"WICK_NESTED_ENABLED=true",
			"WICK_NESTED_DEEP_VALUES=[a, b]",
			"WICK_INLINED=here",
			"WICK_OTHER_NAME=renamed",
		})
		x.NoError(err)
		x.Empty(unknown)

		x.Equal("alice", v.Name)
		x.Equal(12, v.Nested.Count)
		x.True(v.Nested.Enabled)
		x.Equal([]string{"a", "b"}, v.Nested.Deep.Values)
		x.Equal("here", v.Inlined)
		x.Equal("renamed", v.Renamed)
	})
	t.Run("a string keeps its punctuation", func(t *testing.T) {
		x := require.New(t)

		// None of this is YAML; it is a data source name and a greeting.
		const (
			dsn    = "postgres://u:p@h:5432/db?sslmode=disable"
			format = "{Hello}, %s! # and a hash"
		)

		var v Root
		_, err := config.OverrideFromEnv(&v, []string{
			"WICK_NAME=" + dsn,
			"WICK_OTHER_NAME=" + format,
		})
		x.NoError(err)
		x.Equal(dsn, v.Name)
		x.Equal(format, v.Renamed)
	})
	t.Run("a value is made where there was none", func(t *testing.T) {
		x := require.New(t)

		var v Root
		_, err := config.OverrideFromEnv(&v, []string{
			"WICK_NOTE=a note",
			"WICK_ON=false",
			"WICK_LEVEL=3",
			"WICK_ABSENT_COUNT=3",
		})
		x.NoError(err)

		x.NotNil(v.Note)
		x.Equal("a note", *v.Note)
		// Not a string, so it is read rather than taken; the decoder cannot do
		// this one on its own.
		x.NotNil(v.On)
		x.False(*v.On)
		x.NotNil(v.Level)
		x.Equal(3, *v.Level)
		x.NotNil(v.Absent)
		x.Equal(3, v.Absent.Count)
	})
	t.Run("nothing is made where nothing was said", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := config.OverrideFromEnv(&v, []string{"WICK_NAME=alice"})
		x.NoError(err)
		x.Empty(unknown)

		x.Nil(v.Note)
		x.Nil(v.On)
		x.Nil(v.Level)
		x.Nil(v.Absent)
	})
	t.Run("a value that reads itself is read as one", func(t *testing.T) {
		x := require.New(t)

		var v Root
		_, err := config.OverrideFromEnv(&v, []string{"WICK_WHOLE=raw: yes"})
		x.NoError(err)
		x.Equal("raw: yes", v.Whole.Raw)
	})
	t.Run("what was already there is left alone", func(t *testing.T) {
		x := require.New(t)

		v := Root{Name: "alice", Renamed: "kept"}
		_, err := config.OverrideFromEnv(&v, []string{"WICK_NAME=bob"})
		x.NoError(err)

		x.Equal("bob", v.Name)
		x.Equal("kept", v.Renamed)
	})
	t.Run("what is not read from is not named", func(t *testing.T) {
		x := require.New(t)

		v := Root{}
		unknown, err := config.OverrideFromEnv(&v, []string{
			"WICK_HIDDEN=no",
			"WICK_SECRET=no",
		})
		x.NoError(err)
		x.Equal([]string{"WICK_HIDDEN", "WICK_SECRET"}, unknown)
		x.Empty(v.Hidden)
	})
	t.Run("a name nothing answers to is reported", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := config.OverrideFromEnv(&v, []string{
			"WICK_NAM=alice",   // name, not nam
			"WICK_VERSION=1.0", // the build puts this one here
			"PATH=/usr/bin",    // not ours to look at
			"WICK_NAME=alice",
		})
		x.NoError(err)
		x.Equal([]string{"WICK_NAM", "WICK_VERSION"}, unknown)
		x.Equal("alice", v.Name)
	})
	t.Run("a value that does not fit is refused", func(t *testing.T) {
		x := require.New(t)

		var v Root
		_, err := config.OverrideFromEnv(&v, []string{"WICK_NESTED_COUNT=lots"})
		x.ErrorContains(err, "WICK_NESTED_COUNT")
	})
	t.Run("nothing to read into is refused", func(t *testing.T) {
		x := require.New(t)

		_, err := config.OverrideFromEnv(Root{}, []string{"WICK_NAME=alice"})
		x.ErrorContains(err, "pointer to a struct")
	})
	t.Run("nothing is read when nothing is said", func(t *testing.T) {
		x := require.New(t)

		var v Root
		unknown, err := config.OverrideFromEnv(&v, []string{"PATH=/usr/bin"})
		x.NoError(err)
		x.Empty(unknown)
		x.Equal(Root{}, v)
	})
}

// The defaults must not undo what the environment said, which is what keeps
// the order file, then environment, then flags.
func TestOverrideFromEnvThenEvaluate(t *testing.T) {
	x := require.New(t)

	var c config.Config
	_, err := config.OverrideFromEnv(&c, []string{"WICK_IDENTITY_NAME=alice"})
	x.NoError(err)
	x.NoError(c.Evaluate())

	// Not the hostname: the environment said, so the fallback has nothing to do.
	x.Equal("alice", c.Identity.Name)
}

var _ yaml.BytesUnmarshaler = (*Whole)(nil)
