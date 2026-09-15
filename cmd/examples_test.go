package cmd_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lesomnus/xli/xlitest"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd"
)

// Every example under examples/ is loaded and evaluated.
//
// The same guard `wick.yaml` has, for the same reason and a sharper one: a file
// whose whole job is to show somebody how to write one of these, and which does
// not itself load, is worse than no file at all. It is also the only kind of
// documentation here that can be wrong in a way reading does not reveal --
// `when_free_below` reads perfectly well and is not a key.
//
// It stops at Evaluate. Build would want the buckets and the servers these name
// to exist, which is what `wick check` is for on a machine that has them.
func TestTheExamplesLoad(t *testing.T) {
	paths := examples(t)

	// A sanity line, so that a glob which stops matching does not turn this
	// into a test that passes by looking at nothing.
	require.New(t).GreaterOrEqual(len(paths), 5, "examples/ has fewer files than it did; this test may be looking in the wrong place")

	for _, p := range paths {
		t.Run(filepath.Base(p), func(t *testing.T) {
			x := require.New(t)

			// Every ${env:NAME} an example names, set to something. A secret
			// with no default is meant to be a hard failure at startup, which
			// is right on a machine and unhelpful here.
			for _, name := range named(t, p) {
				t.Setenv(name, "set-for-the-test")
			}

			r := xlitest.Run(t, cmd.NewCmdRoot(), "--config", p, "config")
			x.NoError(r.Err)

			// And what it prints is loadable in turn, which is the property
			// `wick config` is supposed to have.
			out := filepath.Join(t.TempDir(), "again.yaml")
			x.NoError(os.WriteFile(out, []byte(r.Stdout), 0o600))

			again := xlitest.Run(t, cmd.NewCmdRoot(), "--config", out, "config")
			x.NoError(again.Err, "what `wick config` printed does not load again")
		})
	}
}

// Nothing in an example should be a secret written down, because somebody will
// copy it.
func TestTheExamplesNameTheirSecretsRatherThanHoldingThem(t *testing.T) {
	for _, p := range examples(t) {
		t.Run(filepath.Base(p), func(t *testing.T) {
			x := require.New(t)

			body := read(t, p)
			for _, line := range strings.Split(body, "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}

				key, value, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}

				key = strings.TrimSpace(strings.TrimPrefix(key, "- "))
				if !secretKey[key] {
					continue
				}

				value = strings.TrimSpace(value)
				x.Contains(value, "${env:",
					"%s holds a %s rather than naming one; somebody will copy this", filepath.Base(p), key)
			}
		})
	}
}

// The keys a value must never be written under in a file somebody copies.
//
// The same set the `wick:"secret"` tags cover, which is what `wick config`
// prints as "(set)". The demo under examples/local is exempt and does not use
// them: its passwords are in the compose file, where they are the demo's own
// and go nowhere.
var secretKey = map[string]bool{
	"secret_access_key": true,
	"session_token":     true,
	"password":          true,
	"key_passphrase":    true,
	"credentials_json":  true,
	"connection_string": true,
	"account_key":       true,
	"sas":               true,
}

// examples is every configuration under examples/, wherever it sits.
func examples(t *testing.T) []string {
	t.Helper()

	var out []string
	err := filepath.WalkDir("../examples", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(p) != ".yaml" {
			return nil
		}
		// compose.yaml is not one of these, and is checked by the demo running.
		if filepath.Base(p) == "compose.yaml" {
			return nil
		}

		out = append(out, p)

		return nil
	})
	require.NoError(t, err)

	return out
}

// named is every environment variable an example asks for.
var envRef = regexp.MustCompile(`\$\{env:([A-Za-z_][A-Za-z0-9_]*)`)

func named(t *testing.T, path string) []string {
	t.Helper()

	var out []string
	for _, m := range envRef.FindAllStringSubmatch(read(t, path), -1) {
		out = append(out, m[1])
	}

	return out
}

func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	require.NoError(t, err)

	return string(b)
}
