package s3

import (
	"context"

	wickhttp "github.com/heojeongbo/wick/sink/http"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/size"
)

// Kind is what a configuration calls this one.
const Kind = "s3"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about an S3-compatible sink.
type Spec struct {
	sink.Typed `yaml:",inline"`

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

	// Profile is the one to read out of the shared configuration, for a
	// machine that has more than one.
	Profile string `yaml:"profile"`

	// AssumeRoleArn is a role to take on once the above has said who this is.
	// It is how a machine reaches a bucket in an account it has no identity
	// in. RoleSessionName is what the session is called in that account's
	// logs, and is the only thing there that says which machine.
	AssumeRoleArn   string `yaml:"assume_role_arn"`
	RoleSessionName string `yaml:"role_session_name"`
	// ExternalId is the secret the other account's role asks for, and is what
	// stops one caller's role being used by another who knows its name.
	ExternalId string `yaml:"external_id"`

	// WebIdentityTokenFile holds a token from something that already knows who
	// this is -- a Kubernetes service account, a CI runner -- and is exchanged
	// for the role. It needs assume_role_arn and replaces the keys above.
	WebIdentityTokenFile string `yaml:"web_identity_token_file"`

	// PartSize and Concurrency are how a large object is broken up.
	PartSize    size.Bytes `yaml:"part_size"`
	Concurrency int        `yaml:"concurrency"`

	// RateLimit is bytes per second; no limit when it is not said.
	RateLimit size.Bytes `yaml:"rate_limit"`

	CaFile   string `yaml:"ca_file"`
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	Insecure bool   `yaml:"insecure"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(ctx, Options{
		Bucket:               s.Bucket,
		Prefix:               s.Prefix,
		Endpoint:             s.Endpoint,
		Region:               s.Region,
		PathStyle:            s.PathStyle,
		AccessKeyID:          s.AccessKeyId,
		SecretAccessKey:      s.SecretAccessKey,
		SessionToken:         s.SessionToken,
		Profile:              s.Profile,
		AssumeRoleARN:        s.AssumeRoleArn,
		RoleSessionName:      s.RoleSessionName,
		ExternalID:           s.ExternalId,
		WebIdentityTokenFile: s.WebIdentityTokenFile,
		PartSize:             s.PartSize.Int64(),
		Concurrency:          s.Concurrency,
		RateLimit:            s.RateLimit.Int64(),
		TLS: wickhttp.TLS{
			CAFile:   s.CaFile,
			CertFile: s.CertFile,
			KeyFile:  s.KeyFile,
			Insecure: s.Insecure,
		},
	})
}

var _ sink.Spec = (*Spec)(nil)
