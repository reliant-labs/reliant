// Package webruntimepeers is the SINGLE source of truth for which packages
// must resolve to exactly one copy when a project links
// @reliantlabs/forge-web-runtime.
//
// WHY THIS PACKAGE EXISTS. The list previously lived in four places kept in
// step only by comments that said "keep the list in step with…":
//
//   - internal/cli/generate_frontend_tsconfig_peers.go  (tsconfig `paths` pins)
//   - internal/cli/generate_frontend_dedupe.go          (vitest resolve.dedupe)
//   - internal/templates/frontend/vite-spa/tsconfig.json.tmpl
//   - internal/templates/frontend/nextjs/tsconfig.json.tmpl
//
// They had ALREADY drifted. Measured against the runtime's real
// peerDependencies, the tsconfig list was missing 9 packages (every
// @opentelemetry/* except api, plus react) and the vitest list was missing 9
// (including @opentelemetry/api). A comment is not a mechanism.
//
// THE SOURCE OF TRUTH IS web-runtime/package.json's `peerDependencies`. That
// is the runtime's own declaration of "the consuming app supplies this copy",
// so it is the same fact the dedupe/pin lists are trying to state. The
// scaffolded vite.config.ts already derived its list that way at RUNTIME
// (Object.keys of the runtime's peerDependencies); this package gives the Go
// side the same derivation, so a peer added to the runtime is picked up by
// every consumer instead of by whichever list someone remembered.
//
// The embedded copy mirrors internal/buildinfo's VERSION pattern: go:embed
// cannot reach outside its own package directory, so peers.json is kept
// byte-equivalent to the runtime's peerDependencies by
// TestEmbeddedPeersMatchWebRuntime.
package webruntimepeers

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed peers.json
var peersJSON []byte

// extraTypeOnlyPins are packages that are NOT peerDependencies of the runtime
// but must still resolve to one copy for TYPE identity.
//
// @tanstack/query-core: @tanstack/react-query re-exports its types FROM
// query-core, so pinning only the wrapper leaves the underlying copy split and
// tsc reports "Property #private in type Query refers to a different member".
// The runtime depends on react-query and never names query-core, so it can
// never appear in peerDependencies — it is a transitive type identity, which
// is exactly the kind of fact a derived list cannot discover on its own.
var extraTypeOnlyPins = []string{
	"@tanstack/query-core",
}

// extraBundlerDedupe are packages that must be deduped by a BUNDLER but are
// not type-identity concerns.
//
// react-dom: React's runtime pairs with react and two copies break hooks at
// runtime (mismatched dispatcher) while typechecking perfectly green. It is
// not in the runtime's peerDependencies because the runtime imports react, not
// react-dom — but a project that ends up with two react-doms has the same
// class of failure, so the bundler list carries it.
var extraBundlerDedupe = []string{
	"react-dom",
}

// typesOnlyDir maps a package whose TYPES ship separately, under @types/, to
// that types package. A tsconfig `paths` entry is a full override of module
// resolution, not a hint: once "react" maps to ./node_modules/react, tsc looks
// there and NOWHERE else, so it finds index.js, finds no bundled .d.ts, and
// never consults @types/react the way it would have unaided:
//
//	error TS7016: Could not find a declaration file for module 'react'.
//	'…/node_modules/react/index.js' implicitly has an 'any' type.
//
// Every other pinned package bundles its own typings, so pointing at the
// implementation directory is right for them and wrong only here. Pointing
// react's entry at @types/react keeps the dedupe intent — one copy of the type
// — while giving tsc a directory that actually declares them.
//
// Reproduced in isolation: with target/lib set and real react + @types/react
// installed, `"react": ["./node_modules/react"]` fails with the TS7016 above
// and `"react": ["./node_modules/@types/react"]` typechecks clean.
var typesOnlyDir = map[string]string{
	"react": "@types/react",
}

