package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"oras.land/oras-go/v2/content/memory"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// stubEnvShapeStream is stubEnvShape over a stated render, so a test can make
// a second render of the same release differ (or not) in its objects.
func stubEnvShapeStream(t *testing.T, project string, stream *string, forgeVersion *string) {
	t.Helper()
	prev := projectEnvShapeFn
	projectEnvShapeFn = func(_ context.Context, _ io.Writer, env string) (envShapeDoc, error) {
		shape, err := bundle.ProjectShape(bundle.ShapeInput{
			Kind:      release.EnvSelfManaged,
			Workloads: []release.ShapeWorkload{{Name: "api", Runtime: "cluster", Cluster: "prod-ctx"}},
			Clusters:  []string{"prod-ctx"},
			Manifests: *stream,
		})
		if err != nil {
			t.Fatalf("project the fixture shape: %v", err)
		}
		return envShapeDoc{
			Project: project, Env: env, Kind: string(shape.Kind), Shape: shape,
			Provenance: release.Provenance{Commit: rep40('c'), Tree: rep40('a'), ForgeVersion: *forgeVersion},
			manifests:  *stream,
		}, nil
	}
	t.Cleanup(func() { projectEnvShapeFn = prev })
}

func bundleRowsFor(t *testing.T, dir, env, version string) []release.BundleRecord {
	t.Helper()
	store, err := openMachineLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Bundles(env)
	if err != nil {
		t.Fatal(err)
	}
	var out []release.BundleRecord
	for _, r := range rows {
		if r.Release == version {
			out = append(out, r)
		}
	}
	return out
}

// ONE BUNDLE PER RELEASE. A later write for the same release whose rendered
// objects are identical — the 2026-10-07 case: the cut and the plan rendered
// byte-identical manifests, but the plan's bundle carried the release's
// source pins and named another forge as the renderer — reuses the recorded
// bundle instead of recording a second one under the same release.
func TestWriteEnvBundle_ARelease_ReusesItsRecordedBundle(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-of-record")
	stream, forge := bundleTestStream, "v0.1.44-d6b5d722"
	stubEnvShapeStream(t, "bundle-of-record", &stream, &forge)
	ctx := context.Background()

	cut, err := writeEnvBundle(ctx, dir, "prod", bundleBuildInputs{
		Release: "v1", Pins: release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Now: bundleTestNow, errOut: io.Discard,
	})
	if err != nil {
		t.Fatalf("cut's bundle: %v", err)
	}

	// The plan, later, from another binary and with the release's sources.
	forge = "v0.1.44-5d2053a6"
	plan, err := writeEnvBundle(ctx, dir, "prod", bundleBuildInputs{
		Release: "v1",
		Pins: release.BundlePins{
			Images:  map[string]string{"api": "sha256:" + rep64('1')},
			Sources: map[string]release.Source{"web": {Repo: "github.com/acme/web", Commit: rep40('e')}},
		},
		Now: bundleTestNow, errOut: io.Discard,
	})
	if err != nil {
		t.Fatalf("plan's bundle: %v", err)
	}
	if plan.Digest != cut.Digest {
		t.Errorf("the plan recorded bundle %s for the release whose bundle is %s: one release, two bundles",
			shortDigest(plan.Digest), shortDigest(cut.Digest))
	}
	if rows := bundleRowsFor(t, dir, "prod", "v1"); len(rows) != 1 {
		t.Errorf("release v1 has %d bundle rows for prod, want exactly 1", len(rows))
	}
}

// A render whose OBJECTS differ is a different deploy, and writing it under
// the same release would make the release mean whichever was written last. It
// is refused, naming the object — unless the caller re-records on purpose.
func TestWriteEnvBundle_ARelease_RefusesADifferentRender(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-of-record")
	stream, forge := bundleTestStream, "v0.1.44-d6b5d722"
	stubEnvShapeStream(t, "bundle-of-record", &stream, &forge)
	ctx := context.Background()
	in := bundleBuildInputs{
		Release: "v1", Pins: release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Now: bundleTestNow, errOut: io.Discard,
	}
	first, err := writeEnvBundle(ctx, dir, "prod", in)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	stream = strings.Replace(bundleTestStream, "replicas: 2", "replicas: 5", 1)
	_, err = writeEnvBundle(ctx, dir, "prod", in)
	if err == nil {
		t.Fatal("a render with different objects was recorded under the same release")
	}
	for _, want := range []string{"DIFFERENT objects", "changed", "Deployment app/api", "--rerecord-bundle"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}

	in.Rerecord = true
	again, err := writeEnvBundle(ctx, dir, "prod", in)
	if err != nil {
		t.Fatalf("--rerecord-bundle: %v", err)
	}
	if again.Digest == first.Digest {
		t.Error("--rerecord-bundle did not record the new render")
	}
}

