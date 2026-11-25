// Copyright (c) 2025 Reliant Labs
package main

import (
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const mod = "github.com/reliant-labs/reliant"

// testGraph is the web import graph (webSrc), an electron test that names a
// workflow, and no generate-hook packages (TestForgeGenHooksOnly adds some).
func testGraph() *Graph {
	g := &Graph{Module: mod, DirPkg: map[string]string{}, Files: map[string]FileInfo{}, BuildDeps: map[string]map[string]bool{}}
	g.Web = BuildWebGraph(webFiles(), func(p string) (string, bool) { s, ok := webSrc[p]; return s, ok })
	g.ElectronText = map[string]string{
		"electron/test/release.test.js": `fs.readFileSync(path.join(REPO_ROOT, '.github/workflows/release.yml'))`,
	}
	return g
}

func path0(n string) string { return n[strings.LastIndex(n, "/")+1:] }

var webSrc = map[string]string{
	"frontends/web/src/lib/leaf.ts":                       `export const x = 1`,
	"frontends/web/src/lib/mid.ts":                        `import { x } from "./leaf"; export const y = x`,
	"frontends/web/src/lib/typeonly.ts":                   `import type { T } from "./leaf"; export type U = T`,
	"frontends/web/src/lib/dyn.ts":                        `export const load = () => import("./leaf")`,
	"frontends/web/src/lib/alias.ts":                      `import { y } from "@/lib/mid"; export const z = y`,
	"frontends/web/src/lib/__tests__/leaf.test.ts":        `import { x } from "../leaf"`,
	"frontends/web/src/lib/__tests__/mid.test.ts":         `import { y } from "../mid"`,
	"frontends/web/src/lib/__tests__/alias.test.ts":       `import { z } from "@/lib/alias"`,
	"frontends/web/src/lib/__tests__/typeonly.test.ts":    `import { U } from "../typeonly"`,
	"frontends/web/src/lib/__tests__/mock.test.ts":        `vi.mock("../leaf", () => ({}))`,
	"frontends/web/src/lib/__tests__/unrelated.test.ts":   `import "../unrelated"`,
	"frontends/web/src/lib/unrelated.ts":                  `export {}`,
	"frontends/web/src/lib/__tests__/guard.test.ts":       "readdirSync(path.resolve(__dirname, '..'))",
	"frontends/web/src/lib/__tests__/readscss.test.ts":    "readFileSync(path.resolve(__dirname, '../../index.css'))",
	"frontends/web/src/lib/__tests__/readsgo.test.ts":     "resolve(process.cwd(), '../../internal/db/core/trigger.go')",
	"frontends/web/src/index.css":                         ``,
	"frontends/web/src/gen/reliant/v1/chat_pb.ts":         `export {}`,
	"frontends/web/src/lib/__tests__/genclient.test.ts":   `import "../../gen/reliant/v1/chat_pb"`,
	"frontends/web/e2e/observability.spec.ts":             `test("x")`,
	"frontends/web/src/components/index.ts":               `export * from "./Button"`,
	"frontends/web/src/components/Button.tsx":             `export const Button = 1`,
	"frontends/web/src/components/__tests__/btn.test.tsx": `import { Button } from ".."`,
}

func webFiles() []string {
	out := make([]string, 0, len(webSrc))
	for p := range webSrc {
		out = append(out, p)
	}
	return out
}

func classify(paths ...string) *Plan {
	return classifyIn(testGraph(), paths...)
}

func classifyIn(g *Graph, paths ...string) *Plan {
	return classifyWith(g, nil, ReachAll, paths...)
}

// classifyWith is Classify over paths with forge's decision fd (nil: forge
// selects nothing and runs no path-gated row).
// goModFull is forge's decision for a go.mod change: the whole Go lane, by a
// rule (not the fail-safe).
var goModFull = &ForgeDecision{Full: true, Reason: "go.mod changed — a Go dependency or toolchain pin changed (ci.gate.affected_rules[0])"}

// unplacedFile is forge's decision for a file it cannot place: the fail-safe.
var unplacedFile = &ForgeDecision{Full: true, Reason: "some-new-top-level-thing/x.dat changed, and it is not Go source, an embedded file or testdata of a package — nothing proves which tests read it"}

// forgeSelects is a forge decision that runs these packages (./relative).
func forgeSelects(pkgs ...string) *ForgeDecision {
	fd := &ForgeDecision{}
	for _, p := range pkgs {
		fd.Packages = append(fd.Packages, mod+"/"+strings.TrimPrefix(p, "./"))
	}
	return fd
}

func classifyWith(g *Graph, fd *ForgeDecision, reachMode string, paths ...string) *Plan {
	var ch []Change
	for _, p := range paths {
		ch = append(ch, Change{Path: p})
	}
	return Classify(ch, fd, g, reachMode)
}

func TestServiceCatalogAndDemoFilesSelectOnlyTheirReaders(t *testing.T) {
	g := testGraph()
	src := maps.Clone(webSrc)
	src["frontends/web/src/demo/__tests__/fixtures.test.ts"] = `const read = (p) => readFileSync(resolve(WEB, p));
read("scripts/demo/capture-forge-scaffold.mjs"); read("scripts/demo/capture-forge-scaffold.sh")`
	src["frontends/web/vite-demo-target.test.ts"] = `path.resolve(__dirname, "demo.html")`
	files := slices.Collect(maps.Keys(src))
	g.Web = BuildWebGraph(files, func(p string) (string, bool) { s, ok := src[p]; return s, ok })
	// The demo scripts are web code (.mjs) under frontends/web/: like any web
	// module they also run the tests that walk its directories (guard.test.ts).
	const walker = "src/lib/__tests__/guard.test.ts"
	for p, want := range map[string][]string{
		"gen/forge_service_catalog.json":                        nil,
		"frontends/web/demo.html":                               {"vite-demo-target.test.ts"},
		"frontends/web/scripts/demo/capture-forge-scaffold.mjs": {"src/demo/__tests__/fixtures.test.ts", walker},
		"frontends/web/scripts/demo/capture-forge-scaffold.sh":  {"src/demo/__tests__/fixtures.test.ts", walker},
		"frontends/web/scripts/demo/smoke-demo-site.mjs":        {walker},
	} {
		for _, mode := range []string{ReachAll, ReachDirect} {
			pl := Classify([]Change{{Path: p}}, nil, g, mode)
			if pl.Go.Full || pl.Web.Full || pl.Electron.Full || pl.DocGen.Full {
				t.Errorf("%s (%s) is claimed: no lane may run in full: go=%v web=%v electron=%v docs-generate=%v (%v)",
					p, mode, pl.Go.Full, pl.Web.Full, pl.Electron.Full, pl.DocGen.Full, pl.Go.Reasons)
			}
			if len(pl.Go.Packages) != 0 {
				t.Errorf("%s (%s): no Go test reads it, got %v", p, mode, pl.Go.Packages)
			}
			if !slices.Equal(pl.Web.Tests, want) {
				t.Errorf("%s (%s): web tests %v, want %v", p, mode, pl.Web.Tests, want)
			}
		}
	}
	// The catalog is forge generate's output: the no-op check still runs.
	if pl := classifyIn(g, "gen/forge_service_catalog.json"); !pl.ForgeGen.Full || strings.Contains(strings.Join(pl.ForgeGen.Reasons, " "), "fail-safe") {
		t.Errorf("the service catalog runs forge-generate as its output, not as the fail-safe: %v", pl.ForgeGen.Reasons)
	}
}

func TestForgeInputsRunForgeGenerate(t *testing.T) {
	for _, p := range []string{
		"proto/reliant/v1/chat.proto", "db/migrations/20261101000000_x.up.sql", "internal/vault/contract.go",
		"internal/vault/doc.go", "internal/handlers/chat/rpc_send.go", "internal/vault/mock_gen.go",
		"gen/forge_descriptor.json", "deploy/observability/x.json", ".forge/hashes.json",
		"forge.yaml", "go.mod", "internal/handlers/chat/handlers_crud_ops_gen_test.go",
		// Generated files outside gen/ and src/gen/: a merge with main can
		// leave one stale, and only generate re-renders it.
		"frontends/web/src/hooks/chat-service-hooks_gen.ts", "internal/handlers/chat/handlers_scaffold_gen_test.go",
	} {
		pl := classify(p)
		if !pl.ForgeGen.Full {
			t.Errorf("%s is a forge generate input or output: the forge-generate lane must run", p)
		}
		if !strings.Contains(strings.Join(pl.ForgeGen.Reasons, " "), p) {
			t.Errorf("%s: the reason must name the path: %v", p, pl.ForgeGen.Reasons)
		}
	}
	// A handler package's hand-written tests: forge reads none of them.
	g := testGraph()
	for _, p := range []string{"internal/leaf/leaf.go", "frontends/web/src/lib/leaf.ts", "electron/src/menu-shortcuts.js", "docs/x.md", ".github/workflows/ci.yml",
		"internal/handlers/chat/rpc_send_test.go", "internal/handlers/chat/handlers_scaffold_send_test.go"} {
		if pl := classifyIn(g, p); pl.ForgeGen.Full {
			t.Errorf("%s is not a forge input: forge-generate must not run (%v)", p, pl.ForgeGen.Reasons)
		}
	}
}

func TestForgeInputClaimsNothingForTheTestLanes(t *testing.T) {
	// deploy/observability is claimed by the data rule: being a forge output
	// must not change what the test lanes select for it.
	pl := classify("deploy/observability/x.json")
	if pl.Go.Full || pl.Web.Full || pl.Electron.Full {
		t.Fatalf("a forge input must not widen the test lanes: go=%v web=%v electron=%v", pl.Go.Full, pl.Web.Full, pl.Electron.Full)
	}
}

func TestElectronTestNamingAWorkflowRunsWhenItChanges(t *testing.T) {
	pl := classify(".github/workflows/release.yml")
	if !pl.Electron.Full {
		t.Fatalf("electron/test/release.test.js reads release.yml: the electron lane must run")
	}
}

func TestWebSelectsImportersTransitively(t *testing.T) {
	pl := classify("frontends/web/src/lib/leaf.ts")
	want := []string{
		"src/lib/__tests__/alias.test.ts", "src/lib/__tests__/leaf.test.ts", "src/lib/__tests__/mid.test.ts", "src/lib/__tests__/mock.test.ts",
	}
	got := pl.Web.Tests
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("leaf.ts change must select %s (transitive importer, alias, or vi.mock); got %v", w, got)
		}
	}
	if slices.Contains(got, "src/lib/__tests__/unrelated.test.ts") {
		t.Errorf("unrelated test selected: %v", got)
	}
	if slices.Contains(got, "src/lib/__tests__/typeonly.test.ts") {
		t.Errorf("type-only imports are erased (verbatimModuleSyntax) and must not be an edge: %v", got)
	}
}

