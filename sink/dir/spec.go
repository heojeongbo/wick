package dir

import (
	"context"

	"github.com/heojeongbo/wick/sink"
)

// Kind is what a configuration calls this one.
const Kind = "dir"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about a directory sink.
type Spec struct {
	// Path is the directory to write into.
	Path string `yaml:"path"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(Options{Path: s.Path})
}

var _ sink.Spec = (*Spec)(nil)
