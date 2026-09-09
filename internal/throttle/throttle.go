// Package throttle slows a read down to a rate.
//
// # Why this exists at all
//
// The machines this is built for are on links that are metered, shared with
// something that matters more, or both. A recording of half a gigabyte sent as
// fast as the link will take it is a recording that takes the link away from
// whatever else was using it, and the daemon has no deadline worth doing that
// for -- it is carrying files nobody is waiting on.
package throttle

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

// Reader is r, read no faster than bytesPerSecond.
//
// A rate of zero or less is no limit, and r is handed straight back: an
// unwritten setting must cost nothing, not a wrapper that does nothing.
func Reader(ctx context.Context, r io.Reader, bytesPerSecond int64) io.Reader {
	if bytesPerSecond <= 0 {
		return r
	}

	// A burst of one second's worth. Smaller would refuse reads larger than
	// itself; larger would let a second's silence pay for a burst that is not
	// the rate that was asked for.
	burst := int(min(bytesPerSecond, int64(maxBurst)))

	return &reader{
		ctx: ctx,
		r:   r,
		lim: rate.NewLimiter(rate.Limit(bytesPerSecond), burst),
	}
}

// maxBurst caps how much a single read may take at once, so that a very high
// rate does not turn into one enormous read that the limiter then has to sleep
// off in one go.
const maxBurst = 1 << 20

type reader struct {
	ctx context.Context
	r   io.Reader
	lim *rate.Limiter
}

func (t *reader) Read(p []byte) (int, error) {
	if len(p) > t.lim.Burst() {
		p = p[:t.lim.Burst()]
	}

	n, err := t.r.Read(p)
	if n <= 0 {
		return n, err
	}

	// Waited for after the read rather than before it, because how much there
	// was to pay for is not known until it has been read. The bytes are
	// answered with either way: they have been taken out of the reader, and
	// dropping them here would lose them.
	if werr := t.lim.WaitN(t.ctx, n); werr != nil && err == nil {
		err = werr
	}

	return n, err
}
