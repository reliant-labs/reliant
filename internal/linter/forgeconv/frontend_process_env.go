// File: internal/linter/forgeconv/frontend_process_env.go
//
// frontend-process-env — the frontend mirror of the backend's os.Getenv
// guardrail (config.enforce_typed_access / forbidigo).
//
// Frontend config is proto-declared and KCL-projected now: a config
// message annotated (forge.v1.frontend_config) generates a typed module at
// src/lib/config_gen.ts, whose values arrive at RUNTIME from a config.js
// document KCL renders per environment. Reading process.env (or Vite's
// import.meta.env) in frontend source bypasses ALL of that:
//
//   - the schema        — the value is not declared anywhere
//   - the types         — `string | undefined` instead of a checked field
//   - the defaults      — no zod .default(), so unset silently becomes
//     undefined at the call site
//   - the secret refusal — a `sensitive` field is a generate-time error on
//     the typed path; process.env has no such gate
//   - PROMOTABILITY     — the load-bearing one. Both bundlers INLINE these
//     reads at BUILD time, freezing the artifact to the environment it was
//     built against. A bundle built with staging's issuer carries it
//     forever, so `forge env deploy` cannot move it to prod without a
//     rebuild. That is the property the whole runtime-injection design
//     exists to buy, and one raw read gives it back.
//
// ── Why a forge-native scan and NOT an ESLint rule ────────────────────
//
// Both web scaffolds DO ship an eslint.config.mjs, so `no-restricted-syntax`
// was genuinely available and was the first thing considered.
//
// It was originally rejected on one decisive fact — eslint.config.mjs was
// SCAFFOLD-ONCE, absent from generator.managedFiles(), so `forge project
// upgrade` never refreshed it and a rule added there would have reached
// projects created afterwards and NO existing project. THAT OBJECTION IS
// GONE: the file is a Tier-2 managed file now (upgrade_frontend_managed.go),
// exactly as .golangci.yml is, so a pristine copy is refreshed on upgrade
// and a rule placed there would in fact arrive.
//
// The check stays here anyway, for the reasons below that the fix does not
// touch — the first of which is the one that matters.
//
// Three further reasons, in descending order:
//
//   - The rule needs to know which variables the project's config proto
//     DECLARES, so it can tell "read this from the typed module" (a
//     one-line fix) from "declare this field first" (a proto edit). That is
//     forge's knowledge, not something a static eslint config can express.
//   - react-native ships no eslint config at all, so an eslint-only rule
//     would silently not apply to a whole frontend kind.
//   - eslint needs node_modules installed; `forge lint` must stay useful on
//     a fresh clone and in a backend-only CI job.
//
// ── The engine is a parser, not a regex ───────────────────────────────
//
// This check WAS textual, and it saw `process.env.X` as two tokens rather
// than a resolved binding. That cost it two documented holes: an aliased
// read (`const e = process.env; e.FOO`) and a `${process.env.X}`
// interpolation inside a template literal were both missed. The alias case
// was rationalised as harmless on the grounds that bundlers do not inline
// it; the corpus disagrees — control-plane's OIDC provider aliases
// process.env to a module-level object and reads config off it.
//
// Detection now runs on a real parse: esbuild lowers TS/TSX to plain JS and
// a JS lexer walks the result, so a read is found as a member expression
// rather than a spelling, and a sourcemap carries the line number back.
// Both holes are closed structurally. See frontend_process_env_ast.go for
// the measurements that chose that pipeline over the alternatives, and
// textualEnvFindings below for the fallback that still covers a file the
// parser cannot read.
//
// ── The precondition that keeps this quiet ────────────────────────────
//
// The rule reports NOTHING unless the frontend actually has
// src/lib/config_gen.ts. This is the single most important false-positive
// guard: a frontend that has not adopted proto-declared config has no typed
// door to point at, so every finding would be advice the author cannot
// follow. Measured on a fresh `forge project new --frontend web` scaffold,
// which emits 20 process.env occurrences and no config_gen.ts, this
// precondition alone is the difference between 13 findings and zero.
//
// ── The allowlist, and where each entry came from ─────────────────────
//
// Every exemption below was derived from a file forge actually scaffolds or
// that exists in the control-plane corpus — none are invented:
//
//   - src/lib/config_gen.ts itself. Its SSR branch reads process.env by
//     design (no window on the server; the values come from the same KCL
//     config either way).
//   - *.config.{ts,js,mjs,cjs} at the frontend root — next.config.ts,
//     vite.config.ts, vitest.config.ts. These run in Node at build/dev-server
//     time. vite.config.ts reads IDP_UPSTREAM_ORIGIN for its dev proxy,
//     which is legitimately not app config.
//   - FORGE-GENERATED files (the `Code generated by forge` banner):
//     basepath_gen.ts and otel_gen.ts read process.env because Next.js can
//     only inline a literal written in project code — the reads cannot move
//     into a library. Flagging them would tell the author to edit a file
//     that is overwritten on the next generate.
//   - Tests and mock/dev harnesses (*.test.*, *.spec.*, __tests__/,
//     src/mocks/) — setting process.env is how a test drives config.
//   - NODE_ENV and Vite's import.meta.env.{DEV,PROD,MODE,SSR,BASE_URL}.
//     These are BUNDLER BUILD-MODE discriminators, not deployment config:
//     they cannot come from KCL (the bundler defines them statically, and
//     dead-code elimination depends on that), so there is no typed field to
//     redirect to. The scaffold's own providers.tsx uses NODE_ENV this way.
//     FORGE_DEV_API_PROXY joins them for the same reason: the frontend's
//     own next.config.ts / vite.config.ts defines it for its dev server,
//     and src/lib/api-base.ts reads it to send RPCs through that server.
//   - NEXT.JS SERVER-ONLY App Router modules — app/**/route.ts, middleware.ts,
//     instrumentation.ts. This one is not a convenience: the rule's central
//     premise is false for them. Next inlines process.env when building the
//     CLIENT bundle; server-side code reads it LIVE at request time from the
//     container's environment, which is the very runtime-injection the typed
//     module exists to provide. Flagging a route handler therefore inverts the
//     rule — control-plane's api/[...path]/route.ts reads a KCL-set,
//     in-cluster ADMIN_API_URL, and moving it to the browser-facing config
//     module would ship a cluster-DNS name to the browser AND turn a runtime
//     read into a build-time one, losing promotability. See
//     isNextServerOnlyModule.
//   - src/gen/ (protobuf-es output), node_modules/, .next/, dist/, build/,
//     coverage/ — vendored or built artefacts, mirroring the ignores the
//     scaffolded eslint config already lists.
//   - *.d.ts type declarations, which declare shapes rather than read them.
//   - COMMENTS AND STRINGS. Several scaffolded files discuss process.env in
//     prose (otel_gen.ts's header explains why its reads cannot move;
//     settings-web/src/lib/env.ts says "These no longer read process.env").
//     Flagging documentation that tells you the right thing would be the
//     most insulting possible false positive.
//
// ── Severity ──────────────────────────────────────────────────────────
//
// Caller-controlled, exactly like the forge-owned-dotenv rule next door, so
// one analyzer backs every verdict. `forge lint` passes the severity
// resolved from lint.rules, which defaults to WARNING. Warning is the right
// default because adoption is incremental: the config system landed
// recently, and a frontend mid-migration (control-plane's settings-web is
// one — connect.ts has moved to loadConfig(), basepath.ts has not) would
// otherwise go red the day it declares its first config field. Projects
// that have finished migrating set
// `lint.rules: {forgeconv-frontend-process-env: error}` to gate.

