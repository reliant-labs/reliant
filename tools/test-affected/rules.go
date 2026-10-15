// Copyright (c) 2025 Reliant Labs
package main

import (
	"path"
	"strings"
)

// lane is one test runner the selector can narrow.
type lane string

const (
	laneGo       lane = "go"
	laneWeb      lane = "web"
	laneElectron lane = "electron"
)

var allLanes = []lane{laneGo, laneWeb, laneElectron}

// rule claims paths and says what a change to them selects. A path claimed by
// no rule and by no Go package forces the full run: that is the fail-safe.
// Rules are a union: a path may match several and gets all of their effects.
type rule struct {
	id       string
	patterns []string
	except   []string

	// full: a change here cannot be judged by a subset in these lanes; reason
	// is printed. A lane absent from full is unaffected by the rule.
	full   []lane
	reason string

	// noGraph: do not also map the file to the Go package owning its directory.
	noGraph bool
	// protoGen: map proto/<dir>/x.proto to gen/<dir> (Go) and web/src/gen/<dir>.
	protoGen bool
	// dbFan: select every package with DB-backed tests.
	dbFan bool
	// webSrc: the file is a web module (or a file a module imports): run the
	// vitest tests that import it.
	webSrc bool
	// electron: run the electron tests.
	electron bool
	// note is printed in the plan, to explain a claim that selects nothing.
	note string
}

func (r *rule) matches(p string) bool {
	hit := false
	for _, pat := range r.patterns {
		if matchGlob(pat, p) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	for _, pat := range r.except {
		if matchGlob(pat, p) {
			return false
		}
	}
	return true
}

// rules is the table dev-docs/testing-affected.md prints.
var rules = []rule{
	{
		id: "go-toolchain",
		patterns: []string{
			"go.mod", "go.sum", "go.work", "go.work.sum", "tools/tools.go",
		},
		full:   []lane{laneGo},
		reason: "a Go dependency or toolchain pin changed (a subset cannot judge a new version)",
	},
	{
		id: "node-toolchain",
		patterns: []string{
			"package.json", "package-lock.json", "pnpm-lock.yaml", ".nvmrc", ".npmrc",
		},
		full:   []lane{laneWeb, laneElectron},
		reason: "a root Node manifest or toolchain pin changed",
	},
	{
		id: "web-config",
		patterns: []string{
			"web/package.json", "web/package-lock.json", "web/pnpm-lock.yaml", "web/.nvmrc",
			"web/vite.config.ts", "web/tsconfig*.json", "web/tailwind.config.js", "web/postcss.config.js",
			"web/src/test/setup.ts",
		},
		full:   []lane{laneWeb},
		reason: "web build/test configuration or dependencies changed (every vitest file runs through it)",
	},
	{
		id:       "electron",
		patterns: []string{"electron/**"},
		electron: true,
	},
	{
		id: "global-config",
		patterns: []string{
			"Makefile", "forge.yaml", "buf.yaml", "buf.gen.yaml", "buf.gen-go-only.yaml",
			"buf.gen.controlplane.yaml", "sqlc.yaml",
		},
		full:   allLanes,
		reason: "build or generation configuration changed (a subset cannot judge it)",
	},
	{
		id:       "proto-source",
		patterns: []string{"proto/**/*.proto", "proto-vendor/**/*.proto"},
		protoGen: true,
		note:     "regenerates its Go and TypeScript clients",
	},
	{
		id:       "proto-config",
		patterns: []string{"proto/**", "proto-vendor/**"},
		except:   []string{"**/*.proto", "**/*.md"},
		full:     []lane{laneGo, laneWeb},
		reason:   "proto tooling configuration changed (it regenerates every client)",
	},
	{
		id:       "migrations",
		patterns: []string{"db/migrations/**", "internal/db/migrations/**"},
		noGraph:  true,
		dbFan:    true,
		note:     "reaches only DB-backed tests (needs DATABASE_URL to run them)",
	},
	{
		id:       "web-source",
		patterns: []string{"web/src/**", "web/vite-plugin-*.ts", "web/*.test.ts"},
		webSrc:   true,
	},
	{
		id:       "web-static",
		patterns: []string{"web/index.html", "web/public/**", "web/design-sandbox/**"},
		note:     "served assets: only tests that name the file run",
	},
	{
		id: "web-inert",
		patterns: []string{
			"web/.gitignore", "web/README.md", "web/eslint.config.js", "web/stylelint.config.js",
			"web/playwright.config.ts", "web/e2e/**",
		},
		note: "lint config and Playwright specs: no vitest file reads them (run Playwright separately)",
	},
	{
		// Data a Go test may read: claimed here so a path no test names is not
		// "unknown"; the reader scan finds the packages that do name it.
		id: "data",
		patterns: []string{
			"deploy/**", "config/**", "scripts/**", "e2e/**", "docker/**", "assets/**",
			"generated/**", "examples/**", "gen/forge_descriptor.json", ".forge/**",
			"bindings_generated.go",
		},
		note: "data a test reads only if its sources name it",
	},
	{
		id: "inert",
		patterns: []string{
			"docs/**", "**/*.md", "**/*.mdx", "research/**", "dev-docs/**", "contributing/**",
			"LICENSE", "LICENSES.txt", ".license-allowlist", ".license-allowlist-npm",
			".githooks/**", ".github/**", ".reliant/**", ".golangci.yml", ".goreleaser.yml",
			".gitignore", ".dockerignore", ".air*.toml", "Dockerfile*", "docker-compose*.yml",
			"tools/protoc-gen-es/**", "tmp/**",
		},
		note: "docs, lint config and CI plumbing: only tests that name the file run",
	},
}

// matchGlob matches a repo-relative slash path against a pattern whose
// segments are path.Match patterns; a `**` segment matches zero or more
// segments.
func matchGlob(pattern, p string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(p, "/"))
}

func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegments(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		ok, err := path.Match(pat[0], segs[0])
		if err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
