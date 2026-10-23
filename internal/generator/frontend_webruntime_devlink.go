// Package generator — the DEV bridge from a project's frontends to a local
// @reliantlabs/forge-web-runtime checkout.
//
// This is the npm mirror of what forge already does for Go, and the shape is
// deliberately the same one:
//
//	Go: a gitignored `go.work` bridges the project to a sibling forge/pkg
//	    checkout. go.mod keeps its published pin. Nothing machine-specific is
//	    committed, and a clone that lacks the sibling still builds.
//
//	JS: a gitignored npm workspace ROOT (package.json + .forge-link/) bridges
//	    the project's frontends to a sibling forge/web-runtime checkout. Each
//	    frontend's package.json keeps its published range. Nothing
//	    machine-specific is committed, and a clone that lacks the sibling
//	    still installs.
//
// # Why the bridge cannot simply be a symlink
//
// npm owns node_modules. A bare symlink dropped into
// node_modules/@reliantlabs/ has no corresponding edge in the dependency
// graph, so the next install that reshapes the tree replaces it with the
// registry copy and the developer silently goes back to testing published
// code. The link has to be something npm itself computes.
//
// The previous design got that right and paid for it in the wrong currency:
// it rewrote the specifier to `file:<path>` inside frontends/<name>/package.json,
// a TRACKED file. That left every maintainer's `git status` permanently dirty,
// made the diff meaningless to review, and made it easy to commit a path that
// resolves only on a machine with a sibling forge checkout at exactly that
// depth — which breaks CI and every other developer.
//
// # The mechanism
//
// A workspace root ABOVE the frontends, both parts of it gitignored:
//
//	<project>/package.json      { "workspaces": ["frontends/web", …, ".forge-link/*"] }
//	<project>/.forge-link/web-runtime -> ../../forge/web-runtime   (symlink)
//
// npm reads the root, sees a workspace member whose package.json declares the
// name @reliantlabs/forge-web-runtime, and hoists it to <project>/node_modules
// as a link. The frontends' own `"^0.3.1"` requirement resolves against that
// member by NAME, so the tracked manifests never mention a path. Node's
// resolution walks up from frontends/<name>/, finds the hoisted link, and lands
// in the live checkout — verified by require.resolve pointing straight into
// forge/web-runtime/dist, and by an edit to that file being visible to the next
// import with no reinstall.
//
// Three properties this buys that the alternatives do not:
//
//   - The tracked package.json AND package-lock.json are both untouched. An
//     in-frontend overlay (workspaces + a link dir inside the frontend) also
//     keeps package.json clean, but npm records the member in the frontend's
//     OWN package-lock.json — measured — so the churn simply moves to the
//     other tracked file. Hoisting to a root the frontend does not own is what
//     keeps both clean.
//   - A stale registry copy already installed under frontends/<name>/node_modules
//     does not shadow the link: the root install REMOVES it (measured: "added 2
//     packages, and removed 5 packages", after which require.resolve pointed at
//     the checkout).
//   - `npm install` run from INSIDE a frontend still honours the root
//     workspace and leaves the nested lockfile byte-identical (measured).
//
// The symlink points at the checkout with a RELATIVE path, and the root
// manifest names no path at all, so — as with `go.work` — even these ignored
// files carry no username and no home directory.
//
// Everything here FOLLOWS the Go bridge rather than deciding on its own: the
// twin exists only while the project's go.work bridges a forge checkout (the
// opt-in record `forge project new --link-forge` or `go work use` writes), and
// it links that same checkout's web-runtime. It used to be laid down by every
// dev build of forge, for whatever checkout that build came from — so a
// project nobody had asked to bridge got .forge-link/ on its first generate.
// The workspace root is a maintainer's dev-loop artifact; scattering one into
// a project that did not opt in changes how its install resolves for no
// benefit.
package generator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgecompat"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// devLinkDir is the project-relative directory holding the workspace-member
// symlinks. Dot-prefixed because it is machine-local scaffolding, not source.
const devLinkDir = ".forge-link"

// devWorkspaceRootName is the name the bridge's root manifest carries. It is
// how forge recognises a root package.json as its OWN (and so safe to
// reconcile) rather than one a user wrote, which forge never touches.
const devWorkspaceRootName = "forge-dev-workspace-root"

