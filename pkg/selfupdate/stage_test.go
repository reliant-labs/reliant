//go:build unix

package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releaseFiles is a well-formed release of "app" for this platform whose
// binary is newBin, signed by a bundle the verifier is handed.
func releaseFiles(t *testing.T, newBin []byte) map[string][]byte {
	t.Helper()
	archive := tarGz(t, map[string][]byte{"app": newBin, "LICENSE": []byte("license")})
	files := map[string][]byte{platformArchive("app"): archive}
	files["checksums.txt"] = checksumsOf(files)
	files["app-release.sigstore.json"] = []byte(`{"bundle":true}`)
	return files
}

func TestPrepareStagesAVerifiedProbedBinary(t *testing.T) {
	t.Parallel()
	newBin := script("echo 2.0.0")
	srv := newFakeRelease(t, "v2.0.0", releaseFiles(t, newBin))
	exe := installed(t, script("echo 1.0.0"))
	v := &recordingVerifier{}
	u := newTestUpdater(t, exe, srv, func(c *Config) { c.Verifier = v })

	// A stale staged release is pruned by the next one.
	stale := filepath.Join(u.cfg.StateDir, "staged", "1.5.0")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}

	s, err := u.Prepare(context.Background(), srv.release())
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	got, err := os.ReadFile(s.Path)
	if err != nil || !bytes.Equal(got, newBin) {
		t.Fatalf("staged binary = %q, %v; want the archive's", got, err)
	}
	if s.SHA256 != sumHex(newBin) || s.Release.Version != "2.0.0" {
		t.Fatalf("Staged = %+v", s)
	}
	if want := filepath.Join(u.cfg.StateDir, "staged", "2.0.0", "app"); s.Path != want {
		t.Fatalf("Path = %s, want %s", s.Path, want)
	}
	if len(v.calls) != 1 {
		t.Fatalf("verifier called %d times, want 1", len(v.calls))
	}
	for _, name := range []string{platformArchive("app"), "checksums.txt", "app-release.sigstore.json"} {
		if v.calls[0][name] == "" {
			t.Errorf("the verifier was not handed %s: %v", name, v.calls[0])
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale staged release survived: %v", err)
	}
	if b, _ := os.ReadFile(exe); !bytes.Equal(b, script("echo 1.0.0")) {
		t.Error("Prepare touched the running binary")
	}
}

