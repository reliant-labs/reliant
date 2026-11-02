package cli

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestPlacementPrerequisitesReaderFirst(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{orderClusterWorkload("api", nil)}, Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)}}
	previous := bundleRecorderForEnv
	t.Cleanup(func() { bundleRecorderForEnv = previous })
	for _, supported := range []bool{false, true} {
		features := []string{}
		if supported {
			features = release.BundleDocFeatures()
		}
		fake := &fakeDSOTCaller{replies: map[string]any{procDeployCapabilities: map[string]any{"bundleDocumentFeatures": features}}}
		bundleRecorderForEnv = func(context.Context, string, string, envLedger) (bundleRecorder, error) {
			return hostedRecordStoreFor(fake, "p"), nil
		}
		err := requirePlacementReader(t.Context(), t.TempDir(), "prod", e, envLedger{Hosted: true})
		if supported && err != nil {
			t.Fatal(err)
		}
		if !supported && (err == nil || !strings.Contains(err.Error(), release.BundleFeaturePlacementPrerequisites)) {
			t.Fatalf("old reader accepted: %v", err)
		}
		for _, call := range fake.calls {
			if call.Proc != procDeployCapabilities {
				t.Fatalf("effect before compatibility: %v", fake.calls)
			}
		}
	}
}

// stubPlacementReaders answers each env's recorder with its own feature
// list, and reports which envs were asked.
func stubPlacementReaders(t *testing.T, supports map[string]bool) *[]string {
	t.Helper()
	var asked []string
	prevLedger, prevRecorder := bundleLedgerFor, bundleRecorderForEnv
	t.Cleanup(func() { bundleLedgerFor, bundleRecorderForEnv = prevLedger, prevRecorder })
	bundleLedgerFor = func(context.Context, string, string) (envLedger, error) {
		return envLedger{Bindings: hostedBindingsStub{}, Releases: hostedReleasesStub{}, Hosted: true}, nil
	}
	bundleRecorderForEnv = func(_ context.Context, _, env string, _ envLedger) (bundleRecorder, error) {
		asked = append(asked, env)
		features := []string{}
		if supports[env] {
			features = release.BundleDocFeatures()
		}
		return hostedRecordStoreFor(&fakeDSOTCaller{replies: map[string]any{procDeployCapabilities: map[string]any{"bundleDocumentFeatures": features}}}, "p"), nil
	}
	return &asked
}

func mixedPlacementEntities() *KCLEntities {
	return &KCLEntities{Workloads: []WorkloadEntity{orderClusterWorkload("api", nil)}, Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)}}
}

// `forge env build <env> --release v --no-build` cuts a release over earlier
// build state. The cut is permanent, so every --bundle-envs destination's
// reader is negotiated first, and one that cannot decode the mixed graph
// refuses before any cut.
func TestPlacementPrerequisitesCutOnlyRefusesBeforeTheCut(t *testing.T) {
	asked := stubPlacementReaders(t, map[string]bool{"prod": true, "prod-eu": false})
	prevRender, prevCut := renderKCLForDeclaration, cutReleaseFromBuildStateFn
	t.Cleanup(func() { renderKCLForDeclaration, cutReleaseFromBuildStateFn = prevRender, prevCut })
	renderKCLForDeclaration = func(context.Context, string, string) (*KCLEntities, error) { return mixedPlacementEntities(), nil }
	cut := false
	cutReleaseFromBuildStateFn = func(context.Context, string, string, string, string, *KCLEntities, buildOptions) (releaseCutOutcome, error) {
		cut = true
		return releaseCutOutcome{}, nil
	}
	err := runEnvBuildCutOnly(t.Context(), buildOptions{env: "prod", release: "v1.2.3", bundleEnvs: "prod,prod-eu", outputDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), release.BundleFeaturePlacementPrerequisites) {
		t.Fatalf("an old reader on a bundle env must refuse the cut-only release: %v", err)
	}
	if cut {
		t.Fatal("the release was cut before the reader refused it")
	}
	if !slices.Equal(*asked, []string{"prod", "prod-eu"}) {
		t.Fatalf("readers asked = %v, want every --bundle-envs destination", *asked)
	}
}

