package config

type HealthConfig struct {
	// Endpoint is where the probes are answered, and nothing means they are
	// not answered at all.
	Endpoint string `yaml:"endpoint"`
}
