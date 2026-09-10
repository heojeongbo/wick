package spool

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/z"

	"github.com/heojeongbo/wick/source"
	"github.com/heojeongbo/wick/trigger"
)

// idle is how long the loop waits when nothing has anything to say about when
// to look again.
//
// It exists because a spool whose only trigger answers to the source -- a count,
// a size -- would otherwise sleep until something nudged it, and the thing that
// nudges it is a watcher that is allowed to fail quietly. This is the floor
// under that: a machine whose watcher has given up still carries its files, an
// hour later than it would have.
const idle = time.Minute

// Run carries until ctx is done.
//
// It does one pass immediately -- a daemon that has just started on a machine
// holding a week of files should not wait for the interval before saying so --
// and after that it asks the trigger, every time, before carrying anything.
//
// # What waking up and carrying are, and why they are not the same thing
//
// The loop wakes on whichever comes first of [idle], a nudge from the watcher,
// and however long the trigger said it wanted. Waking is cheap: it is a scan
// and a question. Carrying is not; it is the link this machine shares with
// whatever it is really for.
//
// So [trigger.Trigger.After] says when to *look* and
// [trigger.Trigger.Fire] says whether to *go*, and the second one is the one
// that decides. Only [Spool.Once] skips the question, because a caller who
// reached for it has already answered it.
//
// The first pass is not put to the trigger. Every clock trigger measures from a
// start this has only just had, so asking would mean a daemon that comes up
// holding a week of recordings sits on them for a full interval -- and the
// machine it came up on is one nobody is watching.
func (s *Spool) Run(ctx context.Context) error {
	l := log.From(ctx).With(slog.String("spool", s.name))

	// The watcher only ever nudges. What it says is not believed; it only
	// makes the next scan sooner. One that cannot be started, or that gives up
	// later, leaves the loop running on its own clock.
	if w, ok := s.src.(source.Watcher); ok {
		c, err := w.Watch(ctx)
		if err != nil {
			l.Warn("nothing will say when something appears, so this waits for the clock",
				slog.String("error", err.Error()),
			)
		} else {
			go func() {
				for range c {
					s.Nudge()
				}
			}()
		}
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	asked := false
	for {
		select {
		case <-ctx.Done():
			return nil

		case <-s.wake:
		case <-timer.C:
		}

		st := s.state(ctx)
		if asked && !s.trig.Fire(st) {
			// Woken, looked, and told no. That costs a scan and nothing on the
			// link, which is the whole arrangement.
			reset(timer, s.wait(st))

			continue
		}
		asked = true

		began := s.now()
		r, err := s.Once(ctx)

		// Read again, because the pass is what changed it, and before the
		// error is looked at: a pass cut short by the context still did
		// whatever it did, and the numbers for it are the last ones there will
		// be.
		st = s.state(ctx)
		s.record(ctx, r, s.now().Sub(began), st)

		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			// A pass that could not be made at all is not a reason to stop.
			// The link comes back, the disk is remounted, and the next pass is
			// the one that works.
			l.Warn("the pass could not be made", slog.String("error", err.Error()))
		}
		s.say(ctx, r)

		reset(timer, s.wait(st))
	}
}

// wait is how long until the next look, and is never zero: a loop that waits
// for no time is a loop that scans continuously.
//
// It is capped at [idle] even when the trigger asks for longer, because a
// trigger that answers to the source rather than to the clock cannot say when
// it will become true -- and the answer is now cheap, since looking is not
// carrying.
func (s *Spool) wait(st trigger.State) time.Duration {
	d := s.trig.After(st)
	if d <= 0 {
		return idle
	}

	return min(d, idle)
}

// reset stops the timer and starts it again, draining it if it had already
// fired -- which is what keeps a pass that took longer than the interval from
// being followed immediately by another.
func reset(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// say puts one line in the log per pass that did something.
//
// A pass that found nothing says nothing: on a quiet machine that is one line a
// minute forever, and a log nobody can read is a log nobody reads.
func (s *Spool) say(ctx context.Context, r Report) {
	l := log.From(ctx).With(slog.String("spool", s.name))

	if err := r.Err(); err != nil {
		l.Warn("some of the pass did not happen", slog.String("error", err.Error()))
	}
	if r.Carried == 0 && r.Retired == 0 && r.SetAside == 0 {
		return
	}

	l.Info("carried",
		slog.Int("carried", r.Carried),
		slog.Int64("bytes", r.Bytes),
		slog.Int("retired", r.Retired),
		slog.Int("set_aside", r.SetAside),
	)
}

// A Group runs several spools at once.
//
// They are independent: one that cannot reach its sink does not stop the others,
// and the group only ends when the context does. That is the shape a machine
// wants -- the recordings going to the cloud and the logs going to the server
// in the building have nothing to say to each other, and an outage in one is
// not a reason to stop the other.
type Group struct {
	spools []*Spool
}

func NewGroup(spools ...*Spool) *Group {
	return &Group{spools: spools}
}

// Run runs all of them until ctx is done.
func (g *Group) Run(ctx context.Context) error {
	eg, ctx := errgroup.WithContext(ctx)
	for _, s := range g.spools {
		eg.Go(func() error { return z.ErrIf(s.Run(ctx), "the spool %q", s.name) })
	}

	return eg.Wait()
}

// Once runs one pass of each, one after another, and answers with what each of
// them did.
//
// One after another rather than at once, because the thing they are usually
// sharing is the link, and two spools each sending half a gigabyte down it is
// slower than either of them doing it alone.
func (g *Group) Once(ctx context.Context) (map[string]Report, error) {
	rs := make(map[string]Report, len(g.spools))

	var errs []error
	for _, s := range g.spools {
		r, err := s.Once(ctx)
		rs[s.name] = r

		if err != nil {
			errs = append(errs, z.Err(err, "the spool %q", s.name))
		}
		if err := r.Err(); err != nil {
			errs = append(errs, z.Err(err, "the spool %q", s.name))
		}
		if ctx.Err() != nil {
			break
		}
	}

	return rs, errors.Join(errs...)
}

// Spools is what the group is holding, so that a command can ask each of them
// about itself.
func (g *Group) Spools() []*Spool { return g.spools }
