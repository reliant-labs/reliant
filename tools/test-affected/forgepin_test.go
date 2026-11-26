// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

const (
	forgeMod = "github.com/reliant-labs/forge"
	oldForge = "v1.10.1-0.20261125040729-73664c6256d3"
	newForge = "v1.11.1-0.20261125185432-83053473ac90"
)

// goModAt is a go.mod pinning forge at v, with extra lines appended to its
// require block.
func goModAt(v string, extra ...string) string {
	return "module github.com/reliant-labs/reliant\n\ngo 1.27.0\n\ntoolchain go1.27.4\n\nrequire (\n" +
		"\tgithub.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2\n" +
		"\t" + forgeMod + " " + v + "\n" +
		"\tgithub.com/robfig/cron/v3 v3.0.1\n" + strings.Join(extra, "") +
		")\n\nreplace github.com/reliant-labs/reliant/sdk/go => ./sdk/go\n"
}

func goSumAt(v string, extra ...string) string {
	return "github.com/quic-go/qpack v0.5.1 h1:giqk=\n" +
		forgeMod + " " + v + " h1:x=\n" + forgeMod + " " + v + "/go.mod h1:y=\n" +
		"github.com/robfig/cron v1.2.0 h1:z=\n" + strings.Join(extra, "")
}

func forgeYAMLAt(v string, extra ...string) string {
	return "name: reliant\nmodule_path: github.com/reliant-labs/reliant\nforge_version: " + v + "\nharness: reliant\n" + strings.Join(extra, "")
}

// pinDiff is the old and new contents of the pin files, as plan reads them.
type pinDiff struct{ old, cur map[string][]byte }

// forgeBump is #3645's pin diff: go.mod, go.sum and forge.yaml move forge
// from oldForge to newForge and change nothing else.
func forgeBump() pinDiff {
	return pinDiff{
		old: map[string][]byte{"go.mod": []byte(goModAt(oldForge)), "go.sum": []byte(goSumAt(oldForge)), "forge.yaml": []byte(forgeYAMLAt(oldForge))},
		cur: map[string][]byte{"go.mod": []byte(goModAt(newForge)), "go.sum": []byte(goSumAt(newForge)), "forge.yaml": []byte(forgeYAMLAt(newForge))},
	}
}

