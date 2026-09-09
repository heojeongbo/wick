package config_test

import (
	"context"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd/config"
	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/source"
)

var errRefused = errors.New("refused")

// The questions that are about the machine rather than about the document are
// asked here, at startup, all of them, before anything is carried.
func TestBuild(t *testing.T) {
	t.Run("everything the configuration describes is opened and wired together", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		c, err := read(t, whole(dir, "    retain: {after: delete}\n"))
		x.NoError(err)

		w, err := c.Build(t.Context())
		x.NoError(err)
		defer w.Close()

		x.NotNil(w.Journal)
		x.Len(w.Group.Spools(), 1)
		x.Equal("recordings", w.Group.Spools()[0].Name())
	})

	t.Run("a journal that cannot be opened is said so", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		blocked := filepath.Join(dir, "blocked")
		x.NoError(os.WriteFile(blocked, nil, 0o600))

		c, err := read(t, "journal:\n  path: "+filepath.Join(blocked, "a", "wick.db")+"\n")
		x.NoError(err)

		_, err = c.Build(t.Context())
		x.ErrorContains(err, "make the directory")
	})

	t.Run("a sink that cannot be made says which one", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		c, err := read(t, "journal:\n  path: "+filepath.Join(dir, "wick.db")+"\n"+
			"sinks:\n  cloud:\n    type: refuses\n")
		x.NoError(err)

		_, err = c.Build(t.Context())
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, `the sink "cloud"`)
	})

	t.Run("a source that cannot be made says which spool", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		c, err := read(t, "journal:\n  path: "+filepath.Join(dir, "wick.db")+"\n"+
			"sinks:\n  cloud: {type: dir, path: "+dir+"/cloud}\n"+
			"spools:\n  - name: recordings\n    source: {type: refuses}\n    to: [{sink: cloud}]\n")
		x.NoError(err)

		_, err = c.Build(t.Context())
		x.ErrorIs(err, errRefused)
		x.ErrorContains(err, `the source of the spool "recordings"`)
	})

	// Refused here rather than at night on a machine: `dir` can delete, but a
	// source that cannot is a legitimate source and the policy has to be
	// checked against the one it was actually given.
	t.Run("a retention the source cannot carry out is refused at startup", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		c, err := read(t, "journal:\n  path: "+filepath.Join(dir, "wick.db")+"\n"+
			"sinks:\n  cloud: {type: dir, path: "+dir+"/cloud}\n"+
			"spools:\n  - name: recordings\n    source: {type: read-only}\n    to: [{sink: cloud}]\n"+
			"    retain: {after: delete}\n")
		x.NoError(err)

		_, err = c.Build(t.Context())
		x.ErrorContains(err, "can delete")
	})

	// A sink holding a connection pool is let go of; one that was handed over
	// is not this to close.
	t.Run("what it opened is let go of, and what it could not be is said", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		c, err := read(t, "journal:\n  path: "+filepath.Join(dir, "wick.db")+"\n"+
			"sinks:\n  cloud:\n    type: closes\n")
		x.NoError(err)

		w, err := c.Build(t.Context())
		x.NoError(err)
		x.ErrorIs(w.Close(), errRefused)
	})

	t.Run("nothing opened is nothing to let go of", func(t *testing.T) {
		x := require.New(t)

		x.NoError((&config.Wick{}).Close())
	})
}

// whole is a configuration with everything in it, pointed at a directory of
// this test's.
func whole(dir string, extra string) string {
	return "identity:\n  name: thor-top\n" +
		"journal:\n  path: " + filepath.Join(dir, "wick.db") + "\n" +
		"sinks:\n  cloud: {type: dir, path: " + dir + "/cloud}\n" +
		"spools:\n  - name: recordings\n" +
		"    source: {type: dir, path: " + dir + "/in}\n" +
		"    to: [{sink: cloud, name_as: \"{host}/{name}\"}]\n" +
		extra
}

// The kinds below exist to be refused, or to have something to let go of.
// There is no way to make the real ones do either on demand, and both are
// things that happen.

type refusingSink struct {
	sink.Typed `yaml:",inline"`
}

func (*refusingSink) New(context.Context) (sink.Sink, error) { return nil, errRefused }

type refusingSource struct {
	source.Typed `yaml:",inline"`
}

func (*refusingSource) New(context.Context) (source.Source, error) { return nil, errRefused }

type readOnlySource struct {
	source.Typed `yaml:",inline"`
}

func (*readOnlySource) New(context.Context) (source.Source, error) { return bare{}, nil }

type bare struct{}

func (bare) Scan(context.Context) iter.Seq2[source.Item, error] {
	return func(func(source.Item, error) bool) {}
}

func (bare) Open(context.Context, string) (io.ReadCloser, error) { return nil, errRefused }

type closingSink struct {
	sink.Typed `yaml:",inline"`
}

func (*closingSink) New(context.Context) (sink.Sink, error) { return &closer{}, nil }

type closer struct{}

func (*closer) Put(context.Context, string, io.Reader, sink.Meta) error { return nil }
func (*closer) Close() error                                            { return errRefused }

func init() {
	sink.Register("refuses", func() sink.Spec { return &refusingSink{} })
	sink.Register("closes", func() sink.Spec { return &closingSink{} })
	source.Register("refuses", func() source.Spec { return &refusingSource{} })
	source.Register("read-only", func() source.Spec { return &readOnlySource{} })
}

var _ yaml.BytesMarshaler = config.SinkConfig{}
