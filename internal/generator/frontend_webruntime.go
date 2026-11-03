// Package generator — @reliantlabs/forge-web-runtime dependency resolution for
// generated frontends.
//
// Generated frontends import @reliantlabs/forge-web-runtime (the web twin of
// forge/pkg): connect.ts builds its interceptor stack from it, providers.tsx
// mounts RuntimeShell, the CRUD list pages render <Resource>. It is therefore
// a REAL dependency of shipped application code and belongs in
// "dependencies", never "devDependencies" — a production install
// (`npm ci --omit=dev`, the Next.js standalone Docker image) has to resolve
// it or `next build` fails.
//
// Which specifier to write is the whole job here, and it is the npm mirror of
// what project_pkgdep.go decides for forge/pkg:
//
//   - RELEASE flow: an ordinary semver range (webRuntimePublishedRange).
//     A released forge scaffolds a project that resolves the package from the
//     registry like any other dependency. A release build never emits a
//     `file:` specifier — the path would be meaningless on the user's disk.
//
//   - DEV flow: `file:<path-to-the-running-forge's-web-runtime>`. npm's
//     file: protocol symlinks the directory into node_modules, so a dev
//     forge's frontend resolves the package straight out of the checkout —
//     edits to web-runtime/src land in the project with nothing published,
//     nothing pushed and no reinstall. Because the dependency is DECLARED,
//     `npm install` maintains the link instead of pruning it, which a bare
//     symlink into node_modules could never survive.
//
// The dev path is written RELATIVE ("../../../../forge/web-runtime") or
// home-anchored ("~/src/forge/web-runtime") — never absolute. An absolute
// path carries the maintainer's home directory and username into a committed
// file, and npm resolves both of the other forms natively. When neither form
// can be expressed without embedding one, forge writes no dev specifier at
// all and says why.
package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/frontenddeps"
	"github.com/reliant-labs/forge/internal/webruntimepeers"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// WebRuntimePackage is the npm package name of forge's frontend runtime
// library.
const WebRuntimePackage = "@reliantlabs/forge-web-runtime"

// webRuntimePublishedRange is the semver range a scaffold declares when it is
// not bridged to a local checkout. Keep the major.minor pointed at
// web-runtime/package.json's version — TestWebRuntimePublishedRangeTracksPackage
// fails the build when the two drift apart.
const webRuntimePublishedRange = "^0.12.0"

// webRuntimeDepRe matches the package's entry wherever it already appears in a
// package.json, capturing everything up to (but not including) the value so a
// rewrite preserves the surrounding formatting byte for byte. The package name
// is unique in the file, so a single match is the entry.
var webRuntimeDepRe = regexp.MustCompile(`"` + regexp.QuoteMeta(WebRuntimePackage) + `"\s*:\s*("[^"]*")`)

// dependenciesOpenRe matches the opening brace of the top-level
// "dependencies" object, the insertion point for a frontend that predates the
// declared dependency.
var dependenciesOpenRe = regexp.MustCompile(`"dependencies"\s*:\s*\{`)

// webRuntimeDecision is what forge wants a frontend's package.json to say
// about the runtime package.
type webRuntimeDecision struct {
	// spec is the desired specifier, e.g. "^0.1.0" or "file:../../forge/web-runtime".
	spec string
	// authoritative reports whether forge should overwrite a DIFFERENT value
	// that is already there. False means "add the entry if it is missing, but
	// leave an existing one alone": a dev build that cannot locate its own
	// source tree has no business replacing a bridge somebody else set up.
	authoritative bool
}

// decideWebRuntimeDependency resolves the specifier for the frontend rooted at
// feAbs (an absolute path to frontends/<name>).
//
// The answer is the published range on EVERY build flavour, dev included.
// frontends/<name>/package.json is a tracked file, and the dev bridge moved
// out of it into a gitignored npm workspace root — see
// frontend_webruntime_devlink.go for the mechanism and why npm needs the link
// to come from a workspace member rather than a bare symlink.
//
// It stays authoritative so a dev build NORMALISES a manifest that an older
// forge already rewrote to `file:<path>`: those checkouts exist, and leaving
// the local path in place would keep them dirty forever.
func decideWebRuntimeDependency(_ string) webRuntimeDecision {
	return webRuntimeDecision{spec: webRuntimePublishedRange, authoritative: true}
}

