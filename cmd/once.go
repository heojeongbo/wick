package cmd

import (
	"context"
	"fmt"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
)

func NewCmdOnce(global func() flg.Flags) *xli.Command {
	return &xli.Command{
		Name:  "once",
		Brief: "carry what is due, once, and stop",
		Synop: synop(`One pass, and then it exits. This is what a cron entry or a systemd timer
calls, and what somebody standing at the machine runs.

It does not consult the trigger. Whoever ran this has already decided that now
is the time, and a "once" that answered "not yet" would do nothing on a machine
somebody had walked up to. ` + "`wick run`" + ` is the one that asks.

It exits non-zero when something did not happen, so that whatever ran it can
tell "there was nothing to do" from "it did not work". A file is only recorded
as carried once every destination has confirmed it, so a pass that half worked
leaves the file alone and says so.`),

		Flags: append(global(),
			&flg.String{
				Name:  "spool",
				Alias: 's',
				Brief: "carry only this spool, instead of every one of them",
			},
		),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			c := use_config.Must(ctx)

			w, err := c.Build(ctx)
			if err != nil {
				return err
			}
			defer w.Close()

			only, _ := flg.Find[string](cmd, "spool")

			found := false
			var failed error
			for _, s := range w.Group.Spools() {
				if only != "" && s.Name() != only {
					continue
				}
				found = true

				r, err := s.Once(ctx)
				cmd.Printf("%s: %d carried, %s, %d retired, %d set aside, %d waiting\n",
					s.Name(), r.Carried, bytes(r.Bytes), r.Retired, r.SetAside, r.Settled-r.Carried,
				)

				if err != nil {
					failed = errorsJoin(failed, err)
				}
				if err := r.Err(); err != nil {
					failed = errorsJoin(failed, err)
				}
			}

			if only != "" && !found {
				return fmt.Errorf("nothing is called %q; `wick config` says what is", only)
			}

			// Non-zero when something did not happen, so that whatever ran
			// this can tell "there was nothing to do" from "it did not work".
			return failed
		}),
	}
}
