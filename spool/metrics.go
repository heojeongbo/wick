package spool

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/lesomnus/otx"

	"github.com/heojeongbo/wick/trigger"
)

// The instruments, and what each one is for.
//
// # The one that matters
//
// `wick.carry.last_success` is the reason this file exists. Everything else
// here can be worked out from a log, and none of it makes the failure this
// daemon actually has visible: uploads stop, the disk keeps filling, and every
// other signal reads exactly as it does on a machine that simply has nothing to
// carry. A gauge that says how long ago the last carry was is the difference
// between an alert and a phone call three weeks later.
//
// The names are dotted and the units are UCUM, which is what the collector on
// the other end expects.
const (
	mCarried  = "wick.items.carried"
	mBytes    = "wick.bytes"
	mDuration = "wick.carry.duration"
	mPending  = "wick.items.pending"
	mWaiting  = "wick.bytes.pending"
	mSetAside = "wick.items.quarantined"
	mFree     = "wick.disk.free"
	mSince    = "wick.carry.last_success"
)

// record says what a pass did.
//
// It is called with the pass's own numbers rather than reading them back off
// the journal, so that the cost of being observed is a few adds. The state is
// handed in for the same reason: the caller has just read it, and reading it
// again is a second scan of the source for numbers nobody has changed since.
func (s *Spool) record(ctx context.Context, r Report, took time.Duration, st trigger.State) {
	spool := metric.WithAttributes(attribute.String("spool", s.name))

	otx.Int64Counter(ctx, mCarried,
		metric.WithDescription("things carried to every destination they were meant for"),
		metric.WithUnit("{item}"),
	).Add(ctx, int64(r.Carried), spool)

	otx.Int64Counter(ctx, mBytes,
		metric.WithDescription("bytes sent, counted once per destination they were sent to"),
		metric.WithUnit("By"),
	).Add(ctx, r.Bytes, spool)

	otx.Float64Histogram(ctx, mDuration,
		metric.WithDescription("how long a pass took"),
		metric.WithUnit("s"),
	).Record(ctx, took.Seconds(), spool)

	otx.Int64Gauge(ctx, mPending,
		metric.WithDescription("things waiting to be carried"),
		metric.WithUnit("{item}"),
	).Record(ctx, int64(r.Settled-r.Carried), spool)

	otx.Int64Gauge(ctx, mSetAside,
		metric.WithDescription("things set aside after too many attempts"),
		metric.WithUnit("{item}"),
	).Record(ctx, int64(r.SetAside), spool)

	otx.Int64Gauge(ctx, mWaiting,
		metric.WithDescription("bytes waiting to be carried"),
		metric.WithUnit("By"),
	).Record(ctx, st.Bytes, spool)

	if st.FreeBytes > 0 {
		otx.Int64Gauge(ctx, mFree,
			metric.WithDescription("room left where the source is"),
			metric.WithUnit("By"),
		).Record(ctx, int64(st.FreeBytes), spool) //nolint:gosec // a free-space count does not reach the sign bit
	}

	// Seconds since the last carry, and not the instant of it, so that the
	// number means the same thing whatever the machine's clock was set to when
	// it booted -- which on one of these is often nothing at all.
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()

	if !last.IsZero() {
		otx.Float64Gauge(ctx, mSince,
			metric.WithDescription("how long ago something was last carried; the one that makes a daemon which has quietly stopped visible"),
			metric.WithUnit("s"),
		).Record(ctx, s.now().Sub(last).Seconds(), spool)
	}
}
