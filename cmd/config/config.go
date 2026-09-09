package config

import (
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

	Otel OtelConfig `yaml:"otel"`
}

func ReadFromFile(p string) (*Config, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, z.Err(err, "open")
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
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, z.Err(err, "decode")
	}

	c.path = p
	return &c, nil
}

func (c *Config) Path() string {
	return c.path
}

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

	return nil
}
