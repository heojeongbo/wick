package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goccy/go-yaml"
	"github.com/stretchr/testify/require"

	"github.com/heojeongbo/wick/cmd/config"
	_ "github.com/heojeongbo/wick/sink/dir"
	_ "github.com/heojeongbo/wick/sink/http"
	_ "github.com/heojeongbo/wick/sink/s3"
	_ "github.com/heojeongbo/wick/source/dir"
)

// read writes a configuration to a file and reads it back the way the app
// would, so that a test about what a configuration means is not also a test
// about how it got here.
func read(t *testing.T, body string) (*config.Config, error) {
	t.Helper()

	p := filepath.Join(t.TempDir(), "wick.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	c, err := config.ReadFromFile(p)
	if err != nil {
		return nil, err
	}

	return c, c.Evaluate()
}

const oneOfEach = `
identity:
  name: thor-top
journal:
  path: /tmp/wick.db
sinks:
  cloud:
    type: s3
    bucket: recordings
    region: auto
    part_size: 16MiB
    rate_limit: 20MiB
  onsite:
    type: http
    endpoint: https://collector.example.invalid/drop
    headers:
      Authorization: Bearer sesame
spools:
  - name: recordings
    source:
      type: dir
      path: /var/log/app/rec
      include: ["*.rec"]
      min_size: 1KiB
    to:
      - sink: cloud
        name_as: "{host}/{yyyy}/{mm}/{dd}/{name}"
      - sink: onsite
        name_as: "{name}"
        verify: false
    trigger:
      every: 15m
      count: 10
      bytes: 2GiB
      free_below: 20GiB
    carry:
      settle_for: 10s
      workers: 2
      attempts: 5
    retain:
      after: delete
      grace: 24h
      free_below: 20GiB
`

func TestOneOfEach(t *testing.T) {
	x := require.New(t)

	c, err := read(t, oneOfEach)
	x.NoError(err)

	x.Equal("thor-top", c.Identity.Name)
	x.Equal("/tmp/wick.db", c.Journal.Path)

	x.Len(c.Sinks, 2)
	x.Equal("s3", c.Sinks["cloud"].Type)
	x.Equal("http", c.Sinks["onsite"].Type)

	x.Len(c.Spools, 1)
	s := c.Spools[0]
	x.Equal("dir", s.Source.Type)
	x.Len(s.To, 2)
	x.True(s.To[0].Verifies())
	x.False(s.To[1].Verifies())

	x.Equal(15*time.Minute, s.Trigger.Every)
	x.Equal(
		"any of [every 15m0s, 10 or more waiting, 2147483648 bytes or more waiting, less than 21474836480 bytes free]",
		fmt.Sprint(s.Trigger.Trigger()),
	)

	p, err := s.Retain.Policy()
	x.NoError(err)
	x.Equal(
		"first of [delete when less than 21474836480 bytes are free, delete after 24h0m0s]",
		fmt.Sprint(p),
	)
}

// It has to come back out as something that could be pasted back in.
func TestItGoesBackOutAgain(t *testing.T) {
	x := require.New(t)

	c, err := read(t, oneOfEach)
	x.NoError(err)

	b, err := yaml.Marshal(c)
	x.NoError(err)

	out := string(b)
	x.Contains(out, "type: s3")
	x.Contains(out, "type: http")
	x.Contains(out, "type: dir")
	x.Contains(out, "16MiB")
	x.Contains(out, "Bearer sesame")

	// And back in.
	again, err := read(t, out)
	x.NoError(err)
	x.Equal(c.Spools[0].Name, again.Spools[0].Name)
	x.Equal(c.Sinks["cloud"].Type, again.Sinks["cloud"].Type)
}

// Nothing said is nothing given: a configuration that names no spool is a
// daemon that does nothing, which is a legitimate thing to hand somebody.
func TestNothingSaid(t *testing.T) {
	x := require.New(t)

	c, err := read(t, "")
	x.NoError(err)
	x.Empty(c.Spools)
	x.Equal(config.DefaultJournalPath, c.Journal.Path)
	// The hostname, whatever it is on the machine running this.
	x.NotEmpty(c.Identity.Name)
}

func TestWhatCannotBeMeant(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		says string
	}{
		{
			"a sink that does not say what kind it is",
			"sinks:\n  cloud:\n    bucket: b\n",
			"has to say what kind it is",
		},
		{
			"a sink of a kind this build was not given",
			"sinks:\n  cloud:\n    type: carrier-pigeon\n",
			"not a kind of sink",
		},
		{
			"a sink whose settings are not its settings",
			"sinks:\n  cloud:\n    type: s3\n    part_size: [1, 2]\n",
			"read the settings of the s3 sink",
		},
		{
			"a source that does not say what kind it is",
			spoolWith("source", "    source:\n      path: /tmp\n"),
			"has to say what kind it is",
		},
		{
			"a source of a kind this build was not given",
			spoolWith("source", "    source:\n      type: carrier-pigeon\n"),
			"not a kind of source",
		},
		{
			// The decoder rebuilds the message with the line and column, which
			// is more use than anything this could have wrapped it with, so
			// what is checked is that it names the setting.
			"a source whose settings are not its settings",
			spoolWith("source", "    source:\n      type: dir\n      recursive: [1]\n"),
			"recursive",
		},
		{
			"a spool with no name",
			"spools:\n  - source: {type: dir, path: /tmp}\n    to: [{sink: cloud}]\n",
			"has to have a name",
		},
		{
			"a spool with no source",
			"spools:\n  - name: a\n    to: [{sink: cloud}]\n",
			"no source",
		},
		{
			"a spool with nowhere to go",
			"spools:\n  - name: a\n    source: {type: dir, path: /tmp}\n",
			"nowhere to carry to",
		},
		{
			"a destination that does not say which sink",
			spoolWith("to", "    to:\n      - name_as: \"{name}\"\n"),
			"does not say which sink",
		},
		{
			"a destination named twice",
			spoolWith("to", "    to:\n      - sink: cloud\n      - sink: cloud\n"),
			"twice",
		},
		{
			"a sink nothing under `sinks` is called",
			spoolWith("to", "    to:\n      - sink: nowhere\n"),
			"nothing under `sinks` is called that",
		},
		{
			"a name made of something that is not a name",
			spoolWith("to", "    to:\n      - sink: cloud\n        name_as: \"{nope}\"\n"),
			"cannot name what it carries",
		},
		{
			"two spools under one name",
			twoSpools,
			"two spools called",
		},
		{
			"a length of time to sit still that is not one",
			spoolWith("carry", "    carry: {settle_for: -1s}\n"),
			"not a length of time",
		},
		{
			"a number of things at once that is not one",
			spoolWith("carry", "    carry: {workers: -1}\n"),
			"at once",
		},
		{
			"a number of attempts that is not one",
			spoolWith("carry", "    carry: {attempts: -1}\n"),
			"would try",
		},
		{
			"an interval that is not one",
			spoolWith("trigger", "    trigger: {every: -1s}\n"),
			"would carry every",
		},
		{
			"a number of bytes below zero",
			spoolWith("trigger", "    trigger: {bytes: -1}\n"),
			"below zero",
		},
		{
			"a window that runs backwards",
			spoolWith("retain", "    retain: {after: keep, grace: -1h}\n"),
			"would hold what it has carried",
		},
		{
			"something that is not what becomes of a file",
			spoolWith("retain", "    retain: {after: incinerate}\n"),
			"is not what becomes of a file",
		},
		{
			"moving with nowhere to move to",
			spoolWith("retain", "    retain: {after: move}\n"),
			"nowhere to move to",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := require.New(t)

			_, err := read(t, tc.body)
			x.ErrorContains(err, tc.says)
		})
	}
}

