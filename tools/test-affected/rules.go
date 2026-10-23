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
	// laneForgeGen is not a test runner: it runs the pinned `forge generate`
	// and fails if that changes anything (task generate:check).
	laneForgeGen lane = "forge-generate"
	// laneDocGen is not a test runner either: it runs every docs generator
	// and fails if that changes anything (task generate:docs:check).
	laneDocGen lane = "docs-generate"
)

var allLanes = []lane{laneGo, laneWeb, laneElectron, laneForgeGen, laneDocGen}

// forgeInputs are the paths `forge generate` reads (protos, migrations,
// contract.go interfaces, package directives in doc.go, handler packages, the
// pins) or writes (gen/, *_gen.go, the handler
// mocks and harnesses, deploy/observability, .forge's records), and what its
// forge.yaml generate.hooks read or write other than compiled Go (the
// control-plane contract and its buf template, the integration projection
// specs and upstream schema, the hook outputs). A change to one runs the
// forge-generate lane: a hand edit of an output is drift too. The Go a hook
// generator compiles is not listed: Classify derives it from the graph
// (forgeHookGenerators).
//
// They are deliberately not rules: a rule claims a path for the test lanes,
// and these claim nothing there, so a path only they name still fails safe.
// The blind spot is a `//forge:` directive in a Go file that is not doc.go;
// CI's "forge generate is a no-op" job checks the whole tree regardless.
var forgeInputs = []string{
	"forge.yaml", "go.mod", "go.sum", "buf.yaml", "buf.gen.yaml",
	"proto/**", "db/**",
	"**/contract.go", "**/doc.go", "**/*_gen.go", "internal/handlers/**",
	"gen/**", "frontends/web/src/gen/**", "pkg/config/**", "deploy/**",
	".forge/hashes.json", ".forge/scaffolded.json", ".forge/disowned.json",
	// generate.hooks: controlplane-client.
	"proto-vendor/**", "buf.gen.controlplane.yaml", "scripts/proto-generate-atomic.sh",
	"tools/protoc-gen-es/package.json", "tools/protoc-gen-es/package-lock.json",
	// generate.hooks: the go:generate outputs, the manifest schemas, and the
	// integration projections' specs, upstream schema and catalog output.
	"internal/workflow/yaml/bindings_generated.go",
	"internal/llm/tools/toolcatalog/catalog_generated.go",
	"internal/integrations/manifest/schema/*.schema.json",
	"internal/integrations/webhook/*/projection/**",
	"internal/integrations/webhook/*/zz_projection_generated.go",
	"internal/integrations/catalog/**",
	"tools/integrationgen/upstream/**",
}

// forgeHookGenerators are the programs forge.yaml's generate.hooks run (a
// `go generate` directive's command, or the `go run` target): a change to the
// code any of them compiles changes what they write, so it runs the
// forge-generate lane. TestForgeHookGeneratorsMatchForgeYAML holds this list
// to forge.yaml.
var forgeHookGenerators = []string{
	"internal/workflow/yaml/cmd/yamlbindingsgen",
	"internal/llm/tools/cmd/toolconfiggen",
	"internal/integrations/manifest/schema/cmd/schemagen",
	"tools/integrationgen",
}

func isForgeInput(p string) bool {
	for _, pat := range forgeInputs {
		if matchGlob(pat, p) {
			return true
		}
	}
	return false
}

// docGenPaths are what the docs generators (Taskfile.yml's generate:docs:
// tools/docgen and scripts/generate-mintlify-reference.py) read or write
// other than compiled Go: their recipes' arguments and the paths their sources
// name. A change to one runs the docs-generate lane: a hand edit of an output
// is drift too (reliant #1228 edited SKILL.md; regenerating reverted it).
//
// The Go they compile is not listed: Classify derives it from the graph (the
// build deps of every tools/docgen package, embedded files included), and a
// .proto through the gen/ package it regenerates.
// TestDocGenTargetsCoverEveryDocsGenerator checks this list against the
// Taskfile's docs tasks and the generators' sources.
//
// Like forgeInputs, these are not rules: they claim nothing for the test lanes.
var docGenPaths = []string{
	// The generators, and the module they build against: cli.md documents
	// forge's whole command tree, so a forge bump changes it.
	"tools/docgen/**", "scripts/generate-mintlify-reference.py", "go.mod", "go.sum",
	// Inputs read at run time.
	"config/shortcuts.yaml",                    // generate-shortcuts
	"internal/workflow/scenario/types.go",      // generate-scenario-schema parses it
	"internal/workflow/builtin/presets/*.yaml", // generate-presets
	"docs/data/releases/**",                    // generate-changelog
	// Outputs (generated/docs-source/** is also read: by the assembler and the
	// Mintlify conversion).
	"generated/docs-source/**",
	"docs/reference/*.mdx",
	"docs/changelog.mdx",
	"frontends/web/src/store/shortcutsData.generated.ts",
	"internal/workflow/runtime/schema/field_descriptions.go",
	"internal/skills/catalog/builtin/workflow-builder/SKILL.md",
}

func isDocGenPath(p string) bool {
	for _, pat := range docGenPaths {
		if matchGlob(pat, p) {
			return true
		}
	}
	return false
}

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
	// protoGen: map proto/<dir>/x.proto to gen/<dir> (Go) and frontends/web/src/gen/<dir>.
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
			"frontends/web/package.json", "frontends/web/package-lock.json", "frontends/web/.nvmrc",
			"frontends/web/vite.config.ts", "frontends/web/tsconfig*.json", "frontends/web/tailwind.config.js", "frontends/web/postcss.config.js",
			"frontends/web/src/test/setup.ts",
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
			"Taskfile.yml", "Makefile", "buf.yaml", "buf.gen.yaml",
			"buf.gen.controlplane.yaml",
		},
		full:   allLanes,
		reason: "build or generation configuration changed (a subset cannot judge it)",
	},
	{
		// The forge pin (forge_version) and forge's own config reach the Go
		// code forge generates and lints, and nothing vitest or electron
		// runs. It changes on every forge bump, several a day, and running
		// every vitest file for each cost four runners.
		id:       "forge-config",
		patterns: []string{"forge.yaml"},
		full:     []lane{laneGo, laneForgeGen},
		reason:   "the forge pin or forge's configuration changed",
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
		patterns: []string{"db/migrations/**"},
		noGraph:  true,
		dbFan:    true,
		note:     "reaches only DB-backed tests",
	},
	{
		id:       "web-source",
		patterns: []string{"frontends/web/src/**", "frontends/web/vite-plugin-*.ts", "frontends/web/*.test.ts"},
		webSrc:   true,
	},
	{
		id:       "web-static",
		patterns: []string{"frontends/web/index.html", "frontends/web/public/**", "frontends/web/design-sandbox/**"},
		note:     "served assets: only tests that name the file run",
	},
	{
		id: "web-inert",
		patterns: []string{
			"frontends/web/.gitignore", "frontends/web/README.md", "frontends/web/eslint.config.js", "frontends/web/stylelint.config.js",
			"frontends/web/playwright.config.ts", "frontends/web/e2e/**",
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