func TestDetectForgePin(t *testing.T) {
	cases := []struct {
		name string
		edit func(d pinDiff)
		// notNarrowed is a substring of the reason the Go lane stays full;
		// empty: a forge pin move.
		notNarrowed string
	}{
		{name: "pin move only", edit: func(pinDiff) {}},
		{name: "go.mod alone, no forge.yaml", edit: func(d pinDiff) { delete(d.old, "forge.yaml"); delete(d.cur, "forge.yaml") }},
		{name: "single-line require", edit: func(d pinDiff) {
			d.old["go.mod"] = []byte("module m\n\nrequire " + forgeMod + " " + oldForge + "\n")
			d.cur["go.mod"] = []byte("module m\n\nrequire " + forgeMod + " " + newForge + " // indirect\n")
		}},
		{name: "go.sum keeps the old go.mod sum", edit: func(d pinDiff) {
			d.cur["go.sum"] = []byte(goSumAt(newForge, forgeMod+" "+oldForge+"/go.mod h1:y=\n"))
		}},
		{name: "another module moved too", notNarrowed: "go.mod changes more than the forge require lines", edit: func(d pinDiff) {
			d.cur["go.mod"] = []byte(strings.Replace(goModAt(newForge), "cron/v3 v3.0.1", "cron/v3 v3.0.2", 1))
		}},
		{name: "a transitive dependency was added", notNarrowed: "go.mod changes more", edit: func(d pinDiff) {
			d.cur["go.mod"] = []byte(goModAt(newForge, "\tgolang.org/x/sync v0.9.0 // indirect\n"))
		}},
		{name: "go.sum moves another module", notNarrowed: "go.sum changes another module's lines (github.com/robfig/cron)", edit: func(d pinDiff) {
			d.cur["go.sum"] = []byte(strings.Replace(goSumAt(newForge), "cron v1.2.0", "cron v1.2.1", 1))
		}},
		{name: "go directive", notNarrowed: "go 1.28.0", edit: func(d pinDiff) {
			d.cur["go.mod"] = []byte(strings.Replace(goModAt(newForge), "go 1.27.0", "go 1.28.0", 1))
		}},
		{name: "toolchain directive", notNarrowed: "toolchain go1.27.5", edit: func(d pinDiff) {
			d.cur["go.mod"] = []byte(strings.Replace(goModAt(newForge), "toolchain go1.27.4", "toolchain go1.27.5", 1))
		}},
		{name: "a replace of forge", notNarrowed: "go.mod changes more", edit: func(d pinDiff) {
			d.cur["go.mod"] = []byte(goModAt(newForge) + "replace " + forgeMod + " => ../forge\n")
		}},
		{name: "an exclude reads like a require but is not one", notNarrowed: "go.mod changes more", edit: func(d pinDiff) {
			d.old["go.mod"] = []byte(goModAt(oldForge) + "exclude (\n\t" + forgeMod + " v1.0.0\n)\n")
			d.cur["go.mod"] = []byte(goModAt(oldForge) + "exclude (\n\t" + forgeMod + " v1.0.1\n)\n")
		}},
		{name: "forge.yaml changes more than the pin", notNarrowed: "forge.yaml changes more than forge_version", edit: func(d pinDiff) {
			d.cur["forge.yaml"] = []byte(forgeYAMLAt(newForge, "lint:\n  rules: {}\n"))
		}},
		{name: "go.sum alone", notNarrowed: "go.mod's forge require did not move", edit: func(d pinDiff) {
			delete(d.old, "go.mod")
			delete(d.cur, "go.mod")
		}},
		{name: "forge required for the first time", notNarrowed: "adds or drops the require of " + forgeMod, edit: func(d pinDiff) {
			d.old["go.mod"] = []byte(strings.Replace(goModAt(oldForge), "\t"+forgeMod+" "+oldForge+"\n", "", 1))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := forgeBump()
			tc.edit(d)
			pm, why := detectForgePin(d.old, d.cur)
			if tc.notNarrowed == "" {
				if why != "" {
					t.Fatalf("a forge pin move was not recognized: %s", why)
				}
				if mv := pm.Moves[forgeMod]; mv.Old != oldForge || mv.New != newForge || len(pm.Moves) != 1 {
					t.Fatalf("moves = %v, want %s %s..%s", pm.Moves, forgeMod, oldForge, newForge)
				}
				if !slices.Equal(pm.Required, []string{forgeMod}) {
					t.Fatalf("required = %v", pm.Required)
				}
				return
			}
			if !strings.Contains(why, tc.notNarrowed) {
				t.Fatalf("reason = %q, want it to contain %q", why, tc.notNarrowed)
			}
		})
	}
}

func TestForgePinUnchangedReplacementKeepsFullLane(t *testing.T) {
	for _, module := range forgeModules {
		for _, replacement := range []string{
			"replace " + module + " => ../forge\n",
			"replace " + module + " " + oldForge + " => " + module + " v1.0.0\n",
			"replace (\n\t" + module + " " + newForge + " => ../forge\n)\n",
			"replace (\n\t\"" + module + "\" => \"../forge\"\n)\n",
		} {
			t.Run(module+"/"+replacement, func(t *testing.T) {
				d := forgeBump()
				for _, files := range []map[string][]byte{d.old, d.cur} {
					files["go.mod"] = append(files["go.mod"], []byte(replacement)...)
				}
				fp := loadForgePin(context.Background(), d.old, d.cur, func(context.Context, string, string) (fs.FS, error) {
					t.Fatal("replacement must force full selection before downloading required versions")
					return nil, errors.New("unexpected download")
				}, []string{module + "/a"})
				if fp.Narrows() || !strings.Contains(fp.NotNarrowed, "replaces forge module "+module) {
					t.Fatalf("replacement must keep the full lane: %+v", fp)
				}
			})
		}
	}
}

