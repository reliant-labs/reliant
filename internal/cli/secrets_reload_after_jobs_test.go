package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/secrets"
)

// A host job may publish into the env's FILE store — idp-provision keeps the
// login broker's token there — and `forge env up` read the store before the
// job ran. The services started after the jobs must see what the jobs wrote,
// or a fresh project's first `forge env up` starts an API without the token
// it was just given and native sign-in 404s.
func TestReloadSecretsAfterJobs_SeesWhatAJobWrote(t *testing.T) {
	projectDir := t.TempDir()
	store := filepath.Join(projectDir, "secrets", "dev.yaml")
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("DATABASE_URL:\nIDP_BROKER_TOKEN:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entities := &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "file", Path: "secrets/dev.yaml"}}

	before, err := secretProviderFromEntities(entities, projectDir)
	if err != nil {
		t.Fatal(err)
	}

	// The job runs and publishes the token.
	if err := os.WriteFile(store, []byte("DATABASE_URL:\nIDP_BROKER_TOKEN: tok-from-job\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if v, _ := before.Resolve("IDP_BROKER_TOKEN"); v != "" {
		t.Fatalf("precondition: the provider read before the job should hold the empty slot, got %q", v)
	}
	after, err := reloadSecretsAfterJobs(before, entities, projectDir)
	if err != nil {
		t.Fatalf("reloadSecretsAfterJobs: %v", err)
	}
	if v, ok := after.Resolve("IDP_BROKER_TOKEN"); !ok || v != "tok-from-job" {
		t.Fatalf("the services would start without the job's value: got %q (ok=%v)", v, ok)
	}
}

// A store a job cannot write is left exactly as it was — in particular a
// hosted store pulled into memory for this run must not be re-pulled or
// dropped.
func TestReloadSecretsAfterJobs_LeavesNonFileProvidersAlone(t *testing.T) {
	pulled := secrets.NewPulledProvider(map[string]string{"K": "v"})
	got, err := reloadSecretsAfterJobs(pulled, &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "hosted"}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Resolve("K"); v != "v" {
		t.Fatalf("a pulled provider was replaced: %q", v)
	}
}