// The two node_modules layouts a forge frontend can have, and the reason a pin
// must name the RIGHT one rather than listing both.
//
// A `paths` entry whose key has no `*` wildcard must be an array of EXACTLY
// ONE element. TypeScript itself tolerates more and falls through to the next
// candidate, but Next.js resolves imports with SWC, whose tsc-compatible
// resolver asserts the single-element rule and PANICS the whole build:
//
//	thread '<unnamed>' panicked at swc_ecma_loader/src/resolvers/tsc.rs:82:21:
//	assertion `left == right` failed: value of `paths.@bufbuild/protobuf`
//	should be an array with one element because the src path does not
//	contains * (wildcard)
//
// So "list both and let the toolchain pick" is not available: it typechecks
// green under tsc and takes `next build` down. The pin has to be resolved to
// the layout the project actually has.
//
// WHICH LAYOUT, AND WHY IT VARIES. Ordinarily each frontend installs its own
// dependencies and localPinPrefix is right. But forge's dev bridge makes the
// project an npm WORKSPACE ROOT (internal/generator/frontend_webruntime_devlink.go),
// and npm hoists every member's dependencies to the root — in a bridged
// project frontends/<name>/node_modules does not exist at all. A pin left
// naming the frontend-local path then points at nothing, which tsc does not
// report as an error: the pin silently no-ops, resolution falls back to the
// ordinary upward walk, and it finds the LINKED runtime's own private copies
// first, producing exactly the duplicate-identity failure the pins exist to
// prevent:
//
//	src/lib/mock-transport_gen.ts(54,3): error TS2322: Type
//	'…/forge/web-runtime/node_modules/@connectrpc/connect/…'.Transport is not
//	assignable to type '…/<project>/node_modules/@connectrpc/connect/…'.Transport
//
// Measured in a scaffolded bridged project: frontend-local pin → 2 TS2322
// errors; hoisted pin → 0, with no tsc setting relaxed.
const (
	// localPinPrefix — the frontend installed its own dependencies.
	localPinPrefix = "./node_modules/"
	// hoistedPinPrefix — an npm workspace hoisted them to the project root,
	// two levels up from frontends/<name>/.
	hoistedPinPrefix = "../../node_modules/"
)

// TypePinTarget returns the node_modules-relative PACKAGE a tsconfig `paths`
// entry for name takes its typings from. It is the package itself for anything
// that bundles its typings, and the @types/ package for anything that does not.
func TypePinTarget(name string) string {
	if types, ok := typesOnlyDir[name]; ok {
		return types
	}
	return name
}

// WHY A PIN NAMES A DECLARATION FILE, NOT A DIRECTORY.
//
// tsconfig `paths` is not read by tsc alone. Next.js's webpack resolver
// (JsConfigPathsPlugin) applies the same mappings to APP code — requests from
// inside node_modules are exempt. A pin whose target is a package DIRECTORY
// is, to webpack, a directory request: it skips the package's `exports` map
// and falls back to `module`/`main`. So whenever such a pin resolves (the
// frontend-local layout in any ordinary checkout; the hoisted layout inside a
// Docker build at /app, where ../../node_modules re-roots onto
// /app/node_modules) app code and the runtime bind DIFFERENT files of one
// package:
//
//	src/app/providers.tsx          => @tanstack/react-query/build/legacy/index.js  (module)
//	forge-web-runtime/service-hooks => @tanstack/react-query/build/modern/index.js  (exports)
//
// Two module instances are two React contexts. The QueryClientProvider the
// app mounts is invisible to the runtime's hooks, and prerender fails with
// "No QueryClient set, use QueryClientProvider to set one". Every pinned
// package whose `exports` disagrees with `module` splits the same way
// (@tanstack/*, @connectrpc/connect, @opentelemetry/api, …).
//
// Next's resolver explicitly skips a `.d.ts` candidate ("Ensure .d.ts is not
// matched"), and Vite does not read `paths` at all, so a pin that names the
// package's DECLARATION ENTRY is invisible to every bundler — they resolve
// through `exports` like any other import — while tsc still binds exactly one
// copy of the types, which is the only job the pin has.
//
// The pins are still needed: with a linked runtime carrying its own
// node_modules and no pins, `tsc --noEmit` fails mock-transport_gen.ts with
// TS2322 (measured). Deleting them is not the fix; retargeting them is.

