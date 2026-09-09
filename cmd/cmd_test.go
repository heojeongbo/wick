package cmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd"
)

// rig is a whole machine's worth of it: a directory that things accumulate in,
// two places for them to go, and a configuration that says so.
type rig struct {
	dir    string
	in     string
	cloud  string
	onsite string
	config string
}

func newRig(t *testing.T, spool string) *rig {
	t.Helper()

	x := require.New(t)

	g := &rig{dir: t.TempDir()}
	g.in = filepath.Join(g.dir, "in")
	g.cloud = filepath.Join(g.dir, "cloud")
	g.onsite = filepath.Join(g.dir, "onsite")
	g.config = filepath.Join(g.dir, "wick.yaml")

	x.NoError(os.MkdirAll(g.in, 0o755))

	body := "identity:\n  name: thor-top\n" +
		"journal:\n  path: " + filepath.Join(g.dir, "wick.db") + "\n" +
		"sinks:\n" +
		"  cloud:\n    type: dir\n    path: " + g.cloud + "\n" +
		"  onsite:\n    type: dir\n    path: " + g.onsite + "\n" +
		"spools:\n" +
		"  - name: recordings\n" +
		"    source:\n      type: dir\n      path: " + g.in + "\n      include: [\"*.rec\"]\n" +
		spool

	x.NoError(os.WriteFile(g.config, []byte(body), 0o600))

	return g
}

func (g *rig) add(t *testing.T, name, data string) {
	t.Helper()

	require.NoError(t, os.WriteFile(filepath.Join(g.in, name), []byte(data), 0o644))
}

// run drives the whole command the way the binary does, config and all.
func (g *rig) run(t *testing.T, args ...string) xlitest.Result {
	t.Helper()

	return xlitest.Run(t, cmd.NewCmdRoot(), append([]string{"--config", g.config}, args...)...)
}

const toBoth = `    to:
      - sink: cloud
        name_as: "{host}/{yyyy}/{mm}/{dd}/{name}"
      - sink: onsite
        name_as: "{name}"
    retain:
      after: delete
`

func TestOnce(t *testing.T) {
	t.Run("it carries to every destination and says what it did", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		g.add(t, "a.rec", "contents")
		g.add(t, "notes.txt", "not a recording")

		r := g.run(t, "once")
		x.NoError(r.Err)
		x.Contains(r.Stdout, "recordings: 1 carried")

		x.FileExists(filepath.Join(g.onsite, "a.rec"))
		x.NoFileExists(filepath.Join(g.in, "a.rec"))
		// The one that was left is still there, which is what `include` is for.
		x.FileExists(filepath.Join(g.in, "notes.txt"))
	})

	t.Run("a second run with nothing new says so and does nothing", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		g.add(t, "a.rec", "contents")

		x.NoError(g.run(t, "once").Err)

		r := g.run(t, "once")
		x.NoError(r.Err)
		x.Contains(r.Stdout, "0 carried")
	})

	t.Run("only the one that was asked for", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		g.add(t, "a.rec", "contents")

		r := g.run(t, "once", "--spool", "recordings")
		x.NoError(r.Err)
		x.Contains(r.Stdout, "recordings:")
	})

	t.Run("one that nothing is called says what is", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		r := g.run(t, "once", "--spool", "nope")
		x.ErrorContains(r.Err, "nothing is called")
	})

	// Non-zero when something did not happen, so that whatever ran this can
	// tell "there was nothing to do" from "it did not work".
	t.Run("something that could not be carried is a failure", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, "    to:\n      - sink: cloud\n        name_as: \"{dir}\"\n")
		g.add(t, "a.rec", "contents")

		r := g.run(t, "once")
		x.Error(r.Err)
	})

	t.Run("a source that is not there is said so at startup", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		x.NoError(os.RemoveAll(g.in))

		// The directory is made by the walk being refused rather than by this,
		// so the pass reports it rather than the startup.
		r := g.run(t, "once")
		x.Error(r.Err)
	})
}