// Top-level `forge build <env>` is compile-only: it never asks a control
// plane what it decodes, even for a mixed env, and its local bundle step
// skips a mixed bundle an old reader cannot take rather than failing.
func TestPlacementPrerequisitesCompileOnlyNeverNegotiates(t *testing.T) {
	prevLedger := bundleLedgerFor
	t.Cleanup(func() { bundleLedgerFor = prevLedger })
	bundleLedgerFor = func(context.Context, string, string) (envLedger, error) {
		t.Fatal("a compile-only build opened its ledger")
		return envLedger{}, nil
	}
	if err := preflightPlacementBuild(t.Context(), mixedPlacementEntities(), buildOptions{env: "prod"}); err != nil {
		t.Fatalf("compile-only build refused: %v", err)
	}
}

func TestPlacementPrerequisitesCompileOnlyBundleSkipsOldReader(t *testing.T) {
	dir := newLedgerTestProject(t, "mixed-compile-project")
	prevShape := projectEnvShapeFn
	t.Cleanup(func() { projectEnvShapeFn = prevShape })
	stream := bundleTestStream + "---\n# cluster: hosted\napiVersion: forge.dev/v1alpha1\nkind: StaticSite\nmetadata:\n  name: web\nspec: {}\n"
	projectEnvShapeFn = func(_ context.Context, _ io.Writer, env string) (envShapeDoc, error) {
		shape, err := bundle.ProjectShape(bundle.ShapeInput{Kind: release.EnvSelfManaged, Clusters: []string{"prod-ctx", release.BundleHostedCluster}, Manifests: stream})
		if err != nil {
			t.Fatal(err)
		}
		return envShapeDoc{Project: "mixed-compile-project", Env: env, Kind: string(shape.Kind), Shape: shape,
			Provenance: release.Provenance{Commit: rep40('c'), Tree: rep40('a')}, manifests: stream, entities: mixedPlacementEntities()}, nil
	}
	stubPlacementReaders(t, map[string]bool{})
	var warned strings.Builder
	written, err := writeEnvBundles(t.Context(), dir, []string{"prod"}, bundleBuildInputs{Now: bundleTestNow, errOut: &warned})
	if err != nil || len(written) != 1 || !written[0].Skipped || !strings.Contains(warned.String(), release.BundleFeaturePlacementPrerequisites) {
		t.Fatalf("compile-only mixed bundle with an old reader: written=%+v err=%v warning=%q", written, err, warned.String())
	}
	if _, err := writeEnvBundles(t.Context(), dir, []string{"prod"}, bundleBuildInputs{Release: "v1", Pushed: true, Now: bundleTestNow, errOut: io.Discard}); err == nil {
		t.Fatal("a publishing bundle must still refuse an old reader")
	}
}

func TestPlacementPrerequisitesFrontendDeclarations(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{orderClusterWorkload("api", nil), orderClusterWorkload("worker", map[string]string{"api": "v1"})}, Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)}}
	decl, err := bundlePlacementDeclarations(e)
	if err != nil {
		t.Fatal(err)
	}
	// web (hosted) waits for both cluster servers; worker -> api is
	// same-stage, a compatibility claim that orders nothing.
	if len(decl) != 3 || len(decl[2].Dependencies) != 2 || len(decl[1].Dependencies) != 0 {
		t.Fatalf("want the conservative cross-stage edges and no same-stage one: %+v", decl)
	}
	e.Frontends[0].ReleaseDependencies = map[string]string{}
	decl, err = bundlePlacementDeclarations(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(decl[2].Dependencies) != 0 {
		t.Fatal("explicit empty treated as absent")
	}
	e.Frontends[0].ReleaseDependencies = map[string]string{"absent": "v1"}
	if _, err := bundlePlacementDeclarations(e); err == nil || !strings.Contains(err.Error(), "missing predecessor") {
		t.Fatalf("missing peer discarded: %v", err)
	}
}

// The composition snapshot keeps a build-only artifact consumer; the
// placement declarations (the preflight, before any build) omit it, and every
// runtime consumer of the snapshot has a declaration, so sealing cannot fail.
func TestPlacementPrerequisitesCompositionWithBuildOnly(t *testing.T) {
	e := &KCLEntities{
		Workloads: []WorkloadEntity{
			{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "prod"}}},
			{Name: "cli", Runtime: RuntimeEntity{Type: RuntimeBuildOnly}},
		},
		Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)},
	}
	consumers, err := compositionConsumers(e)
	if err != nil {
		t.Fatal(err)
	}
	decl, err := bundlePlacementDeclarations(e)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, d := range decl {
		declared[d.Workload] = true
	}
	if declared["cli"] || !declared["api"] || !declared["web"] {
		t.Fatalf("declarations = %+v", decl)
	}
	sawBuildOnly := false
	for _, c := range consumers {
		if c.Key.Workload == "cli" {
			sawBuildOnly = true
			continue
		}
		if !declared[c.Key.Workload] {
			t.Errorf("runtime consumer %s has no placement declaration", c.Key.Workload)
		}
	}
	if !sawBuildOnly {
		t.Fatal("build-only artifact dropped from the composition snapshot")
	}
}