func TestWebDynamicImportIsAnEdge(t *testing.T) {
	g := testGraph()
	g.Web = BuildWebGraph(append(webFiles(), "frontends/web/src/lib/__tests__/dyn.test.ts"), func(p string) (string, bool) {
		if p == "frontends/web/src/lib/__tests__/dyn.test.ts" {
			return `import { load } from "../dyn"`, true
		}
		s, ok := webSrc[p]
		return s, ok
	})
	pl := Classify([]Change{{Path: "frontends/web/src/lib/leaf.ts"}}, nil, g, ReachAll)
	if !slices.Contains(pl.Web.Tests, "src/lib/__tests__/dyn.test.ts") {
		t.Fatalf("import() must be followed: %v", pl.Web.Tests)
	}
}

func TestWebDirectoryIndexResolves(t *testing.T) {
	pl := classify("frontends/web/src/components/Button.tsx")
	if !slices.Contains(pl.Web.Tests, "src/components/__tests__/btn.test.tsx") {
		t.Fatalf("an import of a directory must resolve to its index: %v", pl.Web.Tests)
	}
}

func TestWebTestFileSelectsItself(t *testing.T) {
	pl := classify("frontends/web/src/lib/__tests__/unrelated.test.ts")
	if got, want := pl.Web.Tests, []string{"src/lib/__tests__/unrelated.test.ts"}; !slices.Equal(got, want) {
		t.Fatalf("a changed test selects itself: %v want %v", got, want)
	}
}

