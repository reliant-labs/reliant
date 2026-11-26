package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	digestpkg "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
	"sigs.k8s.io/yaml"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// The prod post-migration shape composed through the real native path: a
// forge-applied server set on two clusters (api and NATS on the hub cluster,
// gateway and pgbouncer on the daemon cluster) and a hosted SPA as the only
// hosted part. The StaticSite record the platform applies must name the
// CANDIDATE SPA build — the artifact the sealed snapshot binds to the frontend
// — never a digest read from the release ledger or the env's current
// promotion (which the fixture's control plane pins at a stale digest), and
// never a placeholder.
//
// The SPA declares no release_dependencies, as prod's does, so every release
// is a full selection and the SPA waits on every server. Across three
// releases the record pins each release's own push; NATS and pgbouncer, whose
// sealed runtime refs keep their declared tag, are byte-identical in all
// three; each server carries exactly the image its release resolved.
//
// With FORGE_PLACEMENT_M5_EXPORT set, each recorded bundle is written there as
// it is recorded, and each next release is based on the promotion id the
// reader writes back: control-plane's end-to-end converge fixture
// (internal/deploystore, scripts/placement-m5-e2e.sh) records and promotes
// every bundle through its real store in between.
func TestCompositionHostedSPARecordNamesTheCandidateBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("temp git project, native KCL and hosted HTTP fixture; runs in task test")
	}
	ctx := context.Background()
	h := newHostedSPACompositionFixture(t, "")
	h.exportDir = os.Getenv("FORGE_PLACEMENT_M5_EXPORT")

	h.nextSite, h.external["registry.test/reliant-api"], h.external["registry.test/reliant-gateway"] = spaDigest('1'), imageDigest('a'), imageDigest('b')
	v1 := h.compose(ctx, t, "v1", nil, nil)
	if got := h.takePushes(); len(got) != 1 || got[0] != "web@"+spaRepository {
		t.Fatalf("the selected hosted SPA must be built and pushed exactly once, got pushes %v", got)
	}
	requireStaticSite(t, v1, spaRepository, spaDigest('1'))
	requireFrontendBinding(t, v1, spaRepository, spaDigest('1'))
	requireSPAWaitsOnEveryServer(t, h, v1)

	// v2: the api image moves; the gateway's does not.
	h.nextSite, h.external["registry.test/reliant-api"] = spaDigest('2'), imageDigest('c')
	v2 := h.compose(ctx, t, "v2", &v1, nil)
	requireStaticSite(t, v2, spaRepository, spaDigest('2'))
	requireFrontendBinding(t, v2, spaRepository, spaDigest('2'))
	// A full selection re-stamps every forge workload with the release
	// (app.kubernetes.io/version), so their images are compared; the raw
	// runtimes must not move at all.
	requireSameBytes(t, v1, v2, "StatefulSet/nats", "Deployment/pgbouncer")
	requireRuntimeRef(t, v2, "Deployment", "api", "registry.test/reliant-api:stable@"+imageDigest('c'))
	requireRuntimeRef(t, v2, "Deployment", "gateway", "registry.test/reliant-gateway:stable@"+imageDigest('b'))

	// v3: the gateway moves; the api does not.
	h.nextSite, h.external["registry.test/reliant-gateway"] = spaDigest('3'), imageDigest('d')
	v3 := h.compose(ctx, t, "v3", &v2, nil)
	requireStaticSite(t, v3, spaRepository, spaDigest('3'))
	requireSameBytes(t, v2, v3, "StatefulSet/nats", "Deployment/pgbouncer")
	requireRuntimeRef(t, v3, "Deployment", "api", "registry.test/reliant-api:stable@"+imageDigest('c'))
	requireRuntimeRef(t, v3, "Deployment", "gateway", "registry.test/reliant-gateway:stable@"+imageDigest('d'))
	for _, rec := range []release.BundleRecord{v1, v2, v3} {
		requireRuntimeRef(t, rec, "StatefulSet", "nats", natsRef)
		requireRuntimeRef(t, rec, "Deployment", "pgbouncer", pgbouncerRef)
	}
	if err := release.ValidateSnapshotTransition(v1.Snapshot, *v2.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := release.ValidateSnapshotTransition(v2.Snapshot, *v3.Snapshot); err != nil {
		t.Fatal(err)
	}
}