func TestPrepareRefusals(t *testing.T) {
	t.Parallel()
	good := script("echo 2.0.0")
	cases := []struct {
		name   string
		files  func(t *testing.T) map[string][]byte
		rel    func(f *fakeRelease) Release
		mutate func(c *Config)
		exe    func(t *testing.T) string
		want   string
	}{
		{
			name: "checksum mismatch",
			files: func(t *testing.T) map[string][]byte {
				f := releaseFiles(t, good)
				f["checksums.txt"] = []byte(strings.Repeat("0", 64) + "  " + platformArchive("app") + "\n")
				return f
			},
			want: "sha256 mismatch",
		},
		{
			name:  "verifier refuses",
			files: func(t *testing.T) map[string][]byte { return releaseFiles(t, good) },
			mutate: func(c *Config) {
				c.Verifier = &recordingVerifier{err: errors.New("not signed by the release workflow")}
			},
			want: "failed signature verification",
		},
		{
			name: "no build for this platform",
			files: func(t *testing.T) map[string][]byte {
				f := releaseFiles(t, good)
				delete(f, platformArchive("app"))
				return f
			},
			want: "has no " + platformArchive("app"),
		},
		{
			name: "no checksums",
			files: func(t *testing.T) map[string][]byte {
				f := releaseFiles(t, good)
				delete(f, "checksums.txt")
				return f
			},
			want: "has no checksums.txt",
		},
		{
			name:  "probe fails",
			files: func(t *testing.T) map[string][]byte { return releaseFiles(t, script("echo wrong arch >&2; exit 3")) },
			want:  "failed its probe",
		},
		{
			name: "archive without the binary",
			files: func(t *testing.T) map[string][]byte {
				f := map[string][]byte{platformArchive("app"): tarGz(t, map[string][]byte{"other": good})}
				f["checksums.txt"] = checksumsOf(f)
				return f
			},
			want: "no app in the archive",
		},
		{
			name:  "http asset",
			files: func(t *testing.T) map[string][]byte { return releaseFiles(t, good) },
			rel: func(f *fakeRelease) Release {
				r := f.release()
				for i := range r.Assets {
					r.Assets[i].URL = strings.Replace(r.Assets[i].URL, "https://", "http://", 1)
				}
				return r
			},
			want: "non-https",
		},
		{
			name:  "asset name escapes",
			files: func(t *testing.T) map[string][]byte { return releaseFiles(t, good) },
			rel: func(f *fakeRelease) Release {
				r := f.release()
				r.Bundle = Asset{Name: "../escape", URL: r.Assets[0].URL}
				return r
			},
			want: "not a plain file name",
		},
		{
			name:  "not self-owned",
			files: func(t *testing.T) map[string][]byte { return releaseFiles(t, good) },
			exe: func(t *testing.T) string {
				exe := installed(t, script("echo 1.0.0"))
				if err := os.WriteFile(exe+".managed-by", []byte("homebrew"), 0o644); err != nil {
					t.Fatal(err)
				}
				return exe
			},
			want: "not self-owned",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := newFakeRelease(t, "v2.0.0", tc.files(t))
			exe := installed(t, script("echo 1.0.0"))
			if tc.exe != nil {
				exe = tc.exe(t)
			}
			u := newTestUpdater(t, exe, srv, tc.mutate)
			rel := srv.release()
			if tc.rel != nil {
				rel = tc.rel(srv)
			}
			_, err := u.Prepare(context.Background(), rel)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
			if _, err := os.Stat(filepath.Join(u.cfg.StateDir, "staged", "2.0.0", "app")); err == nil {
				t.Error("a refused release left a staged binary")
			}
		})
	}
}

func TestPrepareArchiveFormats(t *testing.T) {
	t.Parallel()
	bin := script("echo 2.0.0")
	cases := map[string]struct {
		name string
		body func(t *testing.T) []byte
	}{
		"zip, one directory down": {"app.zip", func(t *testing.T) []byte { return zipOf(t, map[string][]byte{"app-2.0.0/app": bin}) }},
		"raw binary":              {"app-raw", func(*testing.T) []byte { return bin }},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			t.Parallel()
			files := map[string][]byte{tc.name: tc.body(t)}
			files["checksums.txt"] = checksumsOf(files)
			srv := newFakeRelease(t, "v2.0.0", files)
			u := newTestUpdater(t, installed(t, script("echo 1")), srv, func(c *Config) {
				c.ArchiveName = func(string, string) string { return tc.name }
			})
			s, err := u.Prepare(context.Background(), srv.release())
			if err != nil {
				t.Fatal(err)
			}
			if s.SHA256 != sumHex(bin) {
				t.Fatalf("staged %s, want the binary", s.SHA256)
			}
		})
	}
}

func TestPrepareInsecureStillChecksTheChecksum(t *testing.T) {
	t.Parallel()
	files := releaseFiles(t, script("echo 2"))
	files["checksums.txt"] = []byte(strings.Repeat("a", 64) + "  " + platformArchive("app") + "\n")
	srv := newFakeRelease(t, "v2.0.0", files)
	u := newTestUpdater(t, installed(t, script("echo 1")), srv, func(c *Config) {
		c.Verifier, c.InsecureAllowUnsigned = nil, true
	})
	if _, err := u.Prepare(context.Background(), srv.release()); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want a checksum refusal even when unsigned is allowed", err)
	}
}