// webRuntimePublishedRange is a MINIMUM, not an exact pin, and reconcileSpec
// is what makes that true of the file on disk.
//
// THE DEFECT THIS FIXES. Every generate rewrote the specifier to forge's own
// constant, unconditionally. So a project that had deliberately moved AHEAD of
// forge — the motivating case is a security bump published between two forge
// releases — was silently DOWNGRADED on the next `forge generate`, back to a
// range that resolves the vulnerable version. Nothing reported it: the rewrite
// is silent by design (it is idempotent bookkeeping), so the only evidence was
// a package.json diff nobody was looking for and a lockfile that quietly moved
// back.
//
// That is forge overriding a decision that is not forge's to make. forge's
// constant states the OLDEST web-runtime this generator's output is known to
// work against; it says nothing about newer ones, which are compatible by
// semver construction within the same major. A project pinning higher has more
// information than forge does — it knows about a release forge predates.
//
// So the rule is a floor, applied in the one direction that is forge's
// business:
//
//   - current is semver-GREATER THAN OR EQUAL to forge's minimum → keep the
//     project's specifier. It already satisfies the floor.
//   - current is BELOW the floor → raise it. This is the case forge exists to
//     fix: a project regenerated by a newer forge whose output needs a newer
//     runtime.
//   - current is not a comparable version range (a `file:` bridge, a git or
//     npm-alias specifier, anything unparseable) → forge's answer wins, which
//     preserves the stale-bridge normalisation above. "Not comparable" must
//     not be read as "higher"; an unparseable specifier has made no claim.
//
// Comparison is on the MINIMUM version each range admits, because that is the
// only thing a floor can be compared against. `^0.3.2` and `~0.3.2` and
// `>=0.3.2` all admit 0.3.2 as their lowest, so all three clear a 0.3.2 floor;
// `^0.3.1` does not.
func reconcileSpec(current string, decision webRuntimeDecision) string {
	if !decision.authoritative {
		return current
	}
	if specSatisfiesFloor(current, decision.spec) {
		return current
	}
	return decision.spec
}

// rangeOperator strips a leading npm range operator so the version underneath
// can be compared. Only the operators that express a MINIMUM are accepted:
// `^`, `~`, `>=` and a bare version. An exact `=x.y.z` also states a minimum
// of x.y.z for this purpose.
//
// `<` and `<=` are deliberately absent: they express a CEILING, so the lowest
// version they admit is 0 and they can never clear a floor. Returning false
// for them makes forge's answer win, which is correct — a project that has
// capped the runtime below forge's minimum is in a state forge should repair,
// not preserve.
var rangeOperator = regexp.MustCompile(`^\s*(\^|~|>=|=|v)?\s*(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)\s*$`)

// minimumVersion returns the lowest version a specifier admits, as a
// semver-comparable "vX.Y.Z", and whether the specifier is comparable at all.
//
// Anything with no parse — a `file:` path, a git URL, an npm alias, a compound
// range like ">=0.3.2 <0.4.0", "*", "latest" — returns false. A compound range
// is genuinely ambiguous to a regex and the safe reading is "forge decides":
// treating an unparsed string as satisfying the floor would reintroduce the
// silent-downgrade bug in the opposite direction, by letting a meaningless
// specifier survive.
func minimumVersion(spec string) (string, bool) {
	m := rangeOperator.FindStringSubmatch(spec)
	if m == nil {
		return "", false
	}
	v := "v" + m[2]
	if !semver.IsValid(v) {
		return "", false
	}
	return v, true
}

// specSatisfiesFloor reports whether current already admits a version at or
// above floor's minimum.
//
// A DIFFERENT MAJOR is not compared, it is refused. npm majors are not
// compatible with one another, so a project sitting on `^1.0.0` against a
// forge floor of `^0.3.2` has not "exceeded" the floor in any sense that makes
// forge's generated code work — and 0.x majors are stricter still, since under
// npm's caret semantics `^0.3.x` and `^0.4.x` are themselves incompatible
// ranges. Forge's answer wins there, so the mismatch surfaces as a specifier
// the project can see and argue with rather than as a silent acceptance.
func specSatisfiesFloor(current, floor string) bool {
	cur, ok := minimumVersion(current)
	if !ok {
		return false
	}
	min, ok := minimumVersion(floor)
	if !ok {
		return false
	}
	if semver.Major(cur) != semver.Major(min) {
		return false
	}
	// Within 0.x, npm treats the MINOR as the breaking-change axis, so
	// `^0.4.0` is not a compatible superset of `^0.3.2` — it is a different
	// incompatible line, exactly as v1 vs v2 would be.
	if semver.Major(cur) == "v0" && semver.MajorMinor(cur) != semver.MajorMinor(min) {
		return false
	}
	return semver.Compare(cur, min) >= 0
}