func TestWebFileReadingTestsFollowTheirInput(t *testing.T) {
	pl := classify("frontends/web/src/index.css")
	if !slices.Contains(pl.Web.Tests, "src/lib/__tests__/readscss.test.ts") {
		t.Fatalf("a test that reads index.css must run when it changes: %v", pl.Web.Tests)
	}
	pl = classify("internal/db/core/trigger.go")
	if !slices.Contains(pl.Web.Tests, "src/lib/__tests__/readsgo.test.ts") {
		t.Fatalf("a web test that reads a Go source must run when it changes: %v", pl.Web.Tests)
	}
}

func TestWebTreeWalkersRunOnAnySourceChange(t *testing.T) {
	pl := classify("frontends/web/src/lib/unrelated.ts")
	if !slices.Contains(pl.Web.Tests, "src/lib/__tests__/guard.test.ts") {
		t.Fatalf("a test that reads whole directories must run on any web source change: %v", pl.Web.Tests)
	}
}

func TestWebDeletedModuleSelectsWhatStillReachesIt(t *testing.T) {
	g := testGraph()
	// leaf.ts is deleted: it is absent from disk (read fails) but present in
	// the file list, which holds the base tree's paths too.
	g.Web = BuildWebGraph(webFiles(), func(p string) (string, bool) {
		if p == "frontends/web/src/lib/leaf.ts" {
			return "", false
		}
		s, ok := webSrc[p]
		return s, ok
	})
	pl := Classify([]Change{{Path: "frontends/web/src/lib/leaf.ts", Deleted: true}}, nil, g, ReachAll)
	if pl.Web.Full {
		t.Fatalf("a deleted module must not force the full web lane: %v", pl.Web.Reasons)
	}
	for _, w := range []string{"src/lib/__tests__/leaf.test.ts", "src/lib/__tests__/mid.test.ts", "src/lib/__tests__/mock.test.ts", "src/lib/__tests__/guard.test.ts"} {
		if !slices.Contains(pl.Web.Tests, w) {
			t.Errorf("%s still reaches the deleted leaf.ts (or walks the tree) and must run; got %v", w, pl.Web.Tests)
		}
	}
	if slices.Contains(pl.Web.Tests, "src/lib/__tests__/unrelated.test.ts") {
		t.Errorf("unrelated test selected: %v", pl.Web.Tests)
	}
	pl = Classify([]Change{{Path: "frontends/web/src/lib/__tests__/leaf.test.ts", Deleted: true}}, nil, testGraph(), ReachAll)
	if pl.Web.Full || len(pl.Web.Tests) != 0 {
		t.Fatalf("a deleted TEST file selects nothing: full=%v tests=%v", pl.Web.Full, pl.Web.Tests)
	}
}

