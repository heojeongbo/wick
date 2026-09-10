package main

import (
	"context"
	"os"

	"github.com/heojeongbo/wick/cmd"
)

func main() {
	args := os.Args[1:]

	c := cmd.NewCmdRoot()
	c.ErrWriter = os.Stderr

	os.Exit(cmd.Report(c, args, c.Run(context.Background(), args)))
}
