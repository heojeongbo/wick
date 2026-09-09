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
	_ "github.com/heojeongbo/wick/source/dir"

	_ "github.com/heojeongbo/wick/sink/azure"
	_ "github.com/heojeongbo/wick/sink/dir"
	_ "github.com/heojeongbo/wick/sink/gcs"
	_ "github.com/heojeongbo/wick/sink/http"
	_ "github.com/heojeongbo/wick/sink/s3"
	_ "github.com/heojeongbo/wick/sink/sftp"
	_ "github.com/heojeongbo/wick/sink/webdav"
)
