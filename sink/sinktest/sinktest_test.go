package sinktest_test

import (
	"context"
	"io"
	"testing"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/sink/sinktest"
)

// bare is the least a sink can be: it writes, and it cannot be asked what it
// holds. Running the suite against one is what shows that the parts of the
// contract about reading back are genuinely optional rather than a promise the
// suite quietly requires.
type bare struct{}

func (bare) Put(ctx context.Context, name string, r io.Reader, want sink.Meta) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := io.Copy(io.Discard, r)

	return err
}

func TestABareSink(t *testing.T) {
	sinktest.Suite(t, func(t *testing.T) sink.Sink { return bare{} })
}

var _ sink.Sink = bare{}