// forgeTrees is forge's source at oldForge and newForge:
//
//	pkg/a      its code changes
//	pkg/b      unchanged, imports pkg/a
//	pkg/c      unchanged; only its test and its testdata change
//	pkg/tmpl   embeds templates/*.tmpl, and one changes
//	pkg/cgo    its assembly changes
//	.          the root package (changelog.go embeds CHANGELOG.md); README.md changes
//	pkg/gone   vanishes
func forgeTrees() map[string]fstest.MapFS {
	base := func() fstest.MapFS {
		return fstest.MapFS{
			"go.mod":                       {Data: []byte("module " + forgeMod + "\n\ngo 1.27.0\n")},
			"README.md":                    {Data: []byte("old readme")},
			"CHANGELOG.md":                 {Data: []byte("## v1.10.0")},
			"changelog.go":                 {Data: []byte("package forge\n\nimport _ \"embed\"\n\n//go:embed CHANGELOG.md\nvar changelog []byte\n")},
			"pkg/a/a.go":                   {Data: []byte("package a\n\nfunc A() int { return 1 }\n")},
			"pkg/b/b.go":                   {Data: []byte("package b\n\nimport \"" + forgeMod + "/pkg/a\"\n\nvar B = a.A\n")},
			"pkg/c/c.go":                   {Data: []byte("package c\n")},
			"pkg/c/c_test.go":              {Data: []byte("package c\n\n// old\n")},
			"pkg/c/testdata/x.golden":      {Data: []byte("old")},
			"pkg/tmpl/tmpl.go":             {Data: []byte("package tmpl\n\nimport \"embed\"\n\n//go:embed templates/*.tmpl \"static\"\nvar FS embed.FS\n")},
			"pkg/tmpl/templates/main.tmpl": {Data: []byte("old {{.}}")},
			"pkg/tmpl/static/x.css":        {Data: []byte("a{}")},
			"pkg/cgo/cgo.go":               {Data: []byte("package cgo\n")},
			"pkg/cgo/add_amd64.s":          {Data: []byte("TEXT old")},
			"pkg/gone/gone.go":             {Data: []byte("package gone\n")},
		}
	}
	old, cur := base(), base()
	cur["README.md"] = &fstest.MapFile{Data: []byte("new readme")}
	cur["pkg/a/a.go"] = &fstest.MapFile{Data: []byte("package a\n\nfunc A() int { return 2 }\n")}
	cur["pkg/c/c_test.go"] = &fstest.MapFile{Data: []byte("package c\n\n// new\n")}
	cur["pkg/c/testdata/x.golden"] = &fstest.MapFile{Data: []byte("new")}
	cur["pkg/tmpl/templates/main.tmpl"] = &fstest.MapFile{Data: []byte("new {{.}}")}
	cur["pkg/cgo/add_amd64.s"] = &fstest.MapFile{Data: []byte("TEXT new")}
	delete(cur, "pkg/gone/gone.go")
	return map[string]fstest.MapFS{oldForge: old, newForge: cur}
}

// fakeSource serves trees by version; a version it lacks fails as a failed
// download does.
func fakeSource(trees map[string]fstest.MapFS) forgeSource {
	return func(_ context.Context, module, version string) (fs.FS, error) {
		t, ok := trees[version]
		if module != forgeMod || !ok {
			return nil, errors.New("go mod download " + module + "@" + version + ": unknown revision")
		}
		return t, nil
	}
}

func TestChangedForgePackages(t *testing.T) {
	pkgs := []string{forgeMod, forgeMod + "/pkg/a", forgeMod + "/pkg/b", forgeMod + "/pkg/c", forgeMod + "/pkg/cgo", forgeMod + "/pkg/gone", forgeMod + "/pkg/tmpl"}
	d := forgeBump()
	fp := loadForgePin(context.Background(), d.old, d.cur, fakeSource(forgeTrees()), pkgs)
	if !fp.Narrows() {
		t.Fatalf("not narrowed: %s", fp.NotNarrowed)
	}
	if fp.From != "73664c6256d3" || fp.To != "83053473ac90" {
		t.Errorf("versions = %s..%s, want the pseudo-versions' commits", fp.From, fp.To)
	}
	want := map[string]string{
		forgeMod + "/pkg/a":    "forge/pkg/a changed between forge 73664c6256d3..83053473ac90",
		forgeMod + "/pkg/b":    "forge/pkg/b imports forge/pkg/a, which changed between forge 73664c6256d3..83053473ac90",
		forgeMod + "/pkg/cgo":  "forge/pkg/cgo changed between forge 73664c6256d3..83053473ac90",
		forgeMod + "/pkg/gone": "forge/pkg/gone changed between forge 73664c6256d3..83053473ac90",
		forgeMod + "/pkg/tmpl": "forge/pkg/tmpl changed between forge 73664c6256d3..83053473ac90",
	}
	for p, why := range want {
		if fp.Changed[p] != why {
			t.Errorf("%s: why = %q, want %q", p, fp.Changed[p], why)
		}
	}
	for p := range fp.Changed {
		if _, ok := want[p]; !ok {
			t.Errorf("%s reported changed (%s): its build reads nothing that changed", p, fp.Changed[p])
		}
	}

	// The changelog the root package embeds changes: it changed too.
	trees := forgeTrees()
	trees[newForge]["CHANGELOG.md"] = &fstest.MapFile{Data: []byte("## v1.11.0")}
	fp = loadForgePin(context.Background(), d.old, d.cur, fakeSource(trees), pkgs)
	if _, ok := fp.Changed[forgeMod]; !ok {
		t.Errorf("the root package embeds CHANGELOG.md, which changed: want it changed, got %v", fp.Changed)
	}
}

