package cli

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

const testMapDoc = `{"version":3,"sources":["../src/app.ts"],"names":[],"mappings":"AAAA"}`

func hostedEntities(obs *ObservabilityEntity) *KCLEntities {
	return &KCLEntities{
		ControlPlane:  testControlPlane("org-1", "registry.example"),
		Observability: obs,
	}
}

// "Hosted observability" is the decision between pushing maps and keeping them:
// it is true exactly when browser errors from the env will land in the
// platform's store.
func TestSourcemapsHosted(t *testing.T) {
	on := func() *ObservabilityEntity {
		return &ObservabilityEntity{Enabled: true, Policy: &ObservabilityPolicyEntity{Enabled: true}}
	}
	for name, tc := range map[string]struct {
		e    *KCLEntities
		want bool
	}{
		"no render":                   {nil, false},
		"no control plane":            {&KCLEntities{}, false},
		"control plane, no endpoint":  {&KCLEntities{ControlPlane: &ControlPlaneEntity{}}, false},
		"control plane, default on":   {hostedEntities(nil), true},
		"observability on":            {hostedEntities(on()), true},
		"observability off":           {hostedEntities(&ObservabilityEntity{Enabled: false}), false},
		"browser category opted out":  {hostedEntities(&ObservabilityEntity{Enabled: true, Policy: &ObservabilityPolicyEntity{Enabled: true, Disabled: []string{"browser"}}}), false},
		"other category opted out":    {hostedEntities(&ObservabilityEntity{Enabled: true, Policy: &ObservabilityPolicyEntity{Enabled: true, Disabled: []string{"logs"}}}), true},
		"destination of its own":      {hostedEntities(&ObservabilityEntity{Enabled: true, Destination: &ObservabilityDestEntity{Endpoint: "https://otlp.example"}}), false},
		"destination is the platform": {hostedEntities(&ObservabilityEntity{Enabled: true, Destination: &ObservabilityDestEntity{Endpoint: "https://otlp.example", TokenFromControlPlane: true}}), true},
	} {
		if got := sourcemapsHosted(tc.e); got != tc.want {
			t.Errorf("%s: hosted = %v, want %v", name, got, tc.want)
		}
	}
}

func TestSourcemapPublisherForPicksByObservability(t *testing.T) {
	dir := t.TempDir()
	if _, ok := sourcemapPublisherFor(dir, "dev", &KCLEntities{}).(deploytarget.LocalSourcemaps); !ok {
		t.Error("an env with no hosted observability must keep maps locally")
	}
	if _, ok := sourcemapPublisherFor(dir, "prod", hostedEntities(nil)).(deploytarget.RegistrySourcemaps); !ok {
		t.Error("an env with hosted observability must push to the registry")
	}
}

// pushedMaps records what reached the (stubbed) registry.
type pushedMaps struct{ repo, tag, digest string }

func stubSourcemapsRegistry(t *testing.T, digest string) *pushedMaps {
	t.Helper()
	got := &pushedMaps{digest: digest}
	prev := sourcemapsRegistryPush
	sourcemapsRegistryPush = func(_ context.Context, repo, tag string, _ deploytarget.SourcemapBuild, layer []byte) (string, error) {
		if len(layer) == 0 {
			t.Error("pushed an empty layer")
		}
		got.repo, got.tag = repo, tag
		return digest, nil
	}
	t.Cleanup(func() { sourcemapsRegistryPush = prev })
	return got
}

