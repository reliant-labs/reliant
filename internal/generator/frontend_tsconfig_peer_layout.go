package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/webruntimepeers"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// PinLayout names WHICH node_modules a frontend's tsconfig peer pins resolve
// against.
//
// A `paths` value for a non-wildcard key must hold EXACTLY ONE element — SWC
// asserts that rule and panics `next build` on anything longer (measured; see
// webruntimepeers). So forge cannot list both layouts and let the toolchain
// choose: it has to name the one this project actually has, which makes this a
// decision procedure rather than a lookup.
//
// # The two layouts, and what each pin does in each
//
// Measured end-to-end against control-plane's internal-console, both `tsc
// --noEmit` and `next build`:
//
//	layout                     "./node_modules/…"   "../../node_modules/…"
//	standalone (npm ci here)   green                green (pin dangles, inert)
//	workspace (hoisted root)   TS2322               green
//
// The asymmetry is the whole design. A pin that names a directory holding the
// package is right; a pin that names a directory that does not exist is not an
// error tsc reports — it silently no-ops and resolution falls back to the
// ordinary upward walk. In a standalone project that walk finds the one real
// copy and nothing is harmed. In a BRIDGED project it finds the linked
// runtime's own private copy first, and two physically distinct installs are
// two distinct types:
//
//	src/lib/mock-transport_gen.ts(158,5): error TS2322: Type
//	'…/forge/web-runtime/node_modules/@connectrpc/connect/…'.Transport is not
//	assignable to type '…/<project>/node_modules/@connectrpc/connect/…'.Transport
//
// So "hoisted" is the answer that is merely redundant when wrong, and "local"
// is the answer that breaks a build when wrong.
//
// # Why this returns a KNOWN flag instead of just a bool
//
// tsconfig.json is generated AND COMMITTED, and consumers re-run `forge
// generate` in CI and fail the build on any diff. A decision that reads
// untracked state therefore generates one file on a developer's machine and a
// different one on a runner, from the same commit.
//
// That is not hypothetical, and forge shipped it: control-plane's root
// package.json IS forge's own dev web-runtime bridge (frontend_webruntime_devlink.go)
// and is GITIGNORED by construction, while node_modules is a build artifact
// absent from a fresh checkout. A maintainer's tree has both, so the probe
// answered "hoisted" and the committed pins say "../../node_modules/…". A CI
// checkout has NEITHER, so the same probe answered "local" and rewrote all 14
// pins to "./node_modules/…" — turning Verify Generated Code red on a file
// nobody had touched, on a clean clone of main.
//
// Reading only tracked files does not rescue it either, and this is the part
// worth stating plainly: from tracked files alone a bridged project is
// INDISTINGUISHABLE from a standalone one. control-plane's tracked evidence —
// a per-frontend package.json naming the registry range "^0.3.1", a
// per-frontend package-lock.json, no root manifest — is exactly what a plain
// standalone project looks like. The signal that says otherwise is ignored on
// purpose, so no amount of preferring tracked inputs can recover it.
//
// The resolution is to stop forcing an answer. When the evidence identifies a
// layout, forge retargets and heals a stale pin. When it does not, forge
// leaves the committed value alone: that value is itself a tracked, reviewed
// declaration of the layout, and respecting it makes generate a fixed point in
// every environment. Silence is a third state, not a vote for the default.
type PinLayout struct {
	// Hoisted is meaningful only when Known; false otherwise.
	Hoisted bool
	// Known reports whether the evidence identified a layout at all.
	Known bool
}

// tsconfigPinEntryRe matches a single-element `paths` mapping for pkg,
// capturing the key-and-bracket prefix, the bare path value, and the closing
// bracket, so a rewrite can replace the value and put the rest back verbatim.
func tsconfigPinEntryRe(pkg string) *regexp.Regexp {
	return regexp.MustCompile(`("` + regexp.QuoteMeta(pkg) + `"\s*:\s*\[\s*)"([^"]*)"(\s*\])`)
}

// ReconcileFrontendTsconfigPeers retargets every frontend's tsconfig peer pins
// to the node_modules layout the install forge just ran produced, and aims
// each at its package's installed declaration entry. It is the SCAFFOLD-time
// pass (`forge project new --frontend` and `forge scaffold frontend`, right
// after their npm install) and reads node_modules on purpose — see
// ObserveInstalledPinLayout. `forge generate` has its own pass that reads
// declarations only. Best-effort and non-fatal: a missing or unrecognised
// tsconfig is skipped rather than failed, a project whose layout cannot be
// identified is left untouched, and a file already correct is left
// byte-identical so a re-run reports nothing.
func ReconcileFrontendTsconfigPeers(projectDir string) {
	entries, err := os.ReadDir(filepath.Join(projectDir, "frontends"))
	if err != nil {
		return
	}
	var touched []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		feDir := filepath.Join(projectDir, "frontends", entry.Name())
		// The install forge just ran is the evidence here: this is the
		// scaffold-time pass, on a frontend being created.
		layout := ObserveInstalledPinLayout(projectDir, feDir)
		if !layout.Known {
			// Nothing here identifies a layout. The committed pins are the
			// only statement of one that exists, so they stand.
			continue
		}
		if retargetTsconfigPins(filepath.Join(feDir, "tsconfig.json"), layout.Hoisted) {
			touched = append(touched, entry.Name())
		}
	}
	if len(touched) > 0 {
		fmt.Printf("  ♻️  aimed web-runtime peer pins for %s at the installed layout and declaration files\n",
			strings.Join(touched, ", "))
	}
}

