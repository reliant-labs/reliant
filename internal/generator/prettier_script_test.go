package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/templates"
)

// TestPrettierScriptFormatsWhatTheHookFormats holds scripts/prettier.sh — the
// `task fmt:web` contributors run instead of a bare `npx prettier` — to the
// pre-commit hook it claims to mirror: the hook's pinned version, and exactly
// the files the hook's `files` and `exclude` patterns select. A bare `npx
// prettier` resolved a newer release than the pin, reformatted untouched lines
// and failed the Checks job; a script that drifted from the hook would do the
// same by another route.
//
// The no-literal check runs under -short; the bash runs do not (they are
// slow on a loaded box, and the files they read would be invisible to Go's
// test cache — so the full run reads the script and the config itself too).
func TestPrettierScriptFormatsWhatTheHookFormats(t *testing.T) {
	root := forgeRepoRoot(t)
	script := filepath.Join(root, "scripts", "prettier.sh")
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	if regexp.MustCompile(`prettier@[0-9]`).Match(body) {
		t.Error("scripts/prettier.sh names a prettier version; it must read the pin from .pre-commit-config.yaml")
	}
	if testing.Short() {
		t.Skip("runs scripts/prettier.sh under bash four times; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("scripts/prettier.sh is a bash script")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	raw, err := os.ReadFile(filepath.Join(root, ".pre-commit-config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	files, exclude := prettierHookPatterns(t, raw)

	// A fake npx that prints the arguments it was given.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(bash, append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("scripts/prettier.sh %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	if got := run("--version-pin"); got != templates.PrettierVersion {
		t.Errorf("--version-pin = %q, want %q (templates.PrettierVersion)", got, templates.PrettierVersion)
	}

	candidates := []string{
		"web-runtime/src/index.ts",
		"docs/guide.md",
		".github/workflows/ci.yml",
		"pkg/components/button/button.tsx",
		"CHANGELOG.md",
		"internal/cli/testdata/golden.json",
		"internal/templates/skills/forge/SKILL.md",
		"pkg/deploy/eso/crds/externalsecret.yaml",
		"gen/forge_descriptor/services/api/v1/api.json",
		".forge/scaffolded/frontends/web/package.json.json",
		"internal/cli/generate.go",
		"api.pb.go",
	}
	var want []string
	for _, c := range candidates {
		if files.MatchString(c) && !exclude.MatchString(c) {
			want = append(want, c)
		}
	}
	if len(want) == 0 || len(want) == len(candidates) {
		t.Fatalf("the hook's patterns select %v of %v; the candidates no longer test the filter", want, candidates)
	}
	args := append([]string(nil), candidates...)
	args[0] = filepath.Join(root, args[0]) // an absolute path inside the repo counts as its relative one
	wantArgs := "--yes prettier@" + templates.PrettierVersion + " --write --ignore-unknown " + strings.Join(want, " ")
	if got := run(args...); got != wantArgs {
		t.Errorf("npx got\n  %s\nwant\n  %s", got, wantArgs)
	}
	if got := run("--check", "docs/guide.md"); !strings.Contains(got, " --check --ignore-unknown docs/guide.md") {
		t.Errorf("--check: npx got %q", got)
	}
	if got := run("CHANGELOG.md"); !strings.Contains(got, "nothing to format") {
		t.Errorf("an excluded file alone: %q, want nothing formatted", got)
	}
}

// prettierHookPatterns returns the prettier hook's files and exclude patterns.
func prettierHookPatterns(t *testing.T, raw []byte) (files, exclude *regexp.Regexp) {
	t.Helper()
	var cfg struct {
		Repos []struct {
			Hooks []struct {
				ID      string `yaml:"id"`
				Files   string `yaml:"files"`
				Exclude string `yaml:"exclude"`
			} `yaml:"hooks"`
		} `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Repos {
		for _, h := range r.Hooks {
			if h.ID == "prettier" {
				return regexp.MustCompile(h.Files), regexp.MustCompile(h.Exclude)
			}
		}
	}
	t.Fatal("no prettier hook in .pre-commit-config.yaml")
	return nil, nil
}
