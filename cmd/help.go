package cmd

import (
	"context"
	"strings"

	"github.com/lesomnus/xli"
	"github.com/lesomnus/xli/frm"
)

// nextIs says whether the command about to run below this one is the one named.
//
// [frm.From] answers nil when there is no command run to speak of -- a test, or
// a consumer calling into this package directly -- and a nil frame has no Next
// to ask. Every caller wanting to know "is this `run`" would otherwise be a
// place this could panic.
func nextIs(ctx context.Context, name string) bool {
	f := frm.From(ctx)
	if f == nil {
		return false
	}

	return frm.HasSeq(f.Next(), name)
}

// synop is a description written as prose, indented to sit under the heading
// xli prints it below.
//
// The template puts four spaces in front of the string and not in front of each
// of its lines, so a paragraph written the obvious way comes out with its first
// line indented and the rest against the margin. Doing it here rather than by
// hand in every string keeps the source readable as what it is: prose about a
// command, not prose with the layout of the help already baked into it.
func synop(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		if i == 0 || l == "" {
			// The first already has the template's four in front of it, and a
			// blank line indented is a line of trailing whitespace.
			continue
		}
		lines[i] = "    " + l
	}

	return strings.Join(lines, "\n")
}

// described gives a command a Brief when it arrived without one.
//
// [xli.NewCmdCompletion] is somebody else's command and lands in the list with
// an empty column beside it, which reads as an omission rather than as a thing
// that needs no explaining.
func described(c *xli.Command, brief string, synopsis string) *xli.Command {
	c.Brief = brief
	c.Synop = synop(synopsis)

	return c
}
