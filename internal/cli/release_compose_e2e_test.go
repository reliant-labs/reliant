package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	digestpkg "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// Native adapter proof: no real registry, cluster, credentials or production
// data. The render is real KCL -> workload expansion -> inventory -> compose ->
// OCI push -> hosted Cut/Record, not a second implementation of composition.
func TestCompositionNativeBootstrapWorkerAndStaleBase(t *testing.T) {
	if testing.Short() {
		t.Skip("temp git project, native KCL and hosted HTTP fixture; runs in task test")
	}
	ctx := context.Background()
	registry := memory.New()
	var mu sync.Mutex
	cuts := map[string]release.Release{}
	var records []release.BundleRecord
	var layers [][]byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch filepath.Base(r.URL.Path) {
		case "GetCapabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"snapshotSchemaVersions": []string{release.SnapshotSchema}, "immutableSnapshotSeals": true, "frozenPromotionBundles": true})
		case "EnsureEnvironment":
			_ = json.NewEncoder(w).Encode(map[string]any{"environment": map[string]any{"id": "env-prod", "name": "prod", "kind": "self_managed"}})
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
			var repo string
			_ = json.Unmarshal(body["repository"], &repo)
			digest := release.SpecDigest(manifest)
			if err := registry.Tag(ctx, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: digestpkg.Digest(digest), Size: int64(len(manifest))}, digest); err != nil {
				t.Errorf("index pushed manifest: %v", err)
			}
			seal := release.SnapshotSeal{Release: doc.Release, Project: doc.Project, Environment: doc.Env, SnapshotDigest: doc.SnapshotDigest, BundleDigest: digest}
			if err := release.CheckSnapshotSeal(cuts[doc.Release], doc, seal); err != nil {
				t.Errorf("cut->seal: %v", err)
				http.Error(w, err.Error(), 400)
				return
			}
			rec := release.BundleRecord{ID: fmt.Sprintf("bundle-%d", len(records)+1), Env: "prod", Release: doc.Release, Digest: digest, Reference: repo + "@" + digest, ConfigDigest: doc.ConfigDigest, Shape: doc.Shape, Snapshot: doc.Snapshot, SnapshotDigest: doc.SnapshotDigest, CreatedAt: time.Now().UTC()}
			records = append(records, rec)
			layers = append(layers, layer)
			_ = json.NewEncoder(w).Encode(map[string]any{"bundle": map[string]any{"id": rec.ID, "environmentId": "env-prod", "releaseVersion": rec.Release, "digest": rec.Digest, "reference": rec.Reference, "configDigest": rec.ConfigDigest, "shape": rec.Shape, "snapshot": rec.Snapshot, "snapshotDigest": rec.SnapshotDigest, "createdAt": rec.CreatedAt}, "created": true})
		default:
			t.Errorf("unexpected hosted procedure %s", r.URL.Path)
			http.Error(w, "unexpected procedure", 500)
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	dir := t.TempDir()
	main := `import forge
import forge.workloads as fw
_target = forge.ClusterTarget {cluster = "k3d-composition-native", namespace = "app", platform = "amd64"}
output = forge.render(forge.Bundle {
 project = "composition-native"
 env = "prod"
 control_plane = forge.ControlPlane {endpoint = "ENDPOINT", token_env = "FORGE_COMPOSITION_TEST_TOKEN"}
 workloads = [fw.Workload {
  name = workloadName
  image = "registry.test/reliant"
  release_dependencies = {}
  runtime = forge.OnCluster {target = _target}
 } for workloadName in ["api", "worker"]]
})
`
	initGitRepo(t, dir, map[string]string{"forge.yaml": "name: composition-native\nmodule_path: github.com/acme/native\n", "go.mod": "module github.com/acme/native\n\ngo 1.23\n", "deploy/kcl/kcl.mod": "[package]\nname = \"native\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n\n[dependencies]\n", "deploy/kcl/prod/main.k": strings.ReplaceAll(main, "ENDPOINT", server.URL), ".gitignore": ".forge/\nbin/\n**/kcl.mod.lock\n"})
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/acme/native.git")
	t.Chdir(dir)
	useLedgerHome(t, t.TempDir())
	t.Setenv("FORGE_COMPOSITION_TEST_TOKEN", "test-token")
	previousPush := bundlePushTarget
	bundlePushTarget = func(context.Context, string, string, string) (bundle.Pusher, error) { return registry, nil }
	t.Cleanup(func() { bundlePushTarget = previousPush })
	writeHostedPushBaseFixture(t, dir, "prod", "registry.test/acme")
	previousResolve := externalImageDigestResolver
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		if at := strings.LastIndex(ref, "@"); at >= 0 {
			return ref[at+1:], []string{"linux/amd64"}, nil
		}
		return "sha256:" + strings.Repeat("a", 64), []string{"linux/amd64"}, nil
	}
	t.Cleanup(func() { externalImageDigestResolver = previousResolve })
	fake := &fakeDSOTCaller{errs: map[string]error{procGetRelease: &cloud.Error{Code: cloud.CodeNotFound}}, replies: map[string]any{procDeployCapabilities: map[string]any{"snapshotSchemaVersions": []string{release.SnapshotSchema}, "immutableSnapshotSeals": true, "frozenPromotionBundles": true}}}
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
		mu.Lock()
		if existing, ok := cuts[rel.Version]; ok {
			if err := release.CheckRecut(existing, rel); err != nil {
				t.Fatal(err)
			}
		}
		cuts[rel.Version] = rel
		mu.Unlock()
		return map[string]any{"created": true}, true
	}
	store := &hostedStore{client: fake, project: "composition-native", kind: deploytarget.HostedEnvSelfManaged, resolver: stubEnvResolver{id: "env-prod"}}
	ledger := envLedger{Releases: store, Bindings: store, Hosted: true}
	entities, err := RenderKCL(ctx, dir, "prod")
	if err != nil {
		t.Fatal(err)
	}
	consumers, err := compositionConsumers(entities)
	if err != nil {
		t.Fatal(err)
	}
	source := release.Source{Repo: "github.com/acme/native", Commit: gitOut(t, dir, "rev-parse", "HEAD")}
	declaration := func() release.Snapshot {
		return release.Snapshot{Schema: release.SnapshotSchema, Project: "composition-native", Environment: "prod", Source: source, PolicyDigest: release.SpecDigest([]byte("policy")), Consumers: consumers, Artifacts: map[release.ArtifactID]release.SnapshotArtifact{}}
	}
	all := []release.ConsumerKey{}
	var worker release.ConsumerKey
	for _, c := range consumers {
		all = append(all, c.Key)
		if c.Key.Workload == "worker" {
			worker = c.Key
		}
	}
	opts := buildOptions{env: "prod", release: "v1", composition: &compositionRun{Declaration: declaration(), Ledger: ledger, Selected: all, Entities: entities}}
	if err := finishComposition(ctx, &config.ProjectConfig{Name: "composition-native"}, entities, opts, nil); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("bootstrap sealed %d bundles", len(records))
	}
	base := records[0]
	basePromotion := release.Promotion{ID: "promotion-1", Env: "prod", Release: "v1", Kind: release.KindPromote, Snapshot: base.Snapshot, SnapshotDigest: base.SnapshotDigest, BundleID: base.ID, BundleDigest: base.Digest}
	opts.release = "v2"
	opts.composition = &compositionRun{Base: &basePromotion, BaseSnapshot: base.Snapshot, Declaration: declaration(), Ledger: ledger, Selected: []release.ConsumerKey{worker}, Entities: entities}
	if err := finishComposition(ctx, &config.ProjectConfig{Name: "composition-native"}, entities, opts, []buildResult{{name: "worker", repository: "registry.test/reliant", digest: "sha256:" + strings.Repeat("b", 64)}}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("worker sealed %d bundles", len(records))
	}
	next := records[1]
	// Lost record response/retry must keep the original cut timestamp and
	// produce the same OCI bytes, not a newest-bundle association.
	delete(fake.errs, procGetRelease)
	priorBytes, _ := json.Marshal(cuts["v2"])
	fake.replies[procGetRelease] = map[string]any{"release": wireRelease{Version: "v2", SnapshotRelease: priorBytes}}
	retry := opts
	retry.composition = &compositionRun{Base: &basePromotion, BaseSnapshot: base.Snapshot, Declaration: declaration(), Ledger: ledger, Selected: []release.ConsumerKey{worker}, Entities: entities}
	if err := finishComposition(ctx, &config.ProjectConfig{Name: "composition-native"}, entities, retry, []buildResult{{name: "worker", repository: "registry.test/reliant", digest: "sha256:" + strings.Repeat("b", 64)}}); err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[2].Digest != next.Digest || records[2].SnapshotDigest != next.SnapshotDigest {
		t.Fatal("retry changed sealed bytes")
	}
	var apiBefore, apiAfter []byte
	for _, r := range base.Snapshot.Resources {
		if r.Key.Kind == "Deployment" && r.Key.Name == "api" {
			apiBefore = r.Spec
		}
	}
	for _, r := range next.Snapshot.Resources {
		if r.Key.Kind == "Deployment" && r.Key.Name == "api" {
			apiAfter = r.Spec
		}
	}
	if len(apiBefore) == 0 || !bytes.Equal(apiBefore, apiAfter) {
		t.Fatal("native worker composition changed carried API pod template")
	}
	want := map[string]string{"api": "sha256:" + strings.Repeat("a", 64), "worker": "sha256:" + strings.Repeat("b", 64)}
	for _, b := range next.Snapshot.Bindings {
		if next.Snapshot.Artifacts[b.Artifact].Content != want[b.Consumer.Workload] {
			t.Fatalf("mixed repository binding flattened: %+v", b)
		}
	}
	if err := release.ValidateSnapshotTransition(base.Snapshot, *next.Snapshot); err != nil {
		t.Fatal(err)
	}
	if err := release.ValidateSnapshotLayer(*next.Snapshot, layers[1]); err != nil {
		t.Fatal(err)
	}
	// Stale admission fails before registry/approval/promote; no blind retry.
	fake.replies["controlplane.v1.DeployService/ListBundles"] = map[string]any{"bundles": []wireBundle{{ID: next.ID, ReleaseVersion: "v2", Digest: next.Digest, Reference: next.Reference, Snapshot: next.Snapshot, SnapshotDigest: next.SnapshotDigest, CreatedAt: next.CreatedAt}}}
	if err := captureStaleCompositionFixture(ctx, ledger, basePromotion); err == nil {
		t.Fatal("moved captured base was accepted")
	}
	// Exercise the real promotion follow-through helper with deterministic
	// actuator failure/recovery. Desired append is not an applied PASS.
	binding := &compositionAppendBinding{promotion: release.Promotion{ID: "promotion-2", Snapshot: next.Snapshot, SnapshotDigest: next.SnapshotDigest, BundleDigest: next.Digest}}
	applyLedger := envLedger{Bindings: binding, Releases: store, Hosted: true}
	var statuses []string
	gateFake := &fakeDSOTCaller{reply: func(proc string, body map[string]any) (any, bool) {
		if proc != procRecordGate {
			return nil, false
		}
		gate, _ := body["gate"].(map[string]any)
		status, _ := gate["status"].(string)
		statuses = append(statuses, status)
		return map[string]any{"gate": gate, "created": true}, true
	}}
	oldBackend := snapshotResolveGateBackend
	snapshotResolveGateBackend = func(context.Context, string) (gateBackend, error) {
		return gateBackend{hosted: true, store: &gateStore{client: gateFake}, bindings: binding}, nil
	}
	t.Cleanup(func() { snapshotResolveGateBackend = oldBackend })
	oldApply := snapshotClusterApply
	calls := 0
	failApply := true
	snapshotClusterApply = func(_ context.Context, s release.Snapshot, destination string, _ cluster.ApplyOpts) error {
		calls++
		hash, err := s.Digest()
		if err != nil {
			return err
		}
		if hash != next.SnapshotDigest || destination != "k3d-composition-native" {
			t.Fatal("actuator did not receive sealed full snapshot")
		}
		if failApply {
			return errors.New("controlled apply interruption")
		}
		return nil
	}
	t.Cleanup(func() { snapshotClusterApply = oldApply })
	follow := promoteOptions{Follow: &promoteFollowOptions{}}
	plan := release.Plan{Digest: release.SpecDigest([]byte("approved plan"))}
	if err := executeSnapshotPromotion(ctx, cuts["v2"], applyLedger, follow, plan, basePromotion.ID, &next, next.Snapshot); err == nil {
		t.Fatal("partial apply interruption reported success")
	}
	if len(statuses) != 1 || statuses[0] != "failed" {
		t.Fatalf("failure advanced applied baseline: %v", statuses)
	}
	failApply = false
	if err := executeSnapshotPromotion(ctx, cuts["v2"], applyLedger, follow, plan, basePromotion.ID, &next, next.Snapshot); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 2 || statuses[1] != "passed" || calls != 2 {
		t.Fatalf("recovery did not complete same snapshot: gates=%v calls=%d", statuses, calls)
	}
	if binding.guard.ExpectedCurrentID != basePromotion.ID || binding.last.PlanDigest != plan.Digest {
		t.Fatal("promotion lost captured CAS or approved plan")
	}
	// The outer native command verifies the recorded OCI digest and approved
	// plan without rendering or writing. A moved base refuses before approval.
	oldRepository := snapshotBundleRepository
	snapshotBundleRepository = func(context.Context, string, string, string) (bundle.Resolver, error) { return registry, nil }
	t.Cleanup(func() { snapshotBundleRepository = oldRepository })
	wireCurrent := wirePromotion{ID: basePromotion.ID, ReleaseVersion: "v1", Kind: wireKindPromote, Snapshot: base.Snapshot, SnapshotDigest: base.SnapshotDigest, BundleID: base.ID, BundleDigest: base.Digest}
	fake.replies[procListPromotions] = map[string]any{"promotions": []wirePromotion{wireCurrent}}
	outerPlan, err := release.BuildPlan(release.PlanInput{EnvironmentID: "env-prod", BundleID: next.ID, ReleaseVersion: "v2", Candidate: next.Shape, CandidateConfigDigest: next.ConfigDigest, Live: &base.Shape, Basis: release.PlanBasis{CurrentPromotionID: basePromotion.ID, AppliedBundleID: base.ID, AppliedConfigDigest: base.ConfigDigest}})
	if err != nil {
		t.Fatal(err)
	}
	wireApproval := wirePlan{Digest: outerPlan.Digest, EnvironmentID: outerPlan.EnvironmentID, BundleID: outerPlan.BundleID, ReleaseVersion: outerPlan.ReleaseVersion, LiveBasis: &wirePlanBasis{CurrentPromotionID: basePromotion.ID, AppliedBundleID: base.ID, AppliedConfigDigest: base.ConfigDigest}, ConfigIdentical: outerPlan.ConfigIdentical}
	for _, f := range outerPlan.Findings {
		wireApproval.Findings = append(wireApproval.Findings, wirePlanFinding{Code: f.Code, Class: string(f.Class), Section: f.Section, Subject: f.Subject, Detail: f.Detail})
	}
	fake.replies[procPlanDeploy] = map[string]any{"plan": wireApproval}
	if err := runSnapshotPromote(ctx, "prod", cuts["v2"], ledger, promoteOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	// Execute the public native outer path, not just its helper. The hosted
	// Append fake returns only the server's frozen association; actuator sees
	// the same complete sealed bytes verified above.
	fake.replies[procEnsureEnv] = map[string]any{"environment": map[string]any{"id": "env-prod"}}
	wireWritten := wirePromotion{ID: "promotion-outer", ReleaseVersion: "v2", Kind: wireKindPromote, Snapshot: next.Snapshot, SnapshotDigest: next.SnapshotDigest, BundleID: next.ID, BundleDigest: next.Digest, PlanDigest: outerPlan.Digest}
	fake.replies[procPromote] = map[string]any{"promotion": wireWritten}
	beforeWrites := compositionCallCount(fake, procPromote)
	if err := runSnapshotPromote(ctx, "prod", cuts["v2"], ledger, promoteOptions{Approval: deployApproval{Digest: outerPlan.Digest}, Follow: &promoteFollowOptions{}}); err != nil {
		t.Fatal(err)
	}
	if compositionCallCount(fake, procPromote) != beforeWrites+1 || calls != 3 || len(statuses) != 3 || statuses[2] != "passed" {
		t.Fatalf("outer native did not append/apply/verify: writes %d calls%d gates%v", compositionCallCount(fake, procPromote), calls, statuses)
	}
	outerRequest := fake.body(t, procPromote)
	if outerRequest["expectedCurrentPromotionId"] != basePromotion.ID || outerRequest["planDigest"] != outerPlan.Digest {
		t.Fatalf("outer lost exact CAS/approval: %v", outerRequest)
	}
	wireCurrent.ID = "promotion-moved"
	fake.replies[procListPromotions] = map[string]any{"promotions": []wirePromotion{wireCurrent}}
	if err := runSnapshotPromote(ctx, "prod", cuts["v2"], ledger, promoteOptions{DryRun: true}); err == nil || !strings.Contains(err.Error(), "base promotion moved") {
		t.Fatalf("outer stale base refusal: %v", err)
	}
	testCompositionAdoptionRecovery(ctx, t, registry, *base.Snapshot, ledger, fake, &statuses)
}

func compositionCallCount(fake *fakeDSOTCaller, proc string) int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	n := 0
	for _, call := range fake.calls {
		if call.Proc == proc {
			n++
		}
	}
	return n
}

func captureStaleCompositionFixture(ctx context.Context, ledger envLedger, base release.Promotion) error {
	_, err := captureCompositionBase(ctx, envLedger{Bindings: compositionMovedBinding{hostedBindingsStub: hostedBindingsStub{}, current: base}, Releases: ledger.Releases}, "prod", "some-other-promotion", false)
	return err
}

type compositionAppendBinding struct {
	hostedBindingsStub
	promotion release.Promotion
	guard     appendGuard
	last      release.Promotion
}

func (s *compositionAppendBinding) Append(_ context.Context, p release.Promotion, g appendGuard) (release.Promotion, error) {
	s.last = p
	s.guard = g
	return s.promotion, nil
}

type compositionMovedBinding struct {
	hostedBindingsStub
	current release.Promotion
}

func (s compositionMovedBinding) Current(context.Context, string) (release.Promotion, bool, error) {
	p := s.current
	p.ID = "promotion-moved"
	return p, true, nil
}
