package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// storeAt writes a YAML store at an explicit path (dir created).
func storeAt(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	return path
}

// The bug this whole change exists for: a linked worktree materializes no
// gitignored store, so before layering EVERY declared secret was missing
// even though the developer had set them all in the primary checkout.
func TestLayered_WorktreeInheritsPrimaryCheckoutValues(t *testing.T) {
	root := t.TempDir()
	shared := storeAt(t, filepath.Join(root, "primary", "secrets", "dev.yaml"),
		"STRIPE_SECRET_KEY: sk_shared\nGITHUB_CLIENT_SECRET: ghs_shared\n")
	local := filepath.Join(root, "wt", "secrets", "dev.yaml") // never created

	p, err := NewProvider(&ProviderConfig{Type: "file", Path: local, SharedPath: shared})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if v, ok := p.Resolve("STRIPE_SECRET_KEY"); !ok || v != "sk_shared" {
		t.Fatalf("worktree did not inherit shared value: got %q ok=%v", v, ok)
	}
	if err := ValidateDeclaredRefs(p, []SecretRef{
		{EnvName: "STRIPE_SECRET_KEY", SecretName: "app"},
		{EnvName: "GITHUB_CLIENT_SECRET", SecretName: "app"},
	}, local); err != nil {
		t.Fatalf("declared refs should resolve from the shared store: %v", err)
	}
}

// Per-key override is the reason this merges rather than falling back: a
// worktree overriding ONE key must keep inheriting the rest.
func TestLayered_LocalOverridesPerKeyNotWholeFile(t *testing.T) {
	root := t.TempDir()
	shared := storeAt(t, filepath.Join(root, "primary", "secrets", "dev.yaml"),
		"STRIPE_SECRET_KEY: sk_shared\nGITHUB_CLIENT_SECRET: ghs_shared\nRESEND_API_KEY: re_shared\n")
	local := storeAt(t, filepath.Join(root, "wt", "secrets", "dev.yaml"),
		"STRIPE_SECRET_KEY: sk_worktree_only\n")

	p, err := NewProvider(&ProviderConfig{Type: "file", Path: local, SharedPath: shared})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if v, _ := p.Resolve("STRIPE_SECRET_KEY"); v != "sk_worktree_only" {
		t.Errorf("local override lost: got %q", v)
	}
	// The whole-file-fallback failure mode: these would vanish.
	if v, _ := p.Resolve("GITHUB_CLIENT_SECRET"); v != "ghs_shared" {
		t.Errorf("overriding one key shadowed the rest: GITHUB_CLIENT_SECRET = %q", v)
	}
	if v, _ := p.Resolve("RESEND_API_KEY"); v != "re_shared" {
		t.Errorf("overriding one key shadowed the rest: RESEND_API_KEY = %q", v)
	}

	l, ok := p.(Layered)
	if !ok {
		t.Fatal("layered provider should report its layering")
	}
	_, _, inherited, overridden := l.Layering()
	if inherited != 2 || overridden != 1 {
		t.Errorf("Layering() inherited=%d overridden=%d, want 2 and 1", inherited, overridden)
	}
}

// A local-only key (set in the worktree, absent upstream) must resolve and
// must not be counted as an override.
func TestLayered_LocalOnlyKeyResolves(t *testing.T) {
	root := t.TempDir()
	shared := storeAt(t, filepath.Join(root, "primary", "secrets", "dev.yaml"), "SHARED_KEY: a\n")
	local := storeAt(t, filepath.Join(root, "wt", "secrets", "dev.yaml"), "WORKTREE_KEY: b\n")

	p, _ := NewProvider(&ProviderConfig{Type: "file", Path: local, SharedPath: shared})
	if v, ok := p.Resolve("WORKTREE_KEY"); !ok || v != "b" {
		t.Errorf("local-only key = %q ok=%v", v, ok)
	}
	if v, ok := p.Resolve("SHARED_KEY"); !ok || v != "a" {
		t.Errorf("shared key = %q ok=%v", v, ok)
	}
	_, _, inherited, overridden := p.(Layered).Layering()
	if inherited != 1 || overridden != 0 {
		t.Errorf("inherited=%d overridden=%d, want 1 and 0", inherited, overridden)
	}
}

// The primary checkout keeps exactly today's behaviour: one store, no
// layering, and no notice to print.
func TestLayered_PrimaryCheckoutIsUnlayered(t *testing.T) {
	store := writeStore(t, "K: v\n")
	p, err := NewProvider(&ProviderConfig{Type: "file", Path: store, SharedPath: ""})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if _, ok := p.(Layered); ok {
		t.Fatal("unlayered provider must not report layering")
	}
}

// A malformed SHARED store must be fatal, not silently skipped — otherwise
// a typo upstream degrades into "28 secrets missing" in every worktree.
func TestLayered_MalformedSharedStoreIsFatal(t *testing.T) {
	root := t.TempDir()
	shared := storeAt(t, filepath.Join(root, "primary", "secrets", "dev.yaml"), "NESTED:\n  a: b\n")
	local := storeAt(t, filepath.Join(root, "wt", "secrets", "dev.yaml"), "K: v\n")

	if _, err := NewProvider(&ProviderConfig{Type: "file", Path: local, SharedPath: shared}); err == nil {
		t.Fatal("expected a malformed shared store to be fatal")
	}
}

// Refs are collected per SERVICE, so a secret three services declare
// produced three identical lines and a wildly inflated count.
func TestValidateDeclaredRefs_DeduplicatesByEnvName(t *testing.T) {
	store := writeStore(t, "PRESENT: v\n")
	p, _ := NewProvider(&ProviderConfig{Type: "file", Path: store})

	err := ValidateDeclaredRefs(p, []SecretRef{
		{EnvName: "GITHUB_CLIENT_SECRET", SecretName: "app"},
		{EnvName: "GITHUB_CLIENT_SECRET", SecretName: "app"},
		{EnvName: "GITHUB_CLIENT_SECRET", SecretName: "app"},
	}, store)
	if err == nil {
		t.Fatal("expected a missing-value error")
	}
	msg := err.Error()
	// Count LISTED entries, not raw occurrences: the key legitimately
	// appears twice on its own line, because SecretKey defaults to
	// EnvName ("GITHUB_CLIENT_SECRET   (Secret app/GITHUB_CLIENT_SECRET)").
	listed := 0
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "GITHUB_CLIENT_SECRET") {
			listed++
		}
	}
	if listed != 1 {
		t.Errorf("key listed on %d lines, want 1:\n%s", listed, msg)
	}
	if !strings.Contains(msg, "missing 1 declared value") {
		t.Errorf("count should be de-duplicated, got:\n%s", msg)
	}
}
