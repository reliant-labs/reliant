// Copyright (c) 2025 Reliant Labs
package main

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// demoSrc is a forge demo bundle in miniature, shaped like #2669:
//
//	demoBundle/main.tsx -> demoBundle/DemoRoot.tsx -> hooks/useOrgs.ts -> store/authStore.ts
//	                                               -> gen/reliant/v1/org_pb.ts
//	components/Chat/Chat.tsx (Reliant only: no demo module imports it)
func demoSrc() map[string]string {
	return map[string]string{
		demoEntry: `import "../index.css"; import { DemoRoot } from "./DemoRoot"`,
		"frontends/web/src/index.css": `@import "tailwindcss";
@import "./styles/cursor-fixes.css";`,
		"frontends/web/src/styles/cursor-fixes.css":      `@import "../themes/chart-palette.css";`,
		"frontends/web/src/themes/chart-palette.css":     `:root { --c: 1 }`,
		"frontends/web/src/themes/unused.css":            `:root { --u: 1 }`,
		"frontends/web/src/demoBundle/DemoRoot.tsx":      `import { useOrgs } from "@/hooks/useOrgs"; import "../gen/reliant/v1/org_pb"`,
		"frontends/web/src/hooks/useOrgs.ts":             `import { useAuthStore } from "../store/authStore"`,
		"frontends/web/src/store/authStore.ts":           `import type { T } from "../components/Chat/Chat"; export const useAuthStore = 1`,
		"frontends/web/src/gen/reliant/v1/org_pb.ts":     `export {}`,
		"frontends/web/src/components/Chat/Chat.tsx":     `export const Chat = 1`,
		"frontends/web/src/hooks/__tests__/orgs.test.ts": `import { useOrgs } from "../useOrgs"`,
	}
}

func demoGraphOf(src map[string]string) *Graph {
	g := testGraph()
	g.Web = BuildWebGraph(slices.Collect(maps.Keys(src)), func(p string) (string, bool) { s, ok := src[p]; return s, ok })
	return g
}

func TestWebDemoBuildFollowsTheBundlesImportGraph(t *testing.T) {
	for _, tc := range []struct {
		name string
		twin bool // hooks/useOrgs.demo.ts exists (fa551d3a0's fix for #2669)
		path string
		want bool
		why  string
	}{
		// #2669: useOrgs.ts gained an import of authStore.ts, which the demo
		// bundle's guard refuses; tsc -b passed.
		{"a module the bundle imports", false, "frontends/web/src/hooks/useOrgs.ts", true, "in the forge demo bundle's import graph"},
		{"a module two imports deep", false, "frontends/web/src/store/authStore.ts", true, "import graph"},
		{"the entry itself", false, demoEntry, true, "import graph"},
		{"a stylesheet the entry imports", false, "frontends/web/src/index.css", true, "import graph"},
		// index.css @imports styles/ and themes/ sheets; a broken nested
		// @import breaks the demo build as surely as a direct one.
		{"a stylesheet index.css imports", false, "frontends/web/src/styles/cursor-fixes.css", true, "import graph"},
		{"a stylesheet two @imports deep", false, "frontends/web/src/themes/chart-palette.css", true, "import graph"},
		{"a stylesheet nothing imports", false, "frontends/web/src/themes/unused.css", false, ""},
		{"a type-only import is no edge", false, "frontends/web/src/components/Chat/Chat.tsx", false, ""},
		{"a test file", false, "frontends/web/src/hooks/__tests__/orgs.test.ts", false, ""},
		{"a proto whose client the bundle imports", false, "proto/reliant/v1/org.proto", true, "regenerates org_pb.ts"},
		{"a proto whose client it does not", false, "proto/reliant/v1/chat.proto", false, ""},
		// With a twin the build substitutes it: what the original imports
		// is not in the bundle, but the original and the twin both are
		// members (an importer resolves through them).
		{"twin: the original is a member", true, "frontends/web/src/hooks/useOrgs.ts", true, "import graph"},
		{"twin: the twin is a member", true, "frontends/web/src/hooks/useOrgs.demo.ts", true, "import graph"},
		{"twin: the original's imports are not", true, "frontends/web/src/store/authStore.ts", false, ""},
		{"the demo build's plugin", false, "frontends/web/vite-plugin-demo-bundle.ts", true, "the forge demo build reads it"},
		{"the demo build's HTML entry", false, "frontends/web/demo.html", true, "the forge demo build reads it"},
		{"the web dependencies (FULL web lane)", false, "frontends/web/package.json", true, "package.json"},
		{"a Go change", false, "internal/leaf/leaf.go", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := demoSrc()
			if tc.twin {
				src["frontends/web/src/hooks/useOrgs.demo.ts"] = `export const useOrgs = () => []`
			}
			pl := ClassifyReach([]Change{{Path: tc.path}}, demoGraphOf(src), ReachDirect)
			if pl.WebDemo.Full != tc.want {
				t.Fatalf("web-demo full = %v, want %v (reasons %v)", pl.WebDemo.Full, tc.want, pl.WebDemo.Reasons)
			}
			if tc.want && !strings.Contains(strings.Join(pl.WebDemo.Reasons, "; "), tc.why) {
				t.Fatalf("reasons %v do not say %q", pl.WebDemo.Reasons, tc.why)
			}
		})
	}
}

