package sigstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/selfupdate"
)

// The fixture is forge v1.8.0's real signed set: its checksums.txt and
// forge-release.sigstore.json, made by release-attest.yml, plus a snapshot of
// the public-good Sigstore trusted_root.json (sigstore/root-signing,
// targets/trusted_root.json). Pinning the root makes the test offline and
// deterministic; production fetches it through TUF.
//
// The contract values are forge's own (internal/installers/signature_test.go
// holds install.sh to the same three).
const (
	forgeIdentity  = `^https://github\.com/reliant-labs/forge/\.github/workflows/release-attest\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)$`
	forgePredicate = "https://github.com/reliant-labs/forge/attestations/release-assets/v1"
	forgeBundle    = "forge-release.sigstore.json"
)

func trustedRoot(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "trusted_root.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func forgeConfig(t *testing.T) Config {
	return Config{
		Repo:          "reliant-labs/forge",
		Identity:      forgeIdentity,
		PredicateType: forgePredicate,
		TrustedRoot:   trustedRoot(t),
	}
}

// fixture copies the signed set into a temp dir and returns the files map
// Prepare would hand the verifier, and the release.
func fixture(t *testing.T) (selfupdate.Release, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for _, name := range []string{"checksums.txt", forgeBundle} {
		b, err := os.ReadFile(filepath.Join("testdata", "forge-v1.8.0", name))
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		files[name] = p
	}
	rel := selfupdate.Release{
		Version: "1.8.0",
		Tag:     "v1.8.0",
		Assets:  []selfupdate.Asset{{Name: "checksums.txt"}},
		Bundle:  selfupdate.Asset{Name: forgeBundle},
	}
	return rel, files
}

func mustNew(t *testing.T, cfg Config) *Verifier {
	t.Helper()
	v, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVerifiesARealRelease(t *testing.T) {
	rel, files := fixture(t)
	if err := mustNew(t, forgeConfig(t)).Verify(context.Background(), rel, files); err != nil {
		t.Fatalf("forge v1.8.0's signed checksums.txt did not verify: %v", err)
	}
}

func TestRefuses(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, cfg *Config, rel *selfupdate.Release, files map[string]string)
		wantErr string
	}{
		{
			name:    "a signature replayed under another tag",
			mutate:  func(_ *testing.T, _ *Config, rel *selfupdate.Release, _ map[string]string) { rel.Tag = "v1.9.0" },
			wantErr: "replayed",
		},
		{
			name: "another repository's predicate",
			mutate: func(_ *testing.T, cfg *Config, _ *selfupdate.Release, _ map[string]string) {
				cfg.Repo = "reliant-labs/reliant"
			},
			wantErr: "repository",
		},
		{
			name: "another signer",
			mutate: func(_ *testing.T, cfg *Config, _ *selfupdate.Release, _ map[string]string) {
				cfg.Identity = `^https://github\.com/reliant-labs/forge/\.github/workflows/other\.yml@refs/heads/main$`
			},
			wantErr: "does not verify",
		},
		{
			name: "another predicate type",
			mutate: func(_ *testing.T, cfg *Config, _ *selfupdate.Release, _ map[string]string) {
				cfg.PredicateType = "https://example.com/other/v1"
			},
			wantErr: "predicate type",
		},
		{
			name: "a tampered file",
			mutate: func(t *testing.T, _ *Config, _ *selfupdate.Release, files map[string]string) {
				if err := os.WriteFile(files["checksums.txt"], []byte("0000  forge_linux_amd64.tar.gz\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "does not verify",
		},
		{
			name: "an extra file the release did not sign",
			mutate: func(t *testing.T, _ *Config, _ *selfupdate.Release, files map[string]string) {
				// Sorted after checksums.txt: every file's digest, not only
				// the first, must be a subject of the signed statement, so
				// sigstore-go itself refuses it (review B1 of #2504).
				p := filepath.Join(filepath.Dir(files["checksums.txt"]), "forge_linux_amd64.tar.gz")
				if err := os.WriteFile(p, []byte("not the signed archive"), 0o600); err != nil {
					t.Fatal(err)
				}
				files["forge_linux_amd64.tar.gz"] = p
			},
			wantErr: "provided artifact digest does not match any digest in statement",
		},
		{
			name: "a signed file's bytes under another signed file's name",
			mutate: func(t *testing.T, _ *Config, _ *selfupdate.Release, files map[string]string) {
				// Its digest IS a subject, so sigstore-go passes it; the name
				// binding below sigstore-go is what refuses it.
				raw, err := os.ReadFile(files["checksums.txt"])
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(filepath.Dir(files["checksums.txt"]), "forge_linux_amd64.tar.gz")
				if err := os.WriteFile(p, raw, 0o600); err != nil {
					t.Fatal(err)
				}
				files["forge_linux_amd64.tar.gz"] = p
			},
			wantErr: "forge_linux_amd64.tar.gz is not",
		},
		{
			name: "a release with no bundle",
			mutate: func(_ *testing.T, _ *Config, rel *selfupdate.Release, _ map[string]string) {
				rel.Bundle = selfupdate.Asset{}
			},
			wantErr: "unsigned releases are refused",
		},
		{
			name: "a bundle that was not downloaded",
			mutate: func(_ *testing.T, _ *Config, _ *selfupdate.Release, files map[string]string) {
				delete(files, forgeBundle)
			},
			wantErr: "was not downloaded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := forgeConfig(t)
			rel, files := fixture(t)
			tc.mutate(t, &cfg, &rel, files)
			err := mustNew(t, cfg).Verify(context.Background(), rel, files)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Verify() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewRefusesUnsafeConfig(t *testing.T) {
	base := forgeConfig(t)
	for name, mutate := range map[string]func(*Config){
		"unanchored identity": func(c *Config) { c.Identity = `reliant-labs/forge` },
		"no predicate type":   func(c *Config) { c.PredicateType = "" },
		"repo not owner/name": func(c *Config) { c.Repo = "forge" },
		"bad trusted root":    func(c *Config) { c.TrustedRoot = []byte("{}") },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Fatal("New accepted it")
			}
		})
	}
}
