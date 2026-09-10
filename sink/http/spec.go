package http

import (
	"context"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/size"
)

// Kind is what a configuration calls this one.
const Kind = "http"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about an http sink.
//
// This is the one for a server the fleet already runs. Nothing here names a
// cloud.
type Spec struct {
	sink.Typed `yaml:",inline"`

	// Endpoint is the address things are put under.
	Endpoint string `yaml:"endpoint"`
	// Method is PUT when it is not said; POST is the other one servers ask for.
	Method string `yaml:"method"`
	// Headers are sent with every request, as they are written. Anything the
	// far end wants to be told and this has no name for; write a secret as
	// "${env:...}" so that it is named in the file and not held in it.
	Headers map[string]string `yaml:"headers"`
	// Username and Password are Basic authentication.
	Username string `yaml:"username"`
	Password string `yaml:"password" wick:"secret"`
	// TokenFile holds a bearer token and is read at every request, so one that
	// something else renews is picked up without a restart.
	TokenFile string `yaml:"token_file"`
	// DigestHeader is the header the content hash is sent in and read back
	// out of. Unset means the read-back checks the length alone.
	DigestHeader string `yaml:"digest_header"`
	// RateLimit is bytes per second; no limit when it is not said.
	RateLimit size.Bytes `yaml:"rate_limit"`

	// CaFile is who to believe about the server.
	CaFile string `yaml:"ca_file"`
	// CertFile and KeyFile are what this machine says it is, for a server that
	// asks. They are read again at every handshake, so a certificate that
	// something else renews is picked up without a restart.
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	// Insecure stops the server's name being checked. Named so that turning it
	// on is a decision somebody wrote down.
	Insecure bool `yaml:"insecure"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(Options{
		Endpoint: s.Endpoint,
		Method:   s.Method,
		Auth: Auth{
			Headers:   s.Headers,
			Username:  s.Username,
			Password:  s.Password,
			TokenFile: s.TokenFile,
		},
		DigestHeader: s.DigestHeader,
		RateLimit:    s.RateLimit.Int64(),
		TLS: TLS{
			CAFile:   s.CaFile,
			CertFile: s.CertFile,
			KeyFile:  s.KeyFile,
			Insecure: s.Insecure,
		},
	})
}

var _ sink.Spec = (*Spec)(nil)
