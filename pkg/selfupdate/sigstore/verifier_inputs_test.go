package sigstore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/selfupdate"
)

// TestVerifyRefusesBadInputsBeforeTrustingAnything covers the failures that
// are decided from the downloaded files alone, before any trusted root is
// consulted: each must be a refusal with a message naming what is wrong.
func TestVerifyRefusesBadInputsBeforeTrustingAnything(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, rel *selfupdate.Release, files map[string]string)
		wantErr string
	}{
		{
			name:    "a release with no tag",
			mutate:  func(_ *testing.T, rel *selfupdate.Release, _ map[string]string) { rel.Tag = "" },
			wantErr: "no tag to bind the signature to",
		},
		{
			name: "a bundle path that cannot be read",
			mutate: func(_ *testing.T, _ *selfupdate.Release, files map[string]string) {
				files[forgeBundle] = filepath.Join(filepath.Dir(files[forgeBundle]), "gone.json")
			},
			wantErr: "read " + forgeBundle,
		},
		{
			name: "a bundle larger than the limit",
			mutate: func(t *testing.T, _ *selfupdate.Release, files map[string]string) {
				big := bytes.Repeat([]byte(" "), maxBundleBytes+1)
				if err := os.WriteFile(files[forgeBundle], big, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "larger than",
		},
		{
			name: "a bundle that is not a Sigstore bundle",
			mutate: func(t *testing.T, _ *selfupdate.Release, files map[string]string) {
				if err := os.WriteFile(files[forgeBundle], []byte(`{"not":"a bundle"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "parse " + forgeBundle,
		},
		{
			name: "only the bundle downloaded",
			mutate: func(_ *testing.T, _ *selfupdate.Release, files map[string]string) {
				delete(files, "checksums.txt")
			},
			wantErr: "nothing to verify",
		},
		{
			name: "a listed file that cannot be hashed",
			mutate: func(_ *testing.T, _ *selfupdate.Release, files map[string]string) {
				files["checksums.txt"] = filepath.Join(filepath.Dir(files["checksums.txt"]), "gone.txt")
			},
			wantErr: "hash checksums.txt",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel, files := fixture(t)
			tc.mutate(t, &rel, files)
			err := mustNew(t, forgeConfig(t)).Verify(context.Background(), rel, files)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Verify() = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestNewRejectsAnIdentityThatIsNotARegexp(t *testing.T) {
	cfg := forgeConfig(t)
	cfg.Identity = `^(unclosed$`
	_, err := New(cfg)
	if err == nil || !strings.Contains(err.Error(), "Config.Identity") {
		t.Fatalf("New = %v, want a Config.Identity error", err)
	}
}

func TestNewDefaultsTheIssuerToGitHubActions(t *testing.T) {
	cfg := forgeConfig(t)
	cfg.Issuer = ""
	v := mustNew(t, cfg)
	if v.cfg.Issuer != GitHubActionsIssuer {
		t.Errorf("Issuer = %q, want %q", v.cfg.Issuer, GitHubActionsIssuer)
	}
	// And the real release still verifies under the defaulted issuer, which
	// proves the default is the issuer the certificate carries.
	rel, files := fixture(t)
	if err := v.Verify(context.Background(), rel, files); err != nil {
		t.Fatalf("Verify under the default issuer: %v", err)
	}
}

func TestNewRefusesAnotherIssuer(t *testing.T) {
	cfg := forgeConfig(t)
	cfg.Issuer = "https://accounts.example.com"
	rel, files := fixture(t)
	err := mustNew(t, cfg).Verify(context.Background(), rel, files)
	if err == nil || !strings.Contains(err.Error(), "does not verify") {
		t.Fatalf("Verify() = %v, want a refusal for a certificate from another issuer", err)
	}
}

func TestTrustedMaterialUsesThePinnedRoot(t *testing.T) {
	v := mustNew(t, forgeConfig(t))
	tm, err := v.trustedMaterial(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tm != v.pinned {
		t.Error("trustedMaterial did not return the pinned root")
	}
}

func TestReadBounded(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, bytes.Repeat([]byte("x"), n), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name    string
		path    string
		limit   int64
		wantLen int
		wantErr string // substring, for the limit error this package words itself
		wantIs  error  // for OS errors, whose text differs per platform
	}{
		{"empty file", write("empty", 0), 10, 0, "", nil},
		{"exactly at the limit", write("at", 10), 10, 10, "", nil},
		{"one byte over the limit", write("over", 11), 10, 0, "larger than 10 bytes", nil},
		{"missing file", filepath.Join(dir, "missing"), 10, 0, "", fs.ErrNotExist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readBounded(tt.path, tt.limit)
			if tt.wantIs != nil {
				if !errors.Is(err, tt.wantIs) {
					t.Fatalf("err = %v, want errors.Is %v", err, tt.wantIs)
				}
				return
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || len(got) != tt.wantLen {
				t.Errorf("readBounded = %d bytes, %v; want %d bytes", len(got), err, tt.wantLen)
			}
		})
	}
}

func TestSha256Of(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := sha256Of(p)
	if err != nil {
		t.Fatal(err)
	}
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" // sha256("abc")
	if hexed := hex.EncodeToString(got); hexed != want {
		t.Errorf("sha256 = %s, want %s", hexed, want)
	}
	if _, err := sha256Of(filepath.Join(filepath.Dir(p), "missing")); err == nil {
		t.Error("sha256Of a missing file returned no error")
	}
	// A directory opens but cannot be read: the copy error must surface.
	if _, err := sha256Of(filepath.Dir(p)); err == nil {
		t.Error("sha256Of a directory returned no error")
	}
}

// TestUnpinnedRootFailureIsReported pins the unpinned path's error without
// the network: the TUF cache path is a regular file, so the client cannot
// create its cache directory and fails before any request is made. (The
// success path fetches the public-good root over the network and so is not
// exercised here.)
func TestUnpinnedRootFailureIsReported(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := forgeConfig(t)
	cfg.TrustedRoot = nil
	cfg.TUFCachePath = filepath.Join(notADir, "tuf")
	v := mustNew(t, cfg)

	_, err := v.trustedMaterial(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fetch the Sigstore trusted root") {
		t.Fatalf("trustedMaterial = %v, want a 'fetch the Sigstore trusted root' error", err)
	}

	rel, files := fixture(t)
	err = v.Verify(context.Background(), rel, files)
	if err == nil || !strings.Contains(err.Error(), "fetch the Sigstore trusted root") {
		t.Fatalf("Verify = %v, want a refusal because the trusted root could not be obtained", err)
	}
}
