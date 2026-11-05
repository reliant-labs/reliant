package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/webruntimepeers"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// The tsconfig `paths` peer-pin reconcile, for projects that predate it.
//
// WHY THIS EXISTS SEPARATELY FROM THE TEMPLATE. Both frontend tsconfig
// templates already pin the runtime's peerDependencies to the app's own copy
// (see the "peer dedupe note" in internal/templates/frontend/*/tsconfig.json.tmpl,
// pinned by TestFrontendTsconfigDedupesRuntimePeers). But tsconfig.json is
// scaffold-once — written at birth and never rewritten — so a project
// generated before that template change never receives it, and no amount of
// regenerating will hand it over.
//
// The hazard is forge's own: a dev build bridges @reliantlabs/forge-web-runtime
// with a `file:` specifier, which npm materialises as a SYMLINK into the forge
// checkout. That checkout carries its own node_modules, and npm cannot hoist a
// linked package's dependencies away, so the runtime's bare imports resolve
// THERE while the app's resolve here. Two physically distinct installs of the
// same version are two nominally distinct types to TypeScript:
//
//	src/lib/mock-transport_gen.ts(155,3): error TS2322: Type
//	'…/forge/web-runtime/node_modules/@connectrpc/connect/…'.Transport is not
//	assignable to type '…/frontends/internal-console/node_modules/…'.Transport
//
// The bundler configs (vite's resolve.dedupe, next.config.ts's resolver rule)
// already force one copy, and vitest.config.ts is reconciled by
// generate_frontend_dedupe.go — but none of those run under `tsc --noEmit`,
// which is what `forge lint` invokes. The failure therefore lands in a file
// forge GENERATED (mock-transport_gen.ts), caused by a dependency edge forge
// INTRODUCED, in a project that did nothing wrong. That is the same
// justification that lets reconcileFrontendDedupe touch a user-owned file, and
// this pass is held to the same limits: it adds only the missing peer keys to
// the existing paths block, changes nothing else, and does nothing at all when
// they are already present.
//
// A registry (non-dev) install hoists one copy and never had the problem, so
// these entries are inert there. They are written unconditionally anyway: the
// same project is bridged and unbridged at different times depending on who
// built the forge binary, and a config that only typechecks under one of them
// is worse than one that works under both.

// tsconfigPeerPins are the packages that must resolve to exactly one copy —
// the app's.
//
// DERIVED, not hand-written. This used to be a literal list "kept in step
// with" three other literal lists, and it had already drifted: it was missing
// nine real peers (every @opentelemetry/* except api, plus react). See
// internal/webruntimepeers for the source of truth.
func tsconfigPeerPins() []string { return webruntimepeers.TypePins() }

// pathsOpenRe matches the opening of the `"paths": {` object inside
// compilerOptions — the insertion point. Anchored on the quoted key so it
// cannot match a "paths" string appearing in a note or a value.
var pathsOpenRe = regexp.MustCompile(`(?m)^(\s*)"paths"\s*:\s*\{`)

// reconcileFrontendTsconfigPeers adds the missing peer pins to each web
// frontend's tsconfig.json.
//
// Best-effort and non-fatal throughout: a frontend with no tsconfig, an
// unreadable file, or one whose `paths` block this cannot find is skipped
// rather than failed. Idempotent — a config that already carries every pin is
// left byte-identical, which is what keeps `forge generate` twice in a row
// reporting no changes.
func reconcileFrontendTsconfigPeers(cfg *config.ProjectConfig, projectDir string) {
	if cfg == nil {
		return
	}

	var touched []string
	for _, fe := range cfg.Frontends {
		// Only the browser frontends link the web runtime and emit
		// mock-transport_gen.ts, which is where the two copies meet.
		if !isWebFrontendType(fe.Type) {
			continue
		}
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			// No directory in this repository — a cross-repo
			// source pin, or a path outside the project root.
			continue
		}
		rel := filepath.Join(feDir, "tsconfig.json")
		// Which node_modules this frontend's deps resolve from is a property
		// of the project, not of forge.yaml, and it is read per frontend
		// because a project can mix layouts. It may also be UNKNOWABLE here —
		// see generator.DetectFrontendPinLayout for why a fresh clone cannot
		// tell a bridged project from a standalone one, and why guessing is
		// what turned Verify Generated Code red on an untouched file.
		layout := generator.DetectFrontendPinLayout(projectDir, filepath.Join(projectDir, feDir))
		if addPeerPinsToTsconfig(filepath.Join(projectDir, rel), layout) {
			touched = append(touched, filepath.ToSlash(rel))
		}
	}

	if len(touched) > 0 {
		fmt.Printf("  ♻️  reconciled web-runtime peer pins in %d tsconfig(s) — one copy of each peer's types under tsc, none of its code in the bundle\n", len(touched))
		for _, rel := range touched {
			fmt.Printf("      - %s\n", rel)
		}
	}
}

