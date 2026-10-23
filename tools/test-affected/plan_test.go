// Copyright (c) 2025 Reliant Labs
package main

import (
	"slices"
	"strings"
	"testing"
)

const mod = "github.com/reliant-labs/reliant"

// testGraph is a small module:
//
//	internal/leaf   <- internal/mid  <- cmd/app
//	internal/other                       (no relation)
//	internal/db     embeds db/migrations (not in the dir): DB-backed tests
//	internal/guard  walks every Go file
//	internal/deploy reads deploy/kcl/main.k
//	tools/docgen/cli a docs generator: compiles cmd/app (so mid and leaf),
//	                 internal/fixture (and its embedded data) and gen/reliant/v1
func testGraph() *Graph {
	pkg := func(n string) string { return mod + "/" + n }
	g := &Graph{
		Module:    mod,
		DirPkg:    map[string]string{},
		Files:     map[string]FileInfo{},
		HasEmbed:  map[string]bool{},
		TestDeps:  map[string]map[string]bool{},
		Readers:   map[string][]string{},
		Walkers:   map[string]bool{},
		DBTests:   map[string]bool{},
		BuildDeps: map[string]map[string]bool{},
	}
	for _, n := range []string{"internal/leaf", "internal/mid", "cmd/app", "internal/other", "internal/db", "internal/guard", "internal/deploy", "gen/reliant/v1", "internal/fixture"} {
		g.DirPkg[n] = pkg(n)
		g.Files[n+"/"+path0(n)+".go"] = FileInfo{Pkg: pkg(n), Kind: KindCode}
		g.Files[n+"/"+path0(n)+"_test.go"] = FileInfo{Pkg: pkg(n), Kind: KindTest}
	}
	g.Files["internal/leaf/live_test.go"] = FileInfo{Pkg: pkg("internal/leaf"), Kind: KindIgnored}
	g.Files["internal/fixture/data/x.json"] = FileInfo{Pkg: pkg("internal/fixture"), Kind: KindCode, Embedded: true}
	g.HasEmbed[pkg("internal/fixture")] = true
	dep := func(p string, ds ...string) {
		s := map[string]bool{pkg(p): true}
		for _, d := range ds {
			s[pkg(d)] = true
		}
		g.TestDeps[pkg(p)] = s
	}
	dep("internal/leaf")
	dep("internal/mid", "internal/leaf")
	dep("cmd/app", "internal/mid", "internal/leaf")
	dep("internal/other")
	dep("internal/db")
	dep("internal/guard")
	dep("internal/deploy")
	dep("internal/fixture")
	dep("gen/reliant/v1")
	// A generator has no tests: BuildDeps only, so it selects no Go package.
	g.DirPkg["tools/docgen/cli"] = pkg("tools/docgen/cli")
	g.Files["tools/docgen/cli/main.go"] = FileInfo{Pkg: pkg("tools/docgen/cli"), Kind: KindCode}
	g.BuildDeps[pkg("tools/docgen/cli")] = map[string]bool{}
	for _, d := range []string{"tools/docgen/cli", "cmd/app", "internal/mid", "internal/leaf", "internal/fixture", "gen/reliant/v1"} {
		g.BuildDeps[pkg("tools/docgen/cli")][pkg(d)] = true
	}
	g.DBTests[pkg("internal/db")] = true
	g.Walkers[pkg("internal/guard")] = true
	g.Readers[pkg("internal/deploy")] = []string{"deploy/kcl/main.k"}
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
	var ch []Change
	for _, p := range paths {
		ch = append(ch, Change{Path: p})
	}
	return Classify(ch, g)
}

// noWalkers is the test graph without the tree-walking guard package, whose
// "runs on any Go change" rule would otherwise appear in every selection.
func noWalkers() *Graph {
	g := testGraph()
	g.Walkers = map[string]bool{}
	return g
}

func goPkgs(pl *Plan) []string { return pl.Go.Packages }

