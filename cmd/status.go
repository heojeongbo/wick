package cmd

import (
	"context"
	"encoding/json"
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

func NewCmdStatus(global func() flg.Flags) *xli.Command {
	return &xli.Command{
		Name:  "status",
		Brief: "say what the journal knows",
		Synop: synop(`What has been carried, what is waiting, and what was set aside.

It opens the journal and does not open the sinks. This is the command somebody
runs when something is already wrong, and it must not be the command that fails
because the thing that is wrong is the network.

"pending" is waiting to go, "carried" has arrived everywhere and is waiting for
the retention policy, "retired" is done with, and "quarantined" was tried too
many times and has stopped being tried. --quarantined says which ones and why;
` + "`wick forget`" + ` puts one back.`),

		Flags: append(global(),
			&flg.Switch{Name: "quarantined", Brief: "also list the ones that were set aside, and why"},
			&flg.Switch{Name: "json", Brief: "write it as JSON, for something other than a person"},
		),

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
			_, asJSON := flg.Find[bool](cmd, "json")

			if asJSON {
				return sayJSON(ctx, cmd, jnl, c.Spools, listing)
			}

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

// spoolStatus is one spool's line of the JSON form.
//
// It is a type of its own rather than [journal.Sum] with tags on it, because
// what a monitoring script wants and what the engine keeps are two things that
// should be free to change apart from each other. Counts are a flat object
// keyed by state name, so that adding a state does not move any field.
type spoolStatus struct {
	Name        string           `json:"name"`
	Count       map[string]int   `json:"count"`
	Bytes       map[string]int64 `json:"bytes"`
	Oldest      *time.Time       `json:"oldest"`
	Quarantined []setAside       `json:"quarantined,omitempty"`
}

// setAside is one item that stopped being tried, and why.
type setAside struct {
	Key string `json:"key"`
	Err string `json:"error"`
}

func sayJSON(ctx context.Context, cmd *xli.Command, jnl journal.Journal, spools []config.SpoolConfig, listing bool) error {
	out := make([]spoolStatus, 0, len(spools))

	for _, sc := range spools {
		s, err := journal.Summarize(ctx, jnl, sc.Name)
		if err != nil {
			return err
		}

		v := spoolStatus{
			Name:  sc.Name,
			Count: map[string]int{},
			Bytes: map[string]int64{},
		}
		for _, st := range []journal.State{
			journal.Pending, journal.Carried, journal.Retired, journal.Quarantined,
		} {
			v.Count[st.String()] = s.Count[st]
			v.Bytes[st.String()] = s.Bytes[st]
		}
		if !s.Oldest.IsZero() {
			at := s.Oldest.UTC()
			v.Oldest = &at
		}

		if listing {
			for r, err := range jnl.Range(ctx, sc.Name) {
				if err != nil {
					return err
				}
				if r.State != journal.Quarantined {
					continue
				}

				v.Quarantined = append(v.Quarantined, setAside{Key: r.Key, Err: r.Err})
			}
		}

		out = append(out, v)
	}

	e := json.NewEncoder(cmd)
	e.SetIndent("", "  ")

	return e.Encode(out)
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

func NewCmdForget(global func() flg.Flags) *xli.Command {
	return &xli.Command{
		Name:  "forget",
		Brief: "drop what is remembered about something",
		Synop: synop(`Stop believing what the journal says about one item.

Forgetting one that was set aside is how it gets tried again. Forgetting one
that was carried is how a file somebody has put back gets carried again. Both
are the same sentence -- stop believing what you remember about this -- and
there is no third meaning to give either of them.

KEY is what the item was called where it was found, which is what
` + "`wick status --quarantined`" + ` prints and not the name it was given at the far
end. This changes the record and never the file.`),

		Flags: global(),

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
