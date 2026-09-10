package cmd_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd"
)

// runIn drives the command from a directory of its own, for the cases that are
// about what happens when there is no configuration to be found.
func runIn(t *testing.T, dir string, args ...string) xlitest.Result {
	t.Helper()
	t.Chdir(dir)

	return xlitest.Run(t, cmd.NewCmdRoot(), args...)
}

// `wick kinds` is the only way to ask what `type:` may say, and it must work
// before there is a configuration to be right or wrong about.
func TestKinds(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	r := g.run(t, "kinds")
	x.NoError(r.Err)

	out := r.Stdout
	x.Contains(out, "sources:")
	x.Contains(out, "sinks:")
	for _, kind := range []string{"dir", "http", "webdav", "s3", "gcs", "azure", "sftp"} {
		x.Contains(out, kind)
	}
}

// And it works with no configuration at all, which is the case it is for: a
// command that needed a valid config to say what a config may contain would be
// a circle.
func TestKindsNeedsNoConfiguration(t *testing.T) {
	x := require.New(t)

	r := runIn(t, t.TempDir(), "kinds")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "sinks:")
}

func TestCheck(t *testing.T) {
	t.Run("says what it opened and reached", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		r := g.run(t, "check")
		x.NoError(r.Err)

		x.Contains(r.Stdout, "recordings: dir, 2 destinations")
		x.Contains(r.Stdout, "cloud: dir, reached")
		x.Contains(r.Stdout, "onsite: dir, reached")
		x.Contains(r.Stdout, "ok")
	})

	t.Run("carries nothing", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		g.add(t, "a.rec", "contents")

		x.NoError(g.run(t, "check").Err)

		// Still where it was, and nowhere else.
		x.FileExists(filepath.Join(g.in, "a.rec"))
		x.NoFileExists(filepath.Join(g.onsite, "a.rec"))
	})

	t.Run("leaves no journal behind when there was none", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		db := filepath.Join(g.dir, "wick.db")
		x.NoFileExists(db)

		x.NoError(g.run(t, "check").Err)
		x.NoFileExists(db, "a command run to look at a machine left a database on it")
	})

	t.Run("keeps the journal that was already there", func(t *testing.T) {
		x := require.New(t)

		g := newRig(t, toBoth)
		g.add(t, "a.rec", "contents")

		// `once` makes it.
		x.NoError(g.run(t, "once").Err)

		db := filepath.Join(g.dir, "wick.db")
		x.FileExists(db)

		x.NoError(g.run(t, "check").Err)
		x.FileExists(db, "it removed a journal it did not make")
	})

	t.Run("a sink it cannot reach", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()

		// A directory sink whose path is a file, so that it is built and then
		// cannot be asked about anything.
		blocked := filepath.Join(dir, "blocked")
		x.NoError(os.WriteFile(blocked, nil, 0o600))

		body := "journal:\n  path: " + filepath.Join(dir, "wick.db") + "\n" +
			"sinks:\n  nas:\n    type: dir\n    path: " + filepath.Join(blocked, "under") + "\n" +
			"spools:\n  - name: r\n" +
			"    source:\n      type: dir\n      path: " + dir + "\n" +
			"    to:\n      - sink: nas\n"

		p := filepath.Join(dir, "wick.yaml")
		x.NoError(os.WriteFile(p, []byte(body), 0o600))

		r := runIn(t, dir, "--config", p, "check")
		x.Error(r.Err)
	})

	t.Run("a journal it cannot make", func(t *testing.T) {
		x := require.New(t)

		dir := t.TempDir()
		blocked := filepath.Join(dir, "blocked")
		x.NoError(os.WriteFile(blocked, nil, 0o600))

		p := filepath.Join(dir, "wick.yaml")
		x.NoError(os.WriteFile(p, []byte("journal:\n  path: "+filepath.Join(blocked, "a", "wick.db")+"\n"), 0o600))

		r := runIn(t, dir, "--config", p, "check")
		x.ErrorContains(r.Err, "the journal at")
	})
}

