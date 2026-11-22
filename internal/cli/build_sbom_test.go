package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/sbom"
	"github.com/reliant-labs/forge/pkg/release"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// cutSyft is a fake syft for the cut: it answers by source scheme, records
// each source it was asked for, and fails the schemes in fail.
type cutSyft struct {
	mu      sync.Mutex
	sources []string
	fail    map[string]error
}

func (f *cutSyft) Run(_ context.Context, _ string, args, _ []string) ([]byte, error) {
	src := args[1]
	f.mu.Lock()
	f.sources = append(f.sources, src)
	f.mu.Unlock()
	if err := f.fail[src[:strings.Index(src, ":")]]; err != nil {
		return nil, err
	}
	// Like syft, the document names its subject by --source-name (and the
	// source it read, so a test can tell the reads apart).
	name := ""
	for i, a := range args {
		if a == "--source-name" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	return []byte(fmt.Sprintf(`{"bomFormat":"CycloneDX","specVersion":"1.6","metadata":{"component":{"name":%q},"properties":[{"name":"read","value":%q}]}}`, name, src)), nil
}

func (f *cutSyft) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sources...)
}

// useFakeSyft points the cut's SBOM step at a fake syft and a private cache
// for the test. It mutates package vars, so the test must not be parallel.
func useFakeSyft(t *testing.T, fake *cutSyft, cacheDir string) {
	t.Helper()
	prevTool, prevRunner, prevCache := sbomTool, sbomRunner, sbomCacheDir
	sbomTool = func(context.Context) (string, string, error) { return "/fake/syft", "v1.57.2", nil }
	sbomRunner = nil
	if fake != nil {
		sbomRunner = fake
	}
	sbomCacheDir = func() string { return cacheDir }
	t.Cleanup(func() { sbomTool, sbomRunner, sbomCacheDir = prevTool, prevRunner, prevCache })
}

func sbomTestImage(digest string) release.Artifact {
	return release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: digest}}
}

