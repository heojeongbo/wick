package azure

import (
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/stretchr/testify/require"
)

// Azure canonicalizes metadata names: what is written as "sha256" comes back as
// "Sha256". A name that comes back spelled differently from the way it went in
// is a read-back that never matches, which is the one failure this sink's whole
// claim about hashes rests on.
func TestFoldingTheMetadataNames(t *testing.T) {
	x := require.New(t)

	x.Equal(map[string]string{"sha256": "sha256:beef"}, values(pointers(map[string]string{"sha256": "sha256:beef"})))
	x.Equal(map[string]string{"sha256": "sha256:beef"}, values(map[string]*string{"Sha256": ptr("sha256:beef")}))

	// Nothing said is nothing given, both ways.
	x.Nil(pointers(nil))
	x.Nil(values(nil))

	// A name with no value is not a name: the SDK does not produce one, and
	// dereferencing it would be the only way to find that out the hard way.
	x.Empty(values(map[string]*string{"Sha256": nil}))
}

func ptr(s string) *string { return &s }

// A managed identity the platform will not give out is a refusal at startup,
// not a carry that fails later. The real one is built never to fail, so the
// branch is reached the only way it can be.
func TestAnIdentityThatCannotBeHad(t *testing.T) {
	x := require.New(t)

	was := newDefaultCredential
	newDefaultCredential = func(*azidentity.DefaultAzureCredentialOptions) (azcore.TokenCredential, error) {
		return nil, errors.New("no identity here")
	}
	t.Cleanup(func() { newDefaultCredential = was })

	_, err := New(t.Context(), Options{Account: "wick", Container: "c"})
	x.ErrorContains(err, "work out how to reach the store")
}
