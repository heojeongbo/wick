package dir

import (
	"context"

	"github.com/heojeongbo/wick/source"
)

// Kind is what a configuration calls this one.
const Kind = "dir"

func init() { source.Register(Kind, func() source.Spec { return &Spec{} }) }

// A Spec is what a configuration says about a directory source.
//
// The tags are the names the file uses, and they are also what the environment
// variables are made from, so a field added here answers to one without
// anything being written anywhere else.
type Spec struct {
	// Path is the directory to look in.
	Path string `yaml:"path"`

	// Include is what to take; everything, when it is not said. A pattern with
	// a "/" in it is about the whole key, one without is about the file's own
	// name.
	Include []string `yaml:"include"`
	// Exclude is what to leave, and wins over Include.
	Exclude []string `yaml:"exclude"`

	// Recursive says whether to look inside directories.
	Recursive bool `yaml:"recursive"`

	// MinSize leaves anything smaller, in bytes. A file of no length is
	// usually one that has been created and not written yet.
	MinSize int64 `yaml:"min_size"`
}

func (s *Spec) New(ctx context.Context) (source.Source, error) {
	return New(Options{
		Path:      s.Path,
		Include:   s.Include,
		Exclude:   s.Exclude,
		Recursive: s.Recursive,
		MinSize:   s.MinSize,
	})
}

var _ source.Spec = (*Spec)(nil)