package forgeconv

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// RuleFrontendProcessEnv is the rule ID, addressable in forge.yaml's
// `lint.rules` map and in per-line suppression directives.
const RuleFrontendProcessEnv = "forgeconv-frontend-process-env"

// frontendConfigModuleRel is the generated typed config module, relative
// to a frontend root. Its PRESENCE is the precondition for this rule (see
// the file header) and its contents supply the declared field set.
const frontendConfigModuleRel = "src/lib/config_gen.ts"

// rawEnvRead matches the two literal spellings a bundler will inline:
// `process.env.FOO`, `process.env["FOO"]`, and `import.meta.env.FOO`.
// Optional chaining (`process.env?.NODE_ENV`) is matched too — the
// control-plane telemetry module uses that form.
//
// Capture 1/2/3 are the variable name across the three bracketings.
var rawEnvRead = regexp.MustCompile(
	`(?:process\.env|import\.meta\.env)\s*\??\s*(?:\.\s*([A-Za-z_$][A-Za-z0-9_$]*)|\[\s*["']([A-Za-z_$][A-Za-z0-9_$]*)["']\s*\])`)

// buildModeVars are bundler build-mode discriminators rather than
// deployment config. They cannot be projected from KCL — both bundlers
// define them statically at build time and their dead-code elimination
// depends on it — so there is no typed field to point at.
var buildModeVars = map[string]bool{
	"NODE_ENV": true,
	// Vite's own import.meta.env intrinsics.
	"DEV": true, "PROD": true, "MODE": true, "SSR": true, "BASE_URL": true,
	// The scaffold's dev-server API proxy switch (src/lib/api-base.ts). Not
	// deployment config: the frontend's OWN bundler config defines it —
	// next.config.ts's `env` block, vite.config.ts's `define` — as the path
	// its dev server proxies to the API, and as "" in every build. KCL has no
	// value to give it, because no deployed bundle has a dev server.
	"FORGE_DEV_API_PROXY": true,
}

