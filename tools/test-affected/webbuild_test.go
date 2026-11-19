// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The renderer's release build runs when what only the bundler reads
// changed: #2829's supabase-js bump (package.json + pnpm-lock.yaml) imported
// `node:module`, passed vitest and tsc -b, and failed the release gate's
// webbuild (reliant#2850).
func TestWebBuildRunsWhenTheBundlersInputsChange(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
		why  string
	}{
		{"frontends/web/package.json", true, "dependencies changed"},
		{"frontends/web/pnpm-lock.yaml", true, "dependencies changed"},
		{"frontends/web/pnpm-workspace.yaml", true, "dependencies changed"},
		{"sdk/ts/package.json", true, "dependencies changed"},
		{"package-lock.json", true, "root Node manifest"},
		{"frontends/web/vite.config.ts", true, "the renderer's release build reads it"},
		{"frontends/web/vite-plugin-csp.ts", true, "the renderer's release build reads it"},
		{"frontends/web/index.html", true, "the renderer's release build reads it"},
		{"some-new-top-level-thing/x.dat", true, "some-new-top-level-thing/x.dat"},
		// Sources are tsc -b's and vitest's; the bundle of a source change is
		// the release gate's.
		{"frontends/web/src/lib/leaf.ts", false, ""},
		{"frontends/web/demo.html", false, ""},
		{"internal/leaf/leaf.go", false, ""},
		{"electron/package.json", false, ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			pl := classify(tc.path)
			if pl.WebBuild.Full != tc.want {
				t.Fatalf("web-build full = %v, want %v (reasons %v)", pl.WebBuild.Full, tc.want, pl.WebBuild.Reasons)
			}
			if tc.want && !strings.Contains(strings.Join(pl.WebBuild.Reasons, "; "), tc.why) {
				t.Fatalf("reasons %v do not say %q", pl.WebBuild.Reasons, tc.why)
			}
		})
	}
}

func TestRunnerBuildsTheRendererWhenSelected(t *testing.T) {
	pl := classify("frontends/web/pnpm-lock.yaml")
	r := &runner{root: "/repo"}
	var build *step
	for _, s := range r.steps(pl, laneSet(defaultLanes)) {
		if s.name == "renderer release build" {
			build = &s
		}
	}
	if build == nil {
		t.Fatal("no renderer release build step")
	}
	if build.dir != "/repo" || strings.Join(build.argv, " ") != "scripts/web-release-build.sh" {
		t.Fatalf("web-build step = %+v", *build)
	}
	for _, s := range r.steps(pl, laneSet("go,web,electron")) {
		if s.name == build.name {
			t.Fatal("the release build ran with its lane disabled (the merge gate runs it as its own phase)")
		}
	}
}

// A selected release build that never ran fails the gate, by name.
func TestSelectedWebBuildIsObliged(t *testing.T) {
	pl := &Plan{Files: []FileReport{{Path: "frontends/web/pnpm-lock.yaml"}}, WebBuild: WebBuildPlan{LaneFull: selected("dependencies changed")}}
	rows := map[string]int{"plan": 0, "build": 0, "test": 0, "typecheck": 0, "lint": 0}
	dir := gateDir(t, pl, rows)
	if code, _ := verify(t, dir); code == 0 || !strings.Contains(verdict(t, dir), "webbuild never completed") {
		t.Fatalf("a selected web build with no row passed: %q", verdict(t, dir))
	}
	rows["webbuild"] = 0
	if code, v := verify(t, gateDir(t, pl, rows)); code != 0 {
		t.Fatalf("a completed web build failed: %q", v)
	}
}

// merge-gate.sh reads the plan's web_build lane and runs the phase gate-verify
// obliges under the same name, with frontends/web installed for it.
func TestMergeGateRunsTheSelectedWebBuild(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "merge-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		`webbuild=$(j '.web_build.full // false')`,
		`[ "$webbuild" = true ]; then need+=(frontends/web); fi`,
		`phase webbuild scripts/web-release-build.sh`,
		`web-build $webbuild;`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("merge-gate.sh has no %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "scripts", "web-release-build.sh")); err != nil {
		t.Fatal(err)
	}
}
