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
					gen := path.Join(pkg, m[1])
					generators = append(generators, gen)
					if forgeHookOf[gen] != h.Name {
						t.Errorf("forgeHookOf[%q] is %q, but forge.yaml's hook %s runs it", gen, forgeHookOf[gen], h.Name)
					}
					found = true
				}
			}
			if !found {
				t.Errorf("hook %s runs go generate in %s, which has no //go:generate go run directive", h.Name, pkg)
			}
		case len(argv) >= 3 && argv[0] == "go" && argv[1] == "run":
			gen := path.Clean(strings.TrimPrefix(argv[2], "./"))
			generators = append(generators, gen)
			if forgeHookOf[gen] != h.Name {
				t.Errorf("forgeHookOf[%q] is %q, but forge.yaml's hook %s runs it", gen, forgeHookOf[gen], h.Name)
			}
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

// TestForgeGenHooksOnly pins the merge gate's cheap forge-generate check: Go
// that only hook generators compile runs exactly the hooks whose generator
// compiles it (every one of them, not the first), while anything forge itself
// reads keeps the whole forge generate.
func TestForgeGenHooksOnly(t *testing.T) {
	pkg := func(n string) string { return mod + "/" + n }
	g := testGraph()
	tools, yaml := "internal/llm/tools/cmd/toolconfiggen", "internal/workflow/yaml/cmd/yamlbindingsgen"
	for _, gen := range []string{tools, yaml} {
		g.DirPkg[gen] = pkg(gen)
		g.Files[gen+"/main.go"] = FileInfo{Pkg: pkg(gen), Kind: KindCode}
	}
	// toolconfiggen compiles leaf and mid; yamlbindingsgen compiles leaf.
	g.BuildDeps[pkg(tools)] = map[string]bool{pkg(tools): true, pkg("internal/mid"): true, pkg("internal/leaf"): true}
	g.BuildDeps[pkg(yaml)] = map[string]bool{pkg(yaml): true, pkg("internal/leaf"): true}

	cases := []struct {
		paths     []string
		hooksOnly bool
		hooks     []string
	}{
		{[]string{"internal/leaf/leaf.go"}, true, []string{"tool-catalog", "yaml-bindings"}},
		{[]string{"internal/mid/mid.go"}, true, []string{"tool-catalog"}},
		// A forge input beside it: the whole forge generate.
		{[]string{"internal/mid/mid.go", "proto/reliant/v1/chat.proto"}, false, nil},
		{[]string{"internal/mid/mid.go", "Taskfile.yml"}, false, nil},
		// A test file is in no generator's build: the lane does not run.
		{[]string{"internal/mid/mid_test.go"}, false, nil},
	}
	for _, tc := range cases {
		pl := classifyIn(g, tc.paths...)
		if pl.ForgeGen.HooksOnly != tc.hooksOnly || !slices.Equal(pl.ForgeGen.Hooks, tc.hooks) {
			t.Errorf("%v: hooks_only=%v hooks=%v, want %v %v (reasons %v)", tc.paths, pl.ForgeGen.HooksOnly, pl.ForgeGen.Hooks, tc.hooksOnly, tc.hooks, pl.ForgeGen.Reasons)
		}
		if tc.hooksOnly && !pl.ForgeGen.Full {
			t.Errorf("%v: a hooks-only plan must still mark the lane selected", tc.paths)
		}
	}
}