func writeDist(t *testing.T) string {
	t.Helper()
	dist := filepath.Join(t.TempDir(), "dist")
	for rel, body := range map[string]string{
		"index.html":          "<html></html>",
		"assets/app-1.js":     "x();\n//# sourceMappingURL=app-1.js.map\n",
		"assets/app-1.js.map": testMapDoc,
	} {
		p := filepath.Join(dist, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dist
}

// The release records the maps its OnHosted site was built with, as
// `<frontend>.sourcemaps`, and the build id on the site artifact too.
func TestHarvestSourcemapsArtifacts(t *testing.T) {
	dir := t.TempDir()
	digest := "sha256:" + strings.Repeat("5", 64)
	site := "registry.example/org-1/acme/web/static.v1"
	siteDigest := "sha256:" + strings.Repeat("6", 64)
	e := hostedEntities(nil)
	e.Frontends = []FrontendEntity{
		{Name: "web", Image: "registry.example/org-1/acme/web", Runtime: FrontendRuntime{Type: RuntimeHosted}},
		{Name: "docs", Image: "registry.example/org-1/acme/docs", Runtime: FrontendRuntime{Type: RuntimeHosted}},
		{Name: "admin", Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase}},
	}
	if err := deploytarget.WriteSourcemapsState(dir, "prod", deploytarget.SourcemapsRecord{
		Frontend: "web", BuildID: "3f9c0e1a2b4d", Maps: 4, Repository: "registry.example/org-1/acme/web-sourcemaps", Tag: "3f9c0e1a2b4d", Digest: digest,
	}); err != nil {
		t.Fatal(err)
	}
	// docs filed locally only; admin is not hosted: neither is recorded.
	if err := deploytarget.WriteSourcemapsState(dir, "prod", deploytarget.SourcemapsRecord{Frontend: "docs", BuildID: "b", Dir: "/x"}); err != nil {
		t.Fatal(err)
	}
	if err := deploytarget.WriteSourcemapsState(dir, "prod", deploytarget.SourcemapsRecord{Frontend: "admin", BuildID: "b", Repository: "r", Digest: digest}); err != nil {
		t.Fatal(err)
	}

	artifacts := map[string]release.Artifact{
		site: {Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: siteDigest}},
	}
	if n := harvestSourcemapsArtifacts(dir, "prod", e, artifacts); n != 1 {
		t.Fatalf("recorded %d maps artifacts, want 1: %+v", n, artifacts)
	}
	maps, ok := artifacts["web.sourcemaps"]
	if !ok || len(artifacts) != 2 {
		t.Fatalf("artifacts = %+v", artifacts)
	}
	want := release.Artifact{
		Kind: release.KindOCI, Mode: release.ModeShared, Digests: map[string]string{release.SharedVariant: digest},
		Version: "3f9c0e1a2b4d", URI: "registry.example/org-1/acme/web-sourcemaps",
	}
	if maps.Kind != want.Kind || maps.Mode != want.Mode || maps.Digests["*"] != digest || maps.Version != want.Version || maps.URI != want.URI {
		t.Errorf("maps artifact = %+v, want %+v", maps, want)
	}
	if err := maps.Validate("web.sourcemaps"); err != nil {
		t.Errorf("the recorded artifact does not validate: %v", err)
	}
	if got := artifacts[site]; got.Version != "3f9c0e1a2b4d" || got.Digests["*"] != siteDigest {
		t.Errorf("the site artifact must carry the same build id and keep its digest: %+v", got)
	}
	// It is a release artifact like any other: valid, but not a container image.
	rel := release.Release{Version: "v1", Artifacts: artifacts}
	if n := countOCIArtifacts(rel); n != 1 {
		t.Errorf("image count = %d; the maps artifact is not an image", n)
	}
	// The harvest is by env: another env's record is not this one's.
	if n := harvestSourcemapsArtifacts(dir, "staging", e, map[string]release.Artifact{}); n != 0 {
		t.Errorf("another env's maps were recorded: %d", n)
	}
}

// A maps artifact's address is its URI, never URI + name.
func TestReleaseImageRepositoryOfSourcemaps(t *testing.T) {
	art := release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, URI: "registry.example/org-1/acme/web-sourcemaps"}
	if got := releaseImageRepository("web.sourcemaps", art); got != art.URI {
		t.Errorf("repository = %q, want the URI whole", got)
	}
	// A legacy image keyed by bare name still composes URI + name.
	legacy := release.Artifact{Kind: release.KindOCI, Mode: release.ModeShared, URI: "ghcr.io/acme"}
	if got := releaseImageRepository("api", legacy); got != "ghcr.io/acme/api" {
		t.Errorf("legacy repository = %q", got)
	}
	// And a legacy promotion key is not re-keyed onto the maps' URI.
	resolved := map[string]string{"web.sourcemaps": "sha256:x", "api": "sha256:y"}
	got := expandLegacyLedgerKeys(resolved, map[string]string{"web.sourcemaps": art.URI, "api": "ghcr.io/acme"})
	if got["web.sourcemaps"] != "sha256:x" || got["ghcr.io/acme/api"] != "sha256:y" || len(got) != 2 {
		t.Errorf("expanded = %v", got)
	}
}

func TestFrontendSourcemapsPushRequiresItsFlags(t *testing.T) {
	_, err := runForge(t, "frontend", "sourcemaps", "push", "dist")
	if err == nil || !strings.Contains(err.Error(), "required flag") {
		t.Fatalf("push without flags = %v", err)
	}
	if _, err := runForge(t, "frontend", "sourcemaps", "nope"); err == nil {
		t.Error("an unknown subcommand was accepted")
	}
}

