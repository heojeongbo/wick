package config

import (
	"context"
	"log/slog"

	"github.com/lesomnus/otx/log"
)

// # Two kinds of line, and only one of them is anybody's business
//
// A daemon's log is its whole observability story: what it read, what it is
// watching, what it carried. A one-shot command's output is its *answer*, and
// three lines about having found a configuration file are not part of the
// answer -- they are in front of it, on the same terminal, every single time.
//
//	$ wick config
//	|........| 13:06:19.422 ○ 000000 000000 config loaded - path=wick.yaml
//	identity:
//	  ...
//
// The trace and span ids there are zeros, because a `config` that prints and
// exits has no trace. It is a daemon's line printed by something that is not a
// daemon.
//
// So the lines that say what is being done go through [Say], which is silent
// unless somebody asked. The lines that say what went *wrong* do not: a warning
// is not chatter, and a command that hid one would be worse than one that
// chatters.
type verboseKey struct{}

// WithVerbose says whether the lines about what is being done should be
// written. [Run] sets it because a daemon's log is the point of it; everything
// else sets it from --verbose.
func WithVerbose(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, verboseKey{}, on)
}

// Verbose is what [WithVerbose] was told, and false when it was told nothing.
func Verbose(ctx context.Context) bool {
	on, _ := ctx.Value(verboseKey{}).(bool)

	return on
}

// Say is the logger for what is being done. It writes nothing unless this is a
// context [WithVerbose] was told yes about.
//
// Use [log.From] directly for anything that went wrong, which is always
// written.
func Say(ctx context.Context) *slog.Logger {
	if !Verbose(ctx) {
		return slog.New(slog.DiscardHandler)
	}

	return log.From(ctx)
}