// scannedExts are the source extensions this rule reads.
var scannedExts = map[string]bool{
	".ts": true, ".tsx": true, ".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
}

// skipDirs are directory names never descended into: vendored deps,
// build output, and generated protobuf. Mirrors the `ignores` list the
// scaffolded eslint config already carries.
var skipDirs = map[string]bool{
	"node_modules": true, ".next": true, ".next-prod": true, "out": true,
	"dist": true, "build": true, "coverage": true, ".turbo": true, ".vercel": true,
}

// LintFrontendProcessEnv scans each frontend for source files that read
// process.env / import.meta.env directly instead of the generated typed
// config module, and reports each read at the given severity.
//
// The severity is the caller's, matching LintFrontendEnvFiles next door:
// one analyzer behind every verdict, so an advisory `forge lint` run and a
// gating one can never disagree about what is a violation.
//
// root relativizes reported paths; feDirs are the frontend roots. A
// frontend with no generated config module is skipped entirely — see the
// file header for why that precondition is load-bearing.
func LintFrontendProcessEnv(root string, feDirs []string, sev finding.Severity) Result {
	var res Result
	seen := map[string]bool{}
	for _, feDir := range feDirs {
		declared, ok := declaredConfigVars(feDir)
		if !ok {
			// No typed config module: nothing to redirect to.
			continue
		}
		for _, path := range frontendSourceFiles(feDir) {
			if seen[path] {
				continue
			}
			seen[path] = true
			res.Findings = append(res.Findings, scanFrontendSourceForEnv(root, feDir, path, declared, sev)...)
		}
	}
	sort.SliceStable(res.Findings, func(i, j int) bool {
		if res.Findings[i].File != res.Findings[j].File {
			return res.Findings[i].File < res.Findings[j].File
		}
		return res.Findings[i].Line < res.Findings[j].Line
	})
	return res
}

// declaredConfigVars reads the generated config module and returns the set
// of field names its zod schema declares, plus whether the module exists at
// all. The bool is the precondition; the set decides which of the two
// remediations a finding carries.
//
// The schema keys are parsed textually from the `configSchema = z.object({`
// block. A parse that finds nothing still returns ok=true with an empty
// set: the module exists, so the rule runs, and every finding simply gets
// the "declare it first" arm — the conservative direction.
func declaredConfigVars(feDir string) (map[string]bool, bool) {
	data, err := os.ReadFile(filepath.Join(feDir, filepath.FromSlash(frontendConfigModuleRel)))
	if err != nil {
		return nil, false
	}
	declared := map[string]bool{}
	inSchema := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "configSchema") && strings.Contains(trimmed, "z.object(") {
			inSchema = true
			continue
		}
		if !inSchema {
			continue
		}
		if strings.HasPrefix(trimmed, "})") {
			break
		}
		// `NEXT_PUBLIC_API_URL: z.string().default("…"),`
		key, _, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(strings.Trim(strings.TrimSpace(key), `"'`))
		if key != "" && !strings.HasPrefix(key, "/") && !strings.HasPrefix(key, "*") {
			declared[key] = true
		}
	}
	return declared, true
}