// A dist directory forge did not build, for an env with hosted observability,
// reaches the registry under <base>/<frontend>-sourcemaps:<build id>.
func TestFrontendSourcemapsPushToTheRegistry(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := writeHostedStaticProject(t, "https://cp.example")
	t.Chdir(dir)
	stubControlPlaneOrg(t, "org-1")
	stubDockerLoginArgs(t)
	t.Setenv("ACME_CP_TOKEN", "rlat_test")
	t.Setenv("FORGE_HOME", t.TempDir())
	got := stubSourcemapsRegistry(t, "sha256:"+strings.Repeat("7", 64))
	dist := writeDist(t)
	before, _ := os.ReadFile(filepath.Join(dist, "assets/app-1.js"))

	out, err := runForge(t, "frontend", "sourcemaps", "push", dist, "--frontend", "desktop", "--build-id", "3f9c0e1a2b4d", "--env", "hosted", "--base-path", "/app")
	if err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if want := "registry.reliantapi.com/org-1/acme/desktop-sourcemaps"; got.repo != want || got.tag != "3f9c0e1a2b4d" {
		t.Errorf("filed at %s:%s, want %s:3f9c0e1a2b4d\n%s", got.repo, got.tag, want, out)
	}
	if !strings.Contains(out, "filed as "+got.repo+":3f9c0e1a2b4d") {
		t.Errorf("output does not say where the maps went:\n%s", out)
	}
	after, _ := os.ReadFile(filepath.Join(dist, "assets/app-1.js"))
	if string(before) != string(after) {
		t.Error("the dist directory was modified")
	}
	st, err := deploytarget.ReadSourcemapsState(dir, "hosted", "desktop")
	if err != nil || st == nil || !st.Pushed() || st.Digest != got.digest {
		t.Errorf("state = %+v, %v", st, err)
	}
}

// With --dry-run nothing is pushed or kept; with nothing to extract it fails.
func TestFrontendSourcemapsPushDryRunAndEmpty(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; skipped in -short")
	}
	dir := writeHostedStaticProject(t, "https://cp.example")
	t.Chdir(dir)
	stubControlPlaneOrg(t, "org-1")
	stubDockerLoginArgs(t)
	t.Setenv("ACME_CP_TOKEN", "rlat_test")
	t.Setenv("FORGE_HOME", t.TempDir())
	prev := sourcemapsRegistryPush
	sourcemapsRegistryPush = func(context.Context, string, string, deploytarget.SourcemapBuild, []byte) (string, error) {
		return "", errors.New("must not push")
	}
	t.Cleanup(func() { sourcemapsRegistryPush = prev })

	out, err := runForge(t, "frontend", "sourcemaps", "push", writeDist(t), "--frontend", "desktop", "--build-id", "b1", "--env", "hosted", "--dry-run")
	if err != nil || !strings.Contains(out, "--dry-run: nothing pushed") {
		t.Fatalf("dry-run = %v\n%s", err, out)
	}
	if st, _ := deploytarget.ReadSourcemapsState(dir, "hosted", "desktop"); st != nil {
		t.Error("a dry run recorded a push")
	}

	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, "index.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = runForge(t, "frontend", "sourcemaps", "push", empty, "--frontend", "desktop", "--build-id", "b1", "--env", "hosted")
	if err == nil || !strings.Contains(err.Error(), "no source maps found") {
		t.Fatalf("a dist with no maps = %v", err)
	}
	// A name or id the registry cannot hold is refused before any work.
	for _, args := range [][]string{
		{"--frontend", "Bad_Name", "--build-id", "b1"},
		{"--frontend", "desktop", "--build-id", "not a tag"},
	} {
		full := append([]string{"frontend", "sourcemaps", "push", writeDist(t), "--env", "hosted"}, args...)
		if _, err := runForge(t, full...); err == nil {
			t.Errorf("%v was accepted", args)
		}
	}
}