func TestWebReachDirect(t *testing.T) {
	direct := Classify([]Change{{Path: "frontends/web/src/lib/leaf.ts"}}, nil, testGraph(), ReachDirect)
	for _, w := range []string{"src/lib/__tests__/leaf.test.ts", "src/lib/__tests__/mid.test.ts", "src/lib/__tests__/mock.test.ts", "src/lib/__tests__/guard.test.ts"} {
		if !slices.Contains(direct.Web.Tests, w) {
			t.Errorf("direct: %s must run; got %v", w, direct.Web.Tests)
		}
	}
	if slices.Contains(direct.Web.Tests, "src/lib/__tests__/alias.test.ts") {
		t.Errorf("direct: alias.test.ts is three imports from leaf.ts and must be deferred; got %v", direct.Web.Tests)
	}
	if want := []string{"src/lib/__tests__/alias.test.ts"}; !slices.Equal(direct.Web.Deferred, want) || direct.Web.Reach != ReachDirect {
		t.Errorf("direct: reach %q deferred %v, want direct %v", direct.Web.Reach, direct.Web.Deferred, want)
	}
	// A hub (more than webHubFanout tests within two edges) runs only the
	// tests that import or mock it by name.
	g := testGraph()
	src := map[string]string{"frontends/web/src/store/hub.ts": `export const s = 1`,
		"frontends/web/src/store/__tests__/hub.test.ts": `import { s } from "../hub"`}
	for i := 0; i <= webHubFanout; i++ {
		src[fmt.Sprintf("frontends/web/src/c/C%d.tsx", i)] = `import { s } from "@/store/hub"`
		src[fmt.Sprintf("frontends/web/src/c/__tests__/C%d.test.tsx", i)] = fmt.Sprintf(`import "../C%d"`, i)
	}
	files := slices.Collect(maps.Keys(src))
	g.Web = BuildWebGraph(files, func(p string) (string, bool) { s, ok := src[p]; return s, ok })
	hub := Classify([]Change{{Path: "frontends/web/src/store/hub.ts"}}, nil, g, ReachDirect)
	if want := []string{"src/store/__tests__/hub.test.ts"}; !slices.Equal(hub.Web.Tests, want) {
		t.Errorf("direct: a hub runs only its by-name importers' tests: %v, want %v", hub.Web.Tests, want)
	}
	if len(hub.Web.Deferred) != webHubFanout+1 {
		t.Errorf("direct: the hub's %d further tests are deferred, got %d", webHubFanout+1, len(hub.Web.Deferred))
	}
	if all := Classify([]Change{{Path: "frontends/web/src/store/hub.ts"}}, nil, g, ReachAll); len(all.Web.Tests) != webHubFanout+2 {
		t.Errorf("all: every test reaching the hub runs, got %d", len(all.Web.Tests))
	}

	// Modules that are no hub alone but together reach more than
	// webNearBudget files at two edges: one edge from each.
	g = testGraph()
	src = map[string]string{}
	var changed []Change
	per := webNearBudget/3 + 1 // three modules, each under the hub line
	for m := 0; m < 3; m++ {
		mod := fmt.Sprintf("frontends/web/src/m%d.ts", m)
		src[mod] = `export const v = 1`
		src[fmt.Sprintf("frontends/web/src/__tests__/m%d.test.ts", m)] = fmt.Sprintf(`import "../m%d"`, m)
		for i := 0; i < per; i++ {
			src[fmt.Sprintf("frontends/web/src/c%d/C%d.tsx", m, i)] = fmt.Sprintf(`import { v } from "@/m%d"`, m)
			src[fmt.Sprintf("frontends/web/src/c%d/__tests__/C%d.test.tsx", m, i)] = fmt.Sprintf(`import "../C%d"`, i)
		}
		changed = append(changed, Change{Path: mod})
	}
	if per > webHubFanout {
		t.Fatalf("fixture: %d importers per module makes each a hub; lower it", per)
	}
	files = slices.Collect(maps.Keys(src))
	g.Web = BuildWebGraph(files, func(p string) (string, bool) { s, ok := src[p]; return s, ok })
	wide := Classify(changed, nil, g, ReachDirect)
	if want := []string{"src/__tests__/m0.test.ts", "src/__tests__/m1.test.ts", "src/__tests__/m2.test.ts"}; !slices.Equal(wide.Web.Tests, want) || wide.Web.Edges != 1 {
		t.Errorf("direct: past webNearBudget each module runs only its by-name tests: edges %d, %v, want %v", wide.Web.Edges, wide.Web.Tests, want)
	}
	if narrow := Classify(changed[:1], nil, g, ReachDirect); len(narrow.Web.Tests) != per+1 || narrow.Web.Edges != 2 {
		t.Errorf("direct: one such module alone keeps two edges: edges %d, %d tests, want %d", narrow.Web.Edges, len(narrow.Web.Tests), per+1)
	}

	all := classify("frontends/web/src/lib/leaf.ts")
	if !slices.Contains(all.Web.Tests, "src/lib/__tests__/alias.test.ts") || len(all.Web.Deferred) != 0 || all.Web.Reach != ReachAll {
		t.Errorf("all: tests %v deferred %v reach %q: every importer's test must run", all.Web.Tests, all.Web.Deferred, all.Web.Reach)
	}
	// A changed test runs whatever its distance, and a proto's generated
	// client fans out under the same cut.
	pl := Classify([]Change{{Path: "frontends/web/src/lib/__tests__/alias.test.ts"}}, nil, testGraph(), ReachDirect)
	if !slices.Equal(pl.Web.Tests, []string{"src/lib/__tests__/alias.test.ts"}) {
		t.Errorf("direct: a changed test selects itself: %v", pl.Web.Tests)
	}
	pl = Classify([]Change{{Path: "proto/reliant/v1/chat.proto"}}, nil, testGraph(), ReachDirect)
	if !slices.Contains(pl.Web.Tests, "src/lib/__tests__/genclient.test.ts") {
		t.Errorf("direct: a proto change must run the tests importing its client: %v", pl.Web.Tests)
	}
}