// A partial composition carries an unselected hosted SPA: no build, and its
// sealed record is the base's exact bytes; selecting it again pins the new
// push while every carried server object keeps its bytes. (The SPA declares
// its one dependency here, so a partial selection is admissible.)
func TestCompositionCarriesAnUnselectedHostedSPA(t *testing.T) {
	if testing.Short() {
		t.Skip("temp git project, native KCL and hosted HTTP fixture; runs in task test")
	}
	ctx := context.Background()
	h := newHostedSPACompositionFixture(t, `release_dependencies = {api = "reliant-api-v1"}`)

	h.nextSite = spaDigest('1')
	v1 := h.compose(ctx, t, "v1", nil, nil)
	site1 := requireStaticSite(t, v1, spaRepository, spaDigest('1'))
	h.takePushes()

	h.external["registry.test/reliant-gateway"] = imageDigest('b')
	v2 := h.compose(ctx, t, "v2", &v1, []release.ConsumerKey{consumerKey(t, h.consumers, "gateway")})
	if got := h.takePushes(); len(got) != 0 {
		t.Fatalf("an unselected hosted SPA must not be rebuilt, got pushes %v", got)
	}
	if site2 := requireStaticSite(t, v2, spaRepository, spaDigest('1')); !bytes.Equal(site1, site2) {
		t.Fatalf("the carried StaticSite record changed bytes:\n%s\n---\n%s", site1, site2)
	}
	requireSameBytes(t, v1, v2, "Deployment/api", "StatefulSet/nats", "Deployment/pgbouncer")

	h.nextSite = spaDigest('2')
	v3 := h.compose(ctx, t, "v3", &v2, []release.ConsumerKey{consumerKey(t, h.consumers, "web"), consumerKey(t, h.consumers, "api")})
	if got := h.takePushes(); len(got) != 1 {
		t.Fatalf("a selected hosted SPA must be pushed once, got %v", got)
	}
	requireStaticSite(t, v3, spaRepository, spaDigest('2'))
	requireFrontendBinding(t, v3, spaRepository, spaDigest('2'))
	requireSameBytes(t, v2, v3, "Deployment/gateway", "StatefulSet/nats", "Deployment/pgbouncer")
	if err := release.ValidateSnapshotTransition(v2.Snapshot, *v3.Snapshot); err != nil {
		t.Fatal(err)
	}
}

const (
	spaRepository = "registry.test/acme/web/static.v1"
	natsRef       = "registry.test/nats:2.10.22-alpine@sha256:" + "5555555555555555555555555555555555555555555555555555555555555555"
	pgbouncerRef  = "registry.test/pgbouncer:1.23.1@sha256:" + "6666666666666666666666666666666666666666666666666666666666666666"
)

func spaDigest(c byte) string   { return "sha256:" + strings.Repeat(string(c), 64) }
func imageDigest(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }

// requireSPAWaitsOnEveryServer: with no release_dependencies, the hosted SPA
// is the dependent of every forge-applied server workload.
func requireSPAWaitsOnEveryServer(t *testing.T, h *hostedSPACompositionFixture, rec release.BundleRecord) {
	t.Helper()
	doc := h.docs[rec.Digest]
	if doc.PlacementPrerequisites == nil {
		t.Fatal("no placement prerequisites")
	}
	predecessors := map[string]bool{}
	for _, e := range doc.PlacementPrerequisites.Edges {
		if e.Dependent.Runtime == "hosted" && e.Dependent.Name == "web" {
			predecessors[e.Predecessor.Kind+"/"+e.Predecessor.Name] = true
		}
	}
	for _, want := range []string{"Deployment/api", "Deployment/gateway"} {
		if !predecessors[want] {
			t.Errorf("the hosted SPA does not wait on %s; predecessors %v", want, predecessors)
		}
	}
}