func TestPlanReleaseSBOMTargets_EachArtifactKindHasItsSource(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	mustWrite := func(rel, body string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("frontends/web/package.json", `{"name":"web","private":true}`)
	mustWrite("package-lock.json", `{"lockfileVersion":3}`) // a workspace root lockfile
	mustWrite("frontends/docs/package.json", `{"name":"docs","private":true}`)
	mustWrite("packages/sdk/package.json", `{"name":"@acme/sdk","version":"1.2.0"}`)
	mustWrite("packages/sdk/pnpm-lock.yaml", "lockfileVersion: '9.0'\n")
	mustWrite("pkg/go.mod", "module example.com/acme/pkg\n\ngo 1.24\n")

	e := &KCLEntities{Frontends: []FrontendEntity{
		{Name: "web", Path: "frontends/web", Image: "registry.example.com/acme/web", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}},
		{Name: "gone", Image: "registry.example.com/acme/gone", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}},
	}}
	webKey := hostedStaticDestinationForDecl(e, "registry.example.com/acme/web")
	goneKey := hostedStaticDestinationForDecl(e, "registry.example.com/acme/gone")
	variant := release.Artifact{Kind: release.KindOCI, Mode: release.ModeVariant, Digests: map[string]string{"amd64": sha("v")}}
	carried := sbomTestImage(sha("c"))
	carried.CarriedFrom = "v0.9.0"
	multi := sbomTestImage(sha("m"))
	multi.Platforms = []string{"linux/s390x"}
	artifacts := map[string]release.Artifact{
		"ghcr.io/acme/api":            sbomTestImage(sha("a")),
		"ghcr.io/acme/old":            carried,
		"registry.example.com/whoami": multi,
		webKey:                        sbomTestImage(sha("w")),
		goneKey:                       sbomTestImage(sha("g")),
		"ghcr.io/acme/variant":        variant,
		"cli-linux":                   {Kind: release.KindFile, Mode: release.ModeShared, Version: "linux", Integrity: sha("f")},
		"@acme/sdk":                   {Kind: release.KindNPM, Mode: release.ModeShared, Version: "1.2.0"},
		"example.com/acme/pkg":        {Kind: release.KindGoModule, Mode: release.ModeShared, Version: "v0.3.0"},
		"docs":                        {Kind: release.KindGit, Mode: release.ModeSource, Source: &release.Source{Repo: "github.com/acme/docs", Ref: "main"}},
		"web.sourcemaps":              sbomTestImage(sha("s")),
	}
	in := sbomCut{ProjectDir: dir, OutputDir: "dist", Entities: e, Version: "v1.0.0",
		Built: map[string]bool{"ghcr.io/acme/api": true, "ghcr.io/acme/old": true, webKey: true}}
	targets, refused := planReleaseSBOMTargets(in, artifacts)

	got := map[string]sbom.Target{}
	for _, p := range targets {
		got[p.name] = p.target
	}
	want := map[string]struct {
		kind sbom.Kind
		ref  string
	}{
		"ghcr.io/acme/api":            {sbom.KindImage, "ghcr.io/acme/api@" + sha("a")},
		"ghcr.io/acme/old":            {sbom.KindRegistry, "ghcr.io/acme/old@" + sha("c")},
		"registry.example.com/whoami": {sbom.KindRegistry, "registry.example.com/whoami@" + sha("m")},
		webKey:                        {sbom.KindLockfile, filepath.Join(dir, "package-lock.json")},
		"cli-linux":                   {sbom.KindBinary, filepath.Join(dir, "dist", "cli-linux")},
		"@acme/sdk":                   {sbom.KindLockfile, filepath.Join(dir, "packages/sdk/pnpm-lock.yaml")},
		"example.com/acme/pkg":        {sbom.KindLockfile, filepath.Join(dir, "pkg/go.mod")},
	}
	for name, w := range want {
		tg, ok := got[name]
		if !ok {
			t.Errorf("%s: no SBOM target (refused: %+v)", name, refused[name])
			continue
		}
		if tg.Kind != w.kind || tg.Ref != w.ref || tg.Name != name {
			t.Errorf("%s: target %+v, want kind %s ref %s", name, tg, w.kind, w.ref)
		}
	}
	if got["registry.example.com/whoami"].Platform != "linux/s390x" {
		t.Errorf("a single-platform image is described for its platform: %+v", got["registry.example.com/whoami"])
	}
	if got["cli-linux"].Subject != sha("f") {
		t.Errorf("a binary's subject is its recorded integrity: %+v", got["cli-linux"])
	}
	if len(got) != len(want) {
		t.Errorf("targets %v, want exactly %d", got, len(want))
	}

	// Described but not describable: unavailable, with why.
	for name, says := range map[string]string{goneKey: "no directory", "ghcr.io/acme/variant": "variant"} {
		if r, ok := refused[name]; !ok || r.Status != release.SBOMStatusUnavailable || !strings.Contains(r.Reason, says) {
			t.Errorf("%s: refused %+v, want unavailable saying %q", name, r, says)
		}
	}
	// Not shipped code: neither a target nor a refusal.
	for _, name := range []string{"docs", "web.sourcemaps"} {
		if _, ok := got[name]; ok {
			t.Errorf("%s: an SBOM target for something that ships no code", name)
		}
		if _, ok := refused[name]; ok {
			t.Errorf("%s: recorded unavailable; it should record no SBOM at all", name)
		}
	}
}

