//go:build e2e

package cli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/config"
)

// TestE2EScaffoldFrontendRuntime is the gate on how a generated frontend
// consumes @reliantlabs/forge-web-runtime — the web twin of forge/pkg. It scaffolds
// a fresh project with a Next.js frontend + one CRUD entity, runs
// `forge generate`, and asserts that the generated frontend:
//
//   - wires the runtime through thin owned composition — connect.ts builds
//     its transport interceptors from the package, providers.tsx mounts
//     RuntimeShell (session + error boundary + toast host) and feeds it the
//     app-owned auth state and toast wiring;
//   - resolves the package as a DECLARED dependency: package.json carries a
//     `file:` specifier pointing at the running forge's own checkout by a
//     relative or ~-anchored path (never an absolute one), which npm installs
//     as a symlink and — because it is declared — maintains across installs;
//   - tells Tailwind v4 to scan the package (it never scans node_modules), so
//     the semantic utilities the runtime renders survive into the built CSS;
//   - carries a VALID W3C traceparent on every RPC — full-stack tracing joins
//     the backend's otelconnect/otelhttp TraceContext propagator;
//   - builds green: `npm run build` + `tsc --noEmit`;
//   - bundles exactly ONE copy of every library the runtime declares as a
//     peer dependency — see assertRuntimePeersDedupedE2E for why a build
//     gate alone structurally cannot see that.
//
// Module wiring uses the corpus-style local replaces
// (addCorpusForgePkgReplace) like the sibling frontend build e2e.
func TestE2EScaffoldFrontendRuntime(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin,
		"project", "new", "rtapp",
		"--mod", "example.com/rtapp",
		"--frontend", "web",
	)
	projectDir := filepath.Join(dir, "rtapp")
	addCorpusForgePkgReplace(t, projectDir)

	runCmd(t, projectDir, forgeBin, "scaffold", "service", "item")
	writeFileE2E(t, filepath.Join(projectDir, "proto", "services", "item", "v1", "item.proto"), itemCRUDProtoRuntime)
	writeFileE2E(t, filepath.Join(projectDir, "db", "migrations", "0001_create_items.up.sql"), itemsTableMigration)

	runCmd(t, projectDir, forgeBin, "generate")

	webDir := filepath.Join(projectDir, "frontends", "web")

	// ── The runtime is a package, not emitted project code. ──
	if _, err := os.Stat(filepath.Join(webDir, "src", "lib", "runtime")); !os.IsNotExist(err) {
		t.Errorf("frontend still carries an emitted src/lib/runtime directory (stat err = %v)", err)
	}

	// ── The TRACKED manifest declares the published range, never a path. ──
	//
	// This used to assert the opposite — that a dev build rewrote the
	// specifier to `file:<path>` inside frontends/<name>/package.json. That
	// mechanism was deliberately retired: package.json is a TRACKED file, so
	// rewriting it left every maintainer permanently dirty and made it easy
	// to commit a path that resolves only on one machine. The dev bridge now
	// lives in a gitignored npm workspace root above the frontends
	// (<project>/package.json + <project>/.forge-link/), exactly mirroring
	// how a gitignored go.work bridges Go without touching go.mod. See
	// internal/generator/frontend_webruntime_devlink.go.
	//
	// So the invariant is now the REVERSE of the old one: a path specifier
	// here is the defect.
	// npm HOISTS the workspace member to the workspace root's node_modules,
	// so the link lands at <project>/node_modules, not under frontends/web/.
	// Node resolution walks up from the frontend and finds it there. Check
	// both, because which one npm picks is npm's business — what matters is
	// that a link (not a registry copy) is reachable from the frontend.
	linkCandidates := []string{
		filepath.Join(projectDir, "node_modules", "@reliantlabs", "forge-web-runtime"),
		filepath.Join(webDir, "node_modules", "@reliantlabs", "forge-web-runtime"),
	}
	pkgJSON := readFileE2E(t, filepath.Join(webDir, "package.json"))
	spec := webRuntimeSpecE2E(t, pkgJSON)
	if strings.HasPrefix(spec, "file:") || strings.HasPrefix(spec, "link:") {
		t.Fatalf("tracked frontend manifest carries a local path specifier %q; "+
			"the dev bridge belongs in the gitignored workspace root, not in a tracked file", spec)
	}
	if !strings.HasPrefix(spec, "^") {
		t.Fatalf("frontend manifest should declare the published semver range; specifier = %q", spec)
	}
	assertNoHomePathE2E(t, filepath.Join(webDir, "package.json"))

	// ── Tailwind is told to scan the package. ──
	globalsCSS := readFileE2E(t, filepath.Join(webDir, "src", "app", "globals.css"))
	if !strings.Contains(globalsCSS, `@source "../../node_modules/@reliantlabs/forge-web-runtime"`) {
		t.Errorf("globals.css does not @source the runtime package; its utilities would be dropped:\n%s", globalsCSS)
	}

	// ── The transport is wrapped with the runtime interceptor stack. ──
	connectTS := readFileE2E(t, filepath.Join(webDir, "src", "lib", "connect.ts"))
	if !strings.Contains(connectTS, "buildRuntimeInterceptors") ||
		!strings.Contains(connectTS, `from "@reliantlabs/forge-web-runtime"`) {
		t.Errorf("connect.ts does not wire the runtime interceptor stack:\n%s", connectTS)
	}

	// ── The app shell mounts the runtime providers (session + error
	//    boundary + toast host), feeds them the app-owned auth + toast
	//    wiring, and inits client telemetry. ──
	providersTSX := readFileE2E(t, filepath.Join(webDir, "src", "app", "providers.tsx"))
	// The prop must be FED, but the variable it is fed from is the template's
	// business: the scaffold adapts its own auth context into the runtime's
	// shape and passes it as `runtimeAuth`. Matching the whole literal
	// `auth={auth}` pinned an identifier name rather than the wiring, so a
	// rename broke the test with nothing regressing.
	if !strings.Contains(providersTSX, "<RuntimeShell auth={") {
		t.Errorf("providers.tsx does not hand RuntimeShell the app's auth state:\n%s", providersTSX)
	}
	if !strings.Contains(providersTSX, "ToastNotification") {
		t.Errorf("providers.tsx does not supply the toast presentation:\n%s", providersTSX)
	}
	if !strings.Contains(providersTSX, "initTelemetry()") {
		t.Errorf("providers.tsx does not init browser telemetry:\n%s", providersTSX)
	}

	// ── The build gate: the real toolchain must be green. ──
	if !toolAvailable("node") || !toolAvailable("npm") {
		t.Log("node/npm not available — skipping npm build gate (content assertions still ran)")
		return
	}

	// The dev bridge is a MAINTAINER's artifact and is deliberately NOT
	// written under CI (EnsureDevWebRuntimeLink returns early there): bridging
	// in CI added web-runtime's own node_modules as a second resolution root
	// and broke the scaffold's typecheck with two copies of @connectrpc/connect.
	// So on CI the correct outcome is no link at all and a plain registry
	// install of the published range — which is also the shape a user gets,
	// and the thing the npm build gate below actually needs to prove.
	//
	// Asserting the link unconditionally therefore tested for a state CI is
	// designed not to produce. Keep the assertion where it means something.
	//
	// The dependency is DECLARED, so npm creates the link itself and keeps
	// it across repeat installs — the precise failure a bare symlink into
	// node_modules could not survive.
	//
	// The install runs web-runtime's prepare script in the shared repo
	// checkout, so build it once up front rather than letting parallel tests
	// race that bootstrap.
	prebuildWebRuntimeE2E(t)
	for i := 1; i <= 2; i++ {
		runCmdTimeout(t, webDir, 5*time.Minute,
			"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")
		if buildinfo.IsCI() {
			continue // no dev bridge under CI, by design — see above
		}
		linked := false
		for _, candidate := range linkCandidates {
			if _, err := os.Readlink(candidate); err == nil {
				linked = true
				break
			}
		}
		if !linked {
			t.Fatalf("npm install #%d left no link to the runtime package; looked in %v",
				i, linkCandidates)
		}
	}
	// Regenerating is idempotent — it neither duplicates nor disturbs the entry.
	runCmd(t, projectDir, forgeBin, "generate")
	after := readFileE2E(t, filepath.Join(webDir, "package.json"))
	if got := webRuntimeSpecE2E(t, after); got != spec {
		t.Errorf("regenerate changed the specifier: %q -> %q", spec, got)
	}
	if n := strings.Count(after, "@reliantlabs/forge-web-runtime"); n != 1 {
		t.Errorf("package declared %d times after regenerate, want 1:\n%s", n, after)
	}

	// ── Give the bridge the shape a contributor's machine has. ──
	//
	// The dual-copy hazard needs the linked checkout to carry its own
	// node_modules, which is true of every machine that DEVELOPS the runtime
	// (`npm install` in web-runtime for vitest/tsc) and false of a bare CI
	// checkout. Left to chance, the assertion below would be a real gate on a
	// contributor's laptop and a vacuous one in CI — green exactly where
	// regressions get caught. So the test builds the hazard itself, in its own
	// tree, out of the same package source.
	//
	// The specifier forge wrote is already fully asserted above (home-safe,
	// resolvable, link-stable across installs, idempotent under regenerate);
	// what is left to prove is how the toolchain CONSUMES a linked package,
	// and the copy is the faithful subject for that.
	// The package source is this repo's own web-runtime/. It used to be read
	// back out of the `file:` specifier forge wrote into the frontend
	// manifest; now that the tracked manifest carries only the published
	// range, take it from the checkout directly.
	runtimeSrc := filepath.Join(findRepoRoot(t), "web-runtime")
	shadowed := materializeShadowedRuntimeE2E(t, dir, runtimeSrc)
	setWebRuntimeSpecE2E(t, filepath.Join(webDir, "package.json"), "file:"+shadowed)
	runCmdTimeout(t, webDir, 5*time.Minute,
		"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")

	runCmdTimeout(t, webDir, 5*time.Minute, "npm", "run", "build")
	runCmdTimeout(t, webDir, 2*time.Minute, "npx", "--no-install", "tsc", "--noEmit")

	assertRuntimePeersDedupedE2E(t, nextProdDistDirE2E(t, projectDir, "web"), shadowed)
}