// DeclarationEntry returns the package-relative path of the declaration file
// a package presents to a `moduleResolution: bundler` importer, read from its
// package.json — or "" when the manifest names none.
//
// DERIVED, never hardcoded: the entry moves between releases inside the
// ranges forge declares. @opentelemetry/sdk-trace-base 2.0.0 ships
// build/src/index.d.ts and 2.11.0 only build/src/index-shim.d.ts, so a literal
// path is right for one install and dangles for the next.
//
// Order follows what tsc itself consults under `bundler` resolution with the
// `import` condition: exports["."] first (its `types`, then the `import`
// branch, then `default`), then the top-level `types`/`typings`, then `main`.
// A package whose only declaration is an ESM entry under exports
// (react-query's build/modern/index.d.ts) and a legacy one at `types`
// (build/legacy) resolves to the former — the same file every importer's tsc
// already binds, which is what keeps the pin a no-op for the types it dedupes.
//
// An implementation target (`.js`/`.mjs`/`.cjs`) stands for its sibling
// declaration file, as it does for tsc: @connectrpc/connect declares no
// `types` anywhere and is typed by the dist/esm/index.d.ts beside its
// exports import target.
//
// exists reports whether a package-relative path is a file in the installed
// package. Every candidate is checked, so a manifest naming a file the tarball
// does not ship yields the next candidate rather than a pin that dangles. A
// nil exists trusts the manifest.
func DeclarationEntry(manifest []byte, exists func(rel string) bool) string {
	var doc struct {
		Types   string          `json:"types"`
		Typings string          `json:"typings"`
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
	}
	if err := json.Unmarshal(manifest, &doc); err != nil {
		return ""
	}
	candidates := exportsCandidates(doc.Exports)
	candidates = append(candidates, doc.Types, doc.Typings, doc.Main)
	for _, c := range candidates {
		entry := cleanEntry(declarationFor(c))
		if entry == "" {
			continue
		}
		if exists == nil || exists(entry) {
			return entry
		}
	}
	return ""
}

// exportsCandidates lists the paths for "." in an `exports` field, in the
// order tsc tries them, for any of the shapes npm allows: a bare string, a
// condition map, or a subpath map whose "." is either.
func exportsCandidates(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var asMap map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asMap); err != nil {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return []string{s}
		}
		return nil
	}
	if dot, ok := asMap["."]; ok {
		return conditionCandidates(dot)
	}
	for key := range asMap {
		if strings.HasPrefix(key, ".") {
			return nil // a subpath map with no "." entry exports no root
		}
	}
	return conditionCandidates(raw)
}

// conditionCandidates flattens a condition map into its paths, trying the
// conditions tsc applies under `bundler` + import: `types`, then `import`,
// then `default`. Other conditions (require, node, custom ones) are not the
// branch a browser bundle's tsc takes, so they are not candidates.
func conditionCandidates(raw json.RawMessage) []string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []string{s}
	}
	var cond map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cond); err != nil {
		return nil
	}
	var out []string
	for _, key := range []string{"types", "import", "default"} {
		if v, ok := cond[key]; ok {
			out = append(out, conditionCandidates(v)...)
		}
	}
	return out
}

// declarationFor maps a manifest path to the declaration file tsc would read
// for it: a declaration stays as it is, and an implementation file stands for
// its sibling (.js → .d.ts, .mjs → .d.mts, .cjs → .d.cts).
func declarationFor(p string) string {
	switch {
	case isDeclarationFile(p):
		return p
	case strings.HasSuffix(p, ".mjs"):
		return strings.TrimSuffix(p, ".mjs") + ".d.mts"
	case strings.HasSuffix(p, ".cjs"):
		return strings.TrimSuffix(p, ".cjs") + ".d.cts"
	case strings.HasSuffix(p, ".js"):
		return strings.TrimSuffix(p, ".js") + ".d.ts"
	}
	return ""
}

func isDeclarationFile(p string) bool {
	return strings.HasSuffix(p, ".d.ts") || strings.HasSuffix(p, ".d.mts") || strings.HasSuffix(p, ".d.cts")
}

// cleanEntry normalises a manifest path ("./build/x.d.ts", "build/x.d.ts")
// to the bare package-relative form a pin appends.
func cleanEntry(p string) string {
	p = strings.TrimPrefix(strings.TrimSpace(p), "./")
	// Only a .d.ts may be pinned. A tsconfig `paths` value is also resolved
	// by Next's webpack, whose TypeScript loader takes .ts/.tsx/.d.ts and
	// nothing else: a pin at index.d.mts/.d.cts is read as JavaScript and
	// fails the build ("Module parse failed: Unexpected token", measured on
	// @opentelemetry/api 1.10, which ships only those two). Without an entry
	// the pin names the package directory, which both tsc and webpack
	// resolve through the package's own exports.
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") || !strings.HasSuffix(p, ".d.ts") {
		return ""
	}
	return p
}

