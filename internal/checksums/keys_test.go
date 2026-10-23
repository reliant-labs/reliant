package checksums

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoad_NormalizesBackslashedKeys: the state files are committed and read
// on every OS, and a Windows forge that predates slashKey wrote backslashed
// keys into them. Load must hand every host the slash form, or the disown
// recorded on Windows protects nothing when the repo is generated anywhere
// else (and, since slashKey, on Windows too).
func TestLoad_NormalizesBackslashedKeys(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	disowned := `{"files": {"internal\\x\\x.go": {"reason": "mine"}}}`
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(DisownedFile)), []byte(disowned), 0o644); err != nil {
		t.Fatal(err)
	}
	hashes := `{"files": {"web\\gen\\api.json": "abc123"}}`
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(HashesFile)), []byte(hashes), 0o644); err != nil {
		t.Fatal(err)
	}

	cs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cs.IsDisowned("internal/x/x.go") {
		t.Errorf("a backslash-keyed disown was not found by its slash form: %v", cs.Disowned)
	}
	if got := cs.Unstampable["web/gen/api.json"]; got != "abc123" {
		t.Errorf("Unstampable[web/gen/api.json] = %q, want abc123 (keys: %v)", got, cs.Unstampable)
	}
}