// The native OpenBao provider is a composition consumer, so it is declared
// as an ordinary cluster placement under its release owner, and a frontend
// with absent release_dependencies waits for it like any server.
func TestPlacementPrerequisitesNativeOpenBao(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	e := &KCLEntities{
		Workloads: []WorkloadEntity{{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "prod"}}}},
		Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)},
		OpenBao:   &OpenBaoEntity{Name: "store", Namespace: "secrets", Cluster: "prod", ReleaseOwner: "openbao-secrets-store", ReleaseInputs: map[string]string{"container:openbao": "registry.example/openbao@" + digest, "container:converge": "registry.example/converge@" + digest}},
	}
	consumers, err := compositionConsumers(e)
	if err != nil {
		t.Fatal(err)
	}
	decl, err := bundlePlacementDeclarations(e)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]int{}
	for i, d := range decl {
		byName[d.Workload] = i
	}
	for _, c := range consumers {
		if _, ok := byName[c.Key.Workload]; !ok {
			t.Errorf("composition consumer %s has no placement declaration", c.Key.Workload)
		}
	}
	bao := decl[byName["openbao-secrets-store"]]
	if bao.Runtime != "cluster" || bao.Actuator != "forge" {
		t.Errorf("OpenBao placement = %+v", bao)
	}
	if web := decl[byName["web"]]; !slices.Contains(web.Dependencies, "openbao-secrets-store") || !slices.Contains(web.Dependencies, "api") {
		t.Errorf("absent frontend dependencies must include every server, OpenBao too: %+v", web)
	}
	e.OpenBao.ReleaseOwner = ""
	if _, err := bundlePlacementDeclarations(e); err == nil {
		t.Error("an OpenBao with no release owner cannot be placed")
	}
}

// release_dependencies inside one stage is a compatibility claim, not an
// order: the two-way pair control-plane prod and reliant declare (api-server
// and daemon-gateway each name the other's NATS contract) deploys. The same
// pair across stages is a real cycle and is still refused, by item.
func TestPlacementPrerequisitesSameStagePairIsNotACycle(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		orderClusterWorkload("reliant-api-server", map[string]string{"daemon-gateway": "reliant/api-gateway-nats/v1"}),
		orderClusterWorkload("daemon-gateway", map[string]string{"reliant-api-server": "reliant/api-gateway-nats/v1"}),
	}}
	edges, err := deployStageEdges(e)
	if err != nil || len(edges) != 0 {
		t.Fatalf("a same-stage pair ordered something: edges=%+v err=%v", edges, err)
	}
	if _, err := deployStageOrder(e); err != nil {
		t.Fatalf("a same-stage pair was refused: %v", err)
	}
	if _, err := bundlePlacementDeclarations(e); err != nil {
		t.Fatalf("placement declarations refused a same-stage pair: %v", err)
	}
	cross := &KCLEntities{Workloads: []WorkloadEntity{
		orderHostedWorkload("a", map[string]string{"b": "v1"}),
		orderClusterWorkload("b", map[string]string{"a": "v1"}),
	}}
	if _, err := deployStageOrder(cross); err == nil || !strings.Contains(err.Error(), "release dependency cycle") {
		t.Fatalf("a cross-stage cycle was not refused: %v", err)
	}
}

func TestPlacementPrerequisitesFalseStageCycle(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{orderHostedWorkload("a", nil), orderClusterWorkload("b", map[string]string{"a": "v1"}), orderClusterWorkload("c", nil), orderHostedWorkload("d", map[string]string{"c": "v1"})}}
	if _, err := declaredPlacementEdges(e); err != nil {
		t.Fatal(err)
	}
	if _, err := deployStageOrder(e); err == nil || !strings.Contains(err.Error(), "unsupported placement stage interleaving") || strings.Contains(err.Error(), "in a cycle") {
		t.Fatalf("false cycle interpretation: %v", err)
	}
}