// resolvePath makes p absolute and resolves symlinks, best-effort: the
// comparisons in webRuntimeFileSpec only mean anything on canonical paths
// (macOS's /tmp is a symlink to /private/tmp, so an unresolved temp project
// would compute a relative path that npm then resolves differently).
func resolvePath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	return abs
}

// EnsureWebRuntimeDependency reconciles the @reliantlabs/forge-web-runtime entry in
// frontends/<name>/package.json so that npm — not forge — owns the link under
// node_modules.
//
// It runs at scaffold time (before the first `npm install`, so the very first
// install resolves the bridge) and again on every `forge generate`, which is
// what keeps it honest when a project is moved on disk or picked up by a
// differently-built forge. The edit is surgical: the entry's VALUE is replaced
// in place, so a hand-formatted, hand-extended package.json survives
// untouched, and a run that changes nothing writes nothing.
//
// Every failure is a warning. A frontend whose package.json forge cannot parse
// is the user's file to fix, and no dependency bookkeeping justifies failing
// their build.
//
// It reports whether it rewrote the manifest.
func EnsureWebRuntimeDependency(projectDir, feRelDir, feName string) bool {
	return ensureWebRuntimeDependency(projectDir, feRelDir, "frontend "+feName)
}

// EnsureWorkspaceHooksWebRuntimeDependency reconciles the same entry in
// packages/hooks/package.json — the workspace-mode home of the generated
// hooks. Those hooks type every error as ConnectClientError, imported from the
// runtime package, and a type-only import still has to RESOLVE: under pnpm's
// strict node_modules an undeclared package is simply not there. Declaring it
// is what makes the workspace layout typecheck.
//
// No-op when the manifest does not exist, so a non-workspace project calls it
// harmlessly.
func EnsureWorkspaceHooksWebRuntimeDependency(projectDir string) bool {
	return ensureWebRuntimeDependency(projectDir, filepath.Join("packages", "hooks"), "packages/hooks")
}

// ensureWebRuntimeDependency is the shared body. `label` names the manifest's
// package in the one line this prints when it writes a dev bridge.
func ensureWebRuntimeDependency(projectDir, relDir, label string) bool {
	pkgPath := filepath.Join(projectDir, relDir, "package.json")
	info, err := os.Stat(pkgPath)
	if err != nil {
		return false // no manifest here — nothing to reconcile
	}

	decision := decideWebRuntimeDependency(filepath.Join(projectDir, relDir))

	original, err := os.ReadFile(pkgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read %s: %v\n", pkgPath, err)
		return false
	}
	text := string(original)

	current, found := currentWebRuntimeSpec(text)
	switch {
	case found && current == decision.spec:
		return false // already correct — idempotent and silent
	case found && !decision.authoritative:
		return false // declared, and forge has no better answer than what is there
	case found:
		// forge's constant is a FLOOR. A project holding a higher specifier
		// (a security bump published between forge releases) keeps it; only
		// one below the floor is raised. See reconcileSpec.
		want := reconcileSpec(current, decision)
		if want == current {
			return false // the project is at or above the floor — leave it alone
		}
		// Splice over the quoted value only; a literal splice keeps `$` in a
		// path from being read as a regexp expansion.
		at := webRuntimeDepRe.FindStringSubmatchIndex(text)
		text = text[:at[2]] + jsonString(want) + text[at[3]:]
	default:
		text, err = insertDependency(text, WebRuntimePackage, decision.spec)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not declare %s in %s: %v\n", WebRuntimePackage, pkgPath, err)
			return false
		}
	}

	// Never hand back a package.json that npm would refuse to read.
	if !json.Valid([]byte(text)) {
		fmt.Fprintf(os.Stderr, "warning: declaring %s in %s would have produced invalid JSON; left unchanged\n",
			WebRuntimePackage, pkgPath)
		return false
	}
	if err := atomicfile.WriteFile(pkgPath, []byte(text), info.Mode().Perm()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", pkgPath, err)
		return false
	}
	if strings.HasPrefix(decision.spec, "file:") {
		fmt.Printf("🔗 Dev forge build: %s depends on %s at %s (npm links it; live edits, nothing published)\n",
			label, WebRuntimePackage, strings.TrimPrefix(decision.spec, "file:"))
	}
	return true
}