// devWorkspaceRootManifest renders the gitignored root package.json for the
// given workspace members. It is a workspace root and nothing else: no
// dependencies of its own, private so it can never be published, and carrying
// an explanation for whoever finds an untracked package.json at their project
// root and wonders what wrote it.
//
// The members are ENUMERATED, never a `frontends/*` glob. A glob made every
// frontend a member of ONE hoisted tree, Expo apps included, and that tree is
// not one an Expo app can bundle from: see bridgeMembers.
func devWorkspaceRootManifest(members []string) string {
	var ws strings.Builder
	for _, m := range append(append([]string(nil), members...), devLinkDir+"/*") {
		ws.WriteString("\n    \"" + m + "\",")
	}
	return `{
  "name": "` + devWorkspaceRootName + `",
  "private": true,
  "// overrides note": "npm honours overrides only at the install root, not in workspace members. Match the scaffold's fflate security override here while the dev bridge owns the root (GHSA-px8p-9vwx-vf98).",
  "overrides": {
    "@hyperdx/otel-web-session-recorder": {
      "fflate": "^0.7.5"
    }
  },
  "//": [
    "GITIGNORED, machine-local, written by a DEV build of forge. Not part of your project.",
    "This is the npm twin of a gitignored go.work: it bridges this project's frontends to a",
    "local @reliantlabs/forge-web-runtime checkout (symlinked under .forge-link/) so edits in",
    "that checkout are live here, with nothing published and nothing reinstalled.",
    "The frontends' own package.json files keep their published semver range and stay clean.",
    "React Native / Expo apps are deliberately NOT members: they install standalone.",
    "Delete this file and .forge-link/ to go back to the registry copy; run npm install after."
  ],
  "workspaces": [` + strings.TrimSuffix(ws.String(), ",") + `
  ]
}
`
}

// devNpmrcName is the gitignored project-root npm config the bridge writes.
const devNpmrcName = ".npmrc"

// devNpmrcMarker is the first line of the .npmrc the bridge writes. It is how
// forge recognises the file as its OWN (safe to rewrite or remove) rather than
// one a user wrote, which forge never touches.
const devNpmrcMarker = "# forge-dev-workspace-root: GITIGNORED, written by a DEV build of forge."

// devNpmrcBody makes `npm install` survive the bridge on the npm that ships
// with Node 22 (10.9.x).
//
// Linking web-runtime as a workspace member installs ITS devDependencies into
// the project's tree, and those include vitest ^4.1. npm 10.x and 11.0.0 (11.21.0 is fine)
// crashes resolving a tree that holds vitest >= 4.1:
//
//	npm error Cannot read properties of null (reading 'edgesOut')
//
// (arborist's #loadPeerSet; reproducible with a package.json that contains
// nothing but that vitest range, no bridge involved, on every npm 10.x and
// 11.0.0). The scaffold's own frontends skip that line (vitest ^5) and install
// fine, which is why only a bridged project hits it. legacy-peer-deps skips the peer-set
// walk. It loses nothing here: every peer web-runtime declares (react,
// @connectrpc/connect, @bufbuild/protobuf, @opentelemetry/api, @hyperdx/browser,
// @tanstack/react-query) is also a direct dependency of the frontends, which
// is what keeps a single copy of each in the tree. The same holds for vite,
// the peer of vitest and @vitejs/plugin-react: the frontends declare the major
// web-runtime's vitest also accepts (8), so the tree dedupes to one copy.
const devNpmrcBody = devNpmrcMarker + `
# Works around an npm 10.x crash on web-runtime's vitest ^4 devDependency; delete this
# file and .forge-link/ together to go back to the registry copy.
legacy-peer-deps=true
`

// isForgeOwnedNpmrc reports whether path is an .npmrc this bridge wrote.
func isForgeOwnedNpmrc(path string) bool {
	body, err := os.ReadFile(path)
	return err == nil && strings.HasPrefix(string(body), devNpmrcMarker)
}

