package cmd

// What this build can do.
//
// A kind of source or sink becomes available by being imported, and this is the
// one place that happens -- so this list is what `type:` in a configuration may
// say, and changing it is the whole of adding or removing a kind.
//
// It is a list of blank imports rather than a registry written out somewhere,
// because the alternative is a build that carries the code for every sink
// whether or not it names one. The cloud SDK is several megabytes on a machine
// that may only ever write to a directory next door.
import (
	"context"
	"strings"

	"github.com/lesomnus/xli"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/source"

	_ "github.com/heojeongbo/wick/source/dir"

	_ "github.com/heojeongbo/wick/sink/azure"
	_ "github.com/heojeongbo/wick/sink/dir"
	_ "github.com/heojeongbo/wick/sink/gcs"
	_ "github.com/heojeongbo/wick/sink/http"
	_ "github.com/heojeongbo/wick/sink/s3"
	_ "github.com/heojeongbo/wick/sink/sftp"
	_ "github.com/heojeongbo/wick/sink/webdav"
)

// NewCmdKinds says what the list above came to.
//
// The registries have known this all along and there was no way to ask them.
// The only way to see the list was to write a `type:` that is not one and read
// the refusal, which is a strange thing to have to do on purpose.
func NewCmdKinds() *xli.Command {
	return &xli.Command{
		Name:  "kinds",
		Brief: "list what this build can carry from, and to",
		Synop: synop(`What ` + "`type:`" + ` may say in a configuration.

A kind becomes available by its package being imported, so this is a property
of the build and not of the machine. A build that leaves the cloud sinks out
does not carry their SDKs either, which is most of the binary.

docs/SINKS.md says what each one wants written under it.`),

		// No configuration is read. This is the command somebody runs *before*
		// they have one, and one that needed a valid config to say what a
		// config may contain would be a circle.
		Handler: xli.OnRun(func(ctx context.Context, cmd *xli.Command, next xli.Next) error {
			cmd.Printf("sources:\n    %s\n", strings.Join(source.Kinds(), "\n    "))
			cmd.Printf("sinks:\n    %s\n", strings.Join(sink.Kinds(), "\n    "))

			return nil
		}),
	}
}
