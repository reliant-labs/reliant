package deploytarget

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/reliant-labs/forge/pkg/release"
)

// stagedSidecar runs the F1 stage over the Vite-shaped fixture and returns the
// sidecar it produced, exactly as a deploy hands it to the publisher.
func stagedSidecar(t *testing.T) (StageResult, StagePlan) {
	t.Helper()
	projectDir, _ := buildFixture(t)
	plan := fixturePlan(t, projectDir, "")
	res, err := runStagePlan(context.Background(), &fakeRunner{}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.SourcemapsDir == "" || res.Sourcemaps != 3 {
		t.Fatalf("stage result = %+v", res)
	}
	return res, plan
}

func untar(t *testing.T, layer []byte) map[string]string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(layer))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

func fetchManifest(t *testing.T, store *memory.Store, ref string) (ocispec.Descriptor, ocispec.Manifest) {
	t.Helper()
	desc, err := store.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("resolve %q: %v", ref, err)
	}
	rc, err := store.Fetch(context.Background(), desc)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	raw, _ := io.ReadAll(rc)
	var m ocispec.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return desc, m
}

// The artifact the symbolicator reads: media types, the layer's layout, and the
// tag being the build id.
func TestPushSourcemapsArtifactFormat(t *testing.T) {
	res, plan := stagedSidecar(t)
	layer, err := PackSourcemaps(res.SourcemapsDir)
	if err != nil {
		t.Fatal(err)
	}
	build := SourcemapBuild{Project: plan.Project, Frontend: plan.Name, BuildID: res.BuildID, Dir: res.SourcemapsDir, Maps: res.Sourcemaps}

	store := memory.New()
	digest, err := pushSourcemaps(context.Background(), store, res.BuildID, build, layer)
	if err != nil {
		t.Fatal(err)
	}

	desc, m := fetchManifest(t, store, res.BuildID)
	if desc.Digest.String() != digest {
		t.Errorf("tag %q resolves to %s, push returned %s", res.BuildID, desc.Digest, digest)
	}
	// The wire strings are the contract; pin them literally, not via the constants.
	if m.ArtifactType != "application/vnd.forge.sourcemaps.v1" {
		t.Errorf("artifactType = %q", m.ArtifactType)
	}
	if m.MediaType != ocispec.MediaTypeImageManifest || m.SchemaVersion != 2 {
		t.Errorf("manifest = %s v%d", m.MediaType, m.SchemaVersion)
	}
	if m.Config.MediaType != ocispec.MediaTypeEmptyJSON {
		t.Errorf("config = %s, want the empty descriptor", m.Config.MediaType)
	}
	if len(m.Layers) != 1 || m.Layers[0].MediaType != "application/vnd.forge.sourcemaps.v1.tar+gzip" {
		t.Fatalf("layers = %+v", m.Layers)
	}
	if got := m.Annotations; got[SourcemapsAnnotationBuildID] != res.BuildID || got[SourcemapsAnnotationFrontend] != "web" ||
		got[SourcemapsAnnotationProject] != "acme" || got[ocispec.AnnotationCreated] != fixedCreated {
		t.Errorf("annotations = %v", got)
	}

	// The layer is the sidecar: manifest.json plus every map at the SAME
	// relative path it has in the served site.
	files := untar(t, layer)
	for _, want := range []string{"manifest.json", "app/assets/index-abc.js.map", "app/assets/chunk-def.mjs.map", "app/assets/style-123.css.map"} {
		if _, ok := files[want]; !ok {
			t.Errorf("layer lacks %s; has %v", want, keys(files))
		}
	}
	if len(files) != 4 {
		t.Errorf("layer holds %d files, want manifest.json + 3 maps: %v", len(files), keys(files))
	}
	var mf SourcemapManifest
	if err := json.Unmarshal([]byte(files["manifest.json"]), &mf); err != nil {
		t.Fatal(err)
	}
	if mf.Schema != SourcemapManifestSchema || mf.BuildID != res.BuildID || len(mf.Files) != 3 {
		t.Errorf("manifest.json = %+v", mf)
	}
	for _, f := range mf.Files {
		if files[f.Map] == "" {
			t.Errorf("manifest names %s, which the layer lacks", f.Map)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Re-pushing an unchanged build yields the same digest: the manifest depends on
// the content and the build identity alone.
func TestPushSourcemapsIsDeterministic(t *testing.T) {
	res, plan := stagedSidecar(t)
	layer, err := PackSourcemaps(res.SourcemapsDir)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PackSourcemaps(res.SourcemapsDir)
	if err != nil || !bytes.Equal(layer, again) {
		t.Fatalf("identical sidecars packed to different bytes (err %v)", err)
	}
	build := SourcemapBuild{Project: plan.Project, Frontend: plan.Name, BuildID: res.BuildID}
	store := memory.New()
	d1, err := pushSourcemaps(context.Background(), store, res.BuildID, build, layer)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := pushSourcemaps(context.Background(), store, res.BuildID, build, layer)
	if err != nil || d1 != d2 {
		t.Fatalf("re-push: %v, %s != %s", err, d1, d2)
	}
	// A different build id is a different artifact even over identical bytes
	// (the annotations carry it), so two builds never share a digest.
	other, err := pushSourcemaps(context.Background(), memory.New(), "other", SourcemapBuild{Project: "acme", Frontend: "web", BuildID: "other"}, layer)
	if err != nil || other == d1 {
		t.Fatalf("a different build id shared the digest: %v %s", err, other)
	}
}

func TestPackSourcemapsRefusesNonSidecars(t *testing.T) {
	if _, err := PackSourcemaps(t.TempDir()); err == nil || !strings.Contains(err.Error(), SourcemapManifestName) {
		t.Fatalf("a directory with no manifest packed: %v", err)
	}
}

// The registry publisher files the maps at <base>/<frontend>-sourcemaps:<tag>
// and records what it did.
func TestRegistrySourcemapsFilesUnderTheBuildID(t *testing.T) {
	res, plan := stagedSidecar(t)
	projectDir := t.TempDir()

	var gotRepo, gotTag string
	var gotLayer []byte
	pub := RegistrySourcemaps{
		ProjectDir: projectDir, Env: "prod",
		Base: func(context.Context) (string, error) { return "registry.example/org-1/acme", nil },
		Push: func(_ context.Context, repo, tag string, _ SourcemapBuild, layer []byte) (string, error) {
			gotRepo, gotTag, gotLayer = repo, tag, layer
			return "sha256:" + strings.Repeat("c", 64), nil
		},
	}
	rec, err := pub.PublishSourcemaps(context.Background(), SourcemapBuild{
		Project: plan.Project, Frontend: plan.Name, BuildID: res.BuildID, Dir: res.SourcemapsDir, Maps: res.Sourcemaps,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != "registry.example/org-1/acme/web-sourcemaps" || gotTag != res.BuildID {
		t.Errorf("filed at %s:%s", gotRepo, gotTag)
	}
	if _, ok := untar(t, gotLayer)["manifest.json"]; !ok {
		t.Error("the pushed layer is not the sidecar")
	}
	if !rec.Pushed() || rec.Repository != gotRepo || rec.Tag != gotTag || rec.Digest != "sha256:"+strings.Repeat("c", 64) || rec.Maps != 3 {
		t.Errorf("record = %+v", rec)
	}
	// The record is what a later release cut reads.
	st, err := ReadSourcemapsState(projectDir, "prod", "web")
	if err != nil || st == nil || *st != withFiledAt(rec, st.FiledAt) {
		t.Fatalf("state = %+v, %v; want %+v", st, err, rec)
	}
	if other, _ := ReadSourcemapsState(projectDir, "staging", "web"); other != nil {
		t.Error("another env read this env's record")
	}
}

func withFiledAt(r SourcemapsRecord, at string) SourcemapsRecord { r.FiledAt = at; return r }

// A project-set version holding `+` is filed under the tag the symbolicator
// derives from it.
func TestRegistrySourcemapsTagFromProjectVersion(t *testing.T) {
	res, _ := stagedSidecar(t)
	var gotTag string
	pub := RegistrySourcemaps{
		ProjectDir: t.TempDir(), Env: "prod",
		Base: func(context.Context) (string, error) { return "r.example/o/p", nil },
		Push: func(_ context.Context, _, tag string, _ SourcemapBuild, _ []byte) (string, error) {
			gotTag = tag
			return "sha256:" + strings.Repeat("d", 64), nil
		},
	}
	rec, err := pub.PublishSourcemaps(context.Background(), SourcemapBuild{Frontend: "web", BuildID: "1.4.2+build.5", Dir: res.SourcemapsDir})
	if err != nil || gotTag != "1.4.2_build.5" || rec.BuildID != "1.4.2+build.5" {
		t.Fatalf("tag %q, record %+v, err %v", gotTag, rec, err)
	}
	bad := SourcemapBuild{Frontend: "web", BuildID: "has space", Dir: res.SourcemapsDir}
	if _, err := pub.PublishSourcemaps(context.Background(), bad); err == nil {
		t.Error("a build id that cannot be a tag was pushed")
	}
}

// Observability that is not hosted: the maps are kept on disk, nothing is
// uploaded, and the same state file says where.
func TestLocalSourcemapsFallback(t *testing.T) {
	res, plan := stagedSidecar(t)
	projectDir := t.TempDir()
	pub := LocalSourcemaps{ProjectDir: projectDir, Env: "dev"}
	build := SourcemapBuild{Project: plan.Project, Frontend: plan.Name, BuildID: res.BuildID, Dir: res.SourcemapsDir, Maps: res.Sourcemaps}

	rec, err := pub.PublishSourcemaps(context.Background(), build)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(projectDir, ".forge", "sourcemaps", "dev", "web", res.BuildID)
	if rec.Dir != want || rec.Pushed() || rec.Repository != "" {
		t.Fatalf("record = %+v, want a local copy in %s", rec, want)
	}
	local := readTree(t, want)
	if local["app/assets/index-abc.js.map"] != testMapJSON {
		t.Errorf("local copy lacks the map: %v", keys(local))
	}
	var mf SourcemapManifest
	if err := json.Unmarshal([]byte(local["manifest.json"]), &mf); err != nil || mf.BuildID != res.BuildID {
		t.Errorf("local manifest = %+v, %v", mf, err)
	}
	if st, _ := ReadSourcemapsState(projectDir, "dev", "web"); st == nil || st.Dir != want {
		t.Errorf("state = %+v", st)
	}

	// A rebuild of the same id replaces the directory: it never keeps a map the
	// new build does not have.
	if err := os.WriteFile(filepath.Join(want, "stale.map"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishSourcemaps(context.Background(), build); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(want, "stale.map")); !os.IsNotExist(err) {
		t.Error("a rebuild kept a stale map")
	}
}

type failingPublisher struct{ called int }

func (f *failingPublisher) PublishSourcemaps(context.Context, SourcemapBuild) (SourcemapsRecord, error) {
	f.called++
	return SourcemapsRecord{}, errors.New("registry refused the push: denied")
}

// A build whose maps could not be filed must not go live: the Firebase deploy
// stops before it runs the firebase CLI.
func TestFirebaseDeployFilesMapsBeforeGoingLive(t *testing.T) {
	projectDir, _ := buildFixture(t)
	staging := filepath.Join(t.TempDir(), "public")
	fe := fakeFirebaseFrontend(projectDir, staging)
	fe.Name, fe.Path, fe.Spec.PublicDir, fe.Spec.BasePath, fe.Spec.Bundle = "web", "web", "dist", "/app", nil
	fe.Project, fe.VersionEnvVar = "acme", "VITE_APP_VERSION"

	pub := &failingPublisher{}
	fe.SourcemapPublisher = pub
	runner := &fakeRunner{}
	prov := FirebaseProvider{ProjectDir: projectDir, Runner: runner, StagingRoot: staging}

	err := prov.Deploy(context.Background(), ServiceGroup{Env: "prod", Frontends: []FirebaseFrontend{fe}})
	if err == nil || !strings.Contains(err.Error(), "registry refused the push") || !strings.Contains(err.Error(), "does not ship") {
		t.Fatalf("deploy = %v; want the publisher's refusal, and that nothing shipped", err)
	}
	if pub.called != 1 {
		t.Errorf("publisher called %d times", pub.called)
	}
	for _, c := range runner.calls {
		if strings.HasPrefix(c, FirebaseCLI+" deploy") {
			t.Fatalf("firebase deploy ran although the maps were not filed: %v", runner.calls)
		}
	}
}

// With a working publisher the same deploy files the maps and then deploys; the
// publisher sees the sidecar, not the served tree.
func TestFirebaseDeployFilesMapsThenDeploys(t *testing.T) {
	projectDir, _ := buildFixture(t)
	staging := filepath.Join(t.TempDir(), "public")
	fe := fakeFirebaseFrontend(projectDir, staging)
	fe.Name, fe.Path, fe.Spec.PublicDir, fe.Spec.BasePath, fe.Spec.Bundle = "web", "web", "dist", "/app", nil
	fe.Project, fe.VersionEnvVar = "acme", "VITE_APP_VERSION"

	var filed SourcemapBuild
	order := &orderingRunner{}
	fe.SourcemapPublisher = publisherFunc(func(_ context.Context, b SourcemapBuild) (SourcemapsRecord, error) {
		filed = b
		order.events = append(order.events, "publish")
		return SourcemapsRecord{BuildID: b.BuildID, Frontend: b.Frontend, Maps: b.Maps, Dir: b.Dir}, nil
	})
	prov := FirebaseProvider{ProjectDir: projectDir, Runner: order, StagingRoot: staging}
	captureStdout(t, func() {
		if err := prov.Deploy(context.Background(), ServiceGroup{Env: "prod", Frontends: []FirebaseFrontend{fe}}); err != nil {
			t.Fatalf("deploy: %v", err)
		}
	})
	if filed.Frontend != "web" || filed.Project != "acme" || filed.BuildID == "" || filed.Maps != 3 {
		t.Errorf("publisher saw %+v", filed)
	}
	if filed.Dir != SourcemapsDir(staging) {
		t.Errorf("publisher was handed %s, want the sidecar %s", filed.Dir, SourcemapsDir(staging))
	}
	pub, deploy := -1, -1
	for i, e := range order.events {
		switch {
		case e == "publish":
			pub = i
		case strings.HasPrefix(e, FirebaseCLI+" deploy"):
			deploy = i
		}
	}
	if pub < 0 || deploy < 0 || pub > deploy {
		t.Fatalf("maps must be filed before the deploy: %v", order.events)
	}
}

type publisherFunc func(context.Context, SourcemapBuild) (SourcemapsRecord, error)

func (f publisherFunc) PublishSourcemaps(ctx context.Context, b SourcemapBuild) (SourcemapsRecord, error) {
	return f(ctx, b)
}

// orderingRunner is a fakeRunner that also logs publish events in one sequence.
type orderingRunner struct {
	fakeRunner
	events []string
}

func (o *orderingRunner) record2(name string, args []string) {
	o.events = append(o.events, name+" "+strings.Join(args, " "))
}
func (o *orderingRunner) Run(ctx context.Context, name string, args ...string) error {
	o.record2(name, args)
	return o.fakeRunner.Run(ctx, name, args...)
}
func (o *orderingRunner) RunWithEnv(ctx context.Context, env map[string]string, name string, args ...string) error {
	o.record2(name, args)
	return o.fakeRunner.RunWithEnv(ctx, env, name, args...)
}
func (o *orderingRunner) RunInDir(ctx context.Context, dir string, env map[string]string, name string, args ...string) error {
	o.record2(name, args)
	return o.fakeRunner.RunInDir(ctx, dir, env, name, args...)
}

// The bucket provider too: a failed filing stops it before anything is uploaded.
func TestStaticSiteDeployFilesMapsBeforeUpload(t *testing.T) {
	projectDir, _ := buildFixture(t)
	fe := fakeStaticSiteFrontend()
	fe.Name, fe.Path, fe.Spec.PublicDir, fe.Spec.BasePath, fe.Spec.Bundle = "web", "web", "dist", "/app", nil
	fe.Project, fe.VersionEnvVar = "acme", "VITE_APP_VERSION"
	pub := &failingPublisher{}
	fe.SourcemapPublisher = pub
	runner := &fakeRunner{}
	prov := StaticSiteProvider{ProjectDir: projectDir, Runner: runner}

	err := prov.Deploy(context.Background(), ServiceGroup{Env: "prod", StaticSites: []StaticSiteFrontend{fe}})
	if err == nil || !strings.Contains(err.Error(), "registry refused the push") {
		t.Fatalf("deploy = %v", err)
	}
	if pub.called != 1 {
		t.Errorf("publisher called %d times", pub.called)
	}
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "gcloud") {
			t.Fatalf("an upload ran although the maps were not filed: %v", runner.calls)
		}
	}
}

// Extraction with no publisher, no maps, or no build id never fails a deploy.
func TestFileStageSourcemapsNoOps(t *testing.T) {
	ctx := context.Background()
	pub := &failingPublisher{}
	for name, tc := range map[string]struct {
		pub SourcemapPublisher
		res StageResult
	}{
		"no publisher": {nil, StageResult{BuildID: "x", SourcemapsDir: "d", Sourcemaps: 1}},
		"no maps":      {pub, StageResult{BuildID: "x"}},
		"no build id":  {pub, StageResult{SourcemapsDir: "d", Sourcemaps: 1}},
	} {
		captureStdout(t, func() {
			if err := FileStageSourcemaps(ctx, tc.pub, "k", "p", "f", tc.res); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
	if pub.called != 0 {
		t.Errorf("publisher called %d times for a no-op", pub.called)
	}
}

// A dist directory forge did not build: maps extracted from a COPY, the
// original untouched, paths carrying the base path.
func TestExtractDistSourcemaps(t *testing.T) {
	dist := t.TempDir()
	files := map[string]string{
		"index.html":          "<html></html>",
		"assets/app-1.js":     "x();\n//# sourceMappingURL=app-1.js.map\n",
		"assets/app-1.js.map": testMapJSON,
	}
	writeTree(t, dist, files)

	b, cleanup, err := ExtractDistSourcemaps(DistSourcemaps{Dir: dist, Project: "acme", Frontend: "desktop", BuildID: "b1", BasePath: "/app"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if b.Maps != 1 || b.BuildID != "b1" || b.Frontend != "desktop" {
		t.Fatalf("build = %+v", b)
	}
	side := readTree(t, b.Dir)
	if side["app/assets/app-1.js.map"] != testMapJSON {
		t.Errorf("sidecar = %v", keys(side))
	}
	var mf SourcemapManifest
	if err := json.Unmarshal([]byte(side["manifest.json"]), &mf); err != nil {
		t.Fatal(err)
	}
	if mf.BuildID != "b1" || mf.Frontend != "desktop" || mf.BasePath != "/app" || len(mf.Files) != 1 || mf.Files[0].Path != "app/assets/app-1.js" {
		t.Errorf("manifest = %+v", mf)
	}
	// The original is untouched: it still ships exactly as built.
	after := readTree(t, dist)
	for rel, want := range files {
		if after[rel] != want {
			t.Errorf("%s was modified", rel)
		}
	}
	cleanup()
	if _, err := os.Stat(b.Dir); !os.IsNotExist(err) {
		t.Error("cleanup left the sidecar behind")
	}

	if _, _, err := ExtractDistSourcemaps(DistSourcemaps{Dir: filepath.Join(dist, "missing")}); err == nil {
		t.Error("a missing dist extracted")
	}
}

func TestSourcemapsStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if st, err := ReadSourcemapsState(dir, "prod", "web"); st != nil || err != nil {
		t.Fatalf("empty = %+v, %v", st, err)
	}
	if err := WriteSourcemapsState(dir, "prod", SourcemapsRecord{Frontend: "web", BuildID: "b", Repository: "r/o/p/web-sourcemaps", Digest: "sha256:" + strings.Repeat("e", 64)}); err != nil {
		t.Fatal(err)
	}
	st, err := ReadSourcemapsState(dir, "prod", "web")
	if err != nil || st == nil || !st.Pushed() || st.FiledAt == "" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	if err := ClearSourcemapsState(dir, "prod", "web"); err != nil {
		t.Fatal(err)
	}
	if st, _ := ReadSourcemapsState(dir, "prod", "web"); st != nil {
		t.Error("clear left the record")
	}
	if err := ClearSourcemapsState(dir, "prod", "web"); err != nil {
		t.Errorf("clearing nothing: %v", err)
	}
}

// The tag a push uses is what the symbolicator derives from the same build id.
func TestPushedTagMatchesReleaseHelper(t *testing.T) {
	for _, id := range []string{"3f9c0e1a2b4d", "1.2.3+meta"} {
		want, err := release.SourcemapsTag(id)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		pub := RegistrySourcemaps{ProjectDir: t.TempDir(), Env: "e",
			Base: func(context.Context) (string, error) { return "r/o/p", nil },
			Push: func(_ context.Context, _, tag string, _ SourcemapBuild, _ []byte) (string, error) {
				got = tag
				return "sha256:" + strings.Repeat("a", 64), nil
			}}
		res, _ := stagedSidecar(t)
		if _, err := pub.PublishSourcemaps(context.Background(), SourcemapBuild{Frontend: "web", BuildID: id, Dir: res.SourcemapsDir}); err != nil || got != want {
			t.Errorf("id %q pushed as %q, helper says %q (%v)", id, got, want, err)
		}
	}
}
