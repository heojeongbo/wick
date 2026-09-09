package cmd

import (
	"context"

	"github.com/heojeongbo/wick/cmd/version"
	"github.com/lesomnus/xli"
)

func NewCmdVersion() *xli.Command {
	const Template = `WICK_VERSION=%s
WICK_GIT_REV=%s
WICK_GIT_DIRTY=%v
`
	return &xli.Command{
		Name:  "version",
		Brief: "print version information",
		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			v := version.Get()
			cmd.Printf(Template,
				v.Version,
				v.GitRev,
				v.GitDirty,
			)
			return nil
		}),
	}
}
