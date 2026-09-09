// Package sink is where things go.
//
// # Why Put takes what it should expect
//
// A sink is handed the size and the hash the caller believes it is sending, and
// a sink that can check does. That is not belt and braces: a stream that ends
// early looks exactly like a stream that ended, and the only party that can
// tell the difference is the one that knows how long it was supposed to be.
//
// # Why reading it back is a separate thing
//
// [Stater] is optional, and the engine will not record a carry without one when
// it has been asked to verify. That is deliberate. Some stores answer a write
// with a success before the write is durable, and at least one answers it with
// framing so malformed that the client reports a failure for a write that
// worked. The only claim worth recording is the one made by asking afterwards.
package sink

import (
	"context"
	"io"

	"github.com/heojeongbo/wick/internal/registry"
)

// Meta is what is known about an object without reading it.
type Meta struct {
	// Size is in bytes, and is negative when it is not known.
	Size int64
	// Digest is "sha256:<hex>", or empty when the sink keeps none.
	Digest string
}

// A Sink is where things go.
type Sink interface {
	// Put writes the whole of r under name, replacing whatever was there.
	//
	// Replacing rather than refusing is the right way round for this: a name
	// that is already taken is, in every case this is built for, the same
	// bytes arriving a second time because the first attempt died between the
	// write and the record of it.
	Put(ctx context.Context, name string, r io.Reader, want Meta) error
}

// A Stater is a Sink that can be asked what it holds.
//
// A sink that is not one cannot be verified against, and a spool asked to
// verify against one says so at startup rather than at the first carry.
type Stater interface {
	// Stat is what the sink holds under name. A name it does not hold is
	// [fs.ErrNotExist], so that "not there" is told from "cannot say".
	Stat(ctx context.Context, name string) (Meta, error)
}

// A Closer is a Sink holding something worth letting go of -- a connection
// pool, an open file. The engine closes what it made, and does not close what
// it was handed.
type Closer interface {
	Close() error
}

// A Spec is a sink's settings before they are a sink.
type Spec interface {
	New(ctx context.Context) (Sink, error)
}

var kinds = registry.New[Spec]("sink")

// Register makes a kind of sink available under a name, and is called from the
// init of the package that implements it -- so importing that package is what
// makes the kind available, and a build that does not want the cloud SDK simply
// does not import the sink that needs it.
func Register(kind string, f func() Spec) { kinds.Register(kind, f) }

// Kinds is every registered name, sorted, so that an error message can say what
// could have been meant instead.
func Kinds() []string { return kinds.Kinds() }

// NewSpec is an empty spec of the named kind, for a decoder to read into.
func NewSpec(kind string) (Spec, error) { return kinds.New(kind) }
