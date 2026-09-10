package config

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"github.com/lesomnus/mkot"
	"github.com/lesomnus/z"
)

var DefaultConfigPaths = []string{
	"wick.yaml",
	"wick.yml",
}

type Config struct {
	path string

	Identity IdentityConfig `yaml:"identity"`
	Journal  JournalConfig  `yaml:"journal"`
	Health   HealthConfig   `yaml:"health"`

	// Sinks are the places things go, by name, so that a spool can point at
	// one and two spools can share it.
	Sinks map[string]SinkConfig `yaml:"sinks"`

	// Spools are the places watched.
	Spools []SpoolConfig `yaml:"spools"`

	Otel OtelConfig `yaml:"otel"`
}

func ReadFromFile(p string) (*Config, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		// Not wrapped. A *fs.PathError already begins "open <path>:", so
		// putting "open" in front of it produced "read config: open: open
		// /x.yaml: no such file or directory".
		return nil, err
	}

	// "${env:NAME}" and "${env:NAME:-default}" are resolved before the file is
	// read, the way the OpenTelemetry Collector resolves them, so a secret can
	// be named in the file without being written in it. A name that is neither
	// set nor given a default is an error rather than an empty string. Write
	// "$$" for a literal dollar sign.
	b, err = mkot.ExpandEnv(b)
	if err != nil {
		return nil, z.Err(err, "expand")
	}

	var c Config
	if err := Decode(b, &c); err != nil {
		return nil, z.Err(err, "decode")
	}

	c.path = p
	return &c, nil
}

// Decode reads YAML into v, refusing a key that nothing answers to.
//
// Refusing rather than ignoring, for the same reason an environment variable
// that starts with the prefix and answers to nothing is reported: that is what
// a typo looks like, and a setting that was silently ignored is a daemon that
// quietly does not do the thing it was told to. `settle_for` written under
// `source` instead of `carry` is a real afternoon.
func Decode(b []byte, v any) error {
	return yaml.UnmarshalWithOptions(b, v, yaml.DisallowUnknownField())
}

func (c *Config) Path() string {
	return c.path
}

// Evaluate completes the configuration and says whether it is usable: it fills
// in what was left unsaid, and refuses what cannot be meant.
//
// What is here is everything that is true of the configuration as a document.
// What is not here is anything that depends on the machine it is read on -- a
// directory that is not there, a source that cannot delete -- because that is a
// question for the thing that opens them, and refusing it here would make
// `wick config` fail on a machine that was only being asked what it thinks.
func (c *Config) Evaluate() error {
	if c.Identity.Name == "" {
		// An error here would refuse to start on a machine whose hostname
		// cannot be read, which is a machine that can still carry files
		// perfectly well. The name is only how they are told apart.
		if h, err := hostname(); err == nil {
			c.Identity.Name = h
		} else {
			c.Identity.Name = "unknown"
		}
	}

	z.FallbackP(&c.Journal.Path, DefaultJournalPath)

	seen := map[string]bool{}
	for i := range c.Spools {
		s := &c.Spools[i]
		if err := s.evaluate(c.Sinks); err != nil {
			return err
		}
		if seen[s.Name] {
			return fmt.Errorf("there are two spools called %q, and their records would be each other's", s.Name)
		}
		seen[s.Name] = true
	}

	return nil
}

// DefaultJournalPath is where the records go when nothing says otherwise.
//
// Beside the configuration rather than under /var, because a daemon that makes
// a directory under /var is a daemon that has to be run as somebody who may,
// and this one has no other reason to be.
const DefaultJournalPath = "wick.db"
