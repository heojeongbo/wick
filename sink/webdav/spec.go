package webdav

import (
	"context"

	"github.com/heojeongbo/wick/sink"
	wickhttp "github.com/heojeongbo/wick/sink/http"
	"github.com/heojeongbo/wick/size"
)

// Kind is what a configuration calls this one.
const Kind = "webdav"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about a webdav sink.
type Spec struct {
	sink.Typed `yaml:",inline"`

	// Endpoint is the collection things are put under.
	Endpoint string `yaml:"endpoint"`

	// Headers are sent with every request, as they are written.
	Headers map[string]string `yaml:"headers"`
	// Username and Password are Basic authentication, which is what most of
	// these servers ask for.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// TokenFile holds a bearer token and is read at every request.
	TokenFile string `yaml:"token_file"`

	// RateLimit is bytes per second; no limit when it is not said.
	RateLimit size.Bytes `yaml:"rate_limit"`

	CaFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	Insecure bool   `yaml:"insecure"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(Options{
		Endpoint: s.Endpoint,
		Auth: wickhttp.Auth{
			Headers:   s.Headers,
			Username:  s.Username,
			Password:  s.Password,
			TokenFile: s.TokenFile,
		},
		RateLimit: s.RateLimit.Int64(),
		TLS: wickhttp.TLS{
			CAFile:   s.CaFile,
			CertFile: s.CertFile,
			KeyFile:  s.KeyFile,
			Insecure: s.Insecure,
		},
	})
}

var _ sink.Spec = (*Spec)(nil)