func TestStatus(t *testing.T) {
	t.Run("it says what the journal knows", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		g.add(t, "a.rec", "contents")
		x.NoError(g.run(t, "once").Err)

		r := g.run(t, "status")
		x.NoError(r.Err)
		x.Contains(r.Stdout, "recordings")
		x.Contains(r.Stdout, "retired")
		x.Contains(r.Stdout, "oldest")
	})

	t.Run("and, asked, why the ones it set aside were set aside", func(t *testing.T) {
		x := require.New(t)

		// One attempt, and a name that cannot be made, so the first pass sets
		// it aside.
		g := newRig(t, "    to:\n      - sink: cloud\n        name_as: \"{dir}\"\n    carry: {attempts: 1}\n")
		g.add(t, "a.rec", "contents")
		x.Error(g.run(t, "once").Err)

		r := g.run(t, "status", "--quarantined")
		x.NoError(r.Err)
		x.Contains(r.Stdout, "quarantined       1")
		x.Contains(r.Stdout, "! a.rec")
		x.Contains(r.Stdout, "what to call")
	})

	t.Run("a journal that is not there is said so", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		x.NoError(os.WriteFile(filepath.Join(g.dir, "blocked"), nil, 0o600))
		x.NoError(os.WriteFile(g.config,
			[]byte("journal:\n  path: "+filepath.Join(g.dir, "blocked", "a", "wick.db")+"\n"), 0o600))

		x.ErrorContains(g.run(t, "status").Err, "make the directory")
		x.ErrorContains(g.run(t, "forget", "a", "b").Err, "nothing is called")
	})
}

func TestForget(t *testing.T) {
	t.Run("what was remembered is forgotten", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		g.add(t, "a.rec", "contents")
		x.NoError(g.run(t, "once").Err)

		r := g.run(t, "forget", "recordings", "a.rec")
		x.NoError(r.Err)
		x.Contains(r.Stdout, "forgot a.rec of recordings")

		// And it is not remembered any more.
		x.ErrorContains(g.run(t, "forget", "recordings", "a.rec").Err, "not remembered in the first place")
	})

	t.Run("a spool nothing is called says what is", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		x.ErrorContains(g.run(t, "forget", "nope", "a.rec").Err, "nothing is called")
	})
}

func TestConfig(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	r := g.run(t, "config")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "type: dir")
	x.Contains(r.Stdout, "name: recordings")
}

func TestVersion(t *testing.T) {
	x := require.New(t)

	// It works on a machine that has no configuration and no secrets, which is
	// the whole point of it being exempt from reading one.
	r := xlitest.Run(t, cmd.NewCmdRoot(), "version")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "WICK_VERSION=")
	x.Contains(r.Stdout, "WICK_GIT_REV=")
	x.Contains(r.Stdout, "WICK_GIT_DIRTY=")
}

func TestSomethingThatIsNotACommand(t *testing.T) {
	x := require.New(t)

	r := xlitest.Run(t, cmd.NewCmdRoot())
	x.Error(r.Err)
}

func TestAConfigThatIsNotThere(t *testing.T) {
	x := require.New(t)

	r := xlitest.Run(t, cmd.NewCmdRoot(), "--config", filepath.Join(t.TempDir(), "nope.yaml"), "config")
	x.ErrorContains(r.Err, "read config")
}

// A configuration that cannot be meant is refused before anything is opened.
func TestAConfigThatCannotBeMeant(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "wick.yaml")
	x.NoError(os.WriteFile(p, []byte("spools:\n  - name: a\n"), 0o600))

	r := xlitest.Run(t, cmd.NewCmdRoot(), "--config", p, "config")
	x.ErrorContains(r.Err, "no source")
}

// The default paths are looked at when nothing names one, and finding none at
// all is not an error -- it is a daemon that was given nothing to do.
func TestTheDefaultPaths(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	t.Chdir(dir)

	r := xlitest.Run(t, cmd.NewCmdRoot(), "config")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "identity:")

	x.NoError(os.WriteFile(filepath.Join(dir, "wick.yaml"), []byte("identity:\n  name: written-down\n"), 0o600))

	r = xlitest.Run(t, cmd.NewCmdRoot(), "config")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "written-down")
}

