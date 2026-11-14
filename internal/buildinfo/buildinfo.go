// Package buildinfo exposes the forge binary's version metadata to packages
// that cannot depend on internal/cli (to avoid import cycles). The cmd/forge
// entrypoint and internal/cli are responsible for seeding this data at
// startup via Set; anything that wants to stamp the forge version into
// generated artifacts should read it from here.
package buildinfo

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// embeddedVersionFile is a build-time copy of the repo-root VERSION file.
// Because go:embed cannot reach outside its own package directory, this file
// is kept byte-identical to ../../VERSION (see TestEmbeddedVersionFileMatchesSource,
// mirroring internal/assets' proto sync pattern). It states the last RELEASED
// forge version, and is the floor Version()/Build.String() fall back to when
// build info cannot supply a real one — most importantly the go.work case: a
// host binary that `use`s a sibling forge checkout sees the forge dependency
// report "(devel)" with no real version to read at all.
//
//go:embed VERSION
var embeddedVersionFile string

// versionFromFile validates and trims raw VERSION file content into a usable
// version floor. Anything that isn't a clean vX.Y.Z[-pre] release-tag shape —
// a missing file, stray whitespace, a corrupted edit — degrades to "" so
// callers fall through to the next tier rather than trust garbage.
func versionFromFile(raw string) string {
	v := strings.TrimSpace(raw)
	if !releaseTagRE.MatchString(v) {
		return ""
	}
	return v
}

// unknownDevAfter renders "an unidentified build of some commit after release
// v" — the last-resort floor, used only when the git derivation could not run.
//
// It is a PRE-RELEASE of the next patch (`v0.1.15` -> `v0.1.16-0.dev`), not
// build metadata on the release itself. That is the whole point: semver
// ignores build metadata, so the old `v0.1.15+dev` spelling compared equal to
// the release and let an older binary overwrite a newer build's vendored
// output. This spelling sorts strictly after v0.1.15 and strictly before
// v0.1.16, which is exactly what is known about it.
//
// The `+unknown` build metadata is load-bearing, not decoration. Without it
// the string is `v0.1.16-0.dev`, a perfectly valid semver prerelease that
// installableVersionRE MATCHES — so InstallableVersion would hand it back and
// a scaffold would pin a version no proxy can resolve. With it, that regex
// rejects it (it allows no "+") and IsDevVersion reports true (it keys on
// "+"), while semver.Compare ignores build metadata entirely and so still
// orders it strictly after v0.1.15 and strictly before v0.1.16.
func unknownDevAfter(v string) string {
	if v == "" || strings.Contains(v, "+") || strings.Contains(v, "-") {
		return v
	}
	next := nextPatch(v)
	if next == "" {
		return v
	}
	return next + "-0.dev+unknown"
}

// forgeModulePath is the go.mod module path identifying a genuine forge
// source checkout on disk. Runtime source discovery
// (DiscoverDevForgeRootFromSource) confirms it — together with the pkg/
// directory the runtime libraries live in — before treating a directory as
// the local forge root.
const forgeModulePath = "github.com/reliant-labs/forge"

// DevForgeRoot is the absolute path to the LOCAL forge source checkout that
// THIS forge binary was built from. It is injected ONLY on dev builds, via
// ldflags, from the builder's own checkout — never committed, never
// machine-specific in source:
//
//	go install -ldflags \
//	  "-X github.com/reliant-labs/forge/internal/buildinfo.DevForgeRoot=$(git rev-parse --show-toplevel)" \
//	  ./cmd/forge
//
// (see the `dev` target in the Makefile / Taskfile and CONTRIBUTING.md).
// Released builds never pass this ldflag, so it stays "" for anything a user
// installs. The e2e corpus stamps the binary it tests (devForgeRootLDFlag in
// internal/cli/scaffold_e2e_test.go), which is never shipped.
//
// Its only consumer is the project scaffolder (internal/cli/new.go): on a dev
// build it writes a gitignored go.work into the freshly-created project that
// `use`s this path, so the scaffold builds against the in-development
// forge/pkg instead of the last published tag — no manual `go mod edit
// -replace` dance. The path lands ONLY in that generated, gitignored,
// machine-local go.work; it is never written into committed forge source or
// any committed project file.
var DevForgeRoot string

