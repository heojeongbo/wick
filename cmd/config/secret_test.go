package config_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd/config"
	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/source"
)

// A spec with one of each: a secret, something that is not, and an embedded
// struct holding another secret.
type held struct {
	Inner `yaml:",inline"`

	Endpoint string `yaml:"endpoint"`
	Password string `yaml:"password" wick:"secret"`
	Empty    string `yaml:"empty" wick:"secret"`
	Count    int    `yaml:"count" wick:"secret"`
}

type Inner struct {
	sink.Typed `yaml:",inline"`

	Token string `yaml:"token" wick:"secret"`
}

func (h *held) New(context.Context) (sink.Sink, error) { return nil, nil }

// A spec that is not a pointer to a struct, which is not something this build
// can make but is something the reflection has to survive.
type odd string

func (odd) New(context.Context) (sink.Sink, error) { return nil, nil }

func TestRedact(t *testing.T) {
	t.Run("covers what is tagged and nothing else", func(t *testing.T) {
		x := require.New(t)

		spec := &held{Endpoint: "https://example.com", Password: "hunter2", Count: 3}
		spec.Token = "abcd"
		spec.Type = "held"

		c := &config.Config{Sinks: map[string]config.SinkConfig{
			"one": {Type: "held", Spec: spec},
		}}

		out := config.Redact(c)
		got := out.Sinks["one"].Spec.(*held)

		x.Equal(config.Redacted, got.Password)
		x.Equal(config.Redacted, got.Token, "an embedded struct's secret was left alone")
		x.Equal("https://example.com", got.Endpoint)
		x.Equal(3, got.Count, "a tag on something that is not a string was acted on")
		x.Empty(got.Empty, "a secret that is not set was made to look set")
	})

	t.Run("leaves the original alone", func(t *testing.T) {
		x := require.New(t)

		spec := &held{Password: "hunter2"}
		c := &config.Config{Sinks: map[string]config.SinkConfig{"one": {Spec: spec}}}

		config.Redact(c)

		x.Equal("hunter2", spec.Password, "a daemon built after this would authenticate with the word for a secret")
	})

	t.Run("a spec it cannot look into is passed through", func(t *testing.T) {
		x := require.New(t)

		c := &config.Config{Sinks: map[string]config.SinkConfig{
			"one": {Spec: odd("whatever")},
			"two": {Spec: nil},
		}}

		out := config.Redact(c)
		x.Equal(odd("whatever"), out.Sinks["one"].Spec)
		x.Nil(out.Sinks["two"].Spec)
	})

	t.Run("nothing to cover", func(t *testing.T) {
		x := require.New(t)

		out := config.Redact(&config.Config{})
		x.Nil(out.Sinks)
		x.Empty(out.Spools)
	})

	t.Run("a source's secrets too", func(t *testing.T) {
		x := require.New(t)

		c := &config.Config{Spools: []config.SpoolConfig{{
			Name:   "a",
			Source: config.SourceConfig{Type: "held", Spec: &heldSource{Password: "hunter2"}},
		}}}

		out := config.Redact(c)
		x.Equal(config.Redacted, out.Spools[0].Source.Spec.(*heldSource).Password)
		x.Equal("hunter2", c.Spools[0].Source.Spec.(*heldSource).Password)
	})
}

type heldSource struct {
	source.Typed `yaml:",inline"`

	Password string `yaml:"password" wick:"secret"`
}

func (h *heldSource) New(context.Context) (source.Source, error) { return nil, nil }

// The whole of it, through the command: what `wick config` prints is what
// somebody pastes into an issue.
func TestConfigDoesNotPrintSecrets(t *testing.T) {
	x := require.New(t)

	c := &config.Config{Sinks: map[string]config.SinkConfig{
		"cloud": {Type: "held", Spec: &held{Password: "hunter2", Endpoint: "https://example.com"}},
	}}

	hidden := config.Redact(c)
	x.NotEqual(c, hidden)

	// And the two say different things about the same field.
	x.Equal("hunter2", c.Sinks["cloud"].Spec.(*held).Password)
	x.True(strings.HasPrefix(config.Redacted, "("))
}

// An unexported field is not something a spec should have, and is something the
// reflection must walk past rather than panic on.
type withPrivate struct {
	sink.Typed `yaml:",inline"`

	secret   string //nolint:unused // it is here to be walked past
	Password string `yaml:"password" wick:"secret"`
}

func (w *withPrivate) New(context.Context) (sink.Sink, error) { return nil, nil }

func TestRedactWalksPastWhatItCannotTouch(t *testing.T) {
	x := require.New(t)

	c := &config.Config{Sinks: map[string]config.SinkConfig{
		"one": {Spec: &withPrivate{Password: "hunter2"}},
	}}

	out := config.Redact(c)
	x.Equal(config.Redacted, out.Sinks["one"].Spec.(*withPrivate).Password)
}