func TestPlaywrightSpecsRunNoVitest(t *testing.T) {
	pl := classify("frontends/web/e2e/observability.spec.ts")
	if pl.Web.Full || len(pl.Web.Tests) != 0 {
		t.Fatalf("a Playwright spec is not a vitest input: full=%v tests=%v", pl.Web.Full, pl.Web.Tests)
	}
}

func TestProtoSelectsItsGeneratedClients(t *testing.T) {
	pl := classify("proto/reliant/v1/chat.proto")
	if !slices.Contains(pl.Web.Tests, "src/lib/__tests__/genclient.test.ts") {
		t.Fatalf("a proto must select the web tests importing its generated client: %v", pl.Web.Tests)
	}
	if pl.Web.Full {
		t.Fatalf("a proto must not force the web lane: %v", pl.Web.Reasons)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pat, p string
		want   bool
	}{
		{"proto/**", "proto/reliant/v1/a.proto", true},
		{"proto/**/*.proto", "proto/reliant/v1/a.proto", true},
		{"proto/**/*.proto", "proto/a.proto", true},
		{"**/*.md", "a/b/c.md", true},
		{"**/*.md", "c.md", true},
		{"frontends/web/tsconfig*.json", "frontends/web/tsconfig.app.json", true},
		{"frontends/web/tsconfig*.json", "frontends/web/src/tsconfig.json", false},
		{"go.mod", "sub/go.mod", false},
		{".air*.toml", ".air.api.toml", true},
	}
	for _, tc := range cases {
		if got := matchGlob(tc.pat, tc.p); got != tc.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tc.pat, tc.p, got, tc.want)
		}
	}
}

func TestEveryRuleIdIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range rules {
		if seen[r.id] {
			t.Errorf("duplicate rule id %q", r.id)
		}
		seen[r.id] = true
		if len(r.patterns) == 0 {
			t.Errorf("rule %q has no patterns", r.id)
		}
		if r.full && r.reason == "" {
			t.Errorf("rule %q forces full without a reason", r.id)
		}
	}
}

func TestRunnerStepsUseExplicitFilesAndRunDBTestsInline(t *testing.T) {
	pl := classifyWith(testGraph(), forgeSelects("./internal/db", "./internal/leaf"), ReachAll, "internal/leaf/leaf.go", "db/migrations/20261101000000_x.up.sql", "frontends/web/src/lib/leaf.ts")
	r := &runner{root: "/repo"}
	steps := r.steps(pl, laneSet("go,web,electron"))
	var names []string
	for _, s := range steps {
		names = append(names, s.name)
	}
	if !slices.Equal(names, []string{"go", "web"}) {
		t.Fatalf("steps = %v, want [go web]", names)
	}
	for _, s := range steps {
		joined := strings.Join(s.argv, " ")
		switch s.name {
		case "go":
			if !slices.Contains(s.argv, "./internal/db") || !slices.Contains(s.argv, "./internal/leaf") {
				t.Errorf("the go step must run the migration's DB package beside the code change's: %s", joined)
			}
			if !strings.Contains(joined, "-short") || strings.Contains(joined, "./...") {
				t.Errorf("go step = %s", joined)
			}
			if len(s.env) != 0 {
				t.Errorf("the go step needs no environment of its own; got %v", s.env)
			}
		case "web":
			if !strings.HasPrefix(joined, "npx vitest run ") || !strings.Contains(joined, " src/lib/__tests__/leaf.test.ts") {
				t.Errorf("web step must hand vitest explicit files: %s", joined)
			}
		}
	}
}