// DiscoverDevForgeRootFromSource locates the LOCAL forge source checkout that
// THIS binary was COMPILED from, at runtime, with no build-time ldflag. It is
// the embedder-independent complement to DevForgeRoot: forge's CLI runs both
// standalone (cmd/forge) AND embedded in other binaries — reliant imports
// github.com/reliant-labs/forge/cli and runs `reliant forge project new`
// in-process. Only forge's own `make dev` stamps DevForgeRoot; an embedder's
// build almost never remembers to pass forge's internal ldflag, so on a dev
// forge running inside reliant DevForgeRoot is "" and the scaffolder would
// silently fall back to the last PUBLISHED forge/pkg tag — which cannot
// satisfy generated code that targets not-yet-published forge/pkg APIs. Rather
// than make every embedder stamp the flag, we recover the path the compiler
// baked into this source file.
//
// How: runtime.Caller yields the absolute path of THIS file as it existed on
// the build machine. Contributor/dev builds are NOT built with -trimpath, so
// that path is a real, absolute checkout path; we walk upward to the module
// root (the go.mod declaring github.com/reliant-labs/forge) and confirm its
// pkg/ runtime-library directory exists on disk.
//
// It returns "" — bridging nothing — for anything that is not a genuine,
// present-on-disk forge checkout, which is exactly the release/shipped case:
//   - release builds use -trimpath, so the baked path is a module-relative
//     stub that does not exist as an absolute path;
//   - a dev binary copied to another machine no longer finds its source tree;
//   - any walk that fails to find the go.mod module marker and pkg/.
//
// So it is safe to call unconditionally — it self-limits to the machine that
// built a from-source forge. Callers still gate on IsDevBuild first, so a
// clean release never even attempts discovery.
func DiscoverDevForgeRootFromSource() string {
	mu.RLock()
	ov, ovSet := discoverOverride, discoverOverrideSet
	mu.RUnlock()
	if ovSet {
		return ov
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok || file == "" || !filepath.IsAbs(file) {
		return ""
	}
	return forgeRootFromFile(file)
}

// discoverOverride / discoverOverrideSet are a test seam mirroring
// devBuildOverride: under `go test`, runtime.Caller always resolves to the
// LIVE forge checkout, so a test can never naturally exercise the
// "not discoverable" path — it pins the discovery result here instead.
var (
	discoverOverride    string
	discoverOverrideSet bool
)

// SetDiscoveredForgeRoot overrides what DiscoverDevForgeRootFromSource returns,
// bypassing the runtime.Caller walk. Test-only seam (pairs with SetDevBuild):
// pass "" to exercise the not-discoverable/hint path, or a fixture root to
// exercise the bridge path deterministically. Pair with
// ClearDiscoveredForgeRoot in a t.Cleanup.
func SetDiscoveredForgeRoot(v string) {
	mu.Lock()
	defer mu.Unlock()
	discoverOverride = v
	discoverOverrideSet = true
}

// ClearDiscoveredForgeRoot removes any override set by SetDiscoveredForgeRoot,
// restoring the real runtime.Caller discovery.
func ClearDiscoveredForgeRoot() {
	mu.Lock()
	defer mu.Unlock()
	discoverOverride = ""
	discoverOverrideSet = false
}

// forgeRootFromFile is the pure upward walk behind
// DiscoverDevForgeRootFromSource, split out so it can be unit-tested against a
// fixture tree without depending on where the test binary was compiled.
func forgeRootFromFile(file string) string {
	dir := filepath.Dir(file)
	for {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if modfile.ModulePath(data) == forgeModulePath {
				// A MODULE-CACHE copy is not a checkout. `go install
				// github.com/reliant-labs/forge/cmd/forge@<pseudo-version>`
				// — exactly how a project pins forge, and how its CI
				// installs it — builds without -trimpath from
				// $GOMODCACHE/github.com/reliant-labs/forge@<version>/, so
				// the walk lands on a real go.mod and a real pkg/. Treating
				// that as a dev checkout bridged the project to a READ-ONLY
				// tree: .forge-link/web-runtime pointed into the module
				// cache, and the `npm install` forge then asked for failed
				// with EACCES creating web-runtime/node_modules. Nobody
				// edits the module cache, so there is nothing to bridge to —
				// such a binary resolves forge from the registry like any
				// other released build.
				if isModuleCacheDir(dir) {
					return ""
				}
				// Module path matches. Confirm pkg/ is present too, so we
				// never point a go.work `use` at a tree that lacks the
				// runtime libraries a scaffold imports. pkg/ is a plain
				// directory inside this module now, not a submodule, so
				// there is no second go.mod to read — its presence on disk
				// is the whole check.
				if st, serr := os.Stat(filepath.Join(dir, "pkg")); serr == nil && st.IsDir() {
					return dir
				}
				return ""
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // reached the filesystem root without a match
		}
		dir = parent
	}
}

// IsModuleCachePath reports whether path is, or lies inside, a Go module-cache
// extraction of some module — any ancestor directory named
// `<element>@<version>`. See isModuleCacheDir.
func IsModuleCachePath(path string) bool {
	for dir := path; ; {
		if isModuleCacheDir(dir) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// isModuleCacheDir reports whether dir is a Go module-cache extraction of a
// module: its name is `<last path element>@<version>`, the layout the go
// command uses under $GOMODCACHE (e.g. forge@v0.1.18-0.20260926053138-05d5d6999d39).
// Decided from the name alone so discovery never shells out to `go env`; a git
// checkout is named whatever its owner chose and does not carry a module
// version after an @.
func isModuleCacheDir(dir string) bool {
	base := filepath.Base(dir)
	at := strings.LastIndex(base, "@")
	if at <= 0 {
		return false
	}
	return semver.IsValid(base[at+1:])
}

var (
	mu        sync.RWMutex
	version   string = "dev"
	gitCommit string = "unknown"

	// devBuildOverride is a test seam mirroring pkgModuleVersionOverride:
	// IsDevBuild reads the ambient binary build info, which under `go test`
	// is always the "(devel)" test binary, so tests that need to exercise
	// the release path pin it here. Pair Set/Clear in a t.Cleanup.
	devBuildOverride    bool
	devBuildOverrideSet bool
)

// Set records the forge binary's version metadata. It is intended to be
// called exactly once, from the main entrypoint. The date argument is
// accepted for call-site compatibility but is not currently retained.
func Set(v, _, commit string) {
	mu.Lock()
	defer mu.Unlock()
	version = v
	gitCommit = commit
}

// Version returns the forge binary's version. When the binary was produced by
// `go install ...@<ref>`, Set will not have been called with a real value, so
// we fall back to reading the module version from runtime build info.
//
// Returns "dev" if neither source is available.
func Version() string {
	mu.RLock()
	v := version
	mu.RUnlock()

	info, _ := debug.ReadBuildInfo()
	return versionFromInfo(info, v)
}

// versionFromInfo is the pure decision behind Version, split out so the
// standalone and embedded shapes can both be unit-tested (under `go test`
// the ambient build info is always the test binary).
//
// The embedded case is the reason this is not a one-liner. When forge is
// compiled INTO another binary, info.Main is the HOST module — reading its
// version reports e.g. reliant's v1.5.1 as though it were forge's, and the
// scaffolder then writes that into a new project's forge.yaml as
// `forge_version`. Forge's identity must always come from the forge module
// itself: Main when forge IS the main module, the forge DEP otherwise.
func versionFromInfo(info *debug.BuildInfo, stamped string) string {
	if stamped != "" && stamped != "dev" {
		return stamped
	}
	if info == nil {
		return versionFloor(stamped)
	}

	if info.Main.Path == forgeModulePath {
		if info.Main.Version != "" && info.Main.Version != "(devel)" {
			return info.Main.Version
		}
		return versionFloor(stamped)
	}

	// Embedded: forge is a dependency of some host binary.
	for _, dep := range info.Deps {
		if dep == nil || dep.Path != forgeModulePath {
			continue
		}
		if dep.Version != "" && dep.Version != "(devel)" {
			return dep.Version
		}
		break
	}
	return versionFloor(stamped)
}

// versionFloor answers "what version is this build?" when neither ldflags nor
// build info could say — in practice only the go.work embedded case, where
// forge is a dep of a host binary at "(devel)" and the vcs.* settings
// describe the HOST's tree.
//
// Tier 1 is the truth: derive the real pseudo-version from the forge checkout
// this binary was compiled from (see gitversion.go).
//
// Tier 2 is the embedded VERSION file, and it is now a LAST resort that
// deliberately sorts AFTER the release it names. It used to be tier 1 and to
// read `v0.1.15+dev` — the last released version plus build metadata — which
// semver compares EQUAL to a released v0.1.15. Source is arbitrarily far
// ahead of its last release, so that equality was false, and kclvendor's
// downgrade guard allows an equal-version overwrite: a workspace build of
// main carrying forge#202 and a released binary without it were
// indistinguishable, and #202's ClusterRole-scoping fix came one `forge generate` away from
// being silently reverted in control-plane. `v0.1.16-0.dev` instead orders
// after v0.1.15 and before v0.1.16, which is the honest claim — "some commit
// after that release" — and makes the guard refuse instead of shrug.
//
// A missing or malformed VERSION file degrades to returning stamped
// unchanged, never panics.
func versionFloor(stamped string) string {
	if v := gitVersion(); v != "" {
		return v
	}
	if v := versionFromFile(embeddedVersionFile); v != "" {
		return unknownDevAfter(v)
	}
	return stamped
}

// installableVersionRE matches a forge version string that is a valid
// `go install github.com/.../forge/cmd/forge@<ref>` target: either a
// release tag (vX.Y.Z[-pre]) or a clean Go pseudo-version
// (v0.0.0-<timestamp>-<commit>). Crucially it does NOT match build
// metadata (`+dirty`, `+incompatible` is allowed as a require but never
// as an install ref here): a `+dirty` pseudo-version is produced by
// building from a dirty working tree and no module proxy can ever serve
// it, so emitting it as a CI install target makes `go install` fail on
// every run (FRICTION fr-8c8a24ea97).
var installableVersionRE = regexp.MustCompile(
	`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// InstallableVersion returns the forge binary's version ONLY when it is a
// ref that `go install ...@<ref>` can actually resolve from a module
// proxy — a release tag or a clean Go pseudo-version. For anything else
// (the "dev" sentinel, "(devel)", an empty value, or a `+dirty`
// pseudo-version from a dirty-tree build) it returns "" so callers fall
// back to pinning by git SHA instead.
//
// This is the boundary that keeps `+dirty` out of generated CI: callers
// stamp this (not raw Version()) into the `go install` step, and the
// empty return routes the CI template's three-branch policy onto the SHA
// branch. See internal/templates/ci/github/ci.yml.tmpl.
func InstallableVersion() string {
	v := Version()
	if !installableVersionRE.MatchString(v) {
		return ""
	}
	// SHAPE IS NOT AVAILABILITY. A pseudo-version can be perfectly formed and
	// name a commit no module proxy will ever serve, and the Go toolchain
	// hands us exactly that for any build made from a local checkout: since
	// Go started stamping VCS info, `go build`/`go install ./cmd/forge`
	// records a pseudo-version for whatever commit is checked out, clean
	// shape and all.
	//
	// This broke every PULL REQUEST build. GitHub checks out an ephemeral
	// merge commit (refs/pull/N/merge) that exists on no branch, so forge
	// built in a PR job reported e.g.
	// v0.0.0-20260916183946-6fbaa5b262be, the scaffolder wrote it into the
	// test project's go.mod, and `go mod tidy` failed with `invalid version:
	// unknown revision`. Pushes to main passed, because there the commit is
	// real and fetchable — which is why this sat unnoticed while the two
	// scaffold jobs failed on #207, #208, #210 and #213 alike.
	//
	// The discriminator is PROVENANCE, not spelling:
	//
	//   - an ldflags stamp is a release build asserting its own tag: trust it;
	//   - build info with NO vcs.* settings means the module came from the
	//     proxy (`go install ...@version`), so the proxy demonstrably has it;
	//   - build info WITH vcs.* settings means it was compiled from a working
	//     tree, and nothing in that tree can prove the commit was pushed.
	//
	// The last case is the one to refuse. A build that cannot prove its
	// availability must pin nothing and be bridged with go.work instead —
	// which is what unreleasableBuildErr already tells people to do.
	if !versionCameFromStamp() && builtFromLocalCheckout() {
		return ""
	}
	return v
}

// PublishedFloor returns the newest forge RELEASE this binary's source is
// known to descend from — the embedded VERSION file (e.g. "v0.1.17") — or ""
// when that file is missing or malformed.
//
// It exists for the one question InstallableVersion must answer "" to: what
// should a module REQUIRE when this build itself is on no proxy? Requiring
// nothing is not neutral. `go mod tidy` never reads go.work, so a module that
// imports github.com/reliant-labs/forge/pkg/* with no forge requirement makes
// tidy go looking for a module that provides that import path — and the
// longest match on the proxy is the RETIRED github.com/reliant-labs/forge/pkg
// module. tidy writes that in, and the next `forge generate` refuses the
// project. The floor is a version the proxy DOES serve and that genuinely
// provides every forge/pkg package, so tidy resolves the require it already
// has instead of inventing one; a go.work bridge (the dev case) still decides
// what the build actually compiles.
func PublishedFloor() string {
	return versionFromFile(embeddedVersionFile)
}

// builtFromLocalCheckout reports whether FORGE'S OWN bytes in this binary
// were compiled from a local working tree rather than served by a proxy.
func builtFromLocalCheckout() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	return forgeBytesFromCheckout(info)
}

// forgeBytesFromCheckout is the pure decision behind builtFromLocalCheckout.
//
// STANDALONE (forge is the main module): any vcs.* setting means it was built
// from a repository — the go command stamps them only then, and a module
// extracted from the module cache carries none.
//
// EMBEDDED (forge is a dependency of a host binary, e.g. `reliant forge`): the
// vcs.* settings describe the HOST's tree and say nothing about forge. Reading
// them here was the bug — a released reliant is built from a checkout, so
// every scaffold it made pinned no forge, and `go mod tidy` filled the gap
// with the retired github.com/reliant-labs/forge/pkg v0.1.15. What decides it
// is forge's own dep entry, the same rule IsDevBuild follows:
//
//   - a Replace (directory or otherwise) or a "(devel)" / empty version is a
//     local checkout;
//   - a real version WITH a go.sum hash (Sum) came from the module cache,
//     which a proxy filled — so the proxy has it;
//   - a real version with NO Sum has no evidence either way, and is treated
//     as local: refusing to pin is recoverable (go.work), pinning an
//     unfetchable version is not.
func forgeBytesFromCheckout(info *debug.BuildInfo) bool {
	dep, embedded := forgeModuleDep(info)
	if !embedded {
		return hasVCSStamps(info)
	}
	if dep.Replace != nil {
		return true
	}
	if dep.Version == "" || dep.Version == "(devel)" {
		return true
	}
	return dep.Sum == ""
}

// versionCameFromStamp reports whether Version() is answering from the
// ldflags stamp rather than from build info. It mirrors versionFromInfo's
// first branch, which is the one authority on that precedence.
func versionCameFromStamp() bool {
	mu.RLock()
	v := version
	mu.RUnlock()
	return v != "" && v != "dev"
}

// hasVCSStamps is the pure decision behind builtFromLocalCheckout, split out
// because the ambient build info cannot exercise it: `go test` binaries carry
// no vcs.* settings, so a test reading the real thing can only skip. A `go
// build`/`go install ./cmd/forge` binary — what CI runs — does carry them:
//
//	build vcs.revision=18f7a3511aec136620c1bd598bf661d2ba14a614
//	build vcs.time=2026-09-16T18:45:07Z
//	build vcs.modified=true
func hasVCSStamps(info *debug.BuildInfo) bool {
	for _, s := range info.Settings {
		if strings.HasPrefix(s.Key, "vcs.") {
			return true
		}
	}
	return false
}

// releaseTagRE matches a clean release tag: vX.Y.Z with an optional
// pre-release suffix. Note a Go pseudo-version (v0.0.0-<ts>-<sha>) ALSO
// matches this shape, so IsDevBuild rejects pseudo-versions separately via
// module.IsPseudoVersion before consulting this.
var releaseTagRE = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)

// SetDevBuild overrides what IsDevBuild reports, bypassing the ambient binary
// build info. Test-only seam: build info is baked at compile time and is
// always the "(devel)" test binary under `go test`, so tests pin it here to
// exercise both the dev and release paths deterministically. Pair with
// ClearDevBuild in a t.Cleanup.
func SetDevBuild(v bool) {
	mu.Lock()
	defer mu.Unlock()
	devBuildOverride = v
	devBuildOverrideSet = true
}

// ClearDevBuild removes any override set by SetDevBuild, restoring the real
// build-info read.
func ClearDevBuild() {
	mu.Lock()
	defer mu.Unlock()
	devBuildOverride = false
	devBuildOverrideSet = false
}

// IsDevBuild reports whether THIS forge binary is a development build rather
// than a released, tagged one. It is the guard the scaffolder pairs with
// DevForgeRoot before writing a local-forge go.work bridge: a released binary
// must NEVER write one, even if a DevForgeRoot value somehow leaked in.
//
// A build is treated as a RELEASE (returns false) only when the binary's own
// module build info records a clean, tagged semver version (vX.Y.Z[-pre]) from
// an unmodified working tree — i.e. `go install github.com/reliant-labs/forge/cmd/forge@vX.Y.Z`
// or a tag-checkout release build. Everything else is a dev build (returns
// true):
//   - no build info at all,
//   - the "(devel)" marker (a plain `go build`/`go install` from the source
//     tree — the ordinary contributor loop),
//   - a Go pseudo-version (an untagged commit, e.g. `...@main`),
//   - vcs.modified=true (a dirty working tree).
func IsDevBuild() bool {
	mu.RLock()
	ov, ovSet := devBuildOverride, devBuildOverrideSet
	mu.RUnlock()
	if ovSet {
		return ov
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return true
	}
	vcsModified := false
	for _, s := range info.Settings {
		if s.Key == "vcs.modified" && s.Value == "true" {
			vcsModified = true
			break
		}
	}

	// EMBEDDED forge: when forge runs inside a host binary (`reliant forge …`),
	// info.Main describes the HOST, not forge. Classifying on the host asks the
	// wrong question — a dirty reliant that consumes a TAGGED forge would be
	// called a dev forge and write a local-forge go.work bridge into every
	// scaffold, pointing at a checkout the user never asked it to use.
	//
	// The honest signal is how FORGE ITSELF resolved in that build, which is
	// exactly what the dep entry records:
	//
	//	mod  github.com/reliant-labs/reliant  v1.5.1-…+dirty   ← host, dirty
	//	dep  github.com/reliant-labs/forge    v0.0.3           ← forge, tagged
	//
	// A dep carrying a directory Replace is a local checkout (the sibling-repo
	// loop) and stays dev; a dep at a clean tag is a release even when the host
	// tree is filthy. The host's vcs.modified is deliberately NOT consulted
	// here: it describes the host's working tree, and forge's own bytes came
	// from the module cache regardless of what the host has edited.
	if forgeDep, embedded := forgeModuleDep(info); embedded {
		if forgeDep.Replace != nil {
			// Replaced by a local directory → the developer IS iterating on
			// forge. A replacement carrying a version is still a module.
			return forgeDep.Replace.Version == ""
		}
		return isDevBuildFrom(forgeDep.Version, false)
	}

	return isDevBuildFrom(info.Main.Version, vcsModified)
}

// forgeModuleDep returns forge's own entry in a host binary's dependency graph.
// embedded is false when forge IS the main module (the standalone `forge`
// binary), in which case the caller classifies on info.Main as before.
func forgeModuleDep(info *debug.BuildInfo) (*debug.Module, bool) {
	if info.Main.Path == forgeModulePath {
		return nil, false
	}
	for _, dep := range info.Deps {
		if dep != nil && dep.Path == forgeModulePath {
			return dep, true
		}
	}
	return nil, false
}

// isDevBuildFrom is the pure decision behind IsDevBuild, split out so the
// release-vs-dev classification can be unit-tested without controlling the
// ambient binary build info (which is always the "(devel)" test binary under
// `go test`).
func isDevBuildFrom(mainVersion string, vcsModified bool) bool {
	if mainVersion == "" || mainVersion == "(devel)" {
		return true
	}
	if vcsModified {
		return true
	}
	// A pseudo-version (v0.0.0-<ts>-<sha>) is an untagged commit → dev.
	if module.IsPseudoVersion(mainVersion) {
		return true
	}
	// A clean, tagged release version from an unmodified tree → release.
	return !releaseTagRE.MatchString(mainVersion)
}

// IsDevVersion reports whether a forge version STRING denotes a development
// build rather than a published release tag.
//
// IsDevBuild answers that question about the RUNNING binary by reading its own
// build info. IsDevVersion answers it about a version string recorded
// somewhere else — most importantly the `forge_version` a project pins in
// forge.yaml, which outlives the binary that wrote it. A project scaffolded by
// a local forge checkout records a Go pseudo-version (`v0.0.4-0.<ts>-<sha>`,
// optionally `+dirty`). That is honest IDENTITY — it names the exact commit —
// but it is not a release, and anything that REPORTS a version should say so
// rather than let a reader mistake it for a tag.
//
// Development (true): the "dev" / "(devel)" sentinels, an empty value, any
// version carrying build metadata (`+dirty` — only a modified working tree
// produces one), any Go pseudo-version, and anything that is not valid semver.
// Release (false): a canonical tagged semver version.
//
// It is deliberately a pure function of the string: ordering and identity are
// separate concerns, and callers that need ordering compare semver directly.
func IsDevVersion(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" || v == "(devel)" {
		return true
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if strings.Contains(v, "+") {
		return true
	}
	if !semver.IsValid(v) {
		return true
	}
	return module.IsPseudoVersion(v)
}

// GitCommit returns the git commit SHA recorded via Set. Falls back to the
// VCS revision from runtime build info when available.
func GitCommit() string {
	mu.RLock()
	c := gitCommit
	mu.RUnlock()

	if c != "" && c != "unknown" {
		return c
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value
			}
		}
	}
	return c
}
