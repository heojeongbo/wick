package sftp

import (
	"context"
	"time"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/size"
)

// Kind is what a configuration calls this one.
const Kind = "sftp"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about an sftp sink.
type Spec struct {
	sink.Typed `yaml:",inline"`

	// Address is the server, host and port; the port is 22 when it is not
	// said. User is the account on it.
	Address string `yaml:"address"`
	User    string `yaml:"user"`

	// KeyFile is a private key and KeyPassphrase is what it is locked with.
	// Password is the other way. One of the two, not both. Write either as
	// "${env:...}" so that it is named in the file and not held in it.
	KeyFile       string `yaml:"key_file"`
	KeyPassphrase string `yaml:"key_passphrase" wick:"secret"`
	Password      string `yaml:"password" wick:"secret"`

	// KnownHosts is the file the server's key is checked against, and is
	// required: a host key nobody checks is a sink that will one day be
	// somebody else.
	KnownHosts string `yaml:"known_hosts"`
	// InsecureIgnoreHostKey stops it being checked. Named so that turning it
	// on is a decision somebody wrote down.
	InsecureIgnoreHostKey bool `yaml:"insecure_ignore_host_key"`

	// Path is the directory things go under, and is where the account lands
	// when it is not said.
	Path string `yaml:"path"`

	// Timeout is how long reaching the server may take. It is not a deadline
	// on carrying a file.
	Timeout time.Duration `yaml:"timeout"`

	// RateLimit is bytes per second; no limit when it is not said.
	RateLimit size.Bytes `yaml:"rate_limit"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(Options{
		Address:               s.Address,
		User:                  s.User,
		KeyFile:               s.KeyFile,
		KeyPassphrase:         s.KeyPassphrase,
		Password:              s.Password,
		KnownHosts:            s.KnownHosts,
		InsecureIgnoreHostKey: s.InsecureIgnoreHostKey,
		Path:                  s.Path,
		Timeout:               s.Timeout,
		RateLimit:             s.RateLimit.Int64(),
	})
}

var _ sink.Spec = (*Spec)(nil)