// currentWebRuntimeSpec returns the specifier the manifest declares today.
func currentWebRuntimeSpec(text string) (string, bool) {
	m := webRuntimeDepRe.FindStringSubmatch(text)
	if m == nil {
		return "", false
	}
	var spec string
	if err := json.Unmarshal([]byte(m[1]), &spec); err != nil {
		return "", false
	}
	return spec, true
}

// insertDependency adds name@spec to the top-level "dependencies" object,
// matching the surrounding indentation.
func insertDependency(text, name, spec string) (string, error) {
	loc := dependenciesOpenRe.FindStringIndex(text)
	if loc == nil {
		return "", fmt.Errorf(`no "dependencies" object`)
	}
	lineStart := strings.LastIndexByte(text[:loc[0]], '\n') + 1
	keyIndent := text[lineStart:loc[0]]
	if strings.TrimSpace(keyIndent) != "" {
		keyIndent = "  " // "dependencies" did not start its own line
	}
	entryIndent := keyIndent + "  "
	entry := jsonString(name) + ": " + jsonString(spec)

	after := loc[1]
	rest := text[after:]
	trimmed := strings.TrimLeft(rest, " \t\r\n")
	if strings.HasPrefix(trimmed, "}") {
		// Empty object: no trailing comma, and re-indent the closing brace.
		skipped := len(rest) - len(trimmed)
		return text[:after] + "\n" + entryIndent + entry + "\n" + keyIndent + text[after+skipped:], nil
	}
	return text[:after] + "\n" + entryIndent + entry + "," + text[after:], nil
}

// jsonString quotes s as a JSON string literal.
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// browserSDKPackage is the HyperDX browser SDK. web-runtime/telemetry imports it
// lazily and declares it an OPTIONAL, exact-pinned peer, so nothing installs it
// for the app: the app's own package.json must.
const browserSDKPackage = "@hyperdx/browser"

// EnsureBrowserTelemetryDependencies declares the packages the generated
// src/lib/otel_gen.ts needs in a web frontend's package.json.
//
// A project scaffolded before web-runtime 0.4.0 has none of them, and
// `forge generate` rewrites otel_gen.ts (Tier-1) to import the SDK, so without
// this the first build after upgrading fails to resolve "@hyperdx/browser".
//
// Add-only: a version the project already declares is its own decision, and the
// peer is exact-pinned, so a different one is reported by npm, not rewritten
// here. The specifier is the runtime's own (webruntimepeers.PeerSpec).
func EnsureBrowserTelemetryDependencies(projectDir, feRelDir, feName string) {
	spec, ok := webruntimepeers.PeerSpec(browserSDKPackage)
	if !ok {
		return
	}
	pkgPath := filepath.Join(projectDir, feRelDir, "package.json")
	info, err := os.Stat(pkgPath)
	if err != nil {
		return
	}
	original, err := os.ReadFile(pkgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read %s: %v\n", pkgPath, err)
		return
	}
	text := string(original)
	if regexp.MustCompile(`"` + regexp.QuoteMeta(browserSDKPackage) + `"\s*:`).MatchString(text) {
		return
	}
	text, err = insertDependency(text, browserSDKPackage, spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not declare %s in %s: %v\n", browserSDKPackage, pkgPath, err)
		return
	}
	if !json.Valid([]byte(text)) {
		fmt.Fprintf(os.Stderr, "warning: declaring %s in %s would have produced invalid JSON; left unchanged\n", browserSDKPackage, pkgPath)
		return
	}
	if err := atomicfile.WriteFile(pkgPath, []byte(text), info.Mode().Perm()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write %s: %v\n", pkgPath, err)
		return
	}
	fmt.Printf("  + frontend %s: declared %s@%s (browser telemetry); run npm install\n", feName, browserSDKPackage, spec)
}

