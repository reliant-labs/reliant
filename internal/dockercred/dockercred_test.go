package dockercred

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"oras.land/oras-go/v2/registry/remote/auth"
)

// writeConfig writes a docker config.json whose credsStore names a helper
// that does not exist, so any read that reaches the native store fails loudly
// instead of consulting this machine's keychain.
func writeConfig(t *testing.T, credHelpers string) string {
	t.Helper()
	dir := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("forge:rlat_from_login"))
	cfg := `{
  "credsStore": "forge-test-no-such-helper",
  "credHelpers": ` + credHelpers + `,
  "auths": {"registry.example.com": {"auth": "` + auth + `"}}
}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An empty credHelpers entry pins the host to config.json, as docker reads
// it: the credential `docker login` wrote there is the one returned, and the
// credsStore is never consulted. This is the anonymous deploy's config, and
// reading the credsStore instead is what made every oras push 401.
func TestEmptyCredHelperReadsTheConfigFile(t *testing.T) {
	cred, err := mustCredential(t, writeConfig(t, `{"registry.example.com": ""}`))(context.Background(), "registry.example.com")
	if err != nil {
		t.Fatalf("Get: %v (the credsStore was consulted for a host pinned to the file)", err)
	}
	if cred.Username != "forge" || cred.Password != "rlat_from_login" {
		t.Fatalf("got %+v, want the credential docker login wrote to config.json", cred)
	}
}

// A host with no credHelpers entry still goes to credsStore, exactly as
// docker would send it: the pin is per host, not a switch to the file.
func TestUnpinnedHostKeepsTheCredsStore(t *testing.T) {
	if _, err := mustCredential(t, writeConfig(t, `{"registry.example.com": ""}`))(context.Background(), "other.example.com"); err == nil {
		t.Fatal("Get of an unpinned host succeeded without its credsStore helper; it must be asked")
	}
}

// With no empty entry every host keeps oras's reading, credsStore included.
func TestNoPinKeepsTheCredsStore(t *testing.T) {
	if _, err := mustCredential(t, writeConfig(t, `{"gcr.io": "gcloud"}`))(context.Background(), "registry.example.com"); err == nil {
		t.Fatal("a host with no credHelpers entry was read from the file; its credsStore must be asked")
	}
}

// Credential follows $DOCKER_CONFIG, which is how an anonymous deploy
// points both docker and oras at its throwaway config.
func TestNewStoreFromDockerFollowsDockerConfig(t *testing.T) {
	path := writeConfig(t, `{"registry.example.com": ""}`)
	t.Setenv("DOCKER_CONFIG", filepath.Dir(path))
	credential, err := Credential()
	if err != nil {
		t.Fatal(err)
	}
	cred, err := credential(context.Background(), "registry.example.com")
	if err != nil || cred.Password != "rlat_from_login" {
		t.Fatalf("got %+v, %v; want the throwaway config's credential", cred, err)
	}
}

func TestMissingConfigIsEmpty(t *testing.T) {
	if _, err := mustCredential(t, filepath.Join(t.TempDir(), "config.json"))(context.Background(), "registry.example.com"); err != nil {
		t.Fatalf("Get against a missing config: %v", err)
	}
}

func mustCredential(t *testing.T, configPath string) auth.CredentialFunc {
	t.Helper()
	credential, err := CredentialFrom(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return credential
}
