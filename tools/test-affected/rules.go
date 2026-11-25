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
	// laneMigOrder is not a test runner either: it fails a migration added on
	// this branch whose version is at or below origin/main's newest
	// (migrationorder.go).
	laneMigOrder lane = "migration-order"
	// laneWebDemo is not a test runner either: it builds the forge demo
	// bundle (frontends/web's `vite build --mode demo`), whose
	// vite-plugin-demo-bundle.ts guard fails on a Reliant-only module in it.
	// tsc -b cannot see that (#2669 passed it and failed the demo build).
	laneWebDemo lane = "web-demo"
	// laneWebBuild is not a test runner either: it builds the renderer as a
	// release does (frontends/web's production `vite build`) and verifies the
	// bundle. vitest and tsc -b cannot see what only the bundler resolves:
	// #2829's supabase-js bump imported `node:module`, passed both, and
	// failed the release gate's webbuild.
	laneWebBuild lane = "web-build"
)

var allLanes = []lane{laneGo, laneWeb, laneElectron, laneForgeGen, laneDocGen, laneMigOrder, laneWebDemo, laneWebBuild}

// webBuildInputs are the files the renderer's release build reads that are
// not web sources: its vite config, the plugins that config imports, and its
// HTML entry. A change to the web lane's dependencies or build config (the
// web-config and node-toolchain rules: package.json, pnpm-lock.yaml, ...)
// runs the build too, through the web lane's FULL reasons.
var webBuildInputs = []string{
	"frontends/web/vite.config.ts", "frontends/web/vite-plugin-*.ts", "frontends/web/index.html",
}

// demoBuildInputs are the files the forge demo build reads that are not
// modules in its import graph (WebGraph.Demo): its vite config and plugins
// and its HTML entry. A change to the web lane's dependencies or build config
// (the web-config rule) runs the demo build as well, through the web lane's
// FULL reasons.
var demoBuildInputs = []string{
	"frontends/web/vite.config.ts", "frontends/web/vite-plugin-*.ts", "frontends/web/demo.html",
}

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
// The blind spot is a `//forge:` directive in a Go file that is not doc.go;
// CI's "forge generate is a no-op" job checks the whole tree regardless.
var forgeInputs = []string{
	// Taskfile.yml: forge-generate runs through `task generate:forge`.
	"forge.yaml", "go.mod", "go.sum", "buf.yaml", "buf.gen.yaml", "Taskfile.yml",
	"proto/**", "db/**",
	"**/contract.go", "**/doc.go", "internal/handlers/**",
	// The composition root: forge reads compose.go and the providers into
	// internal/app/graph_gen.go, routes_gen.go and gen/forge_service_catalog.json.
	// #3499 changed only internal/app's wiring and main's generate check went
	// red on the catalog (control-plane#3571).
	"internal/app/**",
	// Every forge output, as the merge gate's gen_re names them: a changed
	// generated file runs generate, which re-renders it before build and
	// test judge it (a merge with main can leave one a step behind, and
	// frontends/web/src/hooks/*_gen.ts sits outside every directory below).
	"**/*_gen.go", "**/*_gen_test.go", "**/*_gen.ts", "**/*_gen_test.ts", "**/*_gen.test.ts",
	"**/*_gen.json", "**/*_gen.ndjson", "**/*_gen.k",
	"gen/**", "frontends/web/src/gen/**", "pkg/config/**", "deploy/**",
	// The public protos are generated into the SDK module too
	// (buf.gen.sdk-go.yaml), and proto/public-api.txt decides which rpcs get a
	// client. proto/** above already covers the allowlist; this is the
	// generated half.
	// The rest of the SDK module: its generated protos must match proto/
	// (a hand edit beside them is judged the same way).
	"sdk/go/**",
	".forge/hashes.json", ".forge/scaffolded.json", ".forge/disowned.json",
	// generate.hooks: controlplane-client, sdk-ts-gen.
	"proto-vendor/**", "buf.gen.controlplane.yaml", "scripts/proto-generate-atomic.sh",
	"buf.gen.sdk-ts.yaml", "sdk/ts/src/gen/**",
	// generate.hooks: sdk-go-gen, sdk-aliases.
	"buf.gen.sdk-go.yaml", "scripts/sdk-go-gen.sh", "tools/sdkalias/**",
	"tools/protoc-gen-es/package.json", "tools/protoc-gen-es/pnpm-lock.yaml", "tools/protoc-gen-es/pnpm-workspace.yaml",
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
	"tools/sdkalias",
}

// forgeHookOf names the forge.yaml hook that runs each generator
// (ForgeGenPlan.Hooks). TestForgeHookGeneratorsMatchForgeYAML holds it to
// forge.yaml too.
var forgeHookOf = map[string]string{
	"internal/workflow/yaml/cmd/yamlbindingsgen":          "yaml-bindings",
	"internal/llm/tools/cmd/toolconfiggen":                "tool-catalog",
	"internal/integrations/manifest/schema/cmd/schemagen": "manifest-schema",
	"tools/integrationgen":                                "integrations",
	"tools/sdkalias":                                      "sdk-aliases",
}