// pinLayoutProbe is the package the layout decision is made against. Any
// pinned peer would do; this one is a required (non-optional) peer of the
// runtime, so it is present in every real install.
const pinLayoutProbe = "@connectrpc/connect"

// DetectFrontendPinLayout reports which node_modules feDir's PEER dependencies
// resolve from, as far as the project's DECLARED files say, and whether they
// say anything at all.
//
// It reads no node_modules. That is the whole contract, and it is what keeps
// `forge generate` from rewriting a committed tsconfig.json according to who
// last ran `npm install`. It used to probe for an installed copy — a nested
// frontends/<name>/node_modules/@connectrpc/connect first, then a hoisted one
// — and a nested install is exactly what `npm ci` inside a frontend creates.
// control-plane commits its pins at the project root (its layout is forge's
// dev bridge); a developer who had run `npm ci` in internal-console got all 14
// pins rewritten to "./node_modules/…" by the next `forge generate`, and the
// next bridged generate flipped them back. A committed file must not follow an
// install.
//
// The signals are declarations, strongest-first, and each is a POSITIVE
// identification. Running out of signals yields Known=false, which callers
// must treat as "leave the committed pins alone", never as a default.
//
// What is installed is still the right evidence at ONE moment: the instant
// forge itself has just installed a frontend it is creating, before anything
// is committed. ObserveInstalledPinLayout serves that, and only that.
func DetectFrontendPinLayout(projectDir, feDir string) PinLayout {
	// An npm workspace root covering frontends/* — forge's own dev bridge
	// writes one, and a user may have their own. Either way npm hoists.
	if rootWorkspaceCovers(projectDir, feDir) {
		return PinLayout{Hoisted: true, Known: true}
	}
	// The frontend's own manifest resolving the runtime through a parent
	// workspace says the same thing from the other side.
	if frontendDeclaresWorkspaceMember(feDir) {
		return PinLayout{Hoisted: true, Known: true}
	}
	// Nothing declared — most commonly a fresh clone on a CI runner, or a
	// standalone frontend. The committed pins are the declaration.
	return PinLayout{}
}

// ObserveInstalledPinLayout is DetectFrontendPinLayout plus what an install
// actually produced. It is for the moment forge has just run `npm install`
// on a frontend it is CREATING (`forge scaffold frontend`): the tsconfig was
// written before the install decided the layout, nothing about it is
// committed yet, and the tree on disk is the best evidence there is.
//
// It must not be used by `forge generate`, which re-runs on every machine and
// on committed files — see DetectFrontendPinLayout for what that caused.
func ObserveInstalledPinLayout(projectDir, feDir string) PinLayout {
	probe := filepath.FromSlash(pinLayoutProbe)

	// A genuine nested copy of the PACKAGE outranks everything, including a
	// workspace declaration: node resolution prefers the nearest node_modules,
	// so whatever the root says, this is the copy that wins at runtime and the
	// pin must name it.
	//
	// Probed for the package rather than for a node_modules DIRECTORY. npm
	// hoists what it can and leaves behind only what it cannot, so a workspace
	// frontend routinely holds vite and esbuild locally while the peers live
	// at the root; "the directory exists" is true there and "the peers are
	// here" is false, and pinning at a directory that lacks the package is the
	// dangling pin that resolves to nothing.
	if isDir(filepath.Join(feDir, "node_modules", probe)) {
		return PinLayout{Hoisted: false, Known: true}
	}
	if layout := DetectFrontendPinLayout(projectDir, feDir); layout.Known {
		return layout
	}
	if isDir(filepath.Join(projectDir, "node_modules", probe)) {
		return PinLayout{Hoisted: true, Known: true}
	}
	return PinLayout{}
}