func TestRunnerFullLanesRunEverything(t *testing.T) {
	pl := classifyWith(testGraph(), goModFull, ReachAll, "go.mod", "frontends/web/package.json")
	steps := (&runner{root: "/repo"}).steps(pl, laneSet("go,web"))
	var got []string
	for _, s := range steps {
		got = append(got, strings.Join(s.argv, " "))
	}
	if len(got) != 3 || !strings.HasPrefix(got[0], "pnpm install --frozen-lockfile --lockfile-only") ||
		!strings.HasSuffix(got[1], "./...") || got[2] != "npx vitest run" {
		t.Fatalf("full lanes = %v", got)
	}
}

func TestLockfileChangeChecksTheInstallFirst(t *testing.T) {
	r := &runner{root: "/repo"}
	for _, tc := range []struct {
		path, lanes, dir, lock string
	}{
		{"frontends/web/package.json", "web", "/repo/frontends/web", pnpmLock},
		{"frontends/web/pnpm-lock.yaml", "web", "/repo/frontends/web", pnpmLock},
		{"frontends/web/pnpm-workspace.yaml", "web", "/repo/frontends/web", pnpmLock},
		{"electron/pnpm-lock.yaml", "electron", "/repo/electron", pnpmLock},
		{"electron/pnpm-workspace.yaml", "electron", "/repo/electron", pnpmLock},
		{"pnpm-lock.yaml", "electron", "/repo", pnpmLock},
		{"tools/protoc-gen-es/package.json", "forge-generate", "/repo/tools/protoc-gen-es", pnpmLock},
	} {
		steps := r.steps(classify(tc.path), laneSet(tc.lanes))
		if len(steps) == 0 || strings.Join(steps[0].argv, " ") != strings.Join(lockfileArgv(tc.lock), " ") || steps[0].dir != tc.dir {
			t.Errorf("%s: the first step must be the lockfile check in %s: %+v", tc.path, tc.dir, steps)
		}
	}
	// The check belongs to the lanes that install from that project.
	for _, s := range r.steps(classify("frontends/web/pnpm-lock.yaml"), laneSet("go,electron")) {
		if strings.HasPrefix(s.name, "npm lockfile") {
			t.Errorf("-lanes without web must not check the web lockfile: %+v", s)
		}
	}
	// Both files of one project are one check.
	pl := classify("frontends/web/package.json", "frontends/web/pnpm-lock.yaml")
	if len(pl.Lockfiles) != 1 || pl.Lockfiles[0].Why != "changed: package.json, pnpm-lock.yaml" {
		t.Errorf("one project, one check: %+v", pl.Lockfiles)
	}
	// A package.json that is not an npm project (a fixture, a vendored package) is not one.
	if pl := classify("frontends/web/src/test/fixtures/package.json"); len(pl.Lockfiles) != 0 {
		t.Errorf("not an npm project: %+v", pl.Lockfiles)
	}
}

func TestNPMProjectsNameEveryLockfile(t *testing.T) {
	// The repository root and two levels below it, minus node_modules and
	// hidden directories. Not filepath.Glob("*/*/<lock>"): that lists every
	// second-level directory, .forge/ included, and Go's test cache keys this
	// test on each listing's entries and their mtimes, so every recorded test
	// run (a new .forge/tests/runs/<id>/, scripts/test-capture.sh) made it miss
	// the cache. No project keeps a lockfile in a hidden directory.
	subdirs := func(d string) []string {
		entries, err := os.ReadDir(filepath.Join(repoRoot, d))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "node_modules" {
				out = append(out, path.Join(d, e.Name()))
			}
		}
		return out
	}
	candidates := []string{"."}
	for _, d := range subdirs(".") {
		candidates = append(candidates, d)
		candidates = append(candidates, subdirs(d)...)
	}
	found := map[string]string{}
	for _, d := range candidates {
		for _, lock := range []string{npmLock, pnpmLock} {
			if _, err := os.Stat(filepath.Join(repoRoot, d, lock)); err == nil {
				found[d] = lock
			}
		}
	}
	for dir, lock := range found {
		if p, ok := npmProjects[dir]; !ok {
			t.Errorf("%s/%s is in no npmProjects entry: add it with the lanes that install from it", dir, lock)
		} else if p.lockfile != lock {
			t.Errorf("npmProjects says %s commits %s, the tree has %s", dir, p.lockfile, lock)
		}
	}
	for dir, p := range npmProjects {
		if found[dir] == "" {
			t.Errorf("npmProjects names %s, which has no %s", dir, p.lockfile)
		}
	}
}