// TypePinPath returns the single `paths` value for one pinned package, for a
// frontend whose dependencies are hoisted to the project root when hoisted is
// true and installed locally otherwise.
//
// entry is the package-relative declaration file (DeclarationEntry of the
// installed manifest). An empty entry yields the bare package DIRECTORY, which
// is what a template must emit before any install exists to read — and is
// precisely the value the scaffold-time reconcile replaces once `npm install`
// has run (see generator.ReconcileFrontendTsconfigPeers). A directory pin is
// correct for tsc and wrong for webpack; see the note above TypePinTarget's
// neighbour DeclarationEntry.
//
// Exactly one element, never two — see the layout constants above for why a
// candidate list panics `next build`. Every emitter goes through this so the
// layout decision lands in one place rather than in each template's inline
// string.
func TypePinPath(name string, hoisted bool, entry string) string {
	prefix := localPinPrefix
	if hoisted {
		prefix = hoistedPinPrefix
	}
	dir := prefix + TypePinTarget(name)
	if entry == "" {
		return dir
	}
	return dir + "/" + entry
}

// SplitPinPath takes an existing pin value apart into its layout and
// declaration entry, so a reconcile can change one without disturbing the
// other. ok is false for a value forge did not write (another prefix, another
// package).
//
// The implementation directory of a package typed under @types/ (react's
// "./node_modules/react", which an older forge emitted and which fails every
// .tsx with TS7016) is recognised too, with an empty entry: it is forge's own
// stale value, and naming it lets the reconcile heal it to the @types/ target.
func SplitPinPath(name, value string) (hoisted bool, entry string, ok bool) {
	for _, candidate := range []struct {
		prefix  string
		hoisted bool
	}{{localPinPrefix, false}, {hoistedPinPrefix, true}} {
		dir := candidate.prefix + TypePinTarget(name)
		switch {
		case value == dir:
			return candidate.hoisted, "", true
		case strings.HasPrefix(value, dir+"/"):
			return candidate.hoisted, strings.TrimPrefix(value, dir+"/"), true
		case value == candidate.prefix+name:
			return candidate.hoisted, "", true
		}
	}
	return false, "", false
}

// PinPackageDir returns where a pin's PACKAGE lives relative to a frontend
// directory, for the given layout — the directory whose package.json
// DeclarationEntry reads.
func PinPackageDir(name string, hoisted bool) string {
	return TypePinPath(name, hoisted, "")
}

// PeerSpec returns the version specifier the runtime declares for one peer, and
// whether it declares that peer at all. A scaffold installs the peers it needs
// at exactly this specifier, so the pin lives in one place: web-runtime's own
// package.json.
func PeerSpec(name string) (string, bool) {
	var doc struct {
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(peersJSON, &doc); err != nil {
		return "", false
	}
	spec, ok := doc.PeerDependencies[name]
	return spec, ok && spec != ""
}

// decodePeers returns the runtime's declared peer dependency names.
func decodePeers() []string {
	var doc struct {
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(peersJSON, &doc); err != nil {
		return nil
	}
	out := make([]string, 0, len(doc.PeerDependencies))
	for name := range doc.PeerDependencies {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// merged returns peers plus extra, deduplicated and sorted, so callers get a
// stable order and a generated file does not churn between runs.
func merged(extra ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(names []string) {
		for _, n := range names {
			if n == "" || seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
		}
	}
	add(decodePeers())
	for _, e := range extra {
		add(e)
	}
	sort.Strings(out)
	return out
}

// TypePins is the set for TypeScript `paths` pinning: every runtime peer plus
// the transitive type-identity packages. Two copies of any of these makes tsc
// report two nominally-distinct versions of the same type — the TS2322
// "Type Transport is not assignable to type Transport" failure.
func TypePins() []string { return merged(extraTypeOnlyPins) }

// BundlerDedupe is the set for a bundler's dedupe/alias list: every runtime
// peer plus the runtime-only pairings. Two copies here ship the library twice
// and split its module-level state, which builds and typechecks green.
func BundlerDedupe() []string { return merged(extraBundlerDedupe) }