func requireSameBytes(t *testing.T, a, b release.BundleRecord, objects ...string) {
	t.Helper()
	for _, o := range objects {
		kind, name, _ := strings.Cut(o, "/")
		if !bytes.Equal(resourceSpec(t, a, kind, name), resourceSpec(t, b, kind, name)) {
			t.Errorf("%s changed bytes between %s and %s:\n%s\n---\n%s", o, a.Release, b.Release, resourceSpec(t, a, kind, name), resourceSpec(t, b, kind, name))
		}
	}
}

// requireRuntimeRef: a sealed raw runtime keeps its declared reference
// verbatim, tag included, so an unchanged digest rolls nothing.
func requireRuntimeRef(t *testing.T, rec release.BundleRecord, kind, name, ref string) {
	t.Helper()
	if spec := resourceSpec(t, rec, kind, name); !bytes.Contains(spec, []byte("image: "+ref+"\n")) {
		t.Errorf("%s %s/%s does not carry %s verbatim:\n%s", rec.Release, kind, name, ref, spec)
	}
}

// requireStaticSite finds the sealed hosted StaticSite record and checks it
// addresses repository@digest. It returns the record's exact sealed bytes.
func requireStaticSite(t *testing.T, rec release.BundleRecord, repository, digest string) []byte {
	t.Helper()
	spec := resourceSpec(t, rec, "StaticSite", "web")
	var doc struct {
		Spec struct {
			ReleaseRepository string `json:"releaseRepository"`
			LiveDigest        string `json:"liveDigest"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Spec.ReleaseRepository != repository || doc.Spec.LiveDigest != digest {
		t.Fatalf("sealed StaticSite names %s@%s, want the candidate build %s@%s:\n%s",
			doc.Spec.ReleaseRepository, doc.Spec.LiveDigest, repository, digest, spec)
	}
	return spec
}

// requireFrontendBinding checks the snapshot binds the SPA's frontend slot to
// the exact OCI artifact its record serves, with its source provenance.
func requireFrontendBinding(t *testing.T, rec release.BundleRecord, repository, digest string) {
	t.Helper()
	for _, b := range rec.Snapshot.Bindings {
		if b.Consumer.Workload != "web" || b.Slot != "frontend" {
			continue
		}
		a := rec.Snapshot.Artifacts[b.Artifact]
		if a.Kind != release.KindOCI || a.Location != repository || a.Content != digest || a.Origin.Source == nil {
			t.Fatalf("web/frontend binds %+v, want OCI %s@%s with source provenance", a, repository, digest)
		}
		return
	}
	t.Fatal("snapshot has no web/frontend binding")
}

func resourceSpec(t *testing.T, rec release.BundleRecord, kind, name string) []byte {
	t.Helper()
	for _, r := range rec.Snapshot.Resources {
		if r.Key.Kind == kind && r.Key.Name == name {
			return r.Spec
		}
	}
	t.Fatalf("sealed snapshot has no %s/%s", kind, name)
	return nil
}

func consumerKey(t *testing.T, consumers []release.Consumer, workload string) release.ConsumerKey {
	t.Helper()
	for _, c := range consumers {
		if c.Key.Workload == workload {
			return c.Key
		}
	}
	t.Fatalf("no consumer %s", workload)
	return release.ConsumerKey{}
}

type hostedSPACompositionFixture struct {
	mu        sync.Mutex
	dir       string
	ledger    envLedger
	entities  *KCLEntities
	consumers []release.Consumer
	source    release.Source
	cuts      map[string]release.Release
	records   []release.BundleRecord
	docs      map[string]release.BundleDoc
	blobs     map[string]exportedBundle
	pushes    []string
	nextSite  string
	external  map[string]string
	promoted  int
	exportDir string
}

// exportedBundle is one recorded bundle exactly as the control plane received
// it: the OCI manifest, its config blob and its manifests layer.
type exportedBundle struct {
	Release        string `json:"release"`
	Repository     string `json:"repository"`
	Digest         string `json:"digest"`
	SnapshotDigest string `json:"snapshot_digest"`
	ConfigDigest   string `json:"config_digest"`
	Manifest       []byte `json:"manifest"`
	Config         []byte `json:"config"`
	Layer          []byte `json:"layer"`
	// Cut is the release the composition cut before sealing it.
	Cut release.Release `json:"cut"`
}

// newHostedSPACompositionFixture is the native composition harness of
// TestCompositionNativeBootstrapWorkerAndStaleBase over prod's topology. The
// recording control plane advertises every bundle feature this forge writes
// (placement-prerequisites-v2 included): a mixed bundle must not seal without
// it.
func newHostedSPACompositionFixture(t *testing.T, webDependencies string) *hostedSPACompositionFixture {
	t.Helper()
	ctx := context.Background()
	h := &hostedSPACompositionFixture{cuts: map[string]release.Release{}, docs: map[string]release.BundleDoc{}, blobs: map[string]exportedBundle{},
		external: map[string]string{"registry.test/reliant-api": imageDigest('a'), "registry.test/reliant-gateway": imageDigest('a')}}
	registry := memory.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "GetCapabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"snapshotSchemaVersions": []string{release.SnapshotSchema}, "immutableSnapshotSeals": true, "frozenPromotionBundles": true, "bundleDocumentFeatures": release.BundleDocFeatures()})
		case "EnsureEnvironment":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"id": "env-prod", "name": "prod", "kind": "persistent"}})
		case "ListEnvironments":
			_ = json.NewEncoder(w).Encode(map[string]any{"environments": []map[string]any{{"id": "env-prod", "name": "prod", "kind": "persistent"}}})
		case "ListPromotions":
			// The env's CURRENT promotion pins the SPA at a digest no
			// composition here built. Reading pins from it is the defect.
			_ = json.NewEncoder(w).Encode(map[string]any{"promotions": []wirePromotion{{ID: "promotion-ledger", EnvironmentID: "env-prod", ReleaseVersion: "v0", Kind: wireKindPromote,
				ResolvedArtifacts: map[string]string{siteRepositoryFor("registry.test/acme/web"): staleSPADigest}, CreatedAt: time.Unix(1700000000, 0).UTC()}}})
		case "GetRelease":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "not_found", "message": "release not found"})
		case "RecordBundle":
			var manifest, configBytes, layer []byte
			_ = json.Unmarshal(body["manifest"], &manifest)
			_ = json.Unmarshal(body["config"], &configBytes)
			_ = json.Unmarshal(body["manifestsLayer"], &layer)
			doc, err := release.DecodeBundleDoc(configBytes)
			if err != nil {
				t.Errorf("record config: %v", err)
				http.Error(w, err.Error(), 400)
				return
			}
			if err := release.ValidateSnapshotLayer(*doc.Snapshot, layer); err != nil {
				t.Errorf("record layer: %v", err)
				http.Error(w, err.Error(), 400)
				return
			}
			if doc.PlacementPrerequisites == nil {
				t.Errorf("a mixed cluster + hosted bundle sealed without placement prerequisites")
			}
			var repo string
			_ = json.Unmarshal(body["repository"], &repo)
			digest := release.SpecDigest(manifest)
			if err := registry.Tag(ctx, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digestpkg.Digest(digest), Size: int64(len(manifest))}, digest); err != nil {
				t.Errorf("index pushed manifest: %v", err)
			}
			seal := release.SnapshotSeal{Release: doc.Release, Project: doc.Project, Environment: doc.Env, SnapshotDigest: doc.SnapshotDigest, BundleDigest: digest}
			if err := release.CheckSnapshotSeal(h.cuts[doc.Release], doc, seal); err != nil {
				t.Errorf("cut->seal: %v", err)
				http.Error(w, err.Error(), 400)
				return
			}
			rec := release.BundleRecord{ID: fmt.Sprintf("bundle-%d", len(h.records)+1), Env: "prod", Release: doc.Release, Digest: digest, Reference: repo + "@" + digest, ConfigDigest: doc.ConfigDigest, Shape: doc.Shape, Snapshot: doc.Snapshot, SnapshotDigest: doc.SnapshotDigest, CreatedAt: time.Now().UTC()}
			h.records = append(h.records, rec)
			h.docs[digest] = doc
			h.blobs[doc.Release] = exportedBundle{Release: doc.Release, Repository: repo, Digest: digest, SnapshotDigest: doc.SnapshotDigest, ConfigDigest: doc.ConfigDigest, Manifest: manifest, Config: configBytes, Layer: layer}
			_ = json.NewEncoder(w).Encode(map[string]any{"bundle": map[string]any{"id": rec.ID, "environmentId": "env-prod", "releaseVersion": rec.Release, "digest": rec.Digest, "reference": rec.Reference, "configDigest": rec.ConfigDigest, "shape": rec.Shape, "snapshot": rec.Snapshot, "snapshotDigest": rec.SnapshotDigest, "createdAt": rec.CreatedAt}, "created": true})
		default:
			t.Errorf("unexpected hosted procedure %s", r.URL.Path)
			http.Error(w, "unexpected procedure", 500)
		}
	}))
	t.Cleanup(server.Close)

	h.dir = t.TempDir()
	// prod's topology in miniature. NATS and pgbouncer are raw manifest
	// consumers whose sealed runtime refs keep their declared tag (#1377);
	// their Pods meet the restricted Pod Security profile forge labels its
	// namespaces with, as prod's do.
	main := `import forge
import forge.workloads as fw
_hub = forge.ClusterTarget {cluster = "k3d-prod-hub", namespace = "app", platform = "amd64"}
_daemon = forge.ClusterTarget {cluster = "k3d-prod-daemon", namespace = "daemon", platform = "amd64"}
_nats = "NATS_REF"
_pgbouncer = "PGBOUNCER_REF"
_raw = lambda name: str, kind: str, image: str -> any {
    {
        apiVersion = "apps/v1"
        kind = kind
        metadata = {name = name}
        spec = {
            selector = {matchLabels = {app = name}}
            template = {metadata = {labels = {app = name}}, spec = {
                securityContext = {runAsNonRoot = True, seccompProfile = {type = "RuntimeDefault"}}
                containers = [{name = name, image = image, securityContext = {allowPrivilegeEscalation = False, capabilities = {drop = ["ALL"]}}}]
            }}
        } | ({serviceName = name} if kind == "StatefulSet" else {})
    }
}
output = forge.render(forge.Bundle {
 project = "spa-native"
 env = "prod"
 control_plane = forge.ControlPlane {endpoint = "ENDPOINT", token_env = "FORGE_COMPOSITION_TEST_TOKEN"}
 workloads = [
  fw.Workload {name = "api", image = "registry.test/reliant-api:stable", release_dependencies = {}, ports = [fw.Port {name = "http", port = 8080, expose = True}], runtime = forge.OnCluster {target = _hub}}
  fw.Workload {name = "gateway", image = "registry.test/reliant-gateway:stable", release_dependencies = {}, runtime = forge.OnCluster {target = _daemon}}
 ]
 manifests = [
  forge.Manifests {name = "nats", cluster = "k3d-prod-hub", namespace = "app", release_inputs = {"container:nats" = _nats}, release_dependencies = {}, objects = [_raw("nats", "StatefulSet", _nats)]}
  forge.Manifests {name = "pgbouncer", cluster = "k3d-prod-daemon", namespace = "daemon", release_inputs = {"container:pgbouncer" = _pgbouncer}, release_dependencies = {}, objects = [_raw("pgbouncer", "Deployment", _pgbouncer)]}
 ]
 frontends = [forge.Frontend {
  name = "web"
  image = "registry.test/acme/web"
  path = "frontends/web"
  type = "vite"
  public_dir = "dist"
  runtime_config = {API_URL = "https://api.prod.test"}
  runtime = forge.OnHosted {}
  WEB_DEPENDENCIES
 }]
})
`
	main = strings.NewReplacer("NATS_REF", natsRef, "PGBOUNCER_REF", pgbouncerRef, "WEB_DEPENDENCIES", webDependencies).Replace(main)
	initGitRepo(t, h.dir, map[string]string{
		"forge.yaml":                 "name: spa-native\nmodule_path: github.com/acme/spa\n",
		"go.mod":                     "module github.com/acme/spa\n\ngo 1.23\n",
		"deploy/kcl/kcl.mod":         "[package]\nname = \"spa\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n",
		"deploy/kcl/prod/main.k":     strings.ReplaceAll(main, "ENDPOINT", server.URL),
		"frontends/web/package.json": "{\"name\":\"web\",\"private\":true}\n",
		".gitignore":                 ".forge/\nbin/\n**/kcl.mod.lock\n",
	})
	gitIn(t, h.dir, "remote", "add", "origin", "https://github.com/acme/spa.git")
	t.Chdir(h.dir)
	useLedgerHome(t, t.TempDir())
	t.Setenv("FORGE_COMPOSITION_TEST_TOKEN", "test-token")
	previousPush := bundlePushTarget
	bundlePushTarget = func(context.Context, string, string, string) (bundle.Pusher, error) { return registry, nil }
	t.Cleanup(func() { bundlePushTarget = previousPush })
	writeHostedPushBaseFixture(t, h.dir, "prod", "registry.test/acme")
	previousResolve := externalImageDigestResolver
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		if at := strings.LastIndex(ref, "@"); at >= 0 {
			return ref[at+1:], []string{"linux/amd64"}, nil
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		digest, ok := h.external[imageRepository(ref)]
		if !ok {
			return "", nil, fmt.Errorf("fixture registry has no %s", ref)
		}
		return digest, []string{"linux/amd64"}, nil
	}
	t.Cleanup(func() { externalImageDigestResolver = previousResolve })
	previousSite := hostedStaticPusher
	hostedStaticPusher = func(_ context.Context, _, repository string, fe deploytarget.StaticSiteFrontend) (string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.pushes = append(h.pushes, fe.Name+"@"+repository)
		return h.nextSite, nil
	}
	t.Cleanup(func() { hostedStaticPusher = previousSite })

	fake := &fakeDSOTCaller{errs: map[string]error{procGetRelease: &cloud.Error{Code: cloud.CodeNotFound}}, replies: map[string]any{procDeployCapabilities: map[string]any{"snapshotSchemaVersions": []string{release.SnapshotSchema}, "immutableSnapshotSeals": true, "frozenPromotionBundles": true, "bundleDocumentFeatures": release.BundleDocFeatures()}}}
	fake.reply = func(proc string, body map[string]any) (any, bool) {
		if proc != procCutRelease {
			return nil, false
		}
		raw, _ := json.Marshal(body["snapshotRelease"])
		var rel release.Release
		if err := json.Unmarshal(raw, &rel); err != nil {
			t.Fatal(err)
		}
		if err := rel.Validate(); err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		h.cuts[rel.Version] = rel
		h.mu.Unlock()
		return map[string]any{"created": true}, true
	}
	store := &hostedStore{client: fake, project: "spa-native", kind: deploytarget.HostedEnvPersistent, resolver: stubEnvResolver{id: "env-prod"}}
	h.ledger = envLedger{Releases: store, Bindings: store, Hosted: true}
	entities, err := RenderKCL(ctx, h.dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	h.entities = entities
	if h.consumers, err = compositionConsumers(entities); err != nil {
		t.Fatal(err)
	}
	h.source = release.Source{Repo: "github.com/acme/spa", Commit: gitOut(t, h.dir, "rev-parse", "HEAD")}
	return h
}

// staleSPADigest is what the env's current promotion pins the SPA at.
var staleSPADigest = "sha256:" + strings.Repeat("e", 64)

func siteRepositoryFor(image string) string { return deploytarget.HostedStaticRepository(image) }

// compose runs one native composition finish over base (nil: bootstrap, every
// consumer selected) and returns the bundle the control plane recorded.
func (h *hostedSPACompositionFixture) compose(ctx context.Context, t *testing.T, version string, base *release.BundleRecord, selected []release.ConsumerKey, results ...buildResult) release.BundleRecord {
	t.Helper()
	run := &compositionRun{Declaration: release.Snapshot{Schema: release.SnapshotSchema, Project: "spa-native", Environment: "prod", Source: h.source, PolicyDigest: release.SpecDigest([]byte("policy")), Consumers: h.consumers, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}, Ledger: h.ledger, Entities: h.entities}
	run.Selected = selected
	if selected == nil {
		for _, c := range h.consumers {
			run.Selected = append(run.Selected, c.Key)
		}
	}
	if base != nil {
		run.Base = &release.Promotion{ID: h.basePromotionID(t, base.Release), Env: "prod", Release: base.Release, Kind: release.KindPromote, Snapshot: base.Snapshot, SnapshotDigest: base.SnapshotDigest, BundleID: base.ID, BundleDigest: base.Digest}
		run.BaseSnapshot = base.Snapshot
	}
	opts := buildOptions{env: "prod", release: version, composition: run}
	opts.pushPlan.push = true
	before := len(h.records)
	if err := finishComposition(ctx, &config.ProjectConfig{Name: "spa-native"}, h.entities, opts, results); err != nil {
		t.Fatalf("compose %s: %v", version, err)
	}
	if len(h.records) != before+1 {
		t.Fatalf("compose %s recorded %d bundles", version, len(h.records)-before)
	}
	h.exportRelease(t, version)
	return h.records[len(h.records)-1]
}

// basePromotionID is the id of the promotion that made base current. The
// fixture's control plane numbers its own; exporting, the id is the one the
// real control plane assigned when it promoted the exported bundle, which
// its converge fixture writes to <export>/<release>.promotion.
func (h *hostedSPACompositionFixture) basePromotionID(t *testing.T, baseRelease string) string {
	t.Helper()
	if h.exportDir == "" {
		h.promoted++
		return fmt.Sprintf("promotion-%d", h.promoted)
	}
	path := filepath.Join(h.exportDir, baseRelease+".promotion")
	deadline := time.Now().Add(15 * time.Minute)
	for {
		raw, err := os.ReadFile(path)
		if err == nil && len(bytes.TrimSpace(raw)) > 0 {
			return string(bytes.TrimSpace(raw))
		}
		if time.Now().After(deadline) {
			t.Fatalf("the control plane never promoted %s (%s): %v", baseRelease, path, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (h *hostedSPACompositionFixture) takePushes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.pushes
	h.pushes = nil
	return out
}

// exportRelease writes the bundle just recorded for version to the export
// directory as <release>.json, for control-plane's end-to-end converge
// fixture. The write is a rename, so a reader never sees a partial file.
func (h *hostedSPACompositionFixture) exportRelease(t *testing.T, version string) {
	t.Helper()
	if h.exportDir == "" {
		return
	}
	h.mu.Lock()
	b := h.blobs[version]
	b.Cut = h.cuts[version]
	h.mu.Unlock()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(h.exportDir, "."+version+".json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(h.exportDir, version+".json")); err != nil {
		t.Fatal(err)
	}
}