func TestGenerateReleaseSBOMs_RecordsAndStoresEachSBOM(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomcut")
	fake := &cutSyft{fail: map[string]error{"docker": errors.New("No such image")}}
	cache := t.TempDir()
	useFakeSyft(t, fake, cache)
	artifacts := map[string]release.Artifact{
		"ghcr.io/acme/api":        sbomTestImage(sha("a")),
		"docker.io/library/redis": sbomTestImage(sha("r")),
	}
	var warn bytes.Buffer
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Env: "prod", Version: "v1.0.0",
		Built: map[string]bool{"ghcr.io/acme/api": true}}, artifacts, &warn)

	store := testStore(t, dir)
	for name, source := range map[string]string{
		// Built, but the daemon did not hold it: read back from the registry.
		"ghcr.io/acme/api":        release.SBOMSourceRegistry,
		"docker.io/library/redis": release.SBOMSourceRegistry,
	} {
		ref := artifacts[name].SBOM
		if ref == nil || ref.Status != release.SBOMStatusOK || ref.Source != source ||
			ref.Format != release.SBOMFormatCycloneDXJSON || ref.Generator != "syft@v1.57.2" {
			t.Fatalf("%s: SBOM %+v", name, ref)
		}
		data, err := store.GetSBOM(ref.Digest)
		if err != nil || !strings.Contains(string(data), name) {
			t.Errorf("%s: the machine ledger does not hold the SBOM %s: %v", name, ref.Digest, err)
		}
	}
	if err := (release.Release{Version: "v1.0.0", Artifacts: artifacts}).Validate(); err != nil {
		t.Errorf("the recorded artifacts no longer validate: %v", err)
	}
	if !strings.Contains(warn.String(), "SBOMs: 2 recorded (0 from cache), 0 unavailable") {
		t.Errorf("summary: %s", warn.String())
	}

	// A second cut of the same digests runs no syft.
	before := len(fake.calls())
	again := map[string]release.Artifact{"ghcr.io/acme/api": sbomTestImage(sha("a"))}
	warn.Reset()
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Version: "v1.0.1",
		Built: map[string]bool{"ghcr.io/acme/api": true}}, again, &warn)
	if n := len(fake.calls()) - before; n != 0 {
		t.Errorf("a cached SBOM ran syft %d more time(s)", n)
	}
	if again["ghcr.io/acme/api"].SBOM.Digest != artifacts["ghcr.io/acme/api"].SBOM.Digest ||
		!strings.Contains(warn.String(), "1 recorded (1 from cache)") {
		t.Errorf("cache hit: %+v / %s", again["ghcr.io/acme/api"].SBOM, warn.String())
	}
}

// One image under two names (pushed to two repositories, or mirrored) is two
// SBOMs: each names its own artifact, so their bytes and ledger digests
// differ, though one cache holds both. A later release of the same names
// reads both from the cache: the release version is not in the document.
func TestGenerateReleaseSBOMs_SameDigestUnderTwoNames(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomtwonames")
	fake := &cutSyft{}
	useFakeSyft(t, fake, t.TempDir())
	names := []string{"ghcr.io/acme/api", "registry.example.com/acme/api"}
	cut := func(version string, names ...string) map[string]release.Artifact {
		artifacts := map[string]release.Artifact{}
		for _, n := range names {
			artifacts[n] = sbomTestImage(sha("same"))
		}
		var warn bytes.Buffer
		generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Env: "prod", Version: version}, artifacts, &warn)
		return artifacts
	}
	// One name, then the other: the second must not be served the first's
	// document from the cache.
	first := cut("v1.0.0", names[0])
	for k, v := range cut("v1.0.0", names[1]) {
		first[k] = v
	}
	store := testStore(t, dir)
	digests := map[string]string{}
	for _, n := range names {
		ref := first[n].SBOM
		if !ref.HasSBOM() {
			t.Fatalf("%s: %+v", n, ref)
		}
		data, err := store.GetSBOM(ref.Digest)
		if err != nil {
			t.Fatalf("%s: the ledger does not hold %s: %v", n, ref.Digest, err)
		}
		if !strings.Contains(string(data), fmt.Sprintf(`"component":{"name":%q}`, n)) {
			t.Errorf("%s: its SBOM names another artifact: %s", n, data)
		}
		digests[n] = ref.Digest
	}
	if digests[names[0]] == digests[names[1]] {
		t.Errorf("two documents naming different artifacts share ledger digest %s", digests[names[0]])
	}
	if n := len(fake.calls()); n != 2 {
		t.Errorf("syft ran %d time(s) for two names, want 2", n)
	}

	second := cut("v1.0.1", names...)
	if n := len(fake.calls()); n != 2 {
		t.Errorf("a later release of the same names ran syft %d more time(s)", n-2)
	}
	for _, n := range names {
		if second[n].SBOM.Digest != digests[n] {
			t.Errorf("%s: v1.0.1 SBOM %s, want the cached %s", n, second[n].SBOM.Digest, digests[n])
		}
	}
}