func TestRunnerRunsOnlyFullLanesInAHeavySlot(t *testing.T) {
	r := &runner{root: "/repo", slot: []string{"forge", "machine", "slot", "run", "--class", "heavy", "--"}}
	for _, s := range r.steps(classifyWith(testGraph(), goModFull, ReachAll, "go.mod", "frontends/web/package.json"), laneSet("go,web")) {
		if strings.HasPrefix(s.name, "npm lockfile") {
			continue // a ~10s check, not a lane
		}
		if joined := strings.Join(s.argv, " "); !strings.HasPrefix(joined, "forge machine slot run --class heavy -- ") {
			t.Errorf("full step %q runs outside a heavy slot: %s", s.name, joined)
		}
	}
	for _, s := range r.steps(classifyWith(testGraph(), forgeSelects("./internal/leaf"), ReachAll, "internal/leaf/leaf.go", "frontends/web/src/lib/leaf.ts"), laneSet("go,web")) {
		if s.argv[0] == "forge" {
			t.Errorf("scoped step %q queues for a heavy slot: %s", s.name, strings.Join(s.argv, " "))
		}
	}
}

func TestRunnerRunsTheForgeGenerateCheckFirst(t *testing.T) {
	pl := classify("internal/vault/contract.go")
	steps := (&runner{root: "/repo"}).steps(pl, laneSet("go,web,electron,forge-generate"))
	if len(steps) == 0 || strings.Join(steps[0].argv, " ") != "task generate:check" || steps[0].dir != "/repo" {
		t.Fatalf("the forge-generate check must run first, from the repo root: %+v", steps)
	}
	for _, s := range (&runner{root: "/repo"}).steps(pl, laneSet("go")) {
		if strings.Contains(strings.Join(s.argv, " "), "generate:check") {
			t.Fatal("-lanes go must not run the forge-generate check")
		}
	}
}

func TestRunnerRunsTheDocsCheckAfterForgeAndBeforeTests(t *testing.T) {
	fd := forgeSelects("./internal/leaf")
	fd.Checks = []forgeCheck{{Name: "docs-generate", Runs: true, Note: "affected mode: internal/leaf/leaf.go changed"}}
	pl := classifyWith(testGraph(), fd, ReachAll, "internal/vault/contract.go", "internal/leaf/leaf.go")
	r := &runner{root: "/repo", slot: []string{"forge", "machine", "slot", "run", "--class", "heavy", "--"}}
	var got []string
	for _, s := range r.steps(pl, laneSet("go,web,electron,forge-generate,docs-generate")) {
		got = append(got, strings.Join(s.argv, " "))
	}
	if len(got) < 3 || got[0] != "task generate:check" ||
		got[1] != "forge machine slot run --class heavy -- task generate:docs:check" {
		t.Fatalf("the docs check runs second (after forge, before the tests), in a heavy slot: %v", got)
	}
	for _, s := range r.steps(pl, laneSet("go,forge-generate")) {
		if strings.Contains(strings.Join(s.argv, " "), "generate:docs:check") {
			t.Fatal("-lanes without docs-generate must not run the docs check")
		}
	}
}

func TestNothingToRunRunsNothing(t *testing.T) {
	pl := classify("docs/x.md")
	if steps := (&runner{root: "/repo"}).steps(pl, laneSet("go,web,electron,forge-generate,docs-generate")); len(steps) != 0 {
		t.Fatalf("an inert diff runs nothing, got %v", steps)
	}
}

func TestCompositionRootRunsForgeGenerate(t *testing.T) {
	for _, p := range []string{"internal/app/compose.go", "internal/app/providers_worker.go"} {
		if pl := classify(p); !pl.ForgeGen.Full || pl.ForgeGen.HooksOnly {
			t.Errorf("%s: the whole forge-generate lane must run (full %v, hooks only %v)", p, pl.ForgeGen.Full, pl.ForgeGen.HooksOnly)
		}
	}
	if pl := classify("internal/app/providers_worker_trigger_test.go"); pl.ForgeGen.Full {
		t.Errorf("internal/app's hand-written tests are not a forge input: %v", pl.ForgeGen.Reasons)
	}
}