// THE WHOLE CHAIN, through the real commands, for an OnHosted frontend whose
// observability is hosted: the build files the maps BEFORE pushing the site, the
// cut records them beside the site as `web.sourcemaps` carrying the same build
// id, and the release still promotes and deploys with the extra artifact in it.
func TestHostedBuildFilesMapsAndTheReleaseRecordsThem(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake := newFakeDeployService(map[string]string{})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := writeHostedStaticProject(t, srv.URL)
	t.Chdir(dir)
	t.Setenv("ACME_CP_TOKEN", "rlat_e2e")
	t.Setenv("FORGE_HOME", t.TempDir())
	prevPoll := hostedPollInterval
	hostedPollInterval = time.Millisecond
	t.Cleanup(func() { hostedPollInterval = prevPoll })
	stubHostedBundleRegistry(t, fake)
	stubControlPlaneOrg(t, testStaticOrg)
	stubDockerLoginArgs(t)
	mapsDigest := "sha256:" + strings.Repeat("8", 64)
	filed := stubSourcemapsRegistry(t, mapsDigest)

	// The pusher stands in for build+pack+push of the site, and, like the real
	// one, runs the stage's map filing BEFORE the site push. It must be handed
	// the publisher the build chose.
	prevPush := hostedStaticPusher
	hostedStaticPusher = func(ctx context.Context, _, _ string, fe deploytarget.StaticSiteFrontend) (string, error) {
		if fe.SourcemapPublisher == nil {
			t.Error("the hosted build handed the frontend no source map publisher")
			return hostedStaticDigest, nil
		}
		sidecar := t.TempDir()
		for rel, body := range map[string]string{"manifest.json": "{}", "assets/a.js.map": testMapDoc} {
			p := filepath.Join(sidecar, rel)
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, []byte(body), 0o644)
		}
		if err := deploytarget.FileStageSourcemaps(ctx, fe.SourcemapPublisher, "build", fe.Project, fe.Name,
			deploytarget.StageResult{BuildID: "3f9c0e1a2b4d", SourcemapsDir: sidecar, Sourcemaps: 1}); err != nil {
			return "", err
		}
		return hostedStaticDigest, nil
	}
	t.Cleanup(func() { hostedStaticPusher = prevPush })

	if out, err := runForge(t, "env", "build", "hosted", "--push", "--tag", "t1"); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if want := "registry.reliantapi.com/" + testStaticOrg + "/acme/web-sourcemaps"; filed.repo != want || filed.tag != "3f9c0e1a2b4d" {
		t.Fatalf("maps filed at %s:%s, want %s:3f9c0e1a2b4d", filed.repo, filed.tag, want)
	}
	if out, err := runForge(t, "env", "build", "hosted", "--release", "v1", "--no-build"); err != nil {
		t.Fatalf("cut: %v\n%s", err, out)
	}
	byName := map[string]wireArtifact{}
	for _, a := range fake.releases["v1"].Artifacts {
		byName[a.Name] = a
	}
	maps, ok := byName["web.sourcemaps"]
	if !ok || len(byName) != 2 {
		t.Fatalf("release artifacts = %+v; want the site and web.sourcemaps", fake.releases["v1"].Artifacts)
	}
	if maps.Kind != "oci" || maps.Mode != "shared" || maps.Digest != mapsDigest || maps.Version != "3f9c0e1a2b4d" || maps.URI != filed.repo {
		t.Errorf("maps artifact = %+v", maps)
	}
	if site := byName["ghcr.io/acme/web/static.v1"]; site.Version != "3f9c0e1a2b4d" || site.Digest != hostedStaticDigest {
		t.Errorf("the site artifact must carry the same build id and its own digest: %+v", site)
	}
	// The extra artifact does not break promotion or the deploy.
	if out, err := runForge(t, "env", "deploy", "hosted", "v1", "--yes", "--no-wait"); err != nil {
		t.Fatalf("promote: %v\n%s", err, out)
	}
	if out, err := runForge(t, "env", "deploy", "hosted", "--yes", "--no-wait", "--rollout-timeout", "2s"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if d := fake.bundles[fake.envs["hosted"]]["web"]; d == nil || d.Spec["liveDigest"] != hostedStaticDigest {
		t.Errorf("deployed spec = %+v", d)
	}
}

// A rebuild that files no maps must not leave the PREVIOUS build's record to be
// cut into the new release.
func TestHostedBuildClearsAStaleMapsRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL and runs the whole CLI; skipped in -short")
	}
	fake := newFakeDeployService(map[string]string{})
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	dir := writeHostedStaticProject(t, srv.URL)
	t.Chdir(dir)
	t.Setenv("ACME_CP_TOKEN", "rlat_e2e")
	t.Setenv("FORGE_HOME", t.TempDir())
	stubControlPlaneOrg(t, testStaticOrg)
	stubDockerLoginArgs(t)
	if err := deploytarget.WriteSourcemapsState(dir, "hosted", deploytarget.SourcemapsRecord{
		Frontend: "web", BuildID: "old", Repository: "r/o/p/web-sourcemaps", Digest: "sha256:" + strings.Repeat("9", 64),
	}); err != nil {
		t.Fatal(err)
	}
	prevPush := hostedStaticPusher
	hostedStaticPusher = func(context.Context, string, string, deploytarget.StaticSiteFrontend) (string, error) {
		return hostedStaticDigest, nil // this build extracted no maps
	}
	t.Cleanup(func() { hostedStaticPusher = prevPush })
	if out, err := runForge(t, "env", "build", "hosted", "--push", "--tag", "t1"); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if st, _ := deploytarget.ReadSourcemapsState(dir, "hosted", "web"); st != nil {
		t.Errorf("a stale record survived a build that filed nothing: %+v", st)
	}
}