func TestGenerateReleaseSBOMs_FailureWarnsAndRecordsUnavailable(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomfail")
	useFakeSyft(t, &cutSyft{}, "")
	sbomTool = func(context.Context) (string, string, error) {
		return "", "", errors.New("download syft_1.57.2_linux_amd64.tar.gz: dial tcp: no route to host")
	}
	artifacts := map[string]release.Artifact{"ghcr.io/acme/api": sbomTestImage(sha("a"))}
	var warn bytes.Buffer
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Version: "v2.0.0"}, artifacts, &warn)

	ref := artifacts["ghcr.io/acme/api"].SBOM
	if ref == nil || ref.Status != release.SBOMStatusUnavailable || ref.Digest != "" || !strings.Contains(ref.Reason, "syft is unavailable") {
		t.Fatalf("SBOM %+v, want unavailable naming the cause", ref)
	}
	if !strings.Contains(warn.String(), "release v2.0.0 recorded without an SBOM for ghcr.io/acme/api: syft is unavailable") {
		t.Errorf("warning: %s", warn.String())
	}
	if artifacts["ghcr.io/acme/api"].Digests[release.SharedVariant] != sha("a") {
		t.Error("the artifact itself was changed")
	}
}

func TestGenerateReleaseSBOMs_NoSBOMAndPolicyOff(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomoff")
	fake := &cutSyft{}
	useFakeSyft(t, fake, "")

	artifacts := map[string]release.Artifact{"ghcr.io/acme/api": sbomTestImage(sha("a")), "docs.sourcemaps": sbomTestImage(sha("s"))}
	var warn bytes.Buffer
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Version: "v3.0.0", NoSBOM: true}, artifacts, &warn)
	if ref := artifacts["ghcr.io/acme/api"].SBOM; ref == nil || ref.Status != release.SBOMStatusUnavailable || ref.Reason != "skipped: --no-sbom" {
		t.Errorf("--no-sbom: SBOM %+v", ref)
	}
	if artifacts["docs.sourcemaps"].SBOM != nil {
		t.Error("--no-sbom marked source maps, which never get an SBOM")
	}
	if !strings.Contains(warn.String(), "--no-sbom") {
		t.Errorf("--no-sbom did not warn: %q", warn.String())
	}

	off := &KCLEntities{DeliveryPolicy: func() *release.ShapeDeliveryPolicy {
		p := release.DefaultDeliveryPolicy()
		p.ProvenanceSBOM.Mode = release.DeliveryOff
		return &p
	}()}
	declared := map[string]release.Artifact{"ghcr.io/acme/api": sbomTestImage(sha("a"))}
	warn.Reset()
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Version: "v3.0.1", Entities: off}, declared, &warn)
	if declared["ghcr.io/acme/api"].SBOM != nil || warn.Len() != 0 {
		t.Errorf("provenance_sbom off: SBOM %+v, output %q; want neither", declared["ghcr.io/acme/api"].SBOM, warn.String())
	}
	if n := len(fake.calls()); n != 0 {
		t.Errorf("syft ran %d time(s) with SBOMs off", n)
	}
}