// The hosted half: the control plane has no (env, release) lookup, so the
// release's bundle is found by the registry tag set when it was recorded. A
// later write for the release — another binary, the release's source pins —
// reuses it: the record is re-sent with the RELEASE'S OWN bytes (idempotent on
// (env, digest)), never with this render's.
func TestWriteEnvBundle_HostedRelease_ReusesTheTaggedBundle(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-hosted-project")
	stream, forge := bundleTestStream, "v0.1.44-d6b5d722"
	stubEnvShapeStream(t, "bundle-hosted-project", &stream, &forge)
	writeHostedPushBaseFixture(t, dir, "prod", "ghcr.io/acme")

	registry := memory.New()
	prevTarget := bundlePushTarget
	bundlePushTarget = func(context.Context, string, string, string) (bundle.Pusher, error) { return registry, nil }
	t.Cleanup(func() { bundlePushTarget = prevTarget })

	var recorded []string
	fake := &fakeDSOTCaller{replies: map[string]any{
		procEnsureEnv: map[string]any{"environment": map[string]any{"id": "env_prod"}},
	}}
	fake.reply = func(proc string, body map[string]any) (any, bool) {
		if proc != procRecordBundle {
			return nil, false
		}
		recorded = append(recorded, bundleDigestOfRequest(t, body))
		var served map[string]any
		if err := json.Unmarshal([]byte(cpBundleFixture), &served); err != nil {
			t.Fatal(err)
		}
		served["digest"] = bundleDigestOfRequest(t, body)
		return map[string]any{"bundle": served, "created": true}, true
	}
	// A current control plane: it decodes every optional document feature.
	fake.replies[procDeployCapabilities] = map[string]any{"bundleDocumentFeatures": release.BundleDocFeatures()}
	prevLedger := bundleLedgerFor
	bundleLedgerFor = func(context.Context, string, string) (envLedger, error) {
		return envLedger{Bindings: hostedBindingsStub{}, Releases: hostedReleasesStub{}, Hosted: true}, nil
	}
	t.Cleanup(func() { bundleLedgerFor = prevLedger })
	prevRecorder := bundleRecorderForEnv
	bundleRecorderForEnv = func(context.Context, string, string, envLedger) (bundleRecorder, error) {
		return hostedRecordStore{client: fake, project: "bundle-hosted-project", resolver: stubEnvResolver{id: "env_prod"}}, nil
	}
	t.Cleanup(func() { bundleRecorderForEnv = prevRecorder })

	ctx := context.Background()
	cut, err := writeEnvBundle(ctx, dir, "prod", bundleBuildInputs{Release: "v1", Now: bundleTestNow, Pushed: true, errOut: io.Discard})
	if err != nil {
		t.Fatalf("cut: %v", err)
	}
	if desc, err := registry.Resolve(ctx, releaseBundleTag("v1")); err != nil || string(desc.Digest) != cut.Digest {
		t.Fatalf("the recorded bundle is not tagged %s: %v (%v)", releaseBundleTag("v1"), desc.Digest, err)
	}

	forge = "v0.1.44-5d2053a6"
	plan, err := writeEnvBundle(ctx, dir, "prod", bundleBuildInputs{
		Release: "v1", Now: bundleTestNow, Pushed: true, errOut: io.Discard,
		Pins: release.BundlePins{Sources: map[string]release.Source{"web": {Repo: "github.com/acme/web", Commit: rep40('e')}}},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.Digest != cut.Digest {
		t.Errorf("the plan's bundle is %s, the release's is %s", shortDigest(plan.Digest), shortDigest(cut.Digest))
	}
	for i, d := range recorded {
		if d != cut.Digest {
			t.Errorf("RecordBundle call %d sent bundle %s; every record of release v1 must be its bundle %s",
				i+1, shortDigest(d), shortDigest(cut.Digest))
		}
	}
}

func TestReleaseBundleTag(t *testing.T) {
	for in, want := range map[string]string{
		"20261007.102305-f63c36382f4a": "release-20261007.102305-f63c36382f4a",
		"v1.2.3+build.7":               "release-v1.2.3_build.7",
	} {
		if got := releaseBundleTag(in); got != want {
			t.Errorf("releaseBundleTag(%q) = %q, want %q", in, got, want)
		}
	}
}
