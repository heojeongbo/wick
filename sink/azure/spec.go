package azure

import (
	"context"

	"github.com/heojeongbo/wick/sink"
	"github.com/heojeongbo/wick/size"
)

// Kind is what a configuration calls this one.
const Kind = "azure"

func init() { sink.Register(Kind, func() sink.Spec { return &Spec{} }) }

// A Spec is what a configuration says about an Azure Blob Storage sink.
type Spec struct {
	sink.Typed `yaml:",inline"`

	// Account is the storage account and Container is the container in it.
	// ServiceUrl is worked out from the account when it is not said, which is
	// what a deployment reaching a private endpoint or an emulator sets.
	Account    string `yaml:"account"`
	Container  string `yaml:"container"`
	ServiceUrl string `yaml:"service_url"`

	// Prefix is put in front of every name.
	Prefix string `yaml:"prefix"`

	// One of these, or none of them. None means a managed identity, which is
	// the way round to prefer: the platform mints it, rotates it and can take
	// it away. Write any of them as "${env:...}".
	ConnectionString string `yaml:"connection_string"`
	AccountKey       string `yaml:"account_key"`
	Sas              string `yaml:"sas"`

	// BlockSize and Concurrency are how a large blob is broken up. Each
	// concurrent upload holds a buffer of one block.
	BlockSize   size.Bytes `yaml:"block_size"`
	Concurrency int        `yaml:"concurrency"`

	// RateLimit is bytes per second; no limit when it is not said.
	RateLimit size.Bytes `yaml:"rate_limit"`
}

func (s *Spec) New(ctx context.Context) (sink.Sink, error) {
	return New(ctx, Options{
		Account:          s.Account,
		Container:        s.Container,
		ServiceURL:       s.ServiceUrl,
		Prefix:           s.Prefix,
		ConnectionString: s.ConnectionString,
		AccountKey:       s.AccountKey,
		SAS:              s.Sas,
		BlockSize:        s.BlockSize.Int64(),
		Concurrency:      s.Concurrency,
		RateLimit:        s.RateLimit.Int64(),
	})
}

var _ sink.Spec = (*Spec)(nil)
