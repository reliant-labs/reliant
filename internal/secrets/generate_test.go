package secrets

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateSpecEncodings(t *testing.T) {
	h, err := GenerateSpec{Bytes: 16, Encoding: "hex", Prefix: "k_"}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(h, "k_"))
	if err != nil || len(raw) != 16 || !strings.HasPrefix(h, "k_") {
		t.Fatalf("bad hex value %q", h)
	}
	for _, bad := range []GenerateSpec{{Bytes: 0, Encoding: "hex"}, {Bytes: 8, Encoding: "rot13"}} {
		if _, err := bad.Generate(); err == nil {
			t.Fatalf("%+v should be rejected", bad)
		}
	}
}

// A scaffolded store lists every sensitive slot as "" — the header says an
// empty slot is not a value — so a generated key sitting in one is minted,
// while the other slots and the non-empty values are left exactly as they are.
func TestEnsureGeneratedFillsEmptyScaffoldedSlots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := os.WriteFile(path, []byte("CONNECTION_KEYRING: \"\"\nSTRIPE_KEY: \"\"\nVAULT_KEY: mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	specs := map[string]GenerateSpec{
		"CONNECTION_KEYRING": {Bytes: 32, Encoding: "base64"},
		"VAULT_KEY":          {Bytes: 32, Encoding: "hex"},
	}
	wanted := []string{"CONNECTION_KEYRING", "STRIPE_KEY", "VAULT_KEY"}
	present := map[string]string{"CONNECTION_KEYRING": "", "STRIPE_KEY": "", "VAULT_KEY": "mine"}
	got, err := EnsureGenerated(path, specs, wanted, present)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "CONNECTION_KEYRING" {
		t.Fatalf("generated %v, want [CONNECTION_KEYRING]", got)
	}
	vals, err := ReadSecretFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if vals["CONNECTION_KEYRING"] == "" {
		t.Fatal("the empty CONNECTION_KEYRING slot was not minted")
	}
	if vals["VAULT_KEY"] != "mine" {
		t.Fatalf("a non-empty value was overwritten: VAULT_KEY = %q", vals["VAULT_KEY"])
	}
	if v, ok := vals["STRIPE_KEY"]; !ok || v != "" {
		t.Fatalf("a slot with no generate spec was touched: STRIPE_KEY = %q (present %v)", v, ok)
	}

	// A second pass finds a value and leaves it alone.
	first := vals["CONNECTION_KEYRING"]
	got, err = EnsureGenerated(path, specs, wanted, vals)
	if err != nil || len(got) != 0 {
		t.Fatalf("second pass generated %v err %v", got, err)
	}
	if again, _ := ReadSecretFile(path); again["CONNECTION_KEYRING"] != first {
		t.Fatal("a minted key was replaced on the second pass")
	}
}

// An empty slot in the file does not shadow a value the provider resolves
// from another layer (a worktree inheriting the primary checkout's store).
// A store created with a looser mode than 0600 (by hand, or a scaffold under a
// permissive umask) must not keep it once a generated key is written into it.
func TestEnsureGeneratedRestrictsALooseStoreTo0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on windows")
	}
	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := os.WriteFile(path, []byte("CONNECTION_KEYRING: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	specs := map[string]GenerateSpec{"CONNECTION_KEYRING": {Bytes: 32, Encoding: "base64"}}
	got, err := EnsureGenerated(path, specs, []string{"CONNECTION_KEYRING"}, map[string]string{"CONNECTION_KEYRING": ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("generated %v, want [CONNECTION_KEYRING]", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store holding a generated key has mode %o, want 600", perm)
	}
}

func TestEnsureGeneratedKeepsInheritedValueOverEmptySlot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := os.WriteFile(path, []byte("K: \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureGenerated(path, map[string]GenerateSpec{"K": {Bytes: 8, Encoding: "hex"}}, []string{"K"}, map[string]string{"K": "inherited"})
	if err != nil || len(got) != 0 {
		t.Fatalf("generated %v err %v despite an inherited value", got, err)
	}
}

// The same case through the real layered provider: a scaffolded worktree
// store holds an empty slot for a key the primary checkout already set. The
// provider must resolve the inherited key, and EnsureGenerated must not mint
// a second one into the worktree store.
func TestEnsureGeneratedKeepsInheritedKeyThroughLayeredProvider(t *testing.T) {
	root := t.TempDir()
	shared := storeAt(t, filepath.Join(root, "primary", "secrets", "dev.yaml"), "CONNECTION_KEYRING: inherited-key\n")
	local := storeAt(t, filepath.Join(root, "wt", "secrets", "dev.yaml"), "CONNECTION_KEYRING: \"\"\n")

	p, err := NewProvider(&ProviderConfig{Type: "file", Path: local, SharedPath: shared})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if v, ok := p.Resolve("CONNECTION_KEYRING"); !ok || v != "inherited-key" {
		t.Fatalf("empty local slot shadowed the inherited key: got %q ok=%v", v, ok)
	}
	specs := map[string]GenerateSpec{"CONNECTION_KEYRING": {Bytes: 32, Encoding: "base64"}}
	got, err := EnsureGenerated(local, specs, []string{"CONNECTION_KEYRING"}, p.All())
	if err != nil || len(got) != 0 {
		t.Fatalf("generated %v err %v despite an inherited key", got, err)
	}
	vals, err := ReadSecretFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if vals["CONNECTION_KEYRING"] != "" {
		t.Fatalf("a key was minted into the worktree store: %q", vals["CONNECTION_KEYRING"])
	}
	p2, err := NewProvider(&ProviderConfig{Type: "file", Path: local, SharedPath: shared})
	if err != nil {
		t.Fatalf("NewProvider after EnsureGenerated: %v", err)
	}
	if v, _ := p2.Resolve("CONNECTION_KEYRING"); v != "inherited-key" {
		t.Fatalf("after EnsureGenerated the key resolves to %q, want the inherited one", v)
	}
}

func TestEnsureGeneratedSkipsKeysPresentInProviderView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.yaml")
	got, err := EnsureGenerated(path, map[string]GenerateSpec{"K": {Bytes: 8, Encoding: "hex"}}, []string{"K"}, map[string]string{"K": "inherited"})
	if err != nil || len(got) != 0 {
		t.Fatalf("generated %v err %v despite an inherited value", got, err)
	}
}