func TestPlacementPrerequisitesUnsupportedAndPure(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{orderClusterWorkload("api", nil)}, Frontends: []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, nil)}, ControlPlane: &ControlPlaneEntity{}, HubConverges: []string{release.DefaultDeliveryUnit}}
	// Explicit actuator rejection is also enforced by bundle Build; declaration
	// must preserve the actuator instead of guessing forge from runtime alone.
	decl, err := bundlePlacementDeclarations(e)
	if err == nil && decl[0].Actuator != "hub" {
		t.Fatalf("hub identity lost: %+v", decl)
	}
	e.Frontends = nil
	e.ControlPlane = nil
	e.HubConverges = nil
	decl, err = bundlePlacementDeclarations(e)
	if err != nil || decl != nil {
		t.Fatalf("pure deployment changed: %+v, %v", decl, err)
	}
}

// inventoryEntities declares one of every release consumer compositionConsumers
// knows: a cluster workload, a hosted workload, a build-only artifact, a
// hosted frontend, a raw manifest consumer and the native OpenBao provider.
func inventoryEntities() *KCLEntities {
	digest := "sha256:" + strings.Repeat("a", 64)
	return &KCLEntities{
		Workloads: []WorkloadEntity{
			{Name: "api", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "prod"}}},
			{Name: "edge", Runtime: RuntimeEntity{Type: RuntimeHosted}},
			{Name: "cli", Runtime: RuntimeEntity{Type: RuntimeBuildOnly}},
		},
		// Declared, so the inventory is not mixed up with v1's hosted-predecessor refusal.
		Frontends:                []FrontendEntity{orderFrontend("web", FrontendRuntimeHosted, map[string]string{"api": "v1"})},
		ReleaseManifestConsumers: []releaseManifestConsumer{{Name: "nats", Cluster: "prod"}},
		OpenBao:                  &OpenBaoEntity{Name: "store", Namespace: "secrets", Cluster: "prod", ReleaseOwner: "openbao-secrets-store", ReleaseInputs: map[string]string{"container:openbao": "registry.example/openbao@" + digest, "container:converge": "registry.example/converge@" + digest}},
	}
}

// The placement inventory is exactly compositionConsumers' runtime set: every
// consumer a composition seals, except build-only artifacts, which have no
// placement. A consumer source added to one and not the other fails here,
// before it can pass a preflight and then fail sealing after a push.
func TestPlacementInventoryIsCompositionConsumers(t *testing.T) {
	e := inventoryEntities()
	consumers, err := compositionConsumers(e)
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, c := range consumers {
		if !slices.Equal(c.Slots, []string{"build-only"}) {
			want = append(want, c.Key.Workload)
		}
	}
	decl, err := placementDeclarations(e)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range decl {
		got = append(got, d.Workload)
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("placement inventory %v != composition runtime consumers %v", got, want)
	}
}

// A mixed env with the native OpenBao provider passes the preflight: the
// provider is placed (TestPlacementPrerequisitesNativeOpenBao), not refused.
func TestPlacementPrerequisitesNativeProviderPassesPreflight(t *testing.T) {
	e := inventoryEntities()
	e.Workloads = e.Workloads[:1]
	e.ReleaseManifestConsumers = nil
	stubPlacementReaders(t, map[string]bool{"prod": true})
	if err := preflightPlacementBuild(t.Context(), e, buildOptions{env: "prod", release: "v1", push: true}); err != nil {
		t.Fatalf("mixed + native provider is supported: %v", err)
	}
}

// `forge env build prod --target api --push` on a mixed env: the build is
// narrowed to a pure cluster set, but the bundle it seals is the whole mixed
// env, so the reader is negotiated against the FULL declaration and an old
// one refuses before any login, build or push.
func TestPlacementPrerequisitesTargetedPushNegotiatesFullDeclaration(t *testing.T) {
	stubPlacementReaders(t, map[string]bool{})
	prev := renderBuildEntitiesFn
	t.Cleanup(func() { renderBuildEntitiesFn = prev })
	renderBuildEntitiesFn = func(context.Context, *config.ProjectConfig, buildOptions) (*KCLEntities, *KCLEntities, error) {
		full := mixedPlacementEntities()
		return full, &KCLEntities{Workloads: full.Workloads}, nil
	}
	opts := buildOptions{env: "prod", push: true, buildTarget: "api"}
	_, _, err := renderBuildInputs(t.Context(), &config.ProjectConfig{Name: "p"}, &opts)
	if err == nil || !strings.Contains(err.Error(), release.BundleFeaturePlacementPrerequisites) {
		t.Fatalf("a --target push of a mixed env must negotiate the full declaration's reader first: %v", err)
	}
}
