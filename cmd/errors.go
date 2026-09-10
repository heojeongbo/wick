package cmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lesomnus/xli"
)

// A Misuse is a command line that was wrong about itself, as opposed to one
// that asked for something which then did not work.
//
// The two want different answers. "the sink cloud: connection refused" is a
// complete thing to say: whoever typed it knows what they meant and something
// else went wrong. "unknown subcommand" is not, because whatever they meant,
// they now have to find out what they could have meant instead -- and this
// program is holding that list and was saying nothing.
type Misuse struct {
	Err error

	// Help is the path to the command whose help is the way out -- empty for
	// the root, nil for "the message already says it".
	//
	// A path and not the command itself, because the way to render one is to
	// hand it back to xli with --help on the end. The parent links that make
	// the help say "wick forget" rather than "forget" are put on during
	// parsing, and this only ever runs when parsing is what went wrong.
	Help []string
}

func (e *Misuse) Error() string { return e.Err.Error() }
func (e *Misuse) Unwrap() error { return e.Err }

// RequireSubcommand refuses a bare `wick`, and shows what it could have been.
//
// xli's own is one line, "subcommand is required", which is true and leaves
// somebody who has just installed this exactly where they were.
func RequireSubcommand() xli.Handler {
	// OnRun and not OnRunPass, and then no condition at all.
	//
	// xli takes Pass off the deepest frame and leaves it on every frame above,
	// so a root that runs *without* Pass is a root that had nothing named after
	// it. That is the whole of the test, and it has already been made by the
	// time this is called -- a version with `if f.Next() != nil` in it has a
	// branch that cannot be taken, which on this gate is a branch that has to
	// be explained rather than written.
	return xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
		return &Misuse{Err: errors.New("say what to do"), Help: []string{}}
	})
}

// Explain turns what xli said about a command line into something that says
// where to go next.
//
// It is one function rather than one per command because the ways a command
// line can be wrong are all the same way: a word that is not one of the words
// this program knows, with the program knowing them all along.
func Explain(root *xli.Command, args []string, err error) error {
	if err == nil {
		return nil
	}

	var misuse *Misuse
	if errors.As(err, &misuse) {
		return err
	}

	at, path := deepest(root, args)

	switch {
	case errors.Is(err, xli.ErrUnknownCmd):
		names := make([]string, 0, len(at.Commands))
		for _, c := range at.Commands {
			names = append(names, c.Name)
		}
		if near, ok := nearest(written(err), names); ok {
			return &Misuse{Err: fmt.Errorf("%w; did you mean %q", err, near)}
		}

		return &Misuse{Err: err, Help: path}

	case errors.Is(err, xli.ErrUnknownFlag):
		if near, ok := nearest(strings.TrimLeft(written(err), "-"), flagNames(root)); ok {
			return &Misuse{Err: fmt.Errorf("%w; did you mean --%s", err, near)}
		}

		return &Misuse{Err: err, Help: path}

	case errors.Is(err, xli.ErrNeedArgs),
		errors.Is(err, xli.ErrTooManyArgs),
		errors.Is(err, xli.ErrFlagAfterArg),
		errors.Is(err, xli.ErrNoFlagValue),
		errors.Is(err, xli.ErrFlagRequired):
		return &Misuse{Err: err, Help: path}
	}

	return err
}

// flagNames is every flag this program has, wherever it is declared.
//
// All of them and not just the ones on the command that refused it, because
// the flag that was misspelled is often one that belongs somewhere else, and
// "did you mean --quarantined" is a useful thing to hear from `wick once` even
// though `once` is not where it lives.
func flagNames(root *xli.Command) []string {
	var names []string
	seen := map[string]bool{}

	add := func(c *xli.Command) {
		for _, f := range c.Flags {
			if n := f.Info().Name; !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}

	add(root)
	for _, c := range root.Commands {
		add(c)
	}

	return names
}

// deepest is the last command that was named, so that the help offered is about
// what was being attempted rather than about the program in general.
//
// A word that names no subcommand is passed over rather than stopped at,
// because most of them are the value of the flag in front of them -- and
// stopping at the first one meant `wick --config x.yaml forget` was answered
// with the help for `wick`. Working out which words are flag values would mean
// parsing the line a second time, differently, in the one place that only runs
// when the first parse has already failed.
func deepest(root *xli.Command, args []string) (*xli.Command, []string) {
	c := root
	path := []string{}

	for _, a := range args {
		if len(c.Commands) == 0 {
			break
		}
		if strings.HasPrefix(a, "-") {
			continue
		}

		for _, sub := range c.Commands {
			if sub.Name == a {
				c = sub
				path = append(path, a)

				break
			}
		}
	}

	return c, path
}

// written is the word the error is about, with the quotes xli puts round it
// taken off.
func written(err error) string {
	s := err.Error()
	if i := strings.Index(s, ": "); i >= 0 {
		s = s[:i]
	}

	return strings.Trim(s, `"`)
}

// nearest is the name closest to what was written, when one of them is close
// enough to be worth saying out loud.
//
// Close enough is a third of what was typed, rounded down but never below one,
// so that "statu" finds "status" and "carry" finds nothing. A suggestion that
// is wrong costs more than none at all: it sends somebody off to read about a
// thing they were not looking for.
func nearest(what string, names []string) (string, bool) {
	if what == "" {
		return "", false
	}

	limit := max(len(what)/3, 1)

	best, at := "", limit+1
	for _, name := range names {
		if d := distance(what, name); d < at {
			best, at = name, d
		}
	}

	return best, best != ""
}

// distance is Levenshtein, over bytes.
//
// Bytes rather than runes because every name it is asked about is one this
// program chose and all of them are ASCII. A word that is not would still get
// an answer, just a worse one -- which for a suggestion is the right way round
// to be wrong.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}

	return prev[len(b)]
}

// Report writes an error the way somebody reading a terminal wants it, and
// answers with what to exit with.
func Report(root *xli.Command, args []string, err error) int {
	if err == nil {
		return 0
	}

	w := root.ErrWriter
	err = Explain(root, args, err)
	fmt.Fprintln(w, "error:", err)

	var misuse *Misuse
	if errors.As(err, &misuse) && misuse.Help != nil {
		fmt.Fprintln(w)

		// A command tree of its own, handed the same path with --help on the
		// end. The one that just ran is half-parsed by definition -- that is
		// what went wrong -- and xli renders a command's name from links it
		// puts on during a parse that got further than this one did.
		fresh := NewCmdRoot()
		fresh.Writer = w
		fresh.ErrWriter = w
		_ = fresh.Run(context.Background(), append(slices.Clone(misuse.Help), "--help"))
	}

	return 1
}
