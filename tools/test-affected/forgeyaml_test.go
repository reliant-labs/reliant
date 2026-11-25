// Copyright (c) 2025 Reliant Labs
package main

import (
	"go/ast"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// gateConfig is the part of forge.yaml's ci.gate these tests read.
type gateConfig struct {
	AffectedRules []struct {
		On       string   `yaml:"on"`
		Packages []string `yaml:"packages"`
	} `yaml:"affected_rules"`
	ExtraChecks []struct {
		Name  string   `yaml:"name"`
		Paths []string `yaml:"paths"`
	} `yaml:"extra_checks"`
}

func readGateConfig(t *testing.T) gateConfig {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "forge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		CI struct {
			Gate gateConfig `yaml:"gate"`
		} `yaml:"ci"`
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("parse forge.yaml: %v", err)
	}
	return cfg.CI.Gate
}

// checkPaths is the named extra check's `paths`.
func checkPaths(t *testing.T, name string) []string {
	t.Helper()
	for _, c := range readGateConfig(t).ExtraChecks {
		if c.Name == name {
			return c.Paths
		}
	}
	t.Fatalf("forge.yaml has no ci.gate.extra_checks entry %q", name)
	return nil
}

// forgeGlobMatch is forge's affected-mode glob (cigate.matchGlobs): `dir/**`
// is a prefix, a glob with no slash matches a base name at any depth, and
// anything else is path.Match against the whole path.
func forgeGlobMatch(globs []string, p string) bool {
	for _, g := range globs {
		switch {
		case strings.HasSuffix(g, "/**"):
			if strings.HasPrefix(p, strings.TrimSuffix(g, "**")) {
				return true
			}
		case !strings.Contains(g, "/"):
			if ok, _ := path.Match(g, path.Base(p)); ok {
				return true
			}
		default:
			if ok, _ := path.Match(g, p); ok {
				return true
			}
		}
	}
	return false
}

var docGenGlobs []string

// isDocGenPath reports whether forge.yaml's docs-generate row runs for p.
func isDocGenPath(p string) bool { return forgeGlobMatch(docGenGlobs, p) }

func TestMain(m *testing.M) {
	b, err := os.ReadFile(filepath.Join(repoRoot, "forge.yaml"))
	if err == nil {
		var cfg struct {
			CI struct {
				Gate gateConfig `yaml:"gate"`
			} `yaml:"ci"`
		}
		if yaml.Unmarshal(b, &cfg) == nil {
			for _, c := range cfg.CI.Gate.ExtraChecks {
				if c.Name == "docs-generate" {
					docGenGlobs = c.Paths
				}
			}
		}
	}
	os.Exit(m.Run())
}

func strLit(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BasicLit)
	if !ok || b.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(b.Value)
	return s, err == nil
}

// TestWalkerPackagesAreListed holds forge.yaml's `on: go-source` rule to the
// tests that read every Go file of the module (a tree-wide guard): each such
// package must be listed, or a Go change elsewhere would not run it. A listed
// package that no longer walks the tree is reported too.
//
// It reads every file it judges itself, so a cached pass is never stale.
func TestWalkerPackagesAreListed(t *testing.T) {
	var listed []string
	for _, r := range readGateConfig(t).AffectedRules {
		if r.On == "go-source" {
			listed = append(listed, r.Packages...)
		}
	}
	var walkers []string
	_ = filepath.WalkDir(repoRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			base := d.Name()
			if rel != "." && (strings.HasPrefix(base, ".") || base == "node_modules" || base == "testdata" || rel == "frontends" || rel == "electron" || rel == "sdk") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		text := string(src)
		if strings.Contains(text, "//go:build ") && !strings.Contains(text, "//go:build !") {
			return nil // a tagged test the -short lane does not build
		}
		if (strings.Contains(text, "WalkDir(") || strings.Contains(text, "filepath.Walk(")) && strings.Contains(text, `".go"`) {
			if pkg := "./" + path.Dir(rel); !slices.Contains(walkers, pkg) {
				walkers = append(walkers, pkg)
			}
		}
		return nil
	})
	for _, w := range walkers {
		if !slices.Contains(listed, w) {
			t.Errorf("%s has a test that walks the tree's Go files: list it in forge.yaml's `on: go-source` affected rule, or a Go change elsewhere will not run it", w)
		}
	}
	for _, l := range listed {
		if !slices.Contains(walkers, l) {
			t.Errorf("forge.yaml's `on: go-source` rule lists %s, which has no test walking the tree's Go files: remove it", l)
		}
	}
}