func TestGoReverseDependencies(t *testing.T) {
	pl := classifyIn(noWalkers(), "internal/leaf/leaf.go")
	want := []string{"./cmd/app", "./internal/leaf", "./internal/mid"}
	if got := goPkgs(pl); !slices.Equal(got, want) {
		t.Fatalf("leaf code change selects %v, want %v", got, want)
	}
	if pl.Go.Full {
		t.Fatalf("leaf change must not be full: %v", pl.Go.Reasons)
	}
	if pl.Web.Full || len(pl.Web.Tests) != 0 {
		t.Fatalf("a Go change selects no web tests, got %v", pl.Web.Tests)
	}
}

func TestGoTestFileSelectsOnlyItsOwnPackage(t *testing.T) {
	pl := classifyIn(noWalkers(), "internal/leaf/leaf_test.go")
	if got, want := goPkgs(pl), []string{"./internal/leaf"}; !slices.Equal(got, want) {
		t.Fatalf("a _test.go change selects %v, want only %v", got, want)
	}
}

func TestGoTaggedFileSelectsNothing(t *testing.T) {
	pl := classify("internal/leaf/live_test.go")
	for _, p := range goPkgs(pl) {
		if p == "./internal/leaf" || p == "./cmd/app" {
			t.Fatalf("a build-tagged test must not select %s", p)
		}
	}
}

func TestGoAnyGoChangeSelectsTreeWalkers(t *testing.T) {
	pl := classify("internal/other/other.go")
	if !slices.Contains(goPkgs(pl), "./internal/guard") {
		t.Fatalf("a package that walks every Go file must run on any Go change, got %v", goPkgs(pl))
	}
}

func TestGoEmbeddedFileFansOutLikeCode(t *testing.T) {
	pl := classify("internal/fixture/data/x.json")
	if !slices.Contains(goPkgs(pl), "./internal/fixture") {
		t.Fatalf("an embedded file selects its package, got %v", goPkgs(pl))
	}
}

func TestGoReaderSelectsTheTestThatNamesTheFile(t *testing.T) {
	pl := classify("deploy/kcl/main.k")
	if got, want := goPkgs(pl), []string{"./internal/deploy"}; !slices.Equal(got, want) {
		t.Fatalf("deploy/kcl/main.k selects %v, want %v", got, want)
	}
	if pl.Go.Full {
		t.Fatal("a data file a test names must not force the full run")
	}
}

func TestMigrationReachesOnlyDBBackedTests(t *testing.T) {
	pl := classify("db/migrations/20261101000000_x.up.sql")
	if got, want := goPkgs(pl), []string{"./internal/db"}; !slices.Equal(got, want) {
		t.Fatalf("a migration selects %v, want %v", got, want)
	}
	if pl.Go.Full {
		t.Fatalf("a migration must not force the full run: %v", pl.Go.Reasons)
	}
}

func TestPinsAndConfigForceFull(t *testing.T) {
	cases := []struct {
		path  string
		lanes []lane
	}{
		// The docs generators build against go.mod's pins too (cli.md
		// documents forge's command tree).
		{"go.mod", []lane{laneGo, laneForgeGen, laneDocGen}},
		{"go.sum", []lane{laneGo, laneForgeGen, laneDocGen}},
		{"Taskfile.yml", allLanes},
		{"Makefile", allLanes},
		// The forge pin and forge's own config reach Go and codegen, never a
		// vitest file or electron: a forge bump must not run the whole web lane.
		{"forge.yaml", []lane{laneGo, laneForgeGen}},
		{"frontends/web/package.json", []lane{laneWeb}},
		{"frontends/web/vite.config.ts", []lane{laneWeb}},
		{"frontends/web/src/test/setup.ts", []lane{laneWeb}},
		{"package-lock.json", []lane{laneWeb, laneElectron}},
		{"proto/buf.yaml", []lane{laneGo, laneWeb, laneForgeGen}},
	}
	for _, tc := range cases {
		pl := classify(tc.path)
		for _, l := range allLanes {
			want := slices.Contains(tc.lanes, l)
			var got bool
			switch l {
			case laneGo:
				got = pl.Go.Full
			case laneWeb:
				got = pl.Web.Full
			case laneElectron:
				got = pl.Electron.Full
			case laneForgeGen:
				got = pl.ForgeGen.Full
			case laneDocGen:
				got = pl.DocGen.Full
			}
			if got != want {
				t.Errorf("%s: lane %s full = %v, want %v", tc.path, l, got, want)
			}
		}
	}
}

