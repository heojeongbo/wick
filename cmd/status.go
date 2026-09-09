package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/arg"
	"github.com/lesomnus/xli/flg"

	"github.com/heojeongbo/wick/cmd/config"
	"github.com/heojeongbo/wick/journal"
	"github.com/heojeongbo/wick/journal/bolt"
)

func NewCmdStatus() *xli.Command {
	return &xli.Command{
		Name:  "status",
		Brief: "say what the journal knows",

		Flags: flg.Flags{
			&flg.Switch{Name: "quarantined", Brief: "list the ones that were set aside, and why"},
		},

		// The journal is opened and the sinks are not. This is the command an
		// operator runs when something is wrong, and it must not be the
		// command that fails because the thing that is wrong is the network.
		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			c := use_config.Must(ctx)

			jnl, err := openJournal(c.Journal.Path)
			if err != nil {
				return err
			}
			defer jnl.Close()

			_, listing := flg.Find[bool](cmd, "quarantined")

			for _, sc := range c.Spools {
				s, err := journal.Summarize(ctx, jnl, sc.Name)
				if err != nil {
					return err
				}

				cmd.Printf("%s\n", sc.Name)
				for _, st := range []journal.State{
					journal.Pending, journal.Carried, journal.Retired, journal.Quarantined,
				} {
					cmd.Printf("    %-12s %6d  %s\n", st, s.Count[st], bytes(s.Bytes[st]))
				}
				if !s.Oldest.IsZero() {
					cmd.Printf("    %-12s %s\n", "oldest", s.Oldest.UTC().Format(time.RFC3339))
				}

				if !listing {
					continue
				}
				if err := sayQuarantined(ctx, cmd, jnl, sc.Name); err != nil {
					return err
				}
			}

			return nil
		}),
	}
}

func sayQuarantined(ctx context.Context, cmd *xli.Command, jnl journal.Journal, name string) error {
	for r, err := range jnl.Range(ctx, name) {
		if err != nil {
			return err
		}
		if r.State != journal.Quarantined {
			continue
		}

		cmd.Printf("    ! %s\n      %s\n", r.Key, r.Err)
	}

	return nil
}

func NewCmdForget() *xli.Command {
	return &xli.Command{
		Name:  "forget",
		Brief: "drop what is remembered about something",
		Synop: "wick forget SPOOL KEY",

		// Two things at once, and on purpose. Forgetting a record that was set
		// aside is how it gets tried again; forgetting one that was carried is
		// how a file somebody has put back gets carried again. Both are "stop
		// believing what you remember about this", and there is no third
		// meaning to give either of them.
		Args: arg.Args{
			&arg.String{Name: "spool", Brief: "which spool's records"},
			&arg.String{Name: "key", Brief: "what it was called where it was found"},
		},

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			c := use_config.Must(ctx)

			name := arg.MustGet[string](cmd, "spool")
			key := arg.MustGet[string](cmd, "key")

			if !hasSpool(c.Spools, name) {
				return fmt.Errorf("nothing is called %q; `wick config` says what is", name)
			}

			jnl, err := openJournal(c.Journal.Path)
			if err != nil {
				return err
			}
			defer jnl.Close()

			r, ok, err := jnl.Get(ctx, name, key)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%q of %q was not remembered in the first place", key, name)
			}

			if err := jnl.Delete(ctx, name, key); err != nil {
				return err
			}

			cmd.Printf("forgot %s of %s, which was %s\n", key, name, r.State)

			return nil
		}),
	}
}

// openJournal is a variable so that a test can hand over one that refuses.
//
// What `status` and `forget` do when the records cannot be read is the whole of
// what they are for -- they are the commands somebody runs when something is
// already wrong -- and a real bbolt file cannot be made to refuse on demand.
var openJournal = func(path string) (journal.Journal, error) { return bolt.Open(path) }

func hasSpool(spools []config.SpoolConfig, name string) bool {
	for _, s := range spools {
		if s.Name == name {
			return true
		}
	}

	return false
}

// bytes says a number of bytes the way somebody reads them.
func bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}

	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func errorsJoin(errs ...error) error { return errors.Join(errs...) }