func TestForgePinFailsClosed(t *testing.T) {
	d := forgeBump()
	pkgs := []string{forgeMod + "/pkg/a"}
	t.Run("download fails", func(t *testing.T) {
		trees := forgeTrees()
		delete(trees, newForge)
		fp := loadForgePin(context.Background(), d.old, d.cur, fakeSource(trees), pkgs)
		if fp.Narrows() || !strings.Contains(fp.NotNarrowed, "cannot tell which forge packages changed (go mod download "+forgeMod+"@"+newForge+": unknown revision)") {
			t.Fatalf("a failed download must keep the Go lane full, naming why: %+v", fp)
		}
	})
	t.Run("forge moves its go directive", func(t *testing.T) {
		trees := forgeTrees()
		trees[newForge]["go.mod"] = &fstest.MapFile{Data: []byte("module " + forgeMod + "\n\ngo 1.28.0\n")}
		fp := loadForgePin(context.Background(), d.old, d.cur, fakeSource(trees), pkgs)
		if fp.Narrows() || !strings.Contains(fp.NotNarrowed, "go directive") {
			t.Fatalf("a new language version compiles every forge package differently: %+v", fp)
		}
	})
	t.Run("a package no required forge module provides", func(t *testing.T) {
		fp := loadForgePin(context.Background(), d.old, d.cur, fakeSource(forgeTrees()), []string{"github.com/reliant-labs/forgery/x"})
		if fp.Narrows() {
			t.Fatalf("want full, got %+v", fp)
		}
	})
}

func TestEmbedPrefixes(t *testing.T) {
	src := "package p\n\n//go:embed a.txt templates/*.tmpl all:static \"with space/x\" `raw/*`\n//go:embed *.json\n//go:embedded nothing\n"
	got := embedPrefixes([]byte(src))
	want := []string{"a.txt", "templates", "static", "with space/x", "raw", "."}
	if !slices.Equal(got, want) {
		t.Fatalf("embedPrefixes = %q, want %q", got, want)
	}
}

// forgePinGraph is the test graph whose packages build against forge:
// internal/leaf imports forge/pkg/a (so mid and cmd/app contain it),
// internal/other imports forge/pkg/c, and tools/docgen/cli compiles leaf.
func forgePinGraph(fp *ForgePin) *Graph {
	g := noWalkers()
	pkg := func(n string) string { return mod + "/" + n }
	g.DirectImports = map[string]map[string]bool{
		pkg("internal/leaf"):  {forgeMod + "/pkg/a": true},
		pkg("internal/mid"):   {pkg("internal/leaf"): true},
		pkg("cmd/app"):        {pkg("internal/mid"): true},
		pkg("internal/other"): {forgeMod + "/pkg/c": true},
	}
	for _, p := range []string{"internal/leaf", "internal/mid", "cmd/app"} {
		g.TestDeps[pkg(p)][forgeMod+"/pkg/a"] = true
		g.BuildDeps[pkg(p)] = map[string]bool{pkg(p): true, forgeMod + "/pkg/a": true}
	}
	g.TestDeps[pkg("internal/other")][forgeMod+"/pkg/c"] = true
	g.BuildDeps[pkg("internal/other")] = map[string]bool{pkg("internal/other"): true, forgeMod + "/pkg/c": true}
	g.BuildDeps[pkg("tools/docgen/cli")][forgeMod+"/pkg/a"] = true
	g.ForgePin = fp
	return g
}