func TestSDKModuleChangeIsClaimedAndRunsItsOwnTests(t *testing.T) {
	fd := forgeSelects("./internal/sdkuser")
	fd.Modules = []forgeModule{{Dir: "sdk/go", Path: mod + "/sdk/go", Packages: []string{mod + "/sdk/go/reliant"}}}
	pl := classifyWith(testGraph(), fd, ReachAll, "sdk/go/reliant/runs.go")
	if !slices.Contains(pl.Go.Packages, "./internal/sdkuser") {
		t.Fatalf("forge's root packages that link the SDK must run: %v", pl.Go.Packages)
	}
	if pl.Web.Full || pl.Electron.Full {
		t.Fatalf("an SDK edit forced the web or electron lane: web=%v electron=%v", pl.Web.Full, pl.Electron.Full)
	}
	if !pl.ForgeGen.Full {
		t.Fatal("the SDK holds forge's generated output; a change to it must run the no-op check")
	}

	r := &runner{root: "/repo"}
	steps := r.steps(pl, laneSet("go"))
	var sdk *step
	for i := range steps {
		if steps[i].name == "go (sdk/go module)" {
			sdk = &steps[i]
		}
	}
	if sdk == nil {
		t.Fatalf("no step runs the SDK module's tests: %+v", steps)
	}
	if sdk.dir != "/repo/sdk/go" || sdk.argv[len(sdk.argv)-1] != "./reliant" {
		t.Fatalf("SDK step = %+v", *sdk)
	}
}

func TestPublicAPIAllowlistDoesNotForceTheWebLane(t *testing.T) {
	pl := classify("proto/public-api.txt")
	if pl.Web.Full {
		t.Fatal("editing the public API allowlist forced the web lane")
	}
	if !pl.ForgeGen.Full {
		t.Fatal("the allowlist decides what is generated into the SDK; forge-generate must judge it")
	}
}

// The fail-safe: forge could not place a changed file, so nothing proves what
// reads it and every lane runs whole. A rule's full suite (go.mod) is not
// that: it reaches the Go lane only.
func TestForgeFailSafeRunsEveryLane(t *testing.T) {
	unplaced := &ForgeDecision{Full: true, Reason: "x.dat changed, and it is not Go source, an embedded file or testdata of a package — nothing proves which tests read it"}
	pl := classifyWith(testGraph(), unplaced, ReachAll, "x.dat")
	if !pl.Go.Full || !pl.Web.Full || !pl.Electron.Full || !pl.ForgeGen.Full || !pl.DocGen.Full || !pl.WebDemo.Full || !pl.WebBuild.Full {
		t.Fatalf("an unplaced file must run every lane: %+v", pl)
	}
	if pl.MigOrder.Full {
		t.Fatal("an unplaced file is no migration")
	}
	pl = classifyWith(testGraph(), goModFull, ReachAll, "go.mod")
	if !pl.Go.Full || pl.Web.Full || pl.Electron.Full || pl.WebDemo.Full {
		t.Fatalf("go.mod reaches the Go lane (and forge-generate), never web or electron: go=%v web=%v electron=%v", pl.Go.Full, pl.Web.Full, pl.Electron.Full)
	}
	if !pl.ForgeGen.Full {
		t.Fatal("go.mod is a forge generate input")
	}
}

// forge's Go decision passes through: packages, build set, deferred
// importers and why, in this plan's ./relative shape.
func TestForgeGoDecisionPassesThrough(t *testing.T) {
	fd := &ForgeDecision{
		Packages: []string{mod + "/internal/leaf", mod + "/internal/mid"}, Build: []string{mod + "/internal/leaf"},
		Deferred: []string{mod + "/cmd/app"}, TransitiveCap: 20,
		Why:    []forgeWhy{{Package: mod + "/internal/mid", File: "internal/leaf/leaf.go", Edge: "imports " + mod + "/internal/leaf"}},
		Checks: []forgeCheck{{Name: "electron", Runs: false}, {Name: "docs-generate", Runs: false}},
	}
	pl := classifyWith(testGraph(), fd, ReachDirect, "internal/leaf/leaf.go")
	gp := pl.Go
	if !slices.Equal(gp.Packages, []string{"./internal/leaf", "./internal/mid"}) || !slices.Equal(gp.Build, []string{"./internal/leaf"}) ||
		!slices.Equal(gp.Deferred, []string{"./cmd/app"}) || gp.Reach != ReachDirect || gp.ReachCap != 20 || gp.Full {
		t.Fatalf("go plan = %+v", gp)
	}
	if !strings.Contains(gp.Why["./internal/mid"], "internal/leaf/leaf.go") {
		t.Fatalf("why = %v", gp.Why)
	}
	if pl.Electron.Full || pl.DocGen.Full {
		t.Fatal("rows forge does not run must not run here")
	}
	fd.Checks = []forgeCheck{{Name: "electron", Runs: true, Note: "affected mode: electron/x.js changed"}}
	if pl := classifyWith(testGraph(), fd, ReachDirect, "electron/x.js"); !pl.Electron.Full {
		t.Fatal("forge's electron row runs: so does the electron lane")
	}
}