// A name that starts with the prefix and answers to nothing is a typo, and is
// reported rather than ignored in silence.
func TestSomethingInTheEnvironmentThatAnswersToNothing(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	t.Setenv("WICK_IDENTITY_NAMES", "typo")

	r := g.run(t, "config")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "thor-top")
}

// The environment overrules the file, and the defaults do not undo it.
func TestTheEnvironmentOverrulesTheFile(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	t.Setenv("WICK_IDENTITY_NAME", "from-the-environment")

	r := g.run(t, "config")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "from-the-environment")
	x.False(strings.Contains(r.Stdout, "thor-top"))
}

// A value that does not fit the field it is named after is refused.
func TestSomethingInTheEnvironmentThatDoesNotFit(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	t.Setenv("WICK_SPOOLS", "not a list of spools")

	x.ErrorContains(g.run(t, "config").Err, "WICK_SPOOLS")
}

// The one that was set aside is the one that is listed, and the ones that were
// not are not.
func TestListingOnlyWhatWasSetAside(t *testing.T) {
	x := require.New(t)

	g := newRig(t, "    to:\n      - sink: cloud\n        name_as: \"{dir}\"\n    carry: {attempts: 1}\n")
	g.add(t, "bad.rec", "contents")
	x.Error(g.run(t, "once").Err)

	// And now one that carries, so the listing has both to choose between.
	x.NoError(os.WriteFile(g.config, []byte(strings.Replace(
		mustRead(t, g.config), "name_as: \"{dir}\"", "name_as: \"{name}\"", 1)), 0o600))
	g.add(t, "good.rec", "contents")
	x.NoError(g.run(t, "once").Err)

	r := g.run(t, "status", "--quarantined")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "! bad.rec")
	x.NotContains(r.Stdout, "! good.rec")
}

// A configuration that is a document and not a machine: the spool is fine, and
// the journal it names cannot be made.
func TestOnceRefusesWhatItCannotOpen(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	blocked := filepath.Join(g.dir, "blocked")
	x.NoError(os.WriteFile(blocked, nil, 0o600))
	x.NoError(os.WriteFile(g.config, []byte(strings.Replace(
		mustRead(t, g.config),
		filepath.Join(g.dir, "wick.db"),
		filepath.Join(blocked, "a", "wick.db"), 1)), 0o600))

	x.ErrorContains(g.run(t, "once").Err, "make the directory")
}

// Telemetry the configuration describes has to be buildable, and one it
// describes wrongly is a refusal at startup rather than a signal that quietly
// goes nowhere.
func TestTelemetryThatCannotBeBuilt(t *testing.T) {
	x := require.New(t)

	p := filepath.Join(t.TempDir(), "wick.yaml")
	x.NoError(os.WriteFile(p, []byte("otel:\n  exporters:\n    nope/one: {}\n"), 0o600))

	r := xlitest.Run(t, cmd.NewCmdRoot(), "--config", p, "config")
	x.Error(r.Err)
}

// A default path that is there and cannot be read is not the same as one that
// is not there, and only the second is passed over.
func TestADefaultPathThatIsNotAFile(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	x.NoError(os.Mkdir(filepath.Join(dir, "wick.yaml"), 0o755))
	t.Chdir(dir)

	r := xlitest.Run(t, cmd.NewCmdRoot(), "config")
	x.ErrorContains(r.Err, "read config")
}

func mustRead(t *testing.T, p string) string {
	t.Helper()

	b, err := os.ReadFile(p)
	require.NoError(t, err)

	return string(b)
}

// The file this ships with is the documentation, and a documentation file that
// does not load is worse than none. It has caught itself out once already:
// naming a variable is done to the whole file before any of it is read as
// YAML, so a "${env:...}" written in a *comment* is a name that has to be set.
func TestTheFileThisShipsWith(t *testing.T) {
	x := require.New(t)

	r := xlitest.Run(t, cmd.NewCmdRoot(), "--config", "../wick.yaml", "config")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "identity:")
}