// frontendSourceFiles walks a frontend root and returns the source files
// this rule scans, in deterministic order, with the allowlisted paths
// already filtered out.
func frontendSourceFiles(feDir string) []string {
	var out []string
	_ = filepath.WalkDir(feDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable subtree is skipped, not fatal
		}
		if d.IsDir() {
			name := d.Name()
			if skipDirs[name] || (strings.HasPrefix(name, ".") && name != "." && path != feDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !scannedExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if isEnvExemptPath(feDir, path) {
			return nil
		}
		out = append(out, path)
		return nil
	})
	sort.Strings(out)
	return out
}

// isEnvExemptPath reports whether a file is allowlisted by PATH. Every
// branch is justified in the file header.
func isEnvExemptPath(feDir, path string) bool {
	rel, err := filepath.Rel(feDir, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)

	// The generated typed config module itself — its SSR branch reads
	// process.env by design.
	if rel == frontendConfigModuleRel {
		return true
	}
	// Type declarations declare shapes, they do not read values.
	if strings.HasSuffix(base, ".d.ts") {
		return true
	}
	// Build-time tooling: next.config.ts, vite.config.ts, vitest.config.ts,
	// and friends. Root-level only — a `foo.config.ts` deep in src/ is app
	// code. Also covers the conventional build script dirs.
	if !strings.Contains(rel, "/") && isConfigScriptName(base) {
		return true
	}
	// Tests and mock / dev-only harnesses.
	if isTestOrMockPath(rel, base) {
		return true
	}
	// Generated protobuf-es output.
	if rel == "src/gen" || strings.HasPrefix(rel, "src/gen/") {
		return true
	}
	// Next.js SERVER-ONLY App Router modules — route handlers, and the
	// server files that bracket them. See isNextServerOnlyModule for why
	// the rule's central premise does not apply to these.
	if isNextServerOnlyModule(rel, base) {
		return true
	}
	return false
}

// nextServerOnlyBasenames are the App Router files that Next.js NEVER bundles
// for the browser. `route` is the route handler (an HTTP endpoint); the other
// two are server-only by framework contract in the same way.
//
// A page.tsx or layout.tsx is deliberately NOT here: those are server
// components by default but may carry "use client", and their code can be sent
// to the browser. Only files the framework guarantees are server-side qualify.
var nextServerOnlyBasenames = map[string]bool{
	"route":           true, // app/**/route.ts — an HTTP endpoint
	"middleware":      true, // runs on the server, never shipped to a browser
	"instrumentation": true, // process-level startup hook
}