func TestForgePinMoveSelectsWhatTheChangedForgePackagesReach(t *testing.T) {
	d := forgeBump()
	g := forgePinGraph(nil)
	if got, want := forgePackages(g), []string{forgeMod + "/pkg/a", forgeMod + "/pkg/c"}; !slices.Equal(got, want) {
		t.Fatalf("forgePackages = %v, want %v", got, want)
	}
	g.ForgePin = loadForgePin(context.Background(), d.old, d.cur, fakeSource(forgeTrees()), forgePackages(g))
	pl := classifyIn(g, "go.mod", "go.sum", "forge.yaml")
	if pl.Go.Full {
		t.Fatalf("a forge-pin-only diff ran the whole Go lane: %v", pl.Go.Reasons)
	}
	if want := []string{"./cmd/app", "./internal/leaf", "./internal/mid"}; !slices.Equal(pl.Go.Packages, want) {
		t.Fatalf("packages = %v, want %v (internal/other builds against forge/pkg/c, which did not change)", pl.Go.Packages, want)
	}
	if why := pl.Go.Why["./internal/leaf"]; !strings.Contains(why, "forge/pkg/a changed between forge 73664c6256d3..83053473ac90") {
		t.Errorf("leaf's reason must name the forge package and versions: %q", why)
	}
	if want := []string{"./cmd/app", "./internal/leaf", "./internal/mid", "./tools/docgen/cli"}; !slices.Equal(pl.Go.Build, want) {
		t.Errorf("build = %v, want %v: every package whose build contains a changed forge package compiles", pl.Go.Build, want)
	}
	// The codegen lanes still judge the pin, as before.
	if !pl.ForgeGen.Full || !pl.DocGen.Full {
		t.Errorf("a pin move must still run forge-generate (%v) and docs-generate (%v)", pl.ForgeGen.Full, pl.DocGen.Full)
	}
	for _, f := range pl.Files {
		if !strings.Contains(f.Note, "forge pin move only (forge 73664c6256d3..83053473ac90)") {
			t.Errorf("%s: note %q does not say the Go lane narrowed to the forge pin", f.Path, f.Note)
		}
	}

	// The merge tier's direct reach: the package that imports forge/pkg/a
	// by name runs; its dependents are the full lane's.
	ch := []Change{{Path: "go.mod"}, {Path: "go.sum"}, {Path: "forge.yaml"}}
	pl = ClassifyReach(ch, g, ReachDirect)
	if want := []string{"./internal/leaf"}; !slices.Equal(pl.Go.Packages, want) {
		t.Fatalf("direct packages = %v, want %v", pl.Go.Packages, want)
	}
	if want := []string{"./cmd/app", "./internal/mid"}; !slices.Equal(pl.Go.Deferred, want) {
		t.Fatalf("direct deferred = %v, want %v", pl.Go.Deferred, want)
	}
	if why := pl.Go.Why["./internal/leaf"]; !strings.HasPrefix(why, "imports pkg/a (forge/pkg/a changed") {
		t.Errorf("direct reason = %q", why)
	}
}

func TestForgePinThatIsNotOnlyAPinMoveRunsTheWholeGoLane(t *testing.T) {
	cases := []struct {
		name string
		fp   func(d pinDiff) *ForgePin
		want string
	}{
		{"another module moved", func(d pinDiff) *ForgePin {
			d.cur["go.mod"] = []byte(strings.Replace(goModAt(newForge), "cron/v3 v3.0.1", "cron/v3 v3.0.2", 1))
			return loadForgePin(context.Background(), d.old, d.cur, fakeSource(forgeTrees()), nil)
		}, "not only a forge pin move: go.mod changes more than the forge require lines"},
		{"go directive", func(d pinDiff) *ForgePin {
			d.cur["go.mod"] = []byte(strings.Replace(goModAt(newForge), "go 1.27.0", "go 1.28.0", 1))
			return loadForgePin(context.Background(), d.old, d.cur, fakeSource(forgeTrees()), nil)
		}, "not only a forge pin move: go.mod changes more than the forge require lines (go 1.28.0)"},
		{"download fails", func(d pinDiff) *ForgePin {
			return loadForgePin(context.Background(), d.old, d.cur, fakeSource(nil), []string{forgeMod + "/pkg/a"})
		}, "not only a forge pin move: cannot tell which forge packages changed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pl := classifyIn(forgePinGraph(tc.fp(forgeBump())), "go.mod", "go.sum", "forge.yaml")
			if !pl.Go.Full {
				t.Fatalf("want the whole Go lane, got %v", pl.Go.Packages)
			}
			if !slices.ContainsFunc(pl.Go.Reasons, func(r string) bool { return strings.HasPrefix(r, "go.mod: ") && strings.Contains(r, tc.want) }) {
				t.Fatalf("reasons %q do not say %q", pl.Go.Reasons, tc.want)
			}
		})
	}
}

// Pin files the diff carries beside a toolchain file still narrow; the toolchain
// file is judged by its own rule, and runs the whole lane alone.
func TestForgePinBesideAToolchainFileIsJudgedByItsOwnRule(t *testing.T) {
	d := forgeBump()
	g := forgePinGraph(nil)
	g.ForgePin = loadForgePin(context.Background(), d.old, d.cur, fakeSource(forgeTrees()), forgePackages(g))
	pl := classifyIn(g, "go.mod", "go.sum", "tools/tools.go")
	if !pl.Go.Full || len(pl.Go.Reasons) != 1 || !strings.HasPrefix(pl.Go.Reasons[0], "tools/tools.go: ") {
		t.Fatalf("want full for tools/tools.go alone, got full=%v %q", pl.Go.Full, pl.Go.Reasons)
	}
}
