package cmd

import (
	// Aliased: this package has a `bytes` of its own, for saying a number of
	// them the way somebody reads them.
	buf "bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lesomnus/xli"
	"github.com/stretchr/testify/require"
)

// A word that is nearly one of ours is worth a guess; one that is not, is not.
func TestNearest(t *testing.T) {
	names := []string{"config", "check", "status", "once", "run"}

	for _, tt := range []struct {
		what string
		want string
	}{
		{"statu", "status"},
		{"statuss", "status"},
		{"confg", "config"},
		{"onc", "once"},
		{"", ""},
		{"carry", ""},
		{"absolutely-not-a-command", ""},
	} {
		t.Run(tt.what, func(t *testing.T) {
			x := require.New(t)

			got, ok := nearest(tt.what, names)
			x.Equal(tt.want != "", ok)
			x.Equal(tt.want, got)
		})
	}
}

// A guess that is wrong sends somebody to read about a thing they were not
// looking for, so the distance it is allowed grows with what was typed.
func TestDistance(t *testing.T) {
	x := require.New(t)

	x.Zero(distance("", ""))
	x.Equal(3, distance("", "abc"))
	x.Equal(3, distance("abc", ""))
	x.Zero(distance("status", "status"))
	x.Equal(1, distance("statu", "status"))
	x.Equal(2, distance("kitten", "sitten"[0:5]+"g"))
}

func TestWritten(t *testing.T) {
	x := require.New(t)

	x.Equal("nope", written(errors.New(`"nope": unknown subcommand`)))
	x.Equal("--confg", written(errors.New("--confg: unknown flag")))
	x.Equal("whatever", written(errors.New("whatever")))
}

// The help offered is about what was being attempted, and the value of a flag
// is not a command however much it looks like one.
func TestDeepest(t *testing.T) {
	root := NewCmdRoot()

	for _, tt := range []struct {
		what string
		args []string
		want []string
	}{
		{"nothing named", []string{}, []string{}},
		{"a command", []string{"forget"}, []string{"forget"}},
		{"behind a flag and its value", []string{"--config", "x.yaml", "forget"}, []string{"forget"}},
		{"a word that is nothing", []string{"x.yaml"}, []string{}},
		{"arguments after it", []string{"forget", "a", "b"}, []string{"forget"}},
		{"a subcommand of a subcommand", []string{"completion", "zsh"}, []string{"completion", "zsh"}},
	} {
		t.Run(tt.what, func(t *testing.T) {
			x := require.New(t)

			at, path := deepest(root, tt.args)
			x.Equal(tt.want, path)
			if len(tt.want) > 0 {
				x.Equal(tt.want[len(tt.want)-1], at.Name)
			} else {
				x.Equal("wick", at.Name)
			}
		})
	}
}

func TestFlagNames(t *testing.T) {
	x := require.New(t)

	names := flagNames(NewCmdRoot())
	x.Contains(names, "config")
	x.Contains(names, "verbose")
	x.Contains(names, "spool")
	x.Contains(names, "quarantined")
	x.Contains(names, "reveal")

	// Held by every command, and said once.
	n := 0
	for _, v := range names {
		if v == "config" {
			n++
		}
	}
	x.Equal(1, n)
}

func TestMisuseIsAnError(t *testing.T) {
	x := require.New(t)

	inner := errors.New("what went wrong")
	m := &Misuse{Err: inner}

	x.Equal("what went wrong", m.Error())
	x.ErrorIs(m, inner)
}