func TestUnclaimedPathFailsSafe(t *testing.T) {
	pl := classify("some-new-top-level-thing/x.dat")
	if !pl.Go.Full || !pl.Web.Full || !pl.Electron.Full || !pl.ForgeGen.Full || !pl.DocGen.Full {
		t.Fatalf("a path nothing claims must run everything: go=%v web=%v electron=%v forge-generate=%v docs-generate=%v", pl.Go.Full, pl.Web.Full, pl.Electron.Full, pl.ForgeGen.Full, pl.DocGen.Full)
	}
	if !strings.Contains(strings.Join(pl.Go.Reasons, " "), "some-new-top-level-thing/x.dat") {
		t.Fatalf("the reason must name the path: %v", pl.Go.Reasons)
	}
}

func TestInertPathsSelectNothing(t *testing.T) {
	for _, p := range []string{"docs/automations/overview.mdx", "README.md", ".github/workflows/pr-ci.yml", "research/x/PLAN.md"} {
		pl := classify(p)
		if pl.Go.Full || pl.Web.Full || len(pl.Go.Packages) != 0 || len(pl.Web.Tests) != 0 {
			t.Errorf("%s selected something: go=%v/%v web=%v/%v", p, pl.Go.Full, pl.Go.Packages, pl.Web.Full, pl.Web.Tests)
		}
	}
}