// spoolWith is one whole configuration whose only spool has the named section
// replaced, so that each case above says only the thing it is about.
//
// Replaced rather than appended: a second `source:` is a duplicate key, and
// then every one of these would be testing the YAML parser instead.
func spoolWith(section, body string) string {
	parts := map[string]string{
		"source": "    source:\n      type: dir\n      path: /tmp/in\n",
		"to":     "    to:\n      - sink: cloud\n",
	}
	parts[section] = body

	out := "sinks:\n  cloud:\n    type: dir\n    path: /tmp/cloud\nspools:\n  - name: recordings\n"
	for _, k := range []string{"source", "to", "trigger", "carry", "retain"} {
		out += parts[k]
	}

	return out
}

const twoSpools = `
sinks:
  cloud: {type: dir, path: /tmp/cloud}
spools:
  - name: recordings
    source: {type: dir, path: /tmp/a}
    to: [{sink: cloud}]
  - name: recordings
    source: {type: dir, path: /tmp/b}
    to: [{sink: cloud}]
`

// The two things a sink says about itself before anybody has made one.
func TestASinkThatWasNotRead(t *testing.T) {
	x := require.New(t)

	b, err := config.SinkConfig{Type: "dir"}.MarshalYAML()
	x.NoError(err)
	x.Contains(string(b), "type: dir")

	b, err = config.SourceConfig{Type: "dir"}.MarshalYAML()
	x.NoError(err)
	x.Contains(string(b), "type: dir")
}

func TestSomethingThatIsNotYAMLAtAll(t *testing.T) {
	x := require.New(t)

	_, err := read(t, "sinks:\n  cloud: [not, a, mapping]\n")
	x.ErrorContains(err, "read what kind of sink this is")

	_, err = read(t, spoolWith("source", "    source: [not, a, mapping]\n"))
	x.ErrorContains(err, "read what kind of source this is")
}