// reconcileNpmrc writes the bridge's .npmrc unless the project has an .npmrc
// of its own, which is left exactly as it is. Reports whether a user-owned
// file is standing in the way.
func reconcileNpmrc(path string) (userOwned bool, err error) {
	if _, statErr := os.Stat(path); statErr == nil && !isForgeOwnedNpmrc(path) {
		return true, nil
	}
	existing, _ := os.ReadFile(path)
	if string(existing) == devNpmrcBody {
		return false, nil
	}
	return false, atomicfile.WriteFile(path, []byte(devNpmrcBody), 0o644)
}

// devBridgeIgnoreEntries are the paths forge must ensure are ignored before it
// writes any of them. Ensuring rather than assuming matters: the bridge is
// only safe because it CANNOT be committed, so a project whose .gitignore
// predates this feature must be brought up to date by forge, not by the
// developer noticing.
//
// Each is anchored with a leading "/" so it matches the project root only —
// a bare "package.json" would also ignore every frontend's manifest, which are
// exactly the tracked files this whole change exists to protect.
var devBridgeIgnoreEntries = []string{
	"/package.json",
	"/package-lock.json",
	"/" + devNpmrcName,
	"/node_modules/",
	"/" + devLinkDir + "/",
}

// EnsureDevWebRuntimeLink reconciles the gitignored dev bridge for projectDir.
//
// No-op unless the project's Go build is bridged to a forge checkout and that
// checkout actually carries web-runtime. Every failure is a warning: a bridge
// is a convenience for forge maintainers, and no dev-loop nicety justifies
// failing somebody's generate.
func EnsureDevWebRuntimeLink(projectDir string) {
	target, ok := devWebRuntimeCheckout(projectDir)
	if !ok {
		removeModuleCacheBridge(projectDir)
		return
	}
	members := bridgeMembers(projectDir)
	rootPath := filepath.Join(projectDir, "package.json")
	if len(members) == 0 && !isForgeOwnedWorkspaceRoot(rootPath) {
		return // nothing to bridge (no frontends, or only native apps)
	}

	if err := ensureGitignoreEntries(filepath.Join(projectDir, ".gitignore"), devBridgeIgnoreEntries); err != nil {
		// Refuse to write files we could not first make uncommittable —
		// that is the failure mode this change exists to eliminate.
		fmt.Fprintf(os.Stderr, "warning: could not ignore the dev %s bridge in %s (%v); bridge not written\n",
			WebRuntimePackage, projectDir, err)
		return
	}

	if err := reconcileWorkspaceRoot(rootPath, devWorkspaceRootManifest(members)); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write the dev workspace root in %s: %v\n", projectDir, err)
		return
	}

	userOwned, err := reconcileNpmrc(filepath.Join(projectDir, devNpmrcName))
	switch {
	case err != nil:
		fmt.Fprintf(os.Stderr, "warning: could not write the dev %s in %s: %v\n", devNpmrcName, projectDir, err)
	case userOwned:
		fmt.Fprintf(os.Stderr, "warning: %s/%s is yours, so forge did not add `legacy-peer-deps=true` to it. "+
			"npm 10.x crashes (\"Cannot read properties of null (reading 'edgesOut')\") installing the bridged "+
			"%s because of its vitest ^4 devDependency: add that line to %s yourself, or use a newer npm (11.21 works).\n",
			projectDir, devNpmrcName, WebRuntimePackage, devNpmrcName)
	}

	link := filepath.Join(projectDir, devLinkDir, "web-runtime")
	changed, err := ensureRelativeSymlink(link, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not link %s into %s: %v\n", WebRuntimePackage, link, err)
		return
	}
	if changed {
		fmt.Printf("🔗 Dev forge build: %s is bridged to the local checkout via %s/ "+
			"(gitignored; your frontends' package.json keeps %s). Run `npm install` at the project root.\n",
			WebRuntimePackage, devLinkDir, webRuntimePublishedRange)
	}
}

