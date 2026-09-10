package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/goccy/go-yaml"
	"github.com/heojeongbo/wick/cmd/config"
	"github.com/lesomnus/otx"
	"github.com/lesomnus/otx/log"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
	"github.com/lesomnus/z"
)

var use_config = z.NewUse[*config.Config]()

// startOtel is a variable rather than a call, for the same reason the journal
// and the listener are handed over where they are used: the failure is real on
// a machine -- a collector that cannot be reached, a port already taken -- and
// there is no configuration this build can express that produces it here, so
// there is no other way to have the branch run before a machine runs it.
var startOtel = func(ctx context.Context, o *otx.Otx) error { return o.Start(ctx) }

func NewCmdConfig(global func() flg.Flags) *xli.Command {
	return &xli.Command{
		Name:  "config",
		Brief: "print what it thinks it has been told",
		Synop: synop(`The configuration as it ended up: the file, then the environment over it,
then the flags over that, with every ` + "`${env:...}`" + ` resolved and every default
filled in. This is what to look at when something is not doing what the file
appears to say, and what to paste into a bug report.

It answers the questions that are about the document -- is this spool named
twice, does that destination name a sink -- and none of the questions that are
about the machine. ` + "`wick check`" + ` is the one that opens things.

Secrets are printed as "(set)" unless --reveal is given. The revealed form is
the one that can be written back to a file and read again.`),

		Flags: append(global(),
			&flg.Switch{
				Name:  "reveal",
				Brief: "print secrets instead of hiding them, which makes the output loadable again",
			},
		),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			c := use_config.Must(ctx)

			if _, reveal := flg.Find[bool](cmd, "reveal"); !reveal {
				// A copy, so that hiding them here cannot leave a spool built
				// later in the same process holding "(set)" as a password.
				// Nothing does that today; `config` prints and exits. It is a
				// copy because the day something does, the failure would be a
				// daemon authenticating with the word for a secret.
				c = config.Redact(c)
			}

			return yaml.NewEncoder(cmd).Encode(c)
		}),
	}
}

func NewCmdCheck(global func() flg.Flags) *xli.Command {
	return &xli.Command{
		Name:  "check",
		Brief: "open everything the configuration describes, and carry nothing",
		Synop: synop(`The questions that are about the machine rather than about the document.

` + "`wick config`" + ` can be perfectly happy on a machine where nothing works: it reads
the file and asks whether it makes sense as a document. This does everything
` + "`wick run`" + ` does at startup -- opens the journal, makes every sink, loads every
certificate, checks that a retention which deletes has a source that can delete
-- and then asks each sink whether it holds a name nothing is called.

That last part is the one worth having. Most of these sinks are built without
touching the network on purpose, so building one proves the settings parse and
nothing else. Asking about a name reads nothing and writes nothing, and "I do
not hold that" means the far end was reached and the credentials were taken.
It is what catches a bucket that is not there, a role that is not allowed, and
a known_hosts the sftp sink would otherwise not read until three in the morning.

Nothing is carried and nothing is deleted. It is what to run after editing the
configuration and before restarting the daemon, and on a machine where the
daemon will not start.

If there was no journal already, the one this makes is removed again: a command
somebody ran to look at a machine should not leave a database on it.`),

		Flags: global(),

		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			c := use_config.Must(ctx)

			// Whether the journal was already there decides whether the one
			// Build is about to make is ours to clear up.
			made := false
			if _, err := os.Stat(c.Journal.Path); errors.Is(err, os.ErrNotExist) {
				made = true
			}

			w, err := c.Build(ctx)
			if err != nil {
				if made {
					_ = os.Remove(c.Journal.Path)
				}

				return err
			}

			reached, failed := w.Reach(ctx)

			err = errorsJoin(failed, w.Close())
			if made {
				// After the close, so that bolt has let go of it.
				err = errorsJoin(err, os.Remove(c.Journal.Path))
			}
			if err != nil {
				return err
			}

			for _, sc := range c.Spools {
				cmd.Printf("%s: %s, %d %s, %s, then %s\n",
					sc.Name, sc.Source.Type, len(sc.To), plural(len(sc.To), "destination"),
					sc.When(), sc.Policy(),
				)
			}
			for _, name := range sortedNames(c.Sinks) {
				how := "made, and cannot be asked what it holds"
				if slices.Contains(reached, name) {
					how = "reached"
				}
				cmd.Printf("%s: %s, %s\n", name, c.Sinks[name].Type, how)
			}
			cmd.Printf("ok\n")

			return nil
		}),
	}
}

func plural(n int, what string) string {
	if n == 1 {
		return what
	}

	return what + "s"
}

// sortedNames is the sinks in an order that does not change between runs, so
// that two `wick check` outputs can be compared.
func sortedNames(sinks map[string]config.SinkConfig) []string {
	names := make([]string, 0, len(sinks))
	for name := range sinks {
		names = append(names, name)
	}
	slices.Sort(names)

	return names
}

func UseConfigInit(ctx context.Context, cmd *xli.Command) (context.Context, *config.Config, error) {
	if _, ok := use_config.From(ctx); ok {
		return nil, nil, fmt.Errorf("config already in context")
	}

	// `run` is a daemon and its log is the whole of how anybody knows what it
	// is doing, so it says what it is doing whether or not it was asked. The
	// one-shot commands answer a question and are otherwise quiet.
	_, asked := flg.Find[bool](cmd, "verbose")
	ctx = config.WithVerbose(ctx, asked || nextIs(ctx, "run"))

	var (
		c   *config.Config
		err error
	)
	if p, ok := flg.Find[string](cmd, "config"); ok {
		c, err = config.ReadFromFile(p)
		if err != nil {
			return nil, nil, z.Err(err, "read config")
		}
	} else {
		for _, p := range config.DefaultConfigPaths {
			c, err = config.ReadFromFile(p)
			if err == nil {
				break
			}
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, nil, z.Err(err, "read config: %q", p)
		}
		if c == nil {
			c = &config.Config{}
		}
	}

	// Before the telemetry is built, since that is configured as well. What is
	// unknown is told about once there is something to tell it to.
	unknown, err := config.OverrideFromEnv(c, os.Environ())
	if err != nil {
		return nil, nil, z.Err(err, "read config from environment")
	}

	ctx, o, err := c.Otel.Build(ctx)
	if err != nil {
		return nil, nil, z.Err(err, "build otel")
	}
	if err := startOtel(ctx, o); err != nil {
		return nil, nil, z.Err(err, "start otel")
	}

	if p := c.Path(); p == "" {
		config.Say(ctx).Info("use default config")
	} else {
		config.Say(ctx).Info("config loaded", slog.String("path", p))
	}

	// Not through Say. A WICK_ variable that answers to nothing is somebody
	// having tried to change something and not changed it, and hearing about
	// that only when asked is how it goes unheard.
	if len(unknown) > 0 {
		log.From(ctx).Warn("no configuration is read from these, so they were ignored",
			slog.Any("env", unknown),
		)
	}

	if err := c.Evaluate(); err != nil {
		return nil, nil, z.Err(err, "evaluate config")
	}

	ctx = use_config.Into(ctx, c)
	return ctx, c, nil
}
