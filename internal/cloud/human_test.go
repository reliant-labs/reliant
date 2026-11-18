package cloud

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/credentials"
)

func TestHumanCredentialStorageDoesNotReplaceDelegatedCapability(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	t.Setenv("FORGE_CREDENTIAL_HELPER", "")
	path, err := CredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	ep := Endpoint{URL: "https://cp.example"}
	if err := credentials.Store(path, ep.URL, ClientID, credentials.Credential{Token: "delegated"}); err != nil {
		t.Fatal(err)
	}
	if err := credentials.Store(path, ep.URL, HumanClientID, credentials.Credential{Kind: "human", Token: "person"}); err != nil {
		t.Fatal(err)
	}
	cred, err := ResolveCredential("", ep)
	if err != nil || cred.Token != "person" || cred.Kind != "human" {
		t.Fatalf("identity lookup: %+v %v", cred, err)
	}
	cred, err = ResolveCredential("explicit-delegation", ep)
	if err != nil || cred.Token != "explicit-delegation" || cred.Kind == "human" {
		t.Fatal("explicit capability reclassified")
	}
	if c, err := credentials.Lookup(path, ep.URL, ClientID); err != nil || c.Token != "delegated" {
		t.Fatal("identity login overwrote automation")
	}
}
