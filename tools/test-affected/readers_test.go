// Copyright (c) 2025 Reliant Labs
package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// readerSources are Go packages whose sources go through the real scanner
// (scanSources), so these tests pin what counts as a read, not a hand-built
// Readers map. Every one locates the repo root somewhere, so none is ruled
// out by the package-level gate: the Join anchor alone decides.
var readerSources = map[string]string{
	// A fixture that builds a multi-repo workspace named like this repo's
	// own directories, under a temp dir: no read of web/.
	"internal/fixtures": `package fixtures

import (
	"os"
	"path/filepath"
	"testing"
)

var checkouts = map[string]string{"api": "feat/api", "web": "feat/web"}

func TestWorkspace(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "proj", "multi")
	_ = os.MkdirAll(filepath.Join(ws, "web"), 0o755)
	_ = os.WriteFile(filepath.Join(ws, "web", "manifest.json"), nil, 0o644)
	cwd, _ := os.Getwd()
	_ = cwd
}
`,
	// Reads web/src relative to the package directory, as internal/grpc's
	// CORS test does.
	"internal/relreader": `package relreader

import (
	"path/filepath"
	"testing"
)

func TestReadsWebSrc(t *testing.T) {
	dir, _ := filepath.Abs(filepath.Join("..", "..", "web", "src"))
	_ = dir
}
`,
	// Reads web/ through a repo-root helper.
	"internal/rootreader": `package rootreader

import (
	"os"
	"path/filepath"
	"testing"
)

func repoRootDir(t *testing.T) string {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(filepath.Dir(wd))
}

func TestReadsWeb(t *testing.T) {
	root := repoRootDir(t)
	_, _ = os.ReadDir(filepath.Join(root, "web"))
}
`,
	// A constant between the root and the literal is part of the path.
	"internal/constreader": `package constreader

import (
	"os"
	"path/filepath"
	"testing"
)

const webDir = "web"

func TestReadsWebManifest(t *testing.T) {
	_, _ = os.ReadFile(filepath.Join(repoRoot(t), webDir, "manifest.json"))
}

func repoRoot(t *testing.T) string { return "../.." }
`,
}

// The web files these tests change. Neither exists: a real one, named here,
// would make this package a reader of it.
const (
	webSource   = "web/src/fixture/Widget.tsx"
	webManifest = "web/manifest.json"
)

// readerGraph is the test graph plus the named readerSources packages,
// scanned from disk.
func readerGraph(t *testing.T, pkgs ...string) *Graph {
	t.Helper()
	g := noWalkers()
	root := t.TempDir()
	pkgDirs := map[string]string{}
	tracked := []string{webSource, webManifest}
	for _, rel := range pkgs {
		src := readerSources[rel]
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "x_test.go")
		if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		pkg := mod + "/" + rel
		g.DirPkg[rel] = pkg
		g.Files[rel+"/x_test.go"] = FileInfo{Pkg: pkg, Kind: KindTest}
		g.TestDeps[pkg] = map[string]bool{pkg: true}
		pkgDirs[pkg] = dir
		tracked = append(tracked, rel+"/x_test.go")
	}
	scanSources(g, pkgDirs, tracked)
	return g
}

var allReaders = []string{"internal/fixtures", "internal/relreader", "internal/rootreader", "internal/constreader"}

// A bare "web" in a fixture (a map key, a Join onto a temp dir) is not a read
// of web/: it used to make every web-only diff run the fixture's package.
func TestWebOnlyDiffSelectsNoGoPackage(t *testing.T) {
	g := readerGraph(t, "internal/fixtures")
	if r := g.Readers[mod+"/internal/fixtures"]; len(r) != 0 {
		t.Fatalf("a fixture's temp-dir paths are not repo reads: %v", r)
	}
	pl := classifyIn(g, webSource, webManifest)
	if got := goPkgs(pl); len(got) != 0 {
		t.Fatalf("a web-only diff selects %v, want no Go package (%v)", got, pl.Go.Reasons)
	}
}

// A Go test that really reads web/ still runs when it changes, however it
// anchors the path.
func TestGoReaderOfWebIsStillSelected(t *testing.T) {
	g := readerGraph(t, allReaders...)
	for pkg, want := range map[string][]string{
		"internal/relreader":   {"web/src"},
		"internal/rootreader":  {"web"},
		"internal/constreader": {webManifest},
	} {
		if got := g.Readers[mod+"/"+pkg]; !slices.Equal(got, want) {
			t.Errorf("%s reads %v, want %v", pkg, got, want)
		}
	}
	if got, want := goPkgs(classifyIn(g, webSource)), []string{"./internal/relreader", "./internal/rootreader"}; !slices.Equal(got, want) {
		t.Errorf("%s selects %v, want %v", webSource, got, want)
	}
	if got, want := goPkgs(classifyIn(g, webManifest)), []string{"./internal/constreader", "./internal/rootreader"}; !slices.Equal(got, want) {
		t.Errorf("%s selects %v, want %v (not the fixture that writes one)", webManifest, got, want)
	}
}