// A monitoring script should not have to parse a column.
func TestStatusAsJSON(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	g.add(t, "a.rec", "contents")
	x.NoError(g.run(t, "once").Err)

	r := g.run(t, "status", "--json")
	x.NoError(r.Err)

	var got []struct {
		Name        string           `json:"name"`
		Count       map[string]int   `json:"count"`
		Bytes       map[string]int64 `json:"bytes"`
		Oldest      *string          `json:"oldest"`
		Quarantined []struct {
			Key string `json:"key"`
			Err string `json:"error"`
		} `json:"quarantined"`
	}
	x.NoError(json.Unmarshal([]byte(r.Stdout), &got))

	x.Len(got, 1)
	x.Equal("recordings", got[0].Name)
	x.Equal(1, got[0].Count["carried"]+got[0].Count["retired"])
	x.NotNil(got[0].Oldest)
	x.Empty(got[0].Quarantined)
}

func TestStatusAsJSONListsWhatWasSetAside(t *testing.T) {
	x := require.New(t)

	// A naming template that fills in to nothing, so the item is tried and set
	// aside rather than carried. The same shape the text form is tested with.
	g := newRig(t, "    to:\n      - sink: cloud\n        name_as: \"{dir}\"\n    carry: {attempts: 1}\n")
	g.add(t, "a.rec", "contents")
	x.Error(g.run(t, "once").Err)

	r := g.run(t, "status", "--json", "--quarantined")
	x.NoError(r.Err)
	x.Contains(r.Stdout, `"quarantined"`)
}

// The JSON form and the one a person reads say the same thing.
func TestStatusFormsAgree(t *testing.T) {
	x := require.New(t)

	g := newRig(t, toBoth)
	g.add(t, "a.rec", "contents")
	x.NoError(g.run(t, "once").Err)

	text := g.run(t, "status")
	x.NoError(text.Err)
	x.True(strings.HasPrefix(text.Stdout, "recordings\n"))

	as := g.run(t, "status", "--json")
	x.NoError(as.Err)
	x.Contains(as.Stdout, `"name": "recordings"`)
}

// A build that gets past the journal and then fails is the one that has to tidy
// up after itself.
func TestCheckTidiesUpAfterASinkThatWillNotBuild(t *testing.T) {
	x := require.New(t)

	dir := t.TempDir()
	db := filepath.Join(dir, "wick.db")

	// An sftp sink with neither a known_hosts nor permission to do without one
	// is refused while it is being made, which is after the journal was opened.
	body := "journal:\n  path: " + db + "\n" +
		"sinks:\n  nas:\n    type: sftp\n    address: 127.0.0.1:1\n    user: x\n    password: y\n" +
		"spools:\n  - name: r\n" +
		"    source:\n      type: dir\n      path: " + dir + "\n" +
		"    to:\n      - sink: nas\n"

	p := filepath.Join(dir, "wick.yaml")
	x.NoError(os.WriteFile(p, []byte(body), 0o600))

	r := runIn(t, dir, "--config", p, "check")
	x.ErrorContains(r.Err, "known_hosts")
	x.NoFileExists(db, "it left the journal it had just made")
}

// One destination is one destination.
func TestCheckCountsOne(t *testing.T) {
	x := require.New(t)

	g := newRig(t, "    to:\n      - sink: cloud\n        name_as: \"{name}\"\n")
	r := g.run(t, "check")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "1 destination,")
	x.NotContains(r.Stdout, "1 destinations")
}

// The completion script is a static file, and must print on a machine that has
// no configuration -- which is every machine that is trying to learn about wick.
func TestCompletion(t *testing.T) {
	x := require.New(t)

	r := runIn(t, t.TempDir(), "completion", "zsh")
	x.NoError(r.Err)
	x.Contains(r.Stdout, "wick")
}