// addPeerPinsToTsconfig reconciles one tsconfig's `paths` peer pins: it adds
// any that are missing, RETARGETS any that name the wrong node_modules for
// this project's layout, and HEALS a directory pin that resolves into the
// declaration-file pin it should always have been. Returns true when the file
// changed.
//
// Retargeting matters as much as adding. tsconfig.json is scaffold-once, so a
// project whose layout changed — most commonly because a dev forge build wrote
// the workspace bridge and npm hoisted — keeps pins aimed at a directory that
// no longer exists. tsc does not report a pin that resolves to nothing; it
// silently falls back to the ordinary walk and finds the linked runtime's own
// copy, which is the TS2322 this pass exists to prevent.
func addPeerPinsToTsconfig(path string, layout generator.PinLayout) bool {
	// First pass: rewrite present pins. Done before the insert so `missing`
	// below sees the final shape.
	//
	// The LAYOUT is changed only when it is known. A committed pin is itself
	// a reviewed declaration of this project's layout, and it is better
	// evidence than anything forge can infer from a tree that has not been
	// installed — so when forge cannot identify the layout it defers to the
	// file rather than rewriting it to a guess.
	//
	// The ENTRY is kept once a pin has one: it is a committed statement of
	// which declaration file this project's tsc binds, and generate never
	// second-guesses it from node_modules (see healDirectoryPin for the one
	// exception, and why it cannot flip-flop).
	feDir := filepath.Dir(path)
	retargeted := generator.ReconcileTsconfigPins(path, func(pkg string, hoisted bool, entry string) (bool, string) {
		if layout.Known {
			hoisted = layout.Hoisted
		}
		if entry == "" {
			entry = healDirectoryPin(feDir, pkg, hoisted)
		}
		return hoisted, entry
	})

	body, err := os.ReadFile(path)
	if err != nil {
		return false // no tsconfig here — nothing to reconcile
	}

	missing := make([]string, 0, len(tsconfigPeerPins()))
	for _, pkg := range tsconfigPeerPins() {
		if !pathsKeyRe(pkg).Match(body) {
			missing = append(missing, pkg)
		}
	}
	if len(missing) == 0 {
		return retargeted // any rewrite above has already been written
	}

	loc := pathsOpenRe.FindSubmatchIndex(body)
	if loc == nil {
		// A tsconfig with no `paths` block, or one restructured past
		// recognition. Forge does not invent a compilerOptions key in a file
		// it does not own — a `paths` block also requires `baseUrl` under
		// some configurations, and guessing there breaks more than it fixes.
		return false
	}
	// loc[2]:loc[3] is the indentation captured before `"paths"`; the new
	// entries sit one level deeper, matching the block they are joining.
	indent := string(body[loc[2]:loc[3]])
	insertAt := loc[1] // just past the `{`

	// Each inserted line is prefixed with "\n" rather than suffixed, so the
	// bytes already at insertAt keep the newline that followed the `{` and the
	// block's existing first entry stays where it was.
	var out bytes.Buffer
	out.Write(body[:insertAt])
	out.WriteString("\n" + indent + "  // Added by forge: a dev forge build links @reliantlabs/forge-web-runtime")
	out.WriteString("\n" + indent + "  // by path, so its peerDependencies would otherwise resolve from the")
	out.WriteString("\n" + indent + "  // link target's node_modules. tsc sees two distinct copies of the same")
	out.WriteString("\n" + indent + "  // type and fails mock-transport_gen.ts with TS2322. Pin them here.")
	for _, pkg := range missing {
		// The key is the import specifier; the VALUE is where its typings
		// live, which differs for packages typed by a separate @types/
		// package. Emitting the implementation dir for react pins tsc to a
		// directory with no .d.ts and fails every .tsx with TS7016.
		//
		// Exactly ONE element: SWC panics `next build` on a multi-element
		// value for a non-wildcard key, so the pin names the one layout this
		// project has rather than listing both. See
		// webruntimepeers.TypePinPath.
		//
		// And a DECLARATION FILE when one can be read, for the reason in
		// healDirectoryPin: a directory pin that resolves splits the bundle.
		hoisted := layout.Hoisted || !layout.Known
		entry := healDirectoryPin(feDir, pkg, hoisted)
		fmt.Fprintf(&out, "\n%s  %q: [%q],", indent, pkg, webruntimepeers.TypePinPath(pkg, hoisted, entry))
	}
	out.Write(body[insertAt:])

	return writeTsconfig(path, out.Bytes())
}

// healDirectoryPin returns the declaration entry a DIRECTORY pin for pkg at
// this layout must be upgraded to, or "" to leave it a directory pin.
//
// A directory pin is correct for tsc and wrong for the bundle. Next's webpack
// resolver applies tsconfig `paths` to app code, and a pin naming a package
// directory is a directory request that skips `exports` — so whenever the pin
// RESOLVES, app code and @reliantlabs/forge-web-runtime bind different files
// of the same package (react-query's build/legacy vs build/modern), and
// prerender fails with "No QueryClient set" because two module instances are
// two React contexts. A pin naming a `.d.ts` is skipped by that resolver and
// ignored by Vite, so the bundle resolves through `exports` while tsc still
// binds one copy of the types. See webruntimepeers.DeclarationEntry.
//
// This reads node_modules, which `forge generate` otherwise never does (see
// generator.DetectFrontendPinLayout), and it is safe for one reason: it is a
// ONE-WAY heal. It fires only on a directory pin that resolves — the only
// state in which the pin is harmful — and what it writes is a pin it will
// never touch again (an entry pin is kept verbatim above, installed or not).
// So a fresh clone leaves an inert directory pin alone, an installed tree
// fixes it once, and from then on every environment generates the same bytes.
// The flip-flop #248 removed was two states each rewriting the other; this
// has one absorbing state.
func healDirectoryPin(feDir, pkg string, hoisted bool) string {
	return generator.InstalledDeclarationEntry(feDir, pkg, hoisted)
}

// writeTsconfig writes body back to path, preserving the file's own mode.
func writeTsconfig(path string, body []byte) bool {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	checksums.RecordPreWriteAbs(path) // journaled: a failed run restores it
	return atomicfile.WriteFile(path, body, mode) == nil
}

// pathsKeyRe matches an existing mapping for pkg anywhere in the file — the
// signal that this config already pins it (either from a current scaffold or
// from a previous run of this pass).
func pathsKeyRe(pkg string) *regexp.Regexp {
	return regexp.MustCompile(`"` + regexp.QuoteMeta(pkg) + `"\s*:\s*\[`)
}
