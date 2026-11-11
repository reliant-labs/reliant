package secrets

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeStore writes a YAML secret store and returns its path.
func writeStore(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dev.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write store: %v", err)
	}
	return p
}

func TestFileProvider_ReadsFlatMap(t *testing.T) {
	p, err := NewProvider(&ProviderConfig{Type: "file", Path: writeStore(t,
		"STRIPE_SECRET_KEY: sk_test_123\nGITHUB_CLIENT_SECRET: ghs_abc\n")})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if p.Kind() != "file" {
		t.Fatalf("Kind() = %q, want file", p.Kind())
	}
	if v, ok := p.Resolve("STRIPE_SECRET_KEY"); !ok || v != "sk_test_123" {
		t.Fatalf("Resolve = %q,%v", v, ok)
	}
	if got := len(p.All()); got != 2 {
		t.Fatalf("All() has %d entries, want 2", got)
	}
}

// The whole reason for YAML over a dotenv: a value with newlines, quotes
// or leading whitespace round-trips without escaping games.
func TestFileProvider_MultilineAndQuotedValues(t *testing.T) {
	pem := "-----BEGIN KEY-----\nline2\n-----END KEY-----"
	store := writeStore(t, "TLS_KEY: |-\n  -----BEGIN KEY-----\n  line2\n  -----END KEY-----\nQUOTED: \"has: a colon\"\nSPACED: '  leading'\n")

	p, err := NewProvider(&ProviderConfig{Type: "file", Path: store})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if v, _ := p.Resolve("TLS_KEY"); v != pem {
		t.Fatalf("multi-line value mangled:\n got %q\nwant %q", v, pem)
	}
	if v, _ := p.Resolve("QUOTED"); v != "has: a colon" {
		t.Fatalf("quoted value = %q", v)
	}
	if v, _ := p.Resolve("SPACED"); v != "  leading" {
		t.Fatalf("leading whitespace not preserved: %q", v)
	}
}

// An env var is a string; failing on an unquoted number or bool would be a
// papercut with no upside.
func TestFileProvider_CoercesScalars(t *testing.T) {
	p, _ := NewProvider(&ProviderConfig{Type: "file", Path: writeStore(t,
		"PORT: 8080\nDEBUG: true\nEMPTY:\n")})

	for k, want := range map[string]string{"PORT": "8080", "DEBUG": "true", "EMPTY": ""} {
		if v, ok := p.Resolve(k); !ok || v != want {
			t.Errorf("Resolve(%s) = %q,%v want %q", k, v, ok, want)
		}
	}
}

// A nested map can never become an env var. Accepting it silently would
// leave a value that looks set but never arrives.
func TestFileProvider_RejectsNonScalarAndBadNames(t *testing.T) {
	for name, body := range map[string]string{
		"nested map": "DB:\n  host: x\n",
		"bad key":    "not-an-env-var: v\n",
		"sequence":   "LIST:\n  - a\n  - b\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewProvider(&ProviderConfig{Type: "file", Path: writeStore(t, body)})
			if err == nil {
				t.Fatalf("expected an error for %s", name)
			}
		})
	}
}

// A missing store is the fresh-clone case: not an error at load time, so
// ValidateDeclaredRefs reports exactly which secrets to set.
func TestFileProvider_MissingFileIsEmptyNotFatal(t *testing.T) {
	p, err := NewProvider(&ProviderConfig{Type: "file", Path: filepath.Join(t.TempDir(), "absent.yaml")})
	if err != nil {
		t.Fatalf("missing store should not be fatal: %v", err)
	}
	if len(p.All()) != 0 {
		t.Fatalf("All() = %v, want empty", p.All())
	}
}

// Write -> read must round-trip exactly, including a multi-line value.
func TestWriteSecretFile_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.yaml")
	want := map[string]string{
		"STRIPE_SECRET_KEY": "sk_test_123",
		"TLS_KEY":           "-----BEGIN-----\nmid\n-----END-----",
		"WITH_COLON":        "has: colon",
	}
	if err := WriteSecretFile(path, want); err != nil {
		t.Fatalf("WriteSecretFile: %v", err)
	}
	got, err := ReadSecretFile(path)
	if err != nil {
		t.Fatalf("ReadSecretFile: %v", err)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
	// 0600: a secret is readable by its owner only.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// POSIX mode bits only: Windows reports 0666 for any writable file and
	// protects it with the directory's ACL instead.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
}

// Keys are written sorted so a re-write is a clean diff, not map noise.
func TestWriteSecretFile_SortedKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dev.yaml")
	if err := WriteSecretFile(path, map[string]string{"ZED": "1", "ALPHA": "2", "MID": "3"}); err != nil {
		t.Fatalf("WriteSecretFile: %v", err)
	}
	raw, _ := os.ReadFile(path)
	body := string(raw)
	if strings.Index(body, "ALPHA") > strings.Index(body, "MID") ||
		strings.Index(body, "MID") > strings.Index(body, "ZED") {
		t.Fatalf("keys not sorted:\n%s", body)
	}
}

func TestValidateDeclaredRefs_NamesTheFixCommand(t *testing.T) {
	store := writeStore(t, "PRESENT: v\n")
	p, _ := NewProvider(&ProviderConfig{Type: "file", Path: store})

	err := ValidateDeclaredRefs(p, []SecretRef{
		{EnvName: "PRESENT", SecretName: "app"},
		{EnvName: "ABSENT", SecretName: "app"},
	}, store)
	if err == nil {
		t.Fatal("expected a missing-value error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ABSENT") || strings.Contains(msg, "PRESENT") {
		t.Fatalf("error should name only the missing key, got: %v", msg)
	}
	if !strings.Contains(msg, "forge secret set") {
		t.Fatalf("error should give the fix command, got: %v", msg)
	}
}

func TestValidSecretKey(t *testing.T) {
	for _, k := range []string{"A", "_X", "STRIPE_SECRET_KEY", "K8S_2_TOKEN"} {
		if !ValidSecretKey(k) {
			t.Errorf("ValidSecretKey(%q) = false, want true", k)
		}
	}
	for _, k := range []string{"", "1LEADING", "has-dash", "has.dot", "has space"} {
		if ValidSecretKey(k) {
			t.Errorf("ValidSecretKey(%q) = true, want false", k)
		}
	}
}