// nextProdDistDirE2E is where `next build` writes its intermediates —
// including `trace` — for the named frontend, which depends on the
// next.config.ts branch its `output:` scaffolded.
//
//   - standalone/server fence production builds into .next-prod, so a build
//     cannot clobber a live `next dev` in .next.
//   - static (the scaffold default since #522) has NO fence: Next treats a
//     custom distDir as the export destination, so the export keeps out/ and
//     the intermediates stay in .next.
//
// The answer comes from forge.yaml rather than a probe of both directories,
// so a trace left in the other one can never stand in for this build's.
func nextProdDistDirE2E(t *testing.T, projectDir, frontend string) string {
	t.Helper()
	cfg, err := config.LoadProjectDir(projectDir)
	if err != nil {
		t.Fatalf("load forge.yaml: %v", err)
	}
	for _, fe := range cfg.Frontends {
		if fe.Name != frontend {
			continue
		}
		dist := ".next-prod"
		if fe.EffectiveOutput() == config.FrontendOutputStatic {
			dist = ".next"
		}
		return filepath.Join(projectDir, "frontends", frontend, dist)
	}
	t.Fatalf("forge.yaml declares no frontend %q", frontend)
	return ""
}

// assertRuntimePeersDedupedE2E is the gate that a type-level or build-level
// check structurally cannot be.
//
// @reliantlabs/forge-web-runtime declares its shared libraries as
// peerDependencies: "the consuming app supplies this copy". npm honours that
// for a registry install — one hoisted copy at the app root — and cannot
// honour it across the `file:` bridge a dev forge writes, because that is a
// symlink to a working checkout carrying its own node_modules, and every
// bundler resolves a module's bare imports by walking up from that module's
// own location. The runtime then binds @connectrpc/connect,
// @bufbuild/protobuf and @opentelemetry/api out of the forge checkout while
// the app binds its own: two copies of each in one bundle, two sets of
// classes, module-level state and registries, every byte shipped twice.
//
// Both copies are the same package at the same version, so the app
// typechecks clean and `next build` succeeds — which is exactly why this has
// to be asserted against the BUILD OUTPUT rather than against types, the
// exit status, or a node_modules walk. (A walk is not enough either: with the
// fix in place the shadow copy is still sitting on the filesystem path; what
// changed is that the bundler no longer resolves through it.)
//
// The signal is `<distDir>/trace`, the file-level record `next build` writes
// of everything it touched. It names absolute paths, so "did the bundler read
// a module out of the linked package's own node_modules" is a substring
// question.
func assertRuntimePeersDedupedE2E(t *testing.T, distDir, runtimeDir string) {
	t.Helper()

	tracePath := filepath.Join(distDir, "trace")
	body, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("no build trace at %s, so the single-copy gate could not run "+
			"(a silently skipped gate is worse than a failing one): %v", tracePath, err)
	}
	trace := string(body)

	// Both spellings: the temp root is itself a symlink on macOS (/var ->
	// /private/var) and the toolchain reports real paths.
	dirs := []string{runtimeDir}
	if real, rerr := filepath.EvalSymlinks(runtimeDir); rerr == nil && real != runtimeDir {
		dirs = append(dirs, real)
	}

	// Anti-vacuity: the runtime's own modules have to be IN this build, or
	// the absence of its node_modules below would prove nothing at all.
	//
	// dist/, not src/: the package publishes built declarations and ships no
	// sources, so what the bundler reads — here and off the registry — is
	// dist/. A trace naming src/ would now mean the app resolved past the
	// package's own "exports" map, which is not a build we want to certify.
	compiled := false
	for _, d := range dirs {
		if strings.Contains(trace, filepath.Join(d, "dist")+string(filepath.Separator)) {
			compiled = true
		}
	}
	if !compiled {
		t.Fatalf("build trace never names %s/dist — the runtime was not compiled into this "+
			"build, so the single-copy assertion is vacuous", runtimeDir)
	}

	seen := map[string]bool{}
	for _, d := range dirs {
		for _, name := range shadowedPackagesE2E(trace, filepath.Join(d, "node_modules")) {
			seen[name] = true
		}
	}
	if len(seen) == 0 {
		return
	}
	dupes := make([]string, 0, len(seen))
	for name := range seen {
		dupes = append(dupes, name)
	}
	sort.Strings(dupes)
	t.Errorf("the build resolved %d package(s) out of the LINKED runtime's own node_modules "+
		"instead of this app's — every one of them is in the bundle twice, with two sets of "+
		"classes and module state: %s\n"+
		"next.config.ts must redirect the linked runtime's bare imports at %s/node_modules.",
		len(dupes), strings.Join(dupes, ", "), filepath.Dir(distDir))
}