// Without its entry the bundle's graph is unknown: any web change builds it.
func TestWebDemoBuildFailsSafeWithoutItsEntry(t *testing.T) {
	src := demoSrc()
	delete(src, demoEntry)
	pl := classifyIn(demoGraphOf(src), "frontends/web/src/components/Chat/Chat.tsx")
	if !pl.WebDemo.Full || !strings.Contains(strings.Join(pl.WebDemo.Reasons, " "), "fail-safe") {
		t.Fatalf("web-demo = %+v, want FULL (fail-safe)", pl.WebDemo)
	}
	if pl := classifyIn(demoGraphOf(src), "internal/leaf/leaf.go"); pl.WebDemo.Full {
		t.Fatalf("a Go change selected the demo build: %v", pl.WebDemo.Reasons)
	}
}

func TestWebDemoBuildRunsOnAnUnclaimedPath(t *testing.T) {
	if pl := classify("some-new-top-level-thing/x.dat"); !pl.WebDemo.Full {
		t.Fatal("a path nothing claims must run the demo build too")
	}
}

func TestRunnerBuildsTheDemoBundleWhenSelected(t *testing.T) {
	pl := ClassifyReach([]Change{{Path: "frontends/web/src/hooks/useOrgs.ts"}}, demoGraphOf(demoSrc()), ReachDirect)
	r := &runner{root: "/repo"}
	var demo *step
	for _, s := range r.steps(pl, laneSet(defaultLanes)) {
		if s.name == "forge demo bundle builds" {
			demo = &s
		}
	}
	if demo == nil {
		t.Fatal("no demo build step")
	}
	if demo.dir != "/repo/frontends/web" || !slices.Equal(demo.env, []string{"WEB_BUILD_TARGET=demo"}) || strings.Join(demo.argv, " ") != "npx vite build --logLevel warn" {
		t.Fatalf("demo step = %+v", *demo)
	}
	for _, s := range r.steps(pl, laneSet("go,web,electron")) {
		if s.name == demo.name {
			t.Fatal("the demo build ran with its lane disabled (the merge gate runs it as its own phase)")
		}
	}
}

// A FULL web lane (a package.json change) runs the whole suite by default,
// and with -web-full reached (the merge gate) only the files the diff's own
// web modules reach: #2669's failing tests were among them while #2693's
// package.json made its queue group's web lane FULL.
func TestRunnerWebFullFallbackRunsTheReachedFiles(t *testing.T) {
	pl := ClassifyReach([]Change{{Path: "frontends/web/package.json"}, {Path: "frontends/web/src/lib/leaf.ts"}}, testGraph(), ReachDirect)
	if !pl.Web.Full || !slices.Contains(pl.Web.Tests, "src/lib/__tests__/leaf.test.ts") {
		t.Fatalf("plan: full=%v tests=%v", pl.Web.Full, pl.Web.Tests)
	}
	webStep := func(r *runner, pl *Plan) []string {
		for _, s := range r.steps(pl, laneSet("web")) {
			if strings.HasPrefix(s.name, "web") {
				return s.argv
			}
		}
		return nil
	}
	if got := strings.Join(webStep(&runner{root: "/repo"}, pl), " "); got != "npx vitest run" {
		t.Fatalf("default FULL web step = %q, want the whole suite", got)
	}
	got := webStep(&runner{root: "/repo", webFull: webFullReached}, pl)
	if !slices.Equal(got, append([]string{"npx", "vitest", "run"}, pl.Web.Tests...)) {
		t.Fatalf("reached FULL web step = %v, want vitest over %v", got, pl.Web.Tests)
	}
	// A FULL fallback with no source change runs no vitest here.
	pl = ClassifyReach([]Change{{Path: "frontends/web/package.json"}}, testGraph(), ReachDirect)
	if got := webStep(&runner{root: "/repo", webFull: webFullReached}, pl); got != nil {
		t.Fatalf("a manifest-only FULL fallback ran %v", got)
	}
}