// isNextServerOnlyModule reports whether rel is a Next.js App Router module
// that only ever executes on the server.
//
// ── Why these are exempt ──────────────────────────────────────────────
//
// This rule's load-bearing argument is PROMOTABILITY: both bundlers INLINE a
// process.env read at BUILD time, freezing the artifact to the environment it
// was built against, so `forge env deploy` cannot move it without a rebuild.
// That argument is correct for anything that reaches the browser, and it is
// simply NOT TRUE of a Next.js route handler.
//
// Next.js inlines process.env when it is producing the CLIENT bundle — that is
// what the NEXT_PUBLIC_ prefix opts a variable into. Server-side code (route
// handlers, server components, server actions) runs on the Node runtime and
// reads process.env LIVE, at request time, from the container's actual
// environment. So a route handler reading a non-NEXT_PUBLIC_ variable is
// already runtime-injected: the exact property the typed config module exists
// to buy, obtained by the framework's own contract.
//
// Flagging it inverts the rule. control-plane's api/[...path]/route.ts is the
// worked example: it is the same-origin proxy, `export const runtime =
// "nodejs"`, and it reads ADMIN_API_URL — a value KCL sets per environment
// (deploy/kcl/{dev,prod}/main.k) and which names an IN-CLUSTER host. Moving it
// to the typed config module would be actively wrong on two counts. The module
// is browser-facing, so the cluster-DNS name would be shipped to a browser that
// cannot resolve it — inviting exactly the "fix" of pointing the browser back
// at a port-forward, which is the cross-origin coupling that file was written
// to delete. And it would make a runtime-read value build-time, LOSING
// promotability rather than gaining it.
//
// ── Why not tell them to suppress it ──────────────────────────────────
//
// The remediation already offers `// forge:lint-disable-next-line`. That is
// the right escape hatch for a genuine judgement call; it is the wrong answer
// for a whole file category where the rule's premise does not hold. Every
// scaffolded project with a server-side proxy would carry the same suppression
// with the same reason — which is a rule telling users to silence it, i.e. a
// rule that is wrong, and the noise trains people to suppress the cases that
// DO matter.
//
// ── Structural, not a path blacklist ──────────────────────────────────
//
// The check requires BOTH the App Router location (an `app/` segment) and a
// server-only basename, so `src/lib/route.ts` — an ordinary module that
// happens to be called route.ts — stays subject to the rule. Vite frontends
// have no app/ router, so nothing there is exempted by this.
func isNextServerOnlyModule(rel, base string) bool {
	ext := strings.ToLower(filepath.Ext(base))
	if !scannedExts[ext] {
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	if !nextServerOnlyBasenames[stem] {
		return false
	}
	// middleware.ts and instrumentation.ts live at the frontend or src root
	// by convention rather than under app/.
	if stem == "middleware" || stem == "instrumentation" {
		return rel == base || rel == "src/"+base
	}
	// A route handler must actually be in the App Router tree.
	for _, seg := range strings.Split(rel, "/") {
		if seg == "app" {
			return true
		}
	}
	return false
}

// isConfigScriptName reports whether base is a build-time config script
// (`*.config.{ts,js,mjs,cjs}`), the shape both bundlers use.
func isConfigScriptName(base string) bool {
	ext := strings.ToLower(filepath.Ext(base))
	if !scannedExts[ext] {
		return false
	}
	return strings.HasSuffix(strings.TrimSuffix(base, ext), ".config")
}

// isTestOrMockPath reports whether a file is a test, a test helper, or a
// mock/dev-only harness — places where driving process.env directly is the
// point rather than a mistake.
func isTestOrMockPath(rel, base string) bool {
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
		return true
	}
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "__tests__", "__mocks__", "e2e", "cypress", "playwright":
			return true
		}
	}
	// The scaffold's mock transport + scenario tree.
	return rel == "src/mocks" || strings.HasPrefix(rel, "src/mocks/")
}

// scanFrontendSourceForEnv reads one source file and emits a finding per
// offending read.
func scanFrontendSourceForEnv(root, feDir, path string, declared map[string]bool, sev finding.Severity) []Finding {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	reported := path
	if rel, relErr := filepath.Rel(root, path); relErr == nil && !strings.HasPrefix(rel, "..") {
		reported = filepath.ToSlash(rel)
	}
	feRel := path
	if r, relErr := filepath.Rel(feDir, path); relErr == nil {
		feRel = filepath.ToSlash(r)
	}

	// A forge-generated file is allowlisted by its BANNER rather than by
	// path, so the exemption follows the generator: any file forge starts
	// emitting later is covered without editing a list here. Checked from
	// the file content (first lines) rather than a name convention because
	// `_gen` is a convention, not a guarantee.
	var findings []Finding
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if isForgeGenerated(lines) {
		return nil
	}

	newFinding := func(name string, lineNo int) Finding {
		return Finding{
			Rule:     RuleFrontendProcessEnv,
			Severity: sev,
			File:     reported,
			Line:     lineNo,
			Message: fmt.Sprintf(
				"%s reads %s directly; this bypasses the typed config module (no schema, no defaults, "+
					"no secret refusal) and — because the bundler INLINES it at build time — freezes the "+
					"artifact to the environment it was built against, so `forge env deploy` cannot move it",
				feRel, name),
			Remediation: processEnvFixHint(name, declared),
		}
	}

	// The parser is the primary engine (frontend_process_env_ast.go): it
	// sees a resolved member expression rather than a two-token spelling,
	// which is what catches an aliased read and a read inside a template-
	// literal substitution — the two holes the textual scan documented.
	//
	// A file it cannot parse falls back to the textual scan below. A syntax
	// error must not become a way to hide a raw env read: the weaker check
	// beats no check.
	source := []byte(strings.Join(lines, "\n"))
	if reads, ok := parseEnvReads(path, source); ok {
		for _, r := range reads {
			if buildModeVars[r.Name] {
				continue
			}
			findings = append(findings, newFinding(r.Name, r.Line))
		}
	} else {
		findings = append(findings, textualEnvFindings(lines, newFinding)...)
	}

	// Per-line / per-file opt-out, through the SAME suppression engine
	// every other forge rule uses — so `// forge:lint-disable-line` and
	// the borrowed `//nolint:` spelling both work here with no new
	// vocabulary, and a reason-less suppression of a GATING finding is
	// itself reported (suppress.Result.Violations).
	if len(findings) == 0 {
		return nil
	}
	applied := suppress.Apply(strings.Join(lines, "\n"), findings)
	return append(applied.Kept, applied.Violations...)
}