// removeModuleCacheBridge tears down a bridge an earlier forge wrote into the
// Go MODULE CACHE, and nothing else.
//
// forge used to mistake a `go install …@<version>` build — which compiles from
// $GOMODCACHE/github.com/reliant-labs/forge@<version>/ — for a dev checkout,
// and linked .forge-link/web-runtime into that read-only tree. Discovery no
// longer does that (buildinfo.forgeRootFromFile), but the link it left behind
// outlives the fix: `npm install` at the root keeps trying to create
// node_modules inside the module cache and fails with EACCES, so the
// project's frontend test lane stays broken until someone deletes it by hand.
//
// Only a link that resolves INTO a module cache is removed, together with the
// root manifest forge wrote for it. A bridge to a real sibling checkout is a
// maintainer's dev loop and is left exactly as it is — a released forge
// running in that tree has no business dismantling it. A user's own root
// package.json is never touched.
func removeModuleCacheBridge(projectDir string) {
	link := filepath.Join(projectDir, devLinkDir, "web-runtime")
	dest, err := os.Readlink(link)
	if err != nil {
		return
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(link), dest)
	}
	if !buildinfo.IsModuleCachePath(filepath.Clean(dest)) {
		return
	}
	if err := os.Remove(link); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove the stale %s bridge %s: %v\n", WebRuntimePackage, link, err)
		return
	}
	_ = os.Remove(filepath.Join(projectDir, devLinkDir)) // only if now empty
	rootPath := filepath.Join(projectDir, "package.json")
	if isForgeOwnedWorkspaceRoot(rootPath) {
		_ = os.Remove(rootPath)
	}
	if npmrc := filepath.Join(projectDir, devNpmrcName); isForgeOwnedNpmrc(npmrc) {
		_ = os.Remove(npmrc)
	}
	fmt.Printf("🧹 removed a stale %s bridge into the read-only Go module cache (%s/, root package.json). "+
		"Run `npm install` in each frontend to use the registry copy.\n", WebRuntimePackage, devLinkDir)
}

// devWebRuntimeCheckout returns the absolute web-runtime directory projectDir
// should bridge to, and whether a bridge is warranted at all: the project's
// Go build must already be bridged to a forge checkout, and the twin points
// at THAT checkout, so the Go library and the web runtime always come from
// the same tree.
func devWebRuntimeCheckout(projectDir string) (string, bool) {
	// CI is a dev BUILD (forge's workflows install the binary under test from
	// the working tree) but never a dev LOOP: nobody is editing web-runtime
	// alongside the scaffold, and the bridge's extra resolution root made the
	// scaffold fail to typecheck with duplicate copies of @connectrpc/connect.
	// See internal/buildinfo/ci.go for why pinning more peers is the wrong fix.
	if buildinfo.IsCI() && !buildinfo.DevWebRuntimeLinkForced() {
		return "", false
	}
	bridge, ok := forgecompat.LocalBridge(projectDir)
	if !ok {
		return "", false
	}
	target := filepath.Join(bridge.Dir, "web-runtime")
	if _, err := os.Stat(filepath.Join(target, "package.json")); err != nil {
		return "", false
	}
	return target, true
}

// bridgeMembers returns the project-relative frontends the workspace root
// should adopt, sorted so the rendered manifest is deterministic.
//
// Every frontend with a package.json is a member EXCEPT a React Native / Expo
// app, which stays a standalone install with its own node_modules. The reason
// is measured, not taste. One workspace means one hoisted tree, and the web
// kinds (React 19) and an Expo app (React 18) cannot share one: whichever
// installs first claims the root, and in the usual order — web, then mobile —
// npm nests the Expo app's react, expo and expo-router under
// frontends/<app>/node_modules while hoisting babel-preset-expo to the root.
// babel-preset-expo then does require('expo/config') WITHOUT declaring expo,
// finds nothing from the root, and every bundle fails:
//
//	[BABEL]: Cannot find module 'expo/config'
//
// Install the other way round and it works, which is why this read as a flake.
// Expo's own toolchain assumes it sits beside its own expo; a standalone
// install is the only layout that holds in every order. The price is that a
// native app resolves the REGISTRY web-runtime rather than the live checkout —
// the same thing CI and every released-forge user already get.
func bridgeMembers(projectDir string) []string {
	entries, err := os.ReadDir(filepath.Join(projectDir, "frontends"))
	if err != nil {
		return nil
	}
	var members []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest := filepath.Join(projectDir, "frontends", e.Name(), "package.json")
		if _, err := os.Stat(manifest); err != nil {
			continue // not an npm package, so not something npm can adopt
		}
		if isNativeFrontend(manifest) {
			continue
		}
		members = append(members, "frontends/"+e.Name())
	}
	sort.Strings(members)
	return members
}

