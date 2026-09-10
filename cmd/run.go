package cmd

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
)

func NewCmdRun(global func() flg.Flags) *xli.Command {
	return &xli.Command{
		Name:  "run",
		Brief: "watch, and carry what turns up",
		Synop: synop(`The daemon. It does not return until it is told to stop.

It carries once at startup -- a machine that has just come up holding a week of
recordings should not sit on them for an interval first -- and after that it
asks each spool's trigger before every pass. Waking up is a scan; carrying is
the link this machine shares with whatever it is really for, so the trigger is
what decides.

SIGTERM or the first interrupt means "finish what you are doing", and a carry
most of the way through half a gigabyte is worth finishing. A second one means
now, and costs the bytes in flight and never a file: a carry that is cut off
has written no record, so the next pass sends it again.

With ` + "`health.endpoint`" + ` set it also answers /healthz and /readyz. Liveness stays
up while the link is down, because the link being down is what this exists for
and a probe that failed on it would restart the whole fleet mid-outage.
Readiness fails only when the journal cannot be reached.`),

		Flags: global(),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			c := use_config.Must(ctx)

			w, err := c.Build(ctx)
			if err != nil {
				return err
			}
			defer w.Close()

			_, stopHealth, err := serveHealth(ctx, c.Health.Endpoint, w.Journal)
			if err != nil {
				return err
			}
			defer stopHealth()

			return run(ctx, w.Group.Run)
		}),
	}
}

// run carries until it is asked to stop, and then stops.
//
// # Why the second signal is not ignored
//
// The first one means "finish what you are doing". A carry that is most of the
// way through half a gigabyte is worth finishing, and on a link like these that
// can be minutes.
//
// The second means "now". Whoever sent it has decided that waiting costs more
// than the carry does, and they are in a position to know; the alternative is a
// process that has to be killed, which is the same outcome without the log line
// saying it was asked for.
func run(ctx context.Context, serve func(context.Context) error) error {
	l := log.From(ctx)

	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	done := make(chan error, 1)
	go func() { done <- serve(ctx) }()

	select {
	case err := <-done:
		return z.ErrIf(err, "carry")

	case <-ctx.Done():
		return nil

	case s := <-sig:
		l.Info("stopping", slog.String("signal", s.String()))
	}

	stop()

	select {
	case err := <-done:
		return z.ErrIf(err, "carry")

	case s := <-sig:
		l.Warn("stopping now, leaving whatever is in flight unfinished",
			slog.String("signal", s.String()),
		)

		// Nothing here is lost by it. A carry that is cut off has written no
		// record, so the next pass sends it again; the cost is the bytes and
		// not the file.
		return nil
	}
}
