package envutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParseDotEnv_BasicShapes covers the small parser: comments, blank
// lines, quoted values, `export` prefixes, and unquoted strings.
func TestParseDotEnv_BasicShapes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.dev")
	content := `# leading comment
EMPTY=
SIMPLE=value
QUOTED="with spaces"
SINGLE_QUOTED='another value'
export EXPORTED=ok
WITH_HASH=val#not-a-comment
   PADDED = value-with-spaces

# trailing comment
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ParseDotEnv(path)
	if err != nil {
		t.Fatalf("ParseDotEnv: %v", err)
	}
	want := map[string]string{
		"EMPTY":         "",
		"SIMPLE":        "value",
		"QUOTED":        "with spaces",
		"SINGLE_QUOTED": "another value",
		"EXPORTED":      "ok",
		"WITH_HASH":     "val#not-a-comment",
		"PADDED":        "value-with-spaces",
	}
	if len(got) != len(want) {
		t.Errorf("len(got) = %d, want %d (got=%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseDotEnv_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := ParseDotEnv(filepath.Join(dir, "does-not-exist"))
	if !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist, got %v", err)
	}
}

// TestMergeExtraWins pins the env-overlay precedence the build/deploy
// runners use: extra (BuildEnv / env_file) wins on key conflict over
// the inherited os.Environ.
func TestMergeExtraWins(t *testing.T) {
	base := []string{"PATH=/usr/bin", "FOO=base-foo", "BAR=base-bar"}
	extra := map[string]string{
		"FOO":     "extra-foo",
		"NEW_KEY": "extra-new",
	}
	merged := MergeExtraWins(base, extra)
	got := map[string]string{}
	for _, kv := range merged {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 {
			continue
		}
		got[kv[:eq]] = kv[eq+1:]
	}
	if got["FOO"] != "extra-foo" {
		t.Errorf("FOO: want extra-foo (overlay wins), got %q", got["FOO"])
	}
	if got["BAR"] != "base-bar" {
		t.Errorf("BAR: want base-bar (unchanged), got %q", got["BAR"])
	}
	if got["NEW_KEY"] != "extra-new" {
		t.Errorf("NEW_KEY: want extra-new, got %q", got["NEW_KEY"])
	}
	if got["PATH"] != "/usr/bin" {
		t.Errorf("PATH: want /usr/bin (unchanged), got %q", got["PATH"])
	}
}

// TestMergeExtraWins_EmptyExtra confirms the no-overlay path returns a
// fresh slice (not the base aliased) so callers can mutate the result
// without surprising other code holding the base slice.
func TestMergeExtraWins_EmptyExtra(t *testing.T) {
	base := []string{"PATH=/usr/bin", "FOO=bar"}
	merged := MergeExtraWins(base, nil)
	if len(merged) != 2 {
		t.Fatalf("len: want 2, got %d", len(merged))
	}
	// Mutating the result must not touch the base.
	merged[0] = "PATH=/tmp"
	if base[0] != "PATH=/usr/bin" {
		t.Errorf("base was aliased: %s", base[0])
	}
}

// TestMergeBaseWins: base wins on conflicts (developer-shell-override
// semantics); non-conflicting extras are appended.
func TestMergeBaseWins(t *testing.T) {
	base := []string{"PATH=/usr/bin", "HOME=/home/dev", "EDITOR=vim"}
	extra := map[string]string{
		"PATH":     "/should/lose",   // collides with base
		"DATABASE": "postgres://...", // new
	}
	got := MergeBaseWins(base, extra)

	// base keys come first, unchanged
	if got[0] != "PATH=/usr/bin" {
		t.Errorf("PATH override: got %q, want PATH=/usr/bin", got[0])
	}
	// new key gets appended
	found := false
	for _, kv := range got {
		if kv == "DATABASE=postgres://..." {
			found = true
		}
	}
	if !found {
		t.Errorf("DATABASE not appended; got %v", got)
	}
}
