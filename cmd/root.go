package cmd

import (
	"context"

	"github.com/lesomnus/otx"
	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/flg"
)

const rootSynop = `Files pile up on machines nobody logs into. wick watches the places they pile
up in, waits for one to stop being written, carries it to everywhere it is
meant to go, proves the copy arrived by reading it back, and only then does
whatever the configuration said to do with the original.

Told nothing, it reads ./wick.yaml or ./wick.yml. What that says can be
overruled, and the last word wins:

    built-in defaults  <  the file  <  the environment  <  the flags

Every field answers to a variable named after the path to it: "path" of
"journal" is WICK_JOURNAL_PATH. A value in the file can also name a variable
instead of holding one, which is what a password is for:

    secret_access_key: "${env:S3_KEY_SECRET}"

Starting from nothing: "wick kinds" says what may be carried to, "wick config"
says what it has understood, and "wick check" says whether that would work on
this machine. None of those move a file.`

func NewCmdRoot() *xli.Command {
	// One flag object, held by every command rather than by the root alone.
	//
	// xli matches a flag against the frame it appeared in, so a flag declared
	// only on the root makes `wick once --config x.yaml` -- which is what
	// people type, and what every example of a tool like this looks like --
	// fail with "unknown flag". Sharing the object means whichever frame it
	// was written in is the one that fills it in, and every frame can read it.
	// Every frame is parsed before any handler runs, so by the time the root
	// asks, the answer is there wherever it came from.
	config := &flg.String{
		Name:  "config",
		Alias: 'c',
		Brief: "read this instead of ./wick.yaml or ./wick.yml",
	}
	verbose := &flg.Switch{
		Name:  "verbose",
		Alias: 'v',
		Brief: "say what is being done, and not only what went wrong",
	}
	global := func() flg.Flags { return flg.Flags{config, verbose} }

	return &xli.Command{
		Name:  "wick",
		Brief: "draw what accumulates, and carry it away",
		Synop: synop(rootSynop),

		Flags: global(),

		Commands: []*xli.Command{
			NewCmdVersion(),
			NewCmdKinds(),
			NewCmdConfig(global),
			NewCmdCheck(global),
			NewCmdRun(global),
			NewCmdOnce(global),
			NewCmdStatus(global),
			NewCmdForget(global),
			described(xli.NewCmdCompletion(),
				"print a shell completion script",
				`Write it where the shell will read it:

    wick completion zsh > "${fpath[1]}/_wick"

Only zsh so far, which is what the library this is built on has.`),
		},

		Handler: xli.Chain(
			RequireSubcommand(),
			xli.OnRunPass(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
				if nextIs(ctx, "version") {
					return next(ctx)
				}
				if nextIs(ctx, "kinds") {
					return next(ctx)
				}
				if nextIs(ctx, "completion") {
					// The completion script is a static file. Reading a
					// configuration to print it would make `wick completion
					// zsh` fail in exactly the shell that is trying to learn
					// about wick.
					return next(ctx)
				}

				ctx, _, err := UseConfigInit(ctx, cmd)
				if err != nil {
					return err
				}

				o := otx.From(ctx)
				defer o.Shutdown(ctx)

				return next(ctx)
			}),
		),
	}
}
