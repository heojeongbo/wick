package s3

import (
	"context"

	wickhttp "github.com/heojeongbo/wick/sink/http"

	"github.com/heojeongbo/wick/sink"
)

// Kind is what a configuration calls this one.
const Kind = "s3"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about an S3-compatible sink.
type Spec struct {
	// Bucket is where things go, and Prefix is put in front of every name so
	// that one bucket can hold more than one thing.
	Bucket string `yaml:"bucket"`
	Prefix string `yaml:"prefix"`

	// Endpoint is the store, and is AWS itself when it is not said. Region is
	// what the store is told it is; many of the compatible ones do not care
	// and still require one.
	Endpoint string `yaml:"endpoint"`
	Region   string `yaml:"region"`
	// PathStyle puts the bucket in the path rather than in the host name,
	// which is what a store reached by address rather than by name needs.
	PathStyle bool `yaml:"path_style"`

	// Left unsaid, the machine's own credentials are used -- an instance role,
	// a profile, the environment -- which is the better way round wherever it
	// is available. Written here, write them as "${env:...}" so that they are
	// named in the file and not held in it.
	AccessKeyId     string `yaml:"access_key_id"`
	SecretAccessKey string `yaml:"secret_access_key"`
	SessionToken    string `yaml:"session_token"`

	// PartSize and Concurrency are how a large object is broken up.
	PartSize    int64 `yaml:"part_size"`
	Concurrency int   `yaml:"concurrency"`

	// RateLimit is bytes per second; no limit when it is not said.
	RateLimit int64 `yaml:"rate_limit"`

	CaFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	Insecure bool   `yaml:"insecure"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(ctx, Options{
		Bucket:          s.Bucket,
		Prefix:          s.Prefix,
		Endpoint:        s.Endpoint,
		Region:          s.Region,
		PathStyle:       s.PathStyle,
		AccessKeyID:     s.AccessKeyId,
		SecretAccessKey: s.SecretAccessKey,
		SessionToken:    s.SessionToken,
		PartSize:        s.PartSize,
		Concurrency:     s.Concurrency,
		RateLimit:       s.RateLimit,
		TLS: wickhttp.TLS{
			CAFile:   s.CaFile,
			CertFile: s.CertFile,
			KeyFile:  s.KeyFile,
			Insecure: s.Insecure,
		},
	})
}

var _ sink.Spec = (*Spec)(nil)
