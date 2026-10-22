// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestForgeHookGeneratorsMatchForgeYAML pins the forge-generate lane to
// forge.yaml's generate.hooks, so a hook added there cannot go unchecked by
// `task test:affected`:
//
//   - every `go generate ./<pkg>` hook's //go:generate program, and every
//     `go run ./<pkg>` hook, is in forgeHookGenerators (so a change to the
//     code it compiles runs the lane), and nothing else is;
//   - every path a hook writes, and every repo file its command names (a
//     script, a buf template), is in forgeInputs.
//
// It reads every file it judges itself, so a cached pass is never stale.
func TestForgeHookGeneratorsMatchForgeYAML(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Generate struct {
			Hooks []struct {
				Name    string   `yaml:"name"`
				Command []string `yaml:"command"`
				Writes  []string `yaml:"writes"`
			} `yaml:"hooks"`
		} `yaml:"generate"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("parse forge.yaml: %v", err)
	}
	if len(cfg.Generate.Hooks) == 0 {
		t.Fatal("forge.yaml declares no generate.hooks")
	}

	goGenerate := regexp.MustCompile(`(?m)^//go:generate go run (\S+)`)
	var generators []string
	for _, h := range cfg.Generate.Hooks {
		argv := h.Command
		switch {
		case len(argv) == 3 && argv[0] == "go" && argv[1] == "generate":
			pkg := strings.TrimPrefix(argv[2], "./")
			files, _ := filepath.Glob(filepath.Join(repoRoot, filepath.FromSlash(pkg), "*.go"))
			found := false
			for _, f := range files {
				src, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				for _, m := range goGenerate.FindAllStringSubmatch(string(src), -1) {
					generators = append(generators, path.Join(pkg, m[1]))
					found = true
				}
			}
			if !found {
				t.Errorf("hook %s runs go generate in %s, which has no //go:generate go run directive", h.Name, pkg)
			}
		case len(argv) >= 3 && argv[0] == "go" && argv[1] == "run":
			generators = append(generators, path.Clean(strings.TrimPrefix(argv[2], "./")))
		default:
			for _, arg := range argv {
				if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(arg))); err == nil && !isForgeInput(path.Clean(arg)) {
					t.Errorf("hook %s runs %s, which is not in forgeInputs (tools/test-affected/rules.go): a change to it would not run the forge-generate check", h.Name, arg)
				}
			}
		}
		for _, w := range h.Writes {
			if !isForgeInput(path.Clean(w)) && !isForgeInput(path.Join(w, "x")) {
				t.Errorf("hook %s writes %s, which is not in forgeInputs (tools/test-affected/rules.go): a hand edit of it would not run the forge-generate check", h.Name, w)
			}
		}
	}
	slices.Sort(generators)
	want := slices.Sorted(slices.Values(forgeHookGenerators))
	if !slices.Equal(generators, want) {
		t.Errorf("forge.yaml's generate hooks run %v, but forgeHookGenerators (tools/test-affected/rules.go) lists %v", generators, want)
	}
}
