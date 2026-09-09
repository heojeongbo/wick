package cmd

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/z"
)

func NewCmdRun() *xli.Command {
	return &xli.Command{
		Name:  "run",
		Brief: "watch, and carry what turns up",

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