// Explain leaves alone what it has nothing to add to, and adds to the rest.
func TestExplain(t *testing.T) {
	root := NewCmdRoot()

	t.Run("nothing went wrong", func(t *testing.T) {
		require.New(t).NoError(Explain(root, nil, nil))
	})
	t.Run("something that is not about the command line", func(t *testing.T) {
		x := require.New(t)

		err := errors.New("the sink cloud: connection refused")
		x.Equal(err, Explain(root, nil, err))
	})
	t.Run("one that has already been explained", func(t *testing.T) {
		x := require.New(t)

		err := &Misuse{Err: errors.New("said once")}
		x.Equal(err, Explain(root, nil, err))
	})
	t.Run("a subcommand that is nearly one", func(t *testing.T) {
		x := require.New(t)

		err := Explain(root, []string{"statuss"}, fmt.Errorf(`"statuss": %w`, xli.ErrUnknownCmd))
		x.ErrorContains(err, "did you mean")
	})
	t.Run("a subcommand that is nothing like one", func(t *testing.T) {
		x := require.New(t)

		var m *Misuse
		err := Explain(root, []string{"frobnicate"}, fmt.Errorf(`"frobnicate": %w`, xli.ErrUnknownCmd))
		x.ErrorAs(err, &m)
		x.NotNil(m.Help)
	})
	t.Run("a flag that is nearly one", func(t *testing.T) {
		x := require.New(t)

		err := Explain(root, nil, fmt.Errorf("--confg: %w", xli.ErrUnknownFlag))
		x.ErrorContains(err, "did you mean --config")
	})
	t.Run("a flag that is nothing like one", func(t *testing.T) {
		x := require.New(t)

		var m *Misuse
		err := Explain(root, nil, fmt.Errorf("--absolutely-nothing: %w", xli.ErrUnknownFlag))
		x.ErrorAs(err, &m)
		x.NotNil(m.Help)
	})
	t.Run("an argument that was not given", func(t *testing.T) {
		x := require.New(t)

		var m *Misuse
		err := Explain(root, []string{"forget"}, fmt.Errorf(`"spool": %w`, xli.ErrNeedArgs))
		x.ErrorAs(err, &m)
		x.Equal([]string{"forget"}, m.Help)
	})
}

// What the operator actually sees, and what the shell gets back.
func TestReport(t *testing.T) {
	t.Run("nothing went wrong", func(t *testing.T) {
		x := require.New(t)

		root := NewCmdRoot()
		root.ErrWriter = &buf.Buffer{}
		x.Zero(Report(root, nil, nil))
	})
	t.Run("something that only needs saying", func(t *testing.T) {
		x := require.New(t)

		b := &buf.Buffer{}
		root := NewCmdRoot()
		root.ErrWriter = b

		x.Equal(1, Report(root, nil, errors.New("the sink cloud: connection refused")))
		x.Equal("error: the sink cloud: connection refused\n", b.String())
	})
	t.Run("something that needs the way out as well", func(t *testing.T) {
		x := require.New(t)

		b := &buf.Buffer{}
		root := NewCmdRoot()
		root.ErrWriter = b

		x.Equal(1, Report(root, []string{"forget"}, fmt.Errorf(`"spool": %w`, xli.ErrNeedArgs)))

		out := b.String()
		x.Contains(out, "required argument not given")
		// Named the way it is typed, which needs the parent links a failed
		// parse never got round to putting on.
		x.Contains(out, "wick forget <spool> <key>")
	})
	t.Run("a bare wick lists what it could have been", func(t *testing.T) {
		x := require.New(t)

		b := &buf.Buffer{}
		root := NewCmdRoot()
		root.ErrWriter = b
		root.Writer = &buf.Buffer{}

		x.Equal(1, Report(root, []string{}, root.Run(t.Context(), []string{})))

		out := b.String()
		x.Contains(out, "say what to do")
		for _, name := range []string{"config", "check", "once", "run", "status", "forget", "kinds"} {
			x.Contains(out, name)
		}
	})
}

// synop puts the paragraphs under the heading and leaves the blank lines blank.
func TestSynop(t *testing.T) {
	x := require.New(t)

	got := synop("one\n\ntwo\n")
	x.Equal("one\n\n    two", got)

	for _, line := range strings.Split(got, "\n") {
		x.Equal(strings.TrimRight(line, " "), line, "a blank line was indented into whitespace")
	}
}

func TestNextIsSurvivesNoFrame(t *testing.T) {
	// There is no command run here at all, which is what a consumer calling
	// into this package looks like.
	require.New(t).False(nextIs(t.Context(), "run"))
}