func TestGenerateReleaseSBOMs_PostGenerateHookSeam(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomhook")
	useFakeSyft(t, &cutSyft{}, "")
	prev := sbomPostGenerate
	t.Cleanup(func() { sbomPostGenerate = prev })
	var seen sync.Map
	sbomPostGenerate = []sbomPostGenerateHook{
		func(_ context.Context, in sbomGenerated) (release.SBOMRef, error) {
			seen.Store(in.Name, sbom.Digest(in.Bytes) == in.Artifact.SBOM.Digest && in.Version == "v4.0.0")
			ref := *in.Artifact.SBOM
			ref.Referrer = in.Name + "@" + sha("referrer")
			return ref, nil
		},
		func(_ context.Context, in sbomGenerated) (release.SBOMRef, error) {
			if in.Name == "ghcr.io/acme/b" {
				ref := *in.Artifact.SBOM
				ref.Digest = sha("other")
				return ref, nil // refused: a hook may not swap the SBOM
			}
			return release.SBOMRef{}, errors.New("scanner offline")
		},
	}
	artifacts := map[string]release.Artifact{"ghcr.io/acme/a": sbomTestImage(sha("a")), "ghcr.io/acme/b": sbomTestImage(sha("b"))}
	var warn bytes.Buffer
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Version: "v4.0.0"}, artifacts, &warn)

	for name, art := range artifacts {
		if ok, _ := seen.Load(name); ok != true {
			t.Errorf("%s: the hook did not see the stored SBOM and the cut", name)
		}
		// The first hook's referrer survives the second hook's error and
		// its refused digest swap.
		if art.SBOM == nil || art.SBOM.Status != release.SBOMStatusOK || art.SBOM.Referrer != name+"@"+sha("referrer") {
			t.Errorf("%s: SBOM %+v", name, art.SBOM)
		}
	}
	if !strings.Contains(warn.String(), "scanner offline") || !strings.Contains(warn.String(), "may not change the SBOM") {
		t.Errorf("hook failures were not warned: %s", warn.String())
	}
}

// The cut itself: SBOMs ride on the recorded release, and a cut whose SBOM
// failed is still cut.
func TestCutReleaseFromBuildState_RecordsSBOMs(t *testing.T) {
	testkit.Slow(t, "cuts a release through the real cut path, which runs git for provenance")
	dir := useTestLedger(t, t.TempDir())
	t.Chdir(dir)
	if err := WriteBuildState(dir, "staging", BuildState{
		Image: "demo", Tag: "v1.0.0", Pushed: true, PushedAt: nowRFC3339(), Digest: sha("a"),
	}); err != nil {
		t.Fatal(err)
	}
	fake := &cutSyft{}
	useFakeSyft(t, fake, "")
	opts := buildOptions{run: runOptions{None: true}}
	cut, err := cutReleaseFromBuildState(context.Background(), dir, "staging", "v1.0.0", "", nil, opts)
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	ref := cut.Release.Artifacts["demo"].SBOM
	if ref == nil || ref.Status != release.SBOMStatusOK || ref.Source != release.SBOMSourceImage {
		t.Fatalf("cut SBOM %+v", ref)
	}
	recorded, err := testStore(t, dir).Release("v1.0.0")
	if err != nil || recorded == nil || recorded.Artifacts["demo"].SBOM == nil || recorded.Artifacts["demo"].SBOM.Digest != ref.Digest {
		t.Fatalf("the ledger's record does not carry the SBOM: %+v, %v", recorded.Artifacts["demo"], err)
	}
	if got := fake.calls(); len(got) != 1 || got[0] != "docker:demo@"+sha("a") {
		t.Errorf("syft read %v, want the built image from the daemon", got)
	}

	sbomTool = func(context.Context) (string, string, error) { return "", "", errors.New("no syft for this platform") }
	opts.noSBOM = false
	cut2, err := cutReleaseFromBuildState(context.Background(), dir, "staging", "v1.0.1", "", nil, opts)
	if err != nil {
		t.Fatalf("a cut whose SBOM failed must still cut: %v", err)
	}
	if r := cut2.Release.Artifacts["demo"].SBOM; r == nil || r.Status != release.SBOMStatusUnavailable {
		t.Errorf("failed SBOM recorded as %+v", r)
	}
}

