package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScaffoldExclude_ValidLoads(t *testing.T) {
	in := validBaseYAML + `scaffold:
  exclude:
    - frontends/web/**
    - .github/workflows/deploy.yml
    - docs/
`
	cfg, err := LoadProject([]byte(in), "forge.yaml")
	if err != nil {
		t.Fatalf("expected clean load, got: %v", err)
	}
	if got := strings.Join(cfg.Scaffold.Exclude, ","); got != "frontends/web/**,.github/workflows/deploy.yml,docs/" {
		t.Errorf("exclude = %q", got)
	}
}

func TestScaffoldExclude_BadGlobIsALoadError(t *testing.T) {
	for _, bad := range []string{`"/abs/**"`, `"../outside"`, `"web/a**"`, `"web/[x"`, `""`} {
		in := validBaseYAML + "scaffold:\n  exclude:\n    - " + bad + "\n"
		_, err := LoadProject([]byte(in), "forge.yaml")
		if err == nil {
			t.Errorf("exclude %s: expected a load error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "scaffold.exclude[0]") {
			t.Errorf("exclude %s: error does not name the entry: %v", bad, err)
		}
	}
}

func TestReadScaffoldExclude(t *testing.T) {
	dir := t.TempDir()
	if got := ReadScaffoldExclude(dir); got != nil {
		t.Fatalf("no forge.yaml: got %v", got)
	}
	yaml := "name: x\nscaffold:\n  exclude: [\"frontends/web/\", \"/bad\", \"./docs/**\"]\ngenerate:\n  hooks:\n    - name: h\n      command: [true]\n      writes: [frontends/web/src/gen/]\n"
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	// Normalized, and an invalid entry dropped (the full load reports it).
	if got := strings.Join(ReadScaffoldExclude(dir), ","); got != "frontends/web/**,docs/**" {
		t.Errorf("ReadScaffoldExclude = %q", got)
	}
	if got := strings.Join(ReadGenerateHookWrites(dir), ","); got != "frontends/web/src/gen" {
		t.Errorf("ReadGenerateHookWrites = %q", got)
	}
}

// An exclude entry inside a directory the run's guard never descends (git's,
// forge's own state, a dependency or build cache) is a load error naming it:
// the guard could neither capture nor put back a file there. A source tree
// around one is fine; the guard leaves its node_modules alone.
func TestScaffoldExclude_AnUnguardedDirIsALoadError(t *testing.T) {
	for bad, dir := range map[string]string{
		`".next/"`:                      ".next",
		`".forge/**"`:                   ".forge",
		`"frontends/web/node_modules/"`: "node_modules",
		`"**/node_modules/**"`:          "node_modules",
		`".git/hooks/pre-commit"`:       ".git",
	} {
		in := validBaseYAML + "scaffold:\n  exclude:\n    - " + bad + "\n"
		_, err := LoadProject([]byte(in), "forge.yaml")
		if err == nil {
			t.Errorf("exclude %s: expected a load error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "scaffold.exclude[0]") || !strings.Contains(err.Error(), dir+"/") {
			t.Errorf("exclude %s: error does not name the entry and %s/: %v", bad, dir, err)
		}
	}
	for _, ok := range []string{`"frontends/web/**"`, `"frontends/web/"`, `"docs/next/**"`, `".github/workflows/deploy.yml"`} {
		in := validBaseYAML + "scaffold:\n  exclude:\n    - " + ok + "\n"
		if _, err := LoadProject([]byte(in), "forge.yaml"); err != nil {
			t.Errorf("exclude %s: unexpected load error: %v", ok, err)
		}
	}
}
