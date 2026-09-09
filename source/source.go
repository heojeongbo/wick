// Package source is a place things accumulate.
//
// # Why the interface is three methods and not eight
//
// A source has to be scannable and readable, and that is all that is required
// to carry from one. Everything else -- deleting, moving, saying when something
// changed -- is a capability an implementation may or may not have, declared by
// implementing [Remover], [Mover] or [Watcher] and found by asking. A directory
// on a read-only mount is a perfectly good source; so is an HTTP endpoint that
// lists things and hands them over but has no notion of deleting one. Folding
// those into one interface would make every such implementation carry methods
// that return "no", and would make the engine unable to tell "cannot" from
// "would not".
package source

import (
	"context"
	"io"
	"iter"
	"time"

	"github.com/heojeongbo/wick/internal/registry"
)

// An Item is one thing found in a source.
//
// It is a value and not a handle: everything the engine decides with is in
// here, so deciding does not mean going back to the source to ask, and a
// decision taken about an item is a decision about the item as it was seen.
type Item struct {
	// Key is the identity within the source, and is a relative path with
	// forward slashes for a source that has directories in it.
	Key string
	// Size is in bytes.
	Size int64
	// ModAt is when it was last written.
	ModAt time.Time
}

// A Source is a place things accumulate.
type Source interface {
	// Scan yields what the source holds now.
	//
	// It is a snapshot: something that appears while it runs may or may not be
	// in it, and the next scan will have it either way. An error yielded
	// against a zero Item is about the scan itself and ends it; an error
	// yielded against an Item is about that one thing, and the scan goes on.
	Scan(ctx context.Context) iter.Seq2[Item, error]

	// Open reads one item. A key that is no longer there is [fs.ErrNotExist],
	// which the engine reads as "it went away" rather than as a failure to
	// retry, because retrying is not going to bring it back.
	Open(ctx context.Context, key string) (io.ReadCloser, error)
}

// A Remover is a Source something can be deleted from.
//
// A source that is not one is a source nothing is ever deleted from, and a
// retention policy that would have deleted says so at startup instead of at the
// first carry.
type Remover interface {
	Remove(ctx context.Context, key string) error
}

// A Mover is a Source that can put something somewhere else within itself.
type Mover interface {
	// Move renames key to be under dir, keeping its base name. A destination
	// that already holds a name is overwritten, because the alternative is a
	// file that is neither moved nor carried and that nothing will look at
	// again.
	Move(ctx context.Context, key string, dir string) error
}

// A Watcher is a Source that can say when something has changed.
//
// Nothing is promised. An event may be lost, may arrive twice, or may arrive
// for something that is no longer there. The scan is what is true; this only
// makes it sooner, which on a link that is billed by the hour is the difference
// between carrying at once and carrying at the next tick.
type Watcher interface {
	// Watch closes the channel it returns when ctx is done. A send on it means
	// "look again"; it carries nothing, because anything it carried would be a
	// thing the caller might believe instead of scanning.
	Watch(ctx context.Context) (<-chan struct{}, error)
}

// A Spec is a source's settings before they are a source.
//
// It is the shape a configuration decodes into, so that `type: dir` in a file
// can become something with a Path in it without this package having to know
// what a Path is.
type Spec interface {
	// New makes the source the spec describes.
	New(ctx context.Context) (Source, error)
}

// Typed is embedded, inline, by every spec, so that the `type:` which chose it
// is a field the spec knows about. See [github.com/heojeongbo/wick/sink.Typed]
// for why that matters.
type Typed struct {
	Type string `yaml:"type"`
}

var kinds = registry.New[Spec]("source")

// Register makes a kind of source available under a name, and is called from
// the init of the package that implements it. Importing that package is
// therefore what makes the kind available, which is what lets somebody add one
// without this package being changed.
func Register(kind string, f func() Spec) { kinds.Register(kind, f) }

// Kinds is every registered name, sorted, so that an error message can say what
// could have been meant instead.
func Kinds() []string { return kinds.Kinds() }

// NewSpec is an empty spec of the named kind, for a decoder to read into.
func NewSpec(kind string) (Spec, error) { return kinds.New(kind) }