// textualEnvFindings is the pre-parser scan, kept as the FALLBACK for
// files esbuild cannot parse.
//
// It is deliberately unchanged in behaviour: it matches the two literal
// spellings a bundler inlines, after blanking comments and strings. It
// carries the two holes the parser closes (an aliased read, and a read
// inside a template literal) — which is exactly why it is no longer the
// primary engine, and exactly why keeping it costs nothing: on a file that
// does not parse, these findings are strictly more than zero.
func textualEnvFindings(lines []string, newFinding func(name string, line int) Finding) []Finding {
	var out []Finding
	inBlockComment := false
	for i, text := range lines {
		code, stillInBlock := stripComments(text, inBlockComment)
		inBlockComment = stillInBlock
		if strings.TrimSpace(code) == "" {
			continue
		}
		for _, m := range rawEnvRead.FindAllStringSubmatch(code, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if name == "" || buildModeVars[name] {
				continue
			}
			out = append(out, newFinding(name, i+1))
		}
	}
	return out
}

// isForgeGenerated reports whether the file carries forge's generated
// banner in its first few lines. ("use client" may precede it, as in the
// generated nav component.)
func isForgeGenerated(lines []string) bool {
	for i, l := range lines {
		if i > 4 {
			return false
		}
		if strings.Contains(l, "Code generated by forge") || strings.Contains(l, "forge:hash=") {
			return true
		}
	}
	return false
}

// stripComments blanks out `//` and `/* */` comment spans and string
// literals so prose that MENTIONS process.env is not mistaken for a read.
// It returns the code-only text and whether a block comment is still open.
//
// This is a scanner, not a JS parser, and deliberately so: the job is to
// avoid matching inside comments and strings, and a character walk does
// that for every shape that occurs in practice. Template literals are
// treated as strings; a `${process.env.X}` interpolation inside one is
// therefore missed, which is the safe direction (a miss is silence, a
// false hit is noise).
func stripComments(line string, inBlock bool) (string, bool) {
	var out strings.Builder
	runes := []rune(line)
	var quote rune
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inBlock:
			if c == '*' && i+1 < len(runes) && runes[i+1] == '/' {
				inBlock = false
				i++
			}
		case quote != 0:
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
		case c == '/' && i+1 < len(runes) && runes[i+1] == '/':
			return out.String(), false
		case c == '/' && i+1 < len(runes) && runes[i+1] == '*':
			inBlock = true
			i++
		case c == '"' || c == '\'' || c == '`':
			quote = c
		default:
			out.WriteRune(c)
		}
	}
	return out.String(), inBlock
}

// processEnvFixHint renders the remediation. It branches on whether the
// variable is ALREADY declared in the generated schema, because the two
// cases have genuinely different fixes and sending an author to edit a
// proto for a field that already exists is how a lint loses credibility.
func processEnvFixHint(name string, declared map[string]bool) string {
	if declared[name] {
		return fmt.Sprintf(
			"%s is already declared for this frontend — read it from the generated module instead: "+
				"`import { loadConfig } from \"@/lib/config_gen\";` then `loadConfig().%s`. "+
				"The value arrives at runtime from the KCL-rendered config.js, typed and defaulted.",
			name, name)
	}
	return fmt.Sprintf(
		"declare %s in the config message annotated `option (forge.v1.frontend_config)` in "+
			"proto/<project>/config/v1/config.proto, run `forge generate`, then read it as "+
			"`loadConfig().%s` (import { loadConfig } from \"@/lib/config_gen\"). "+
			"If it is genuinely build-time-only and not deployment config, suppress the line with "+
			"`// forge:lint-disable-next-line %s: <reason>`.",
		name, name, RuleFrontendProcessEnv)
}