// rootWorkspaceCovers reports whether the project root's package.json is an
// npm workspace root that adopts THIS frontend.
//
// Membership is per frontend, not per project: forge's dev bridge enumerates
// its members and deliberately leaves React Native apps standalone, so a root
// that covers frontends/web says nothing about frontends/mobile.
func rootWorkspaceCovers(projectDir, feDir string) bool {
	body, err := os.ReadFile(filepath.Join(projectDir, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Workspaces []string `json:"workspaces"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		// The object form ({"packages": [...]}) fails this decode. Fall
		// through rather than guess at a shape forge did not write.
		return false
	}
	rel, err := filepath.Rel(projectDir, feDir)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, pattern := range manifest.Workspaces {
		if matched, _ := path.Match(strings.TrimSuffix(strings.TrimPrefix(pattern, "./"), "/"), rel); matched {
			return true
		}
	}
	return false
}

// frontendDeclaresWorkspaceMember reports whether the frontend's own
// package.json shows it resolving the web runtime through a parent workspace,
// by carrying it as a `workspace:`-protocol or `file:`-linked dependency.
//
// Both spellings mean "supplied by something above me", which is the hoisted
// layout. A plain semver range means the opposite and is deliberately not
// matched: that is what control-plane's tracked manifest carries while its
// workspace root sits gitignored beside it, and reading it as evidence either
// way is what made the answer differ between a developer's tree and CI.
func frontendDeclaresWorkspaceMember(feDir string) bool {
	body, err := os.ReadFile(filepath.Join(feDir, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return false
	}
	for _, deps := range []map[string]string{manifest.Dependencies, manifest.DevDependencies} {
		constraint, ok := deps[WebRuntimePackage]
		if !ok {
			continue
		}
		if strings.HasPrefix(constraint, "workspace:") || strings.HasPrefix(constraint, "file:") {
			return true
		}
	}
	return false
}

// retargetTsconfigPins rewrites each present peer pin in path to the layout
// hoisted selects, and aims it at the package's DECLARATION ENTRY as read from
// the install forge just ran. Reports whether the file changed.
//
// The entry is read here — at scaffold time, right after forge's own
// `npm install` — because this is the one moment the installed manifest is
// the authority on it: the tsconfig is being created and nothing is committed
// yet. See webruntimepeers.DeclarationEntry for why the entry must be derived
// rather than written as a literal, and ReconcileTsconfigPins for why a
// directory pin is the value this pass exists to replace.
func retargetTsconfigPins(path string, hoisted bool) bool {
	feDir := filepath.Dir(path)
	return ReconcileTsconfigPins(path, func(pkg string, _ bool, currentEntry string) (bool, string) {
		entry := InstalledDeclarationEntry(feDir, pkg, hoisted)
		if entry == "" {
			// Nothing installed to read (an install that failed, or a
			// package npm put somewhere unexpected). Keep what the file
			// already says rather than regressing a known entry to a
			// directory pin.
			entry = currentEntry
		}
		return hoisted, entry
	})
}

// InstalledDeclarationEntry reads the declaration entry of pkg's TYPES package
// as installed under feDir's node_modules for the given layout, or "" when no
// manifest is there to read.
func InstalledDeclarationEntry(feDir, pkg string, hoisted bool) string {
	pkgDir := filepath.Join(feDir, filepath.FromSlash(webruntimepeers.PinPackageDir(pkg, hoisted)))
	manifest, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		return ""
	}
	// Only a file that is really in the installed package may be pinned: a
	// pin naming a missing file resolves to nothing, and tsc falls back to
	// the ordinary walk without a word.
	return webruntimepeers.DeclarationEntry(manifest, func(rel string) bool {
		info, statErr := os.Stat(filepath.Join(pkgDir, filepath.FromSlash(rel)))
		return statErr == nil && !info.IsDir()
	})
}

// PinDecision is what a reconcile wants one existing pin to become, given the
// layout and declaration entry the pin currently carries.
type PinDecision func(pkg string, hoisted bool, entry string) (wantHoisted bool, wantEntry string)

// ReconcileTsconfigPins rewrites every present, forge-shaped peer pin in path
// to whatever decide returns for it, leaving every other byte of the file
// alone. Reports whether the file changed.
//
// A pin forge does not recognise (another prefix, several candidates, a
// multi-line value) is left exactly as written: it is a shape a human chose.
func ReconcileTsconfigPins(path string, decide PinDecision) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	changed := false
	for _, pkg := range webruntimepeers.TypePins() {
		re := tsconfigPinEntryRe(pkg)
		loc := re.FindSubmatchIndex(body)
		// len < 6 means the value group did not participate, so loc[4:6] is
		// not addressable — the pin is absent or in a shape this does not own.
		if len(loc) < 6 {
			continue
		}
		current := string(body[loc[4]:loc[5]])
		hoisted, entry, ok := webruntimepeers.SplitPinPath(pkg, current)
		if !ok {
			continue
		}
		wantHoisted, wantEntry := decide(pkg, hoisted, entry)
		want := webruntimepeers.TypePinPath(pkg, wantHoisted, wantEntry)
		if current == want {
			continue
		}
		body = re.ReplaceAll(body, []byte(`${1}"`+want+`"${3}`))
		changed = true
	}
	if !changed {
		return false
	}
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
	}
	return atomicfile.WriteFile(path, body, mode) == nil
}

// isDir reports whether path exists and is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