func TestForgeInputsRunForgeGenerate(t *testing.T) {
	for _, p := range []string{
		"proto/reliant/v1/chat.proto", "db/migrations/20261101000000_x.up.sql", "internal/vault/contract.go",
		"internal/vault/doc.go", "internal/handlers/chat/rpc_send.go", "internal/vault/mock_gen.go",
		"gen/forge_descriptor.json", "deploy/observability/x.json", ".forge/hashes.json",
		"forge.yaml", "go.mod",
	} {
		pl := classify(p)
		if !pl.ForgeGen.Full {
			t.Errorf("%s is a forge generate input or output: the forge-generate lane must run", p)
		}
		if !strings.Contains(strings.Join(pl.ForgeGen.Reasons, " "), p) {
			t.Errorf("%s: the reason must name the path: %v", p, pl.ForgeGen.Reasons)
		}
	}
	for _, p := range []string{"internal/leaf/leaf.go", "frontends/web/src/lib/leaf.ts", "electron/src/menu-shortcuts.js", "docs/x.md", ".github/workflows/pr-ci.yml"} {
		if pl := classify(p); pl.ForgeGen.Full {
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

// The hand edit #1228 made: SKILL.md is generated (tools/docgen/assembler), so
// editing it must run the docs check, which regenerates it and fails.
func TestHandEditedSkillMDRunsTheDocsCheck(t *testing.T) {
	const skill = "internal/skills/catalog/builtin/workflow-builder/SKILL.md"
	pl := classify(skill)
	if !pl.DocGen.Full {
		t.Fatalf("%s is a docs generator's output: the docs-generate lane must run", skill)
	}
	if !strings.Contains(strings.Join(pl.DocGen.Reasons, " "), skill) {
		t.Fatalf("the reason must name the path: %v", pl.DocGen.Reasons)
	}
	if pl.Go.Full || pl.Web.Full || pl.Electron.Full || pl.ForgeGen.Full {
		t.Fatalf("a docs output must not widen the other lanes: go=%v web=%v electron=%v forge-generate=%v", pl.Go.Full, pl.Web.Full, pl.Electron.Full, pl.ForgeGen.Full)
	}
}

func TestUnrelatedWebChangeSkipsTheDocsCheck(t *testing.T) {
	for _, p := range []string{"frontends/web/src/lib/leaf.ts", "frontends/web/src/lib/__tests__/leaf.test.ts", "frontends/web/e2e/observability.spec.ts"} {
		if pl := classify(p); pl.DocGen.Full {
			t.Errorf("%s reaches no docs generator: docs-generate must not run (%v)", p, pl.DocGen.Reasons)
		}
	}
}

func TestDocGenInputsRunTheDocsCheck(t *testing.T) {
	for _, tc := range []struct{ path, why string }{
		{"tools/docgen/cli/main.go", "a docs generator reads or writes it"},
		{"generated/docs-source/reference/cli.md", "a docs generator reads or writes it"},
		{"docs/reference/cli.mdx", "a docs generator reads or writes it"},
		{"docs/changelog.mdx", "a docs generator reads or writes it"},
		{"docs/data/releases/v1.yaml", "a docs generator reads or writes it"},
		{"config/shortcuts.yaml", "a docs generator reads or writes it"},
		{"frontends/web/src/store/shortcutsData.generated.ts", "a docs generator reads or writes it"},
		{"scripts/generate-mintlify-reference.py", "a docs generator reads or writes it"},
		// Compiled into a generator, directly or transitively, or embedded by
		// a package that is.
		{"internal/leaf/leaf.go", "tools/docgen/cli compiles internal/leaf"},
		{"internal/fixture/data/x.json", "tools/docgen/cli compiles internal/fixture"},
		// A proto, through the gen/ package it regenerates.
		{"proto/reliant/v1/chat.proto", "tools/docgen/cli compiles gen/reliant/v1"},
		// A Go file the graph no longer lists, in a compiled package.
		{"internal/mid/gone.go", "tools/docgen/cli compiles internal/mid"},
	} {
		pl := classify(tc.path)
		if !pl.DocGen.Full {
			t.Errorf("%s: the docs-generate lane must run", tc.path)
			continue
		}
		if want := tc.path + ": " + tc.why; !slices.Contains(pl.DocGen.Reasons, want) {
			t.Errorf("%s: reasons %v, want %q", tc.path, pl.DocGen.Reasons, want)
		}
	}
	// Not in any generator's build: another package, a test file, a
	// build-tagged file, docs prose.
	for _, p := range []string{"internal/other/other.go", "internal/leaf/leaf_test.go", "internal/leaf/live_test.go", "docs/automations/overview.mdx", ".github/workflows/pr-ci.yml"} {
		if pl := classify(p); pl.DocGen.Full {
			t.Errorf("%s reaches no docs generator: docs-generate must not run (%v)", p, pl.DocGen.Reasons)
		}
	}
}

func TestElectronChangeRunsElectronOnly(t *testing.T) {
	pl := classify("electron/src/menu-shortcuts.js")
	if !pl.Electron.Full {
		t.Fatal("electron source change must run the electron lane")
	}
	if pl.Go.Full || pl.Web.Full {
		t.Fatal("electron source change must not widen other lanes")
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
	pl := Classify([]Change{{Path: "frontends/web/src/lib/leaf.ts"}}, g)
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

func TestWebDeletedModuleForcesFullWeb(t *testing.T) {
	pl := Classify([]Change{{Path: "frontends/web/src/lib/leaf.ts", Deleted: true}}, testGraph())
	if !pl.Web.Full {
		t.Fatal("a deleted module has no graph position left: run every web test")
	}
	pl = Classify([]Change{{Path: "frontends/web/src/lib/__tests__/leaf.test.ts", Deleted: true}}, testGraph())
	if pl.Web.Full {
		t.Fatal("a deleted TEST file selects nothing and must not force the full lane")
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
	if !slices.Contains(goPkgs(pl), "./gen/reliant/v1") {
		t.Fatalf("a proto must select its generated Go package: %v", goPkgs(pl))
	}
	if !slices.Contains(pl.Web.Tests, "src/lib/__tests__/genclient.test.ts") {
		t.Fatalf("a proto must select the web tests importing its generated client: %v", pl.Web.Tests)
	}
	if pl.Go.Full {
		t.Fatalf("a proto with a known generated package must not force full: %v", pl.Go.Reasons)
	}
}

func TestProtoWithoutGeneratedPackageFailsSafe(t *testing.T) {
	pl := classify("proto/brandnew/v1/x.proto")
	if !pl.Go.Full {
		t.Fatal("a proto whose generated package is unknown must run the whole Go lane")
	}
}

func TestSelectorChangesItsOwnPackageTests(t *testing.T) {
	g := testGraph()
	g.DirPkg["tools/test-affected"] = mod + "/tools/test-affected"
	g.Files["tools/test-affected/plan.go"] = FileInfo{Pkg: mod + "/tools/test-affected", Kind: KindCode}
	g.TestDeps[mod+"/tools/test-affected"] = map[string]bool{mod + "/tools/test-affected": true}
	pl := Classify([]Change{{Path: "tools/test-affected/plan.go"}}, g)
	if pl.Go.Full {
		t.Fatalf("the selector's own source is a Go package: %v", pl.Go.Reasons)
	}
	if !slices.Contains(goPkgs(pl), "./tools/test-affected") {
		t.Fatalf("got %v", goPkgs(pl))
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
		if len(r.full) > 0 && r.reason == "" {
			t.Errorf("rule %q forces full without a reason", r.id)
		}
	}
}

// DB-backed tests run under -short (each gets its own copy of a migrated
// template on pgtest's server), so a package a migration selects runs in the
// one go step with every other, with no database configuration in its env.
func TestRunnerStepsUseExplicitFilesAndRunDBTestsInline(t *testing.T) {
	pl := classify("internal/leaf/leaf.go", "db/migrations/20261101000000_x.up.sql", "frontends/web/src/lib/leaf.ts")
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
	pl := classify("go.mod", "frontends/web/package.json")
	steps := (&runner{root: "/repo"}).steps(pl, laneSet("go,web"))
	var got []string
	for _, s := range steps {
		got = append(got, strings.Join(s.argv, " "))
	}
	if len(got) != 2 || !strings.HasSuffix(got[0], "./...") || got[1] != "npx vitest run" {
		t.Fatalf("full lanes = %v", got)
	}
}

// A FULL lane is a whole-module run and queues for a host heavy slot; a scoped
// run is the inner loop and never does.
func TestRunnerRunsOnlyFullLanesInAHeavySlot(t *testing.T) {
	r := &runner{root: "/repo", slot: []string{"forge", "slot", "run", "--class", "heavy", "--"}}
	for _, s := range r.steps(classify("go.mod", "frontends/web/package.json"), laneSet("go,web")) {
		if joined := strings.Join(s.argv, " "); !strings.HasPrefix(joined, "forge slot run --class heavy -- ") {
			t.Errorf("full step %q runs outside a heavy slot: %s", s.name, joined)
		}
	}
	for _, s := range r.steps(classify("internal/leaf/leaf.go", "frontends/web/src/lib/leaf.ts"), laneSet("go,web")) {
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
	pl := classify("internal/vault/contract.go", "internal/leaf/leaf.go")
	r := &runner{root: "/repo", slot: []string{"forge", "slot", "run", "--class", "heavy", "--"}}
	var got []string
	for _, s := range r.steps(pl, laneSet("go,web,electron,forge-generate,docs-generate")) {
		got = append(got, strings.Join(s.argv, " "))
	}
	if len(got) < 3 || got[0] != "task generate:check" ||
		got[1] != "forge slot run --class heavy -- task generate:docs:check" {
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
