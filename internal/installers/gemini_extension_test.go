package installers

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeminiExtensionVersionMatchesNpm(t *testing.T) {
	versions := make(map[string]string)
	for _, rel := range []string{"gemini-extension.json", "npm/package.json"} {
		body, err := os.ReadFile(repoFile(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		var manifest struct{ Version string }
		if err := json.Unmarshal(body, &manifest); err != nil {
			t.Fatal(err)
		}
		versions[rel] = manifest.Version
	}
	if versions["gemini-extension.json"] == "" || versions["gemini-extension.json"] != versions["npm/package.json"] {
		t.Fatalf("Gemini extension must follow npm: %v", versions)
	}
}

// Execute only the release script's bookkeeping blocks: no release gates,
// publishing, tags or network. Using the actual blocks catches a missing sync
// or TOUCHED entry without running a release.
func TestGeminiExtensionReleaseVersionSync(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	body, err := os.ReadFile(repoFile(t, "scripts/release-forge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	block := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(string(body), start)
		if !ok {
			t.Fatalf("release script missing %q", start)
		}
		selected, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("release script missing %q", end)
		}
		return start + selected
	}
	setup := block("BACKUP_DIR=", "# The web runtime's bookkeeping")
	sync := block("# The npm shim's version IS", "# ── 7b. CHANGELOG.md")
	for _, tc := range []struct {
		name, version, manifest string
		wantFailure             bool
	}{
		{"final", "v1.6.0", "{\n  \"version\": \"1.3.0\",\n  \"description\": \"unchanged\"\n}\n", false},
		{"prerelease", "v1.6.0-rc.1", "{\n  \"version\": \"1.3.0\"\n}\n", false},
		{"malformed", "v1.6.0", "{\"version\":\"1.3.0\"}\n", true},
		{"absent", "v1.6.0", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "gemini-extension.json")
			if tc.manifest != "" {
				if err := os.WriteFile(path, []byte(tc.manifest), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-c", "set -euo pipefail\nVERSION_FILES=()\nNEXT_FILE=NEXT_VERSION\n"+setup+`
trap 'rm -rf "$BACKUP_DIR"' EXIT
if [ -f gemini-extension.json ]; then
  [[ " ${TOUCHED[*]} " == *" gemini-extension.json "* ]]
fi
`+sync)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "VERSION="+tc.version)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantFailure {
				t.Fatalf("failure=%v, want %v: %s", err, tc.wantFailure, out)
			}
			if tc.manifest == "" {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("created an absent Gemini manifest")
				}
				return
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.manifest
			if !tc.wantFailure {
				want = strings.Replace(want, "1.3.0", strings.TrimPrefix(tc.version, "v"), 1)
			}
			if string(got) != want {
				t.Fatalf("manifest = %s, want %s", got, want)
			}
		})
	}
}
