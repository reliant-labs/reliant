package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/webruntimepeers"
)

// An ADOPTED frontend's package.json is reconciled like a scaffolded one's:
// forge regenerates hooks into it that call the runtime at the floor this
// forge generates against, so leaving the declared range below it hands the
// owner a tree that does not typecheck (reliant's web app bumped it by hand on
// every forge pin). Disowning the manifest is the opt-out.

// The browser SDK is declared already (it is the other range forge manages),
// so the runtime floor is the one change under test.
var adoptedStaleManifest = `{
  "name": "web",
  "dependencies": {
    "@hyperdx/browser": "` + browserSDKSpec() + `",
    "` + generator.WebRuntimePackage + `": "0.1.0"
  }
}
`

var adoptedStaleLock = `{"lockfileVersion":3,"packages":{"":{"name":"web","dependencies":{"@hyperdx/browser":"` +
	browserSDKSpec() + `","` + generator.WebRuntimePackage + `":"0.1.0"}}}}`

func browserSDKSpec() string {
	spec, _ := webruntimepeers.PeerSpec("@hyperdx/browser")
	return spec
}

// adoptedStaleFrontend writes an adopted Vite frontend declaring the runtime
// below the floor, with a lockfile recording the same stale range, and a fake
// npm that records each call and does what a lockfile-only install does.
func adoptedStaleFrontend(t *testing.T) (dir string, fe config.FrontendConfig, npmCalls func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake npm")
	}
	t.Setenv("FORGE_SKIP_NPM_INSTALL", "")
	dir = t.TempDir()
	writeAdoptionFile(t, dir, "frontends/web/package.json", adoptedStaleManifest)
	writeAdoptionFile(t, dir, "frontends/web/package-lock.json", adoptedStaleLock)

	bin := t.TempDir()
	logPath := filepath.Join(bin, "calls.log")
	// Copy the manifest's runtime range into the lockfile, as
	// `npm install --package-lock-only` would.
	script := "#!/bin/sh\necho \"$*\" >> '" + logPath + "'\n" +
		`want=$(sed -n 's|.*"` + generator.WebRuntimePackage + `": *"\([^"]*\)".*|\1|p' package.json)` + "\n" +
		`sed "s|\"0.1.0\"|\"$want\"|" package-lock.json > lock.new && mv lock.new package-lock.json` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	npmCalls = func() string {
		b, _ := os.ReadFile(logPath)
		return string(b)
	}
	return dir, adoptedWeb("vite-spa"), npmCalls
}

func declaredRuntimeRange(t *testing.T, manifest string) string {
	t.Helper()
	marker := `"` + generator.WebRuntimePackage + `": "`
	i := strings.Index(manifest, marker)
	if i < 0 {
		t.Fatalf("manifest does not declare %s:\n%s", generator.WebRuntimePackage, manifest)
	}
	rest := manifest[i+len(marker):]
	return rest[:strings.IndexByte(rest, '"')]
}

func TestAdoptedFrontendRuntimeRangeIsRaisedWithItsLockfile(t *testing.T) {
	checksums.SetTier1TargetSet(map[string]bool{})
	dir, fe, npmCalls := adoptedStaleFrontend(t)
	cfg := &config.ProjectConfig{Name: "app", Frontends: []config.FrontendConfig{fe}}

	reconcileFrontendDependencies(dir, "frontends/web", fe, nil)
	if err := syncFrontendLocks(cfg, dir, nil); err != nil {
		t.Fatalf("syncFrontendLocks: %v", err)
	}

	got := declaredRuntimeRange(t, readAdoptionFile(t, dir, "frontends/web/package.json"))
	if got == "0.1.0" {
		t.Fatal("an adopted frontend below the runtime floor was left there")
	}
	if !strings.Contains(npmCalls(), "--package-lock-only") {
		t.Fatalf("the lockfile was not refreshed after the range moved (npm calls: %q)", npmCalls())
	}
	if lock := readAdoptionFile(t, dir, "frontends/web/package-lock.json"); !strings.Contains(lock, `"`+got+`"`) {
		t.Fatalf("the lockfile still records the old range, so `npm ci` fails:\n%s", lock)
	}
	if !checksums.IsTier1Target("frontends/web/package.json") {
		t.Error("the manifest forge edits is not recorded as a target, so a disown of it would be retired as obsolete")
	}

	// Converged: a second run changes nothing and runs no package manager.
	before := npmCalls()
	reconcileFrontendDependencies(dir, "frontends/web", fe, nil)
	if err := syncFrontendLocks(cfg, dir, nil); err != nil {
		t.Fatal(err)
	}
	if npmCalls() != before {
		t.Error("an agreeing lockfile must not invoke the package manager again")
	}
}

func TestDisownedFrontendManifestIsLeftAloneAndItsDisownKept(t *testing.T) {
	checksums.SetTier1TargetSet(map[string]bool{})
	dir, fe, npmCalls := adoptedStaleFrontend(t)
	cfg := &config.ProjectConfig{Name: "app", Frontends: []config.FrontendConfig{fe}}
	cs := &checksums.FileChecksums{Disowned: map[string]checksums.DisownedEntry{
		"frontends/web/package.json": {Reason: "the app pins its own runtime"},
	}}

	reconcileFrontendDependencies(dir, "frontends/web", fe, cs)
	if err := syncFrontendLocks(cfg, dir, cs); err != nil {
		t.Fatalf("syncFrontendLocks: %v", err)
	}

	if got := readAdoptionFile(t, dir, "frontends/web/package.json"); got != adoptedStaleManifest {
		t.Fatalf("a disowned package.json was edited:\n%s", got)
	}
	if got := readAdoptionFile(t, dir, "frontends/web/package-lock.json"); got != adoptedStaleLock {
		t.Fatalf("a disowned package.json's lockfile was refreshed:\n%s", got)
	}
	if calls := npmCalls(); calls != "" {
		t.Fatalf("a package manager ran for a disowned manifest: %q", calls)
	}
	if retired := checksums.RetireObsoleteDisowns(cs, nil); len(retired) != 0 {
		t.Fatalf("the manifest's disown was retired as obsolete: %v", retired)
	}
}

// `forge project disown` has to accept the manifest the opt-out names, though
// it carries no forge:hash marker.
func TestRunDisown_AcceptsFrontendManifest(t *testing.T) {
	root := withDisownProjectRoot(t, nil)
	const rel = "frontends/web/package.json"
	seedDisownFile(t, root, rel, adoptedStaleManifest)
	seedDisownFile(t, root, "frontends/web/vite.config.ts", "export default {};\n")

	if err := runDisown([]string{rel}, "the app pins its own runtime", false); err != nil {
		t.Fatalf("runDisown: %v", err)
	}
	cs, err := checksums.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.IsDisowned(rel) {
		t.Fatalf("expected %s to be disowned; disowned set = %v", rel, cs.Disowned)
	}
	if got := readAdoptionFile(t, root, rel); got != adoptedStaleManifest {
		t.Fatalf("disown rewrote the manifest:\n%s", got)
	}

	// A package.json that is not a frontend's stays refused.
	seedDisownFile(t, root, "tools/thing/package.json", "{}\n")
	if err := runDisown([]string{"tools/thing/package.json"}, "not forge's", false); err == nil {
		t.Fatal("disown accepted a package.json forge never edits")
	}
}