// A hosted image is read with the env's control-plane identity, the one its
// bundle push presents, so a hosted backend CI pushed (which forge never
// `docker login`s for) still gets an SBOM. No other registry sees it.
func TestGenerateReleaseSBOMs_PlatformRegistryReadsUseTheControlPlaneCredential(t *testing.T) {
	dir := newLedgerTestProject(t, "sbomauth")
	fake := &envSyft{}
	useFakeSyft(t, nil, "")
	sbomRunner = fake
	e := &KCLEntities{ControlPlane: &ControlPlaneEntity{Type: "control_plane", Endpoint: "https://cp.example", RegistryHost: "registry.example.com"}}
	// The env's identity as a bundle push memoizes it (bundleRegistryAuthOf
	// needs a control plane); the real registryCredentialFor decides which
	// host it is presented to.
	key := dir + "\x00prod"
	bundleRegistryAuths.Store(key, bundleRegistryAuth{Host: "registry.example.com", credential: func(context.Context) (string, error) {
		return "rlat_cut-secret", nil
	}})
	t.Cleanup(func() { bundleRegistryAuths.Delete(key) })
	prev := sbomRegistryCredential
	t.Cleanup(func() { sbomRegistryCredential = prev })
	var mu sync.Mutex
	lookups := map[string]int{}
	sbomRegistryCredential = func(projectDir, env string, e *KCLEntities, host string) (auth.CredentialFunc, error) {
		mu.Lock()
		lookups[host]++
		mu.Unlock()
		return registryCredentialFor(projectDir, env, e, host)
	}
	artifacts := map[string]release.Artifact{
		"registry.example.com/org/shop/api": sbomTestImage(sha("a")), // a hosted backend CI pushed
		"ghcr.io/acme/worker":               sbomTestImage(sha("b")),
	}
	var warn bytes.Buffer
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Env: "prod", Version: "v1.0.0", Entities: e}, artifacts, &warn)

	for name, art := range artifacts {
		if !art.SBOM.HasSBOM() {
			t.Fatalf("%s: %+v (%s)", name, art.SBOM, warn.String())
		}
	}
	platform, other := fake.env("registry:registry.example.com/org/shop/api@"+sha("a")), fake.env("registry:ghcr.io/acme/worker@"+sha("b"))
	for _, want := range []string{"SYFT_REGISTRY_AUTH_AUTHORITY=registry.example.com", "SYFT_REGISTRY_AUTH_USERNAME=" + platformRegistryUsername, "SYFT_REGISTRY_AUTH_PASSWORD=rlat_cut-secret"} {
		if !containsString(platform, want) {
			t.Errorf("platform read env %q lacks %q", platform, want)
		}
	}
	if len(other) != 0 {
		t.Errorf("the control-plane credential leaked to ghcr.io: %q", other)
	}
	if lookups["registry.example.com"] != 1 || lookups["ghcr.io"] != 1 {
		t.Errorf("credential lookups %v: want one per registry read", lookups)
	}

	// An env with no control plane has no platform registry: nothing is
	// resolved and nothing is passed.
	plain := map[string]release.Artifact{"registry.example.com/org/shop/api": sbomTestImage(sha("c"))}
	generateReleaseSBOMs(context.Background(), sbomCut{ProjectDir: dir, Env: "dev", Version: "v1.0.1"}, plain, &warn)
	if got := fake.env("registry:registry.example.com/org/shop/api@" + sha("c")); len(got) != 0 {
		t.Errorf("no control plane, yet env %q", got)
	}
}

// envSyft records the environment each syft source ran with.
type envSyft struct {
	mu   sync.Mutex
	envs map[string][]string
}

func (f *envSyft) Run(_ context.Context, _ string, args, env []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.envs == nil {
		f.envs = map[string][]string{}
	}
	f.envs[args[1]] = env
	return []byte(`{"bomFormat":"CycloneDX","specVersion":"1.6"}`), nil
}

func (f *envSyft) env(src string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.envs[src]
}