// forgeInputsExcept are paths under forgeInputs that forge generate neither
// reads nor writes: a handler package's hand-written tests. forge scans a
// handler package's non-test files for the rpcs already implemented
// (codegen.ScanExistingMethods skips _test.go), and of its tests reads only
// its own one-shot scaffold tests, to warn when one names a pb type that no
// longer exists (a warning, never a write). The tests it generates
// (*_gen_test.go) stay inputs: a hand edit to one is drift. A handler test
// fix selected the whole forge generate, ~5 minutes on the loaded host.
// internal/app's hand-written tests are the same: forge reads its providers
// and compose.go, never its tests.
var forgeInputsExcept = []string{"internal/handlers/**/*_test.go", "internal/app/**/*_test.go"}

func isForgeInput(p string) bool {
	if strings.HasSuffix(p, "_gen_test.go") {
		return true
	}
	for _, pat := range forgeInputsExcept {
		if matchGlob(pat, p) {
			return false
		}
	}
	for _, pat := range forgeInputs {
		if matchGlob(pat, p) {
			return true
		}
	}
	return false
}

// rule claims paths for the web lanes and says what a change to them selects.
// The Go, electron and docs-generate lanes are forge's (forge.yaml's
// ci.gate), and so is the fail-safe: a path forge cannot place makes every
// lane here FULL too (Classify). Rules are a union: a path may match several
// and gets all of their effects.
type rule struct {
	id       string
	patterns []string
	except   []string

	// full: a change here cannot be judged by a subset of the web lane;
	// reason is printed.
	full   bool
	reason string

	// protoGen: a .proto regenerates frontends/web/src/gen/<dir>/<name>_pb.ts.
	protoGen bool
	// webSrc: the file is a web module (or a file a module imports): run the
	// vitest tests that import it.
	webSrc bool
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

// rules is the web table dev-docs/testing-affected.md prints.
var rules = []rule{
	{
		id: "node-toolchain",
		patterns: []string{
			"package.json", "package-lock.json", "pnpm-lock.yaml", ".nvmrc", ".npmrc",
		},
		full:   true,
		reason: "a root Node manifest or toolchain pin changed",
	},
	{
		id: "web-config",
		patterns: []string{
			"frontends/web/package.json", "frontends/web/pnpm-lock.yaml", "frontends/web/pnpm-workspace.yaml", "frontends/web/.nvmrc",
			"frontends/web/vite.config.ts", "frontends/web/tsconfig*.json", "frontends/web/tailwind.config.js", "frontends/web/postcss.config.js",
			"frontends/web/src/test/setup.ts",
			// pnpm-workspace.yaml's patchedDependencies: a patch rewrites an
			// installed dependency that any vitest file may load.
			"frontends/web/patches/**",
			// frontends/web installs sdk/ts as a workspace member: its manifest is
			// part of web's lockfile and dependency graph.
			"sdk/ts/package.json",
		},
		full:   true,
		reason: "web build/test configuration or dependencies changed (every vitest file runs through it)",
	},
	{
		id:       "proto-source",
		patterns: []string{"proto/**/*.proto", "proto-vendor/**/*.proto"},
		protoGen: true,
		note:     "regenerates its TypeScript client",
	},
	{
		id:       "proto-config",
		patterns: []string{"proto/**", "proto-vendor/**"},
		// public-api.txt regenerates through buf.gen.sdk-go.yaml's filters
		// only; the forge-generate lane judges it, so it does not force the
		// web suite.
		except: []string{"**/*.proto", "**/*.md", "proto/public-api.txt"},
		full:   true,
		reason: "proto tooling configuration changed (it regenerates every client)",
	},
	{
		// @reliantlabs/sdk is a workspace member of frontends/web and the web app
		// compiles it from source (vite and tsc alias it), so a change to it can
		// change any vitest file; its own tests run in the web lane too
		// (frontends/web/vite.config.ts test.include).
		id:       "sdk-ts",
		patterns: []string{"sdk/ts/**"},
		full:     true,
		reason:   "@reliantlabs/sdk changed: the web app compiles it from source, and the web lane runs its tests",
	},
	{
		id:       "web-source",
		patterns: []string{"frontends/web/src/**", "frontends/web/vite-plugin-*.ts", "frontends/web/*.test.ts"},
		webSrc:   true,
	},
	{
		id:       "web-static",
		patterns: []string{"frontends/web/index.html", "frontends/web/demo.html", "frontends/web/public/**", "frontends/web/design-sandbox/**"},
		note:     "served assets: only tests that name the file run",
	},
	{
		// The forge demo's capture and smoke scripts (run by hand, never by
		// vitest): the fixtures test reads the capture script's text, a web
		// module importing one reaches it through the import graph, and the
		// tests that walk frontends/web/ run as for any web module.
		id:       "web-demo-scripts",
		patterns: []string{"frontends/web/scripts/demo/**"},
		webSrc:   true,
		note:     "the forge demo's scripts: only tests that import or name the file run",
	},
	{
		id: "web-inert",
		patterns: []string{
			"frontends/web/.gitignore", "frontends/web/README.md", "frontends/web/eslint.config.js", "frontends/web/eslint-rules/**", "frontends/web/stylelint.config.js",
			"frontends/web/playwright.config.ts", "frontends/web/playwright.visual.config.ts", "frontends/web/e2e/**",
		},
		note: "lint config and Playwright specs: no vitest file reads them (run Playwright separately)",
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

// isMigrationPath reports whether p is under db/migrations/, the directory the
// migration-order lane judges.
func isMigrationPath(p string) bool { return matchGlob("db/migrations/**", p) }
