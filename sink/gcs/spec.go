package gcs

import (
	"context"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/size"
)

// Kind is what a configuration calls this one.
const Kind = "gcs"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about a Google Cloud Storage sink.
type Spec struct {
	sink.Typed `yaml:",inline"`

	// Bucket is where things go, and Prefix is put in front of every name.
	Bucket string `yaml:"bucket"`
	Prefix string `yaml:"prefix"`

	// Left unsaid, the machine's own credentials are used -- workload identity
	// on GKE, the metadata server on Compute Engine -- which is the way round
	// to prefer: the credential is short-lived and not in a file on a machine
	// somebody can walk up to.
	CredentialsFile string `yaml:"credentials_file"`
	// CredentialsJson is the same thing held here rather than named, for a
	// deployment whose secrets arrive as environment variables. Write it as
	// "${env:...}".
	CredentialsJson string `yaml:"credentials_json" wick:"secret"`

	// Endpoint is the store, and is Google's own when it is not said.
	Endpoint string `yaml:"endpoint"`

	// ChunkSize is how much is buffered and sent at a time.
	ChunkSize size.Bytes `yaml:"chunk_size"`

	// RateLimit is bytes per second; no limit when it is not said.
	RateLimit size.Bytes `yaml:"rate_limit"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(ctx, Options{
		Bucket:          s.Bucket,
		Prefix:          s.Prefix,
		CredentialsFile: s.CredentialsFile,
		CredentialsJSON: s.CredentialsJson,
		Endpoint:        s.Endpoint,
		ChunkSize:       int(s.ChunkSize.Int64()),
		RateLimit:       s.RateLimit.Int64(),
	})
}

var _ sink.Spec = (*Spec)(nil)