// FrontendDependencyNotices is the read-only counterpart of
// EnsureWebRuntimeDependency + EnsureBrowserTelemetryDependencies, for a
// frontend whose package.json the project DISOWNED (`forge project disown
// <frontend>/package.json`): it reports what those would change instead of
// changing it, one line each.
//
// It used to be what every ADOPTED frontend got. That left an adopted app
// below the runtime floor after each forge bump that raised it — reliant's
// web app regenerated hooks calling runtime APIs its declared range did not
// have, and had to bump the range by hand every time. Adopted frontends are
// reconciled like scaffolded ones now; disowning the manifest is the opt-out.
// A dev forge build's `file:` bridge is never reported: it is machine-local
// by definition.
func FrontendDependencyNotices(projectDir, feRelDir string) []string {
	feDir := filepath.Join(projectDir, feRelDir)
	original, err := os.ReadFile(filepath.Join(feDir, "package.json"))
	if err != nil {
		return nil
	}
	text := string(original)
	var out []string

	decision := decideWebRuntimeDependency(feDir)
	if !strings.HasPrefix(decision.spec, "file:") {
		current, found := currentWebRuntimeSpec(text)
		switch {
		case !found:
			out = append(out, fmt.Sprintf("generated code imports %s, which package.json does not declare — `npm install %s@%s`",
				WebRuntimePackage, WebRuntimePackage, decision.spec))
		case decision.authoritative && current != decision.spec:
			if want := reconcileSpec(current, decision); want != current {
				out = append(out, fmt.Sprintf("package.json declares %s %s; this forge generates against %s — bump it when you review (`npm install %s@%s`)",
					WebRuntimePackage, current, want, WebRuntimePackage, want))
			}
		}
	}
	if spec, ok := webruntimepeers.PeerSpec(browserSDKPackage); ok &&
		!regexp.MustCompile(`"`+regexp.QuoteMeta(browserSDKPackage)+`"\s*:`).MatchString(text) {
		out = append(out, fmt.Sprintf("generated src/lib/otel_gen.ts imports %s, which package.json does not declare — `npm install %s@%s`",
			browserSDKPackage, browserSDKPackage, spec))
	}
	return out
}

// SyncFrontendLock brings the lockfile that governs feRelDir's package.json
// back in step with the dependency ranges forge manages there
// (WebRuntimePackage, browserSDKPackage).
//
// The Ensure* functions above rewrite those ranges in package.json and nothing
// else, so a project with a lockfile is left with one that records the OLD
// range: `npm ci` then fails ("package.json and package-lock.json are not in
// sync") until someone runs an install by hand. This runs the package
// manager's lockfile-only refresh (no node_modules, no lifecycle scripts) when,
// and only when, the lockfile disagrees about one of those packages. A project
// with no lockfile has nothing to refresh, and a lockfile that already agrees
// is never rewritten.
//
// It returns an error when the refresh could not run (no package manager on
// PATH, registry unreachable), naming the frontend and the exact command, so
// the inconsistency is never silent. FORGE_SKIP_NPM_INSTALL opts out of
// running a package manager; the command is printed instead.
func SyncFrontendLock(projectDir, feRelDir string) error {
	manifestDir := filepath.Join(projectDir, feRelDir)
	lock, ok := frontenddeps.FindLock(manifestDir, projectDir)
	if !ok {
		return nil
	}
	managed := []string{WebRuntimePackage, browserSDKPackage}
	drift, err := frontenddeps.LockDrift(lock, manifestDir, managed...)
	if err != nil {
		return fmt.Errorf("%s: compare package.json with %s: %w", feRelDir, lock.File, err)
	}
	if drift == nil {
		return nil
	}
	cmd := frontenddeps.LockRefreshCommand(lock, manifestDir)
	if os.Getenv("FORGE_SKIP_NPM_INSTALL") != "" {
		fmt.Fprintf(os.Stderr, "warning: %s/package.json and %s are out of sync (%s); FORGE_SKIP_NPM_INSTALL is set, so forge did not refresh it — run %s\n",
			feRelDir, lock.File, strings.Join(drift.Details, "; "), cmd)
		return nil
	}
	fmt.Printf("  ↻ %s: refreshing %s for the dependency ranges forge changed in package.json\n", feRelDir, lock.File)
	if err := frontenddeps.RefreshLock(context.Background(), lock, manifestDir); err != nil {
		return fmt.Errorf("%s/package.json changed but %s could not be refreshed, so `%s ci`-style frozen installs will fail until it is — run %s: %w",
			feRelDir, lock.File, lock.Runner, cmd, err)
	}
	return nil
}