// shadowedPackagesE2E returns the distinct package names the trace resolved
// beneath nodeModules.
func shadowedPackagesE2E(trace, nodeModules string) []string {
	prefix := nodeModules + string(filepath.Separator)
	seen := map[string]bool{}
	for _, tail := range strings.Split(trace, prefix)[1:] {
		segs := strings.Split(filepath.ToSlash(tail), "/")
		name := segs[0]
		if strings.HasPrefix(name, "@") && len(segs) > 1 {
			name += "/" + segs[1]
		}
		if name != "" {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

// materializeShadowedRuntimeE2E copies the linked runtime package into the
// test's own tree and installs the package's own devDependencies there, which
// is precisely what gives a DEVELOPED forge checkout the node_modules that
// shadows its consumer. It returns the copy's path.
//
// Installing rather than hand-placing the peers matters: a partial shadow is
// not the hazard, it is a different and much louder one (tsc finds the peer
// but not its @types, and the build fails for a reason no user would ever
// hit). The real thing is a complete install, so do the real thing.
func materializeShadowedRuntimeE2E(t *testing.T, tmpRoot, runtimeSrc string) string {
	t.Helper()
	dst := filepath.Join(tmpRoot, "web-runtime-shadowed")
	copyTreeE2E(t, runtimeSrc, dst, "node_modules")
	runCmdTimeout(t, dst, 5*time.Minute,
		"npm", "install", "--no-audit", "--no-fund", "--prefer-offline")

	shadowed := 0
	for _, peer := range runtimePeersE2E(t, filepath.Join(dst, "package.json")) {
		if _, err := os.Stat(filepath.Join(dst, "node_modules", filepath.FromSlash(peer))); err == nil {
			shadowed++
		}
	}
	if shadowed == 0 {
		t.Fatalf("installing %s left none of its peer dependencies under node_modules; "+
			"the single-copy gate would test nothing", dst)
	}
	return dst
}

// runtimePeersE2E lists the peerDependencies a package manifest declares.
func runtimePeersE2E(t *testing.T, pkgPath string) []string {
	t.Helper()
	var doc struct {
		PeerDependencies map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal([]byte(readFileE2E(t, pkgPath)), &doc); err != nil {
		t.Fatalf("%s is not valid JSON: %v", pkgPath, err)
	}
	if len(doc.PeerDependencies) == 0 {
		t.Fatalf("%s declares no peerDependencies; the runtime is supposed to take its "+
			"shared libraries from the consuming app", pkgPath)
	}
	out := make([]string, 0, len(doc.PeerDependencies))
	for name := range doc.PeerDependencies {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// setWebRuntimeSpecE2E repoints the runtime specifier in a frontend manifest,
// leaving the rest of the file byte-for-byte alone.
func setWebRuntimeSpecE2E(t *testing.T, pkgPath, spec string) {
	t.Helper()
	body := readFileE2E(t, pkgPath)
	key := strconv.Quote("@reliantlabs/forge-web-runtime") + ": "
	old := key + strconv.Quote(webRuntimeSpecE2E(t, body))
	updated := strings.Replace(body, old, key+strconv.Quote(spec), 1)
	if updated == body {
		t.Fatalf("could not repoint the runtime specifier in %s:\n%s", pkgPath, body)
	}
	if err := os.WriteFile(pkgPath, []byte(updated), 0o644); err != nil {
		t.Fatalf("write %s: %v", pkgPath, err)
	}
}

// copyTreeE2E copies a directory tree, skipping any directory named skipDir
// (pass "" to copy everything) and anything that is not a regular file.
func copyTreeE2E(t *testing.T, src, dst, skipDir string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir != "" && d.Name() == skipDir {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), content, info.Mode().Perm())
	})
	if err != nil {
		t.Fatalf("copy %s -> %s: %v", src, dst, err)
	}
}

// webRuntimeSpecE2E extracts the @reliantlabs/forge-web-runtime specifier a
// frontend's package.json declares, failing the test when it declares none.
func webRuntimeSpecE2E(t *testing.T, manifest string) string {
	t.Helper()
	var doc struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(manifest), &doc); err != nil {
		t.Fatalf("package.json is not valid JSON: %v\n%s", err, manifest)
	}
	if spec, ok := doc.Dependencies["@reliantlabs/forge-web-runtime"]; ok {
		return spec
	}
	if spec, ok := doc.DevDependencies["@reliantlabs/forge-web-runtime"]; ok {
		t.Errorf("runtime declared in devDependencies; shipped app code imports it")
		return spec
	}
	t.Fatalf("package.json declares no dependency on the runtime package:\n%s", manifest)
	return ""
}

// assertNoHomePathE2E is the path-hygiene gate: a committed manifest must not
// name the machine it was generated on.
func assertNoHomePathE2E(t *testing.T, path string) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	body := readFileE2E(t, path)
	if strings.Contains(body, home) {
		t.Errorf("%s embeds the home directory %q:\n%s", path, home, body)
	}
	if user := filepath.Base(home); strings.Contains(body, user) {
		t.Errorf("%s embeds the username %q:\n%s", path, user, body)
	}
}

// itemCRUDProtoRuntime is the canonical CRUD-entity proto that carries
// `forge generate` through to the frontend generation steps this test guards.
const itemCRUDProtoRuntime = `syntax = "proto3";

package services.item.v1;

import "forge/v1/forge.proto";
import "google/protobuf/timestamp.proto";

option go_package = "example.com/rtapp/gen/services/item/v1;itemv1";

// ItemService defines the item service RPCs.
service ItemService {
  option (forge.v1.service) = {
    name: "ItemService"
    version: "1.0.0"
    description: "item service"
  };

  // GetItem retrieves an item by ID.
  rpc GetItem(GetItemRequest) returns (GetItemResponse) {}

  // ListItems returns a list of items.
  rpc ListItems(ListItemsRequest) returns (ListItemsResponse) {}

  // CreateItem creates a new item.
  rpc CreateItem(CreateItemRequest) returns (CreateItemResponse) {}
}

// Item represents an item entity.
message Item {
  string id = 1;
  string name = 2;
  string description = 3;
  google.protobuf.Timestamp created_at = 4;
}

message GetItemRequest {
  string id = 1;
}

message GetItemResponse {
  Item item = 1;
}

message ListItemsRequest {
  int32 page_size = 1;
  string page_token = 2;
}

message ListItemsResponse {
  repeated Item items = 1;
  string next_page_token = 2;
}

message CreateItemRequest {
  string name = 1;
  string description = 2;
}

message CreateItemResponse {
  Item item = 1;
}
`

// snapshotDirE2E returns a stable string digest of a directory's files
// (sorted name + content) for idempotency comparison.
func snapshotDirE2E(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read runtime dir %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		b.WriteString(e.Name())
		b.WriteByte('\n')
		b.Write(content)
		b.WriteByte('\n')
	}
	return b.String()
}
