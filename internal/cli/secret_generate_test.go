package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/secrets"
	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

func generateFixture(t *testing.T, provider *SecretProviderEntity) (*KCLEntities, string) {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "secrets", "dev.yaml")
	if provider.Type == "file" {
		provider.Path = storePath
	}
	e := &KCLEntities{
		SecretProvider: provider,
		Workloads: []WorkloadEntity{{
			Name: "api",
			Spec: deployv1alpha1.WorkloadSpec{Env: []deployv1alpha1.EnvVar{
				{Name: "RELIANT_VAULT_KEY", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "reliant-vault", Key: "vault_key"}},
				{Name: "STRIPE_KEY", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "app", Key: "stripe"}},
			}},
		}},
	}
	prev := renderEntitiesForSecrets
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) { return e, nil }
	t.Cleanup(func() { renderEntitiesForSecrets = prev })
	return e, storePath
}

var vaultSpec = map[string]secrets.GenerateSpec{
	"RELIANT_VAULT_KEY": {Bytes: 32, Encoding: "base64", Prefix: "v1:"},
}

func TestSecretEnsure_GeneratesMissingValue(t *testing.T) {
	_, store := generateFixture(t, &SecretProviderEntity{Type: "file", Generate: vaultSpec})
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("STRIPE_KEY: sk\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runSecretEnsure(context.Background(), "dev", &out); err != nil {
		t.Fatalf("ensure: %v\n%s", err, out.String())
	}
	vals, err := secrets.ReadSecretFile(store)
	if err != nil {
		t.Fatal(err)
	}
	v := vals["RELIANT_VAULT_KEY"]
	if !strings.HasPrefix(v, "v1:") {
		t.Fatalf("value %q lacks v1: prefix", v)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, "v1:"))
	if err != nil || len(raw) != 32 {
		t.Fatalf("want 32 base64 bytes, got %d (err %v)", len(raw), err)
	}
	if vals["STRIPE_KEY"] != "sk" {
		t.Fatalf("existing value disturbed: %v", vals)
	}
	// POSIX mode bits only: Windows reports 0666 for any writable file and
	// protects it with the directory's ACL instead.
	if info, _ := os.Stat(store); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v, want 0600", info.Mode().Perm())
	}
	if strings.Contains(out.String(), v) {
		t.Fatal("generated value leaked into output")
	}
	if !strings.Contains(out.String(), "generated RELIANT_VAULT_KEY") {
		t.Fatalf("output does not name the generated key:\n%s", out.String())
	}
}

func TestSecretEnsure_NeverOverwritesAndIsIdempotent(t *testing.T) {
	_, store := generateFixture(t, &SecretProviderEntity{Type: "file", Generate: vaultSpec})
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(store, []byte("STRIPE_KEY: sk\n"), 0o600)
	var out bytes.Buffer
	if err := runSecretEnsure(context.Background(), "dev", &out); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(store)
	out.Reset()
	if err := runSecretEnsure(context.Background(), "dev", &out); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(store)
	if !bytes.Equal(first, second) {
		t.Fatalf("second ensure changed the store:\n%s\n---\n%s", first, second)
	}
	if strings.Contains(out.String(), "generated") {
		t.Fatalf("second ensure claims to have generated:\n%s", out.String())
	}

	// A hand-set value is never replaced.
	os.WriteFile(store, []byte("STRIPE_KEY: sk\nRELIANT_VAULT_KEY: mine\n"), 0o600)
	if err := runSecretEnsure(context.Background(), "dev", &out); err != nil {
		t.Fatal(err)
	}
	vals, _ := secrets.ReadSecretFile(store)
	if vals["RELIANT_VAULT_KEY"] != "mine" {
		t.Fatalf("existing value overwritten: %q", vals["RELIANT_VAULT_KEY"])
	}
}

func TestSecretEnsure_NonGeneratableStillMissing(t *testing.T) {
	_, store := generateFixture(t, &SecretProviderEntity{Type: "file", Generate: vaultSpec})
	var out bytes.Buffer
	err := runSecretEnsure(context.Background(), "dev", &out)
	if err == nil || !strings.Contains(out.String(), "STRIPE_KEY") {
		t.Fatalf("want STRIPE_KEY reported missing, err=%v out=%s", err, out.String())
	}
	if strings.Contains(out.String(), "RELIANT_VAULT_KEY\n") && strings.Contains(out.String(), "have no value yet") &&
		strings.Contains(strings.SplitN(out.String(), "have no value yet", 2)[1], "RELIANT_VAULT_KEY") {
		t.Fatalf("generatable key reported missing:\n%s", out.String())
	}
	vals, _ := secrets.ReadSecretFile(store)
	if _, ok := vals["STRIPE_KEY"]; ok {
		t.Fatal("non-generatable secret was generated")
	}
}

func TestGenerateMissingSecrets_NeverForNonFileProviders(t *testing.T) {
	for _, typ := range []string{"hosted", "external", "rendered"} {
		e, store := generateFixture(t, &SecretProviderEntity{Type: typ, Generate: vaultSpec})
		var out bytes.Buffer
		got, err := generateMissingSecrets(e, store, map[string]string{}, &out)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: generated %v err %v", typ, got, err)
		}
		if _, statErr := os.Stat(store); statErr == nil {
			t.Fatalf("%s: wrote a store file", typ)
		}
	}
}

func TestSecretEnsure_HostedStaysAnError(t *testing.T) {
	generateFixture(t, &SecretProviderEntity{Type: "hosted", Generate: vaultSpec})
	if err := runSecretEnsure(context.Background(), "prod", &bytes.Buffer{}); err == nil {
		t.Fatal("ensure on a hosted provider must still refuse")
	}
}

// A generate key whose only ref is OPTIONAL is still minted: the env naming
// it in FileSecrets.generate is the statement that it wants a value, and an
// optional config field (a key required only in one auth mode) is still read
// when present. Before, it was skipped and the process refused to start.
func TestGenerateMissingSecrets_CoversOptionalRefs(t *testing.T) {
	e, store := generateFixture(t, &SecretProviderEntity{Type: "file", Generate: map[string]secrets.GenerateSpec{
		"AUTH_API_KEY": {Bytes: 24, Encoding: "hex", Prefix: "rdev_"},
	}})
	e.Workloads[0].Spec.Env = append(e.Workloads[0].Spec.Env, deployv1alpha1.EnvVar{
		Name: "AUTH_API_KEY", SecretRef: &deployv1alpha1.SecretKeyRef{Name: "app", Key: "auth_api_key", Optional: true},
	})
	got, err := generateMissingSecrets(e, store, map[string]string{}, &bytes.Buffer{})
	if err != nil || len(got) != 1 || got[0] != "AUTH_API_KEY" {
		t.Fatalf("generated %v, err %v; want [AUTH_API_KEY]", got, err)
	}
	vals, err := secrets.ReadSecretFile(store)
	if err != nil || !strings.HasPrefix(vals["AUTH_API_KEY"], "rdev_") {
		t.Fatalf("store holds %q, err %v", vals["AUTH_API_KEY"], err)
	}
}
