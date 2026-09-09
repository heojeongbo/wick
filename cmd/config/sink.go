package config

import (
	"context"
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/heojeongbo/wick/sink"
)

// A SinkConfig is one place things go, named in the configuration so that a
// spool can point at it and two spools can share it.
//
// What it holds depends on what it is: `type: s3` and `type: http` want
// different things said about them. So this reads the type first and hands the
// rest to whatever answers to it -- which is how a sink somebody else wrote
// becomes available by being imported and nothing else.
type SinkConfig struct {
	// Type is the kind of sink; see [sink.Kinds].
	Type string
	// Spec is the rest of it, read by the kind.
	Spec sink.Spec
}

// UnmarshalYAML reads the type and hands the rest over.
//
// It takes the bytes rather than a node because the whole of it has to be read
// twice: once for the type, once by the kind the type names.
func (c *SinkConfig) UnmarshalYAML(b []byte) error {
	var head struct {
		Type string `yaml:"type"`
	}
	if err := yaml.Unmarshal(b, &head); err != nil {
		return fmt.Errorf("read what kind of sink this is: %w", err)
	}
	if head.Type == "" {
		return fmt.Errorf("a sink has to say what kind it is; it is one of %s", strings.Join(sink.Kinds(), ", "))
	}

	spec, err := sink.NewSpec(head.Type)
	if err != nil {
		return err
	}
	if err := Decode(b, spec); err != nil {
		return fmt.Errorf("read the settings of the %s sink: %w", head.Type, err)
	}

	c.Type = head.Type
	c.Spec = spec

	return nil
}

// MarshalYAML writes it back out with the type in front, so that `wick config`
// shows something that could be pasted back into the file.
func (c SinkConfig) MarshalYAML() ([]byte, error) {
	if c.Spec == nil {
		return yaml.Marshal(map[string]string{"type": c.Type})
	}

	// The type is already in there: every spec carries the one that chose it,
	// so that reading is strict without the one key they all get being the
	// first thing refused.
	return yaml.Marshal(c.Spec)
}

// New makes the sink.
func (c SinkConfig) New(ctx context.Context) (sink.Sink, error) {
	return c.Spec.New(ctx)
}

var (
	_ yaml.BytesUnmarshaler = (*SinkConfig)(nil)
	_ yaml.BytesMarshaler   = SinkConfig{}
)