// isNativeFrontend reports whether the package.json at manifest belongs to a
// React Native app (bare or Expo). The manifest is the evidence rather than
// forge.yaml's frontend type because the question is how npm will lay the
// package out, and a hand-added frontend has a manifest but no config entry.
func isNativeFrontend(manifest string) bool {
	body, err := os.ReadFile(manifest)
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return false
	}
	for _, deps := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for _, native := range []string{"expo", "react-native"} {
			if _, ok := deps[native]; ok {
				return true
			}
		}
	}
	return false
}

// isForgeOwnedWorkspaceRoot reports whether path is a root manifest this
// bridge wrote. Anything else — absent, unreadable, or a user's own root — is
// not forge's to rewrite.
func isForgeOwnedWorkspaceRoot(path string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pkg struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(body, &pkg) == nil && pkg.Name == devWorkspaceRootName
}

// reconcileWorkspaceRoot writes want to path when there is no root manifest,
// or when the existing one is forge's own and has drifted (a frontend was
// added, or it predates the enumerated member list). A user's own root
// package.json is left exactly as it is.
func reconcileWorkspaceRoot(path, want string) error {
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return atomicfile.WriteFile(path, []byte(want), 0o644)
	case err != nil:
		return err
	case string(existing) == want || !isForgeOwnedWorkspaceRoot(path):
		return nil
	}
	return atomicfile.WriteFile(path, []byte(want), 0o644)
}

// ensureRelativeSymlink makes linkPath a symlink to targetDir, expressed
// relative to the link's own directory so the ignored file still names no
// absolute path. Reports whether anything changed, so a re-run is silent.
func ensureRelativeSymlink(linkPath, targetDir string) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return false, err
	}
	rel, err := filepath.Rel(resolvePath(filepath.Dir(linkPath)), resolvePath(targetDir))
	if err != nil {
		rel = targetDir // absolute fallback: still gitignored, still correct
	}
	rel = filepath.ToSlash(rel)
	abs := resolvePath(targetDir)

	switch existing, err := os.Readlink(linkPath); {
	case err == nil && linkMatches(existing, rel, abs):
		return false, nil // already correct
	case err == nil:
		// Points somewhere else (the checkout moved): replace it.
		if err := os.Remove(linkPath); err != nil {
			return false, err
		}
	case errors.Is(err, os.ErrNotExist):
		// Nothing there yet.
	default:
		// Exists but is not a symlink — a real directory somebody put here.
		// Leave it alone rather than deleting a developer's files.
		return false, fmt.Errorf("%s exists and is not a symlink", linkPath)
	}
	return true, createDirLink(rel, abs, linkPath)
}

// ensureGitignoreEntries appends any of want that the file does not already
// ignore, under a short explanatory header. Creates the file when absent.
func ensureGitignoreEntries(path string, want []string) error {
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	body := string(existing)

	var missing []string
	for _, entry := range want {
		if !gitignoreHasEntry(body, entry) {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString(body)
	if body != "" && !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(`
# ── Dev web-runtime bridge (machine-local; NEVER commit) ──
# A dev build of forge writes an npm workspace ROOT here that links this
# project's frontends to a local @reliantlabs/forge-web-runtime checkout —
# the npm twin of a gitignored go.work. The frontends' own package.json files
# keep their published semver range, so nothing machine-specific is tracked.
# A committed root manifest would change how every other clone and CI resolves
# the runtime, which is exactly what these entries prevent.
`)
	for _, entry := range missing {
		b.WriteString(entry + "\n")
	}
	return atomicfile.WriteFile(path, []byte(b.String()), 0o644)
}

// gitignoreHasEntry reports whether body already ignores entry, ignoring
// comments, blank lines and surrounding whitespace. It also accepts the
// unanchored spelling ("package.json" for "/package.json") — a project that
// already ignores it more broadly needs nothing added.
func gitignoreHasEntry(body, entry string) bool {
	bare := strings.Trim(entry, "/")
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == entry || line == bare || line == bare+"/" || line == "/"+bare {
			return true
		}
	}
	return false
}
