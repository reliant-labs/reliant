package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// sampleSecretProviderJSON is an entity render carrying a FileSecrets
// secret_provider declaration plus a mix of cluster/host services with
// declared secret_refs. Exercises the parse + wiring helpers.
const sampleSecretProviderJSON = `{"output": {
  "workloads": [
    {
      "name": "api", "kind": "service",
      "runtime": {"type": "cluster", "cluster": "c", "namespace": "dev"},
      "spec": {"kind": "service", "env": [
        {"name": "GITHUB_CLIENT_ID", "secretRef": {"name": "github-oauth", "key": "client-id"}},
        {"name": "TOKEN", "secretRef": {"name": "creds"}},
        {"name": "LOG_LEVEL", "value": "debug"}
      ]}
    },
    {
      "name": "worker", "kind": "worker",
      "runtime": {"type": "host", "runner": "go-run"},
      "spec": {"kind": "worker", "env": [
        {"name": "STRIPE_KEY", "secretRef": {"name": "stripe"}}
      ]}
    }
  ],
  "secret_provider": {"type": "file", "path": "secrets/dev.yaml"}
}}`

func TestParseKCLEntities_SecretProvider(t *testing.T) {
	e, err := parseKCLEntities([]byte(sampleSecretProviderJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	if e.SecretProvider == nil {
		t.Fatal("SecretProvider is nil")
	}
	if e.SecretProvider.Type != "file" {
		t.Errorf("type: got %q, want file", e.SecretProvider.Type)
	}
	if e.SecretProvider.Path != "secrets/dev.yaml" {
		t.Errorf("path: got %q, want secrets/dev.yaml", e.SecretProvider.Path)
	}
}

func TestParseKCLEntities_SecretProvider_AbsentNil(t *testing.T) {
	// A contract with no secret_provider key must parse to nil.
	e, _ := loadContract(t, "compose")
	if e.SecretProvider != nil {
		t.Errorf("SecretProvider should be nil when absent, got %+v", e.SecretProvider)
	}
}

func TestSecretRefsFromEntities(t *testing.T) {
	e, err := parseKCLEntities([]byte(sampleSecretProviderJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	refs := secretRefsFromEntities(e)
	// api: GITHUB_CLIENT_ID + TOKEN; worker: STRIPE_KEY. LOG_LEVEL has no
	// secret_ref so it's excluded.
	if len(refs) != 3 {
		t.Fatalf("got %d refs, want 3: %+v", len(refs), refs)
	}
	byName := map[string]struct{ secret, key string }{}
	for _, r := range refs {
		byName[r.EnvName] = struct{ secret, key string }{r.SecretName, r.SecretKey}
	}
	if got := byName["GITHUB_CLIENT_ID"]; got.secret != "github-oauth" || got.key != "client-id" {
		t.Errorf("GITHUB_CLIENT_ID ref wrong: %+v", got)
	}
	// SecretKey falls back to EnvName when KCL secret_key is empty.
	if got := byName["TOKEN"]; got.secret != "creds" || got.key != "TOKEN" {
		t.Errorf("TOKEN ref should default key to EnvName: %+v", got)
	}
	if _, ok := byName["LOG_LEVEL"]; ok {
		t.Error("LOG_LEVEL (no secret_ref) wrongly included")
	}
}

func TestSecretRefsForK8sServices(t *testing.T) {
	e, err := parseKCLEntities([]byte(sampleSecretProviderJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	refs := secretRefsForK8sServices(e)
	// Only the cluster service "api" contributes (2 refs). The host
	// service "worker" (STRIPE_KEY) is excluded.
	if len(refs) != 2 {
		t.Fatalf("got %d k8s refs, want 2: %+v", len(refs), refs)
	}
	for _, r := range refs {
		if r.EnvName == "STRIPE_KEY" {
			t.Error("host-service ref STRIPE_KEY wrongly included in k8s refs")
		}
	}
}

func TestSecretProviderFromEntities_StorePathResolved(t *testing.T) {
	dir := t.TempDir()
	// Populate the store so the provider loads it; assert the value
	// resolves (proving the relative path was joined against projectDir).
	store := filepath.Join(dir, "secrets", "dev.yaml")
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	body := "GITHUB_CLIENT_ID: abc\nTOKEN: tok\nSTRIPE_KEY: sk\n"
	if err := os.WriteFile(store, []byte(body), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	e, err := parseKCLEntities([]byte(sampleSecretProviderJSON))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	p, err := secretProviderFromEntities(e, dir)
	if err != nil {
		t.Fatalf("secretProviderFromEntities: %v", err)
	}
	if p.Kind() != "file" {
		t.Fatalf("Kind: got %q, want file", p.Kind())
	}
	if v, ok := p.Resolve("GITHUB_CLIENT_ID"); !ok || v != "abc" {
		t.Errorf("Resolve through resolved path: got (%q,%v), want (abc,true)", v, ok)
	}
}

func TestSecretProviderFromEntities_NoneNoop(t *testing.T) {
	e, _ := loadContract(t, "compose")
	p, err := secretProviderFromEntities(e, t.TempDir())
	if err != nil {
		t.Fatalf("secretProviderFromEntities: %v", err)
	}
	if p.Kind() != "none" {
		t.Errorf("Kind: got %q, want none", p.Kind())
	}
}
