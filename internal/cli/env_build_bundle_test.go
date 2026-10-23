package cli

// `forge env build <env>` WRITES THE BUNDLE (doc §4.4, §7.2).
//
// WHAT THESE PIN, and why each is a separate claim. The bundle is the record
// that makes "what was shipped" answerable, so three independent things have
// to hold: the bytes exist, they are in the place the env's LEDGER says they
// go, and the record points at the bytes that were actually written. Any one
// of them silently wrong produces a ledger that reads fine and describes a
// deploy nobody can reproduce — which is strictly worse than no record.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"oras.land/oras-go/v2/content/memory"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// bundleTestNow is the bundle's creation time. Stated, because bundle.Build
// refuses a zero time and never reads a clock: its digest must be a function
// of the render and nothing else, so a test that let the clock in would get a
// different digest on every run.
var bundleTestNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// stubEnvShape points the projection at a literal, so these tests need no KCL
// and no helm.
//
// The projection itself is NOT what is under test — internal/bundle covers it
// from a literal fixture, and env_shape_test.go covers the render. What is
// under test is everything downstream: build the bundle from that shape, put
// the bytes where the ledger says, record what was written.
func stubEnvShape(t *testing.T, project string) {
	t.Helper()
	prev := projectEnvShapeFn
	projectEnvShapeFn = func(_ context.Context, _ io.Writer, env string) (envShapeDoc, error) {
		shape, err := bundle.ProjectShape(bundle.ShapeInput{
			Kind:      release.EnvSelfManaged,
			Workloads: []release.ShapeWorkload{{Name: "api", Runtime: "cluster", Cluster: "prod-ctx"}},
			Clusters:  []string{"prod-ctx"},
			Manifests: bundleTestStream,
		})
		if err != nil {
			t.Fatalf("project the fixture shape: %v", err)
		}
		return envShapeDoc{
			Project: project, Env: env, Kind: string(shape.Kind), Shape: shape,
			Provenance: release.Provenance{Commit: rep40('c'), Tree: rep40('a')},
			manifests:  bundleTestStream,
		}, nil
	}
	t.Cleanup(func() { projectEnvShapeFn = prev })
}

// bundleTestStream is a two-document render in the annotated form
// internal/bundle parses — the `# cluster:` header included, because that is
// how the deploy's own router reports which cluster a document lands on.
const bundleTestStream = `# cluster: prod-ctx
apiVersion: v1
kind: Namespace
metadata:
  name: app
---
# cluster: prod-ctx
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: app
spec:
  replicas: 2
`

// TestEnvBuildBundle_FileLedgerEnvWritesLocallyAndRecords is the file-ledger
// half. A self-managed env writes into the machine ledger's OWN OCI layout,
// beside the records that index it, and the recorded reference names that
// layout — not a registry.
//
// The two halves sitting together is the property: a bundle's row and its
// blob must not be able to end up on different machines or in different
// lifetimes, because a row pointing at blobs nobody has is F-4's shape.
func TestEnvBuildBundle_FileLedgerEnvWritesLocallyAndRecords(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-build-project")
	stubEnvShape(t, "bundle-build-project")

	written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0",
		Pins:    release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Now:     bundleTestNow,
		errOut:  io.Discard,
	})
	if err != nil {
		t.Fatalf("writeEnvBundles: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("wrote %d bundles, want 1", len(written))
	}
	got := written[0]

	if got.Pushed {
		t.Error("a build that pushed nothing must not push its bundle: a registry reference to bytes " +
			"nobody pushed is a record of a deploy that cannot be performed")
	}
	if !got.Recorded || !got.Created {
		t.Errorf("the bundle must be recorded in the machine ledger: recorded=%v created=%v", got.Recorded, got.Created)
	}
	if !strings.HasPrefix(got.Reference, "oci-layout:") {
		t.Errorf("a local bundle's reference must name the layout holding its blobs, got %q", got.Reference)
	}
	if got.Objects != 2 {
		t.Errorf("the bundle's shape describes %d object(s), want the render's 2", got.Objects)
	}

	// THE BLOBS ARE REALLY THERE, read back through the same verifying
	// path a deploy uses. A record whose blobs are missing is exactly the
	// failure the reference exists to prevent, and only a fetch can tell
	// the two apart.
	store := testStore(t, dir)
	layout, err := bundle.NewLocalLayout(store.OCIDir())
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := bundle.Fetch(context.Background(), layout, got.Digest)
	if err != nil {
		t.Fatalf("the recorded bundle must be fetchable from the layout it names: %v", err)
	}
	if fetched.Doc.Release != "v1.4.0" || fetched.Doc.Env != "prod" {
		t.Errorf("the bundle document must name the env and release it was built for, got %+v", fetched.Doc)
	}

	// And the LEDGER ROW points at those bytes.
	bundles, err := store.Bundles("prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundles) != 1 {
		t.Fatalf("the ledger holds %d bundles for prod, want 1", len(bundles))
	}
	if bundles[0].Digest != got.Digest || bundles[0].Reference != got.Reference {
		t.Errorf("the row must point at the bytes that were written: row=%+v written=%+v", bundles[0], got)
	}
	if bundles[0].ConfigDigest == "" {
		t.Error("the row must carry the config digest: it is what tells a spec change from a promotion")
	}
}

// TestEnvBuildBundle_IsIdempotentOnItsOwnBytes: a bundle IS its content, so a
// re-run of the same build re-records nothing.
//
// This is what makes a retried deploy free (F-3). It also proves the digest is
// a function of the RENDER and not of the clock or the run: a bundle that
// re-cut a record on every build would make "has this already been deployed"
// always answer no.
func TestEnvBuildBundle_IsIdempotentOnItsOwnBytes(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-retry-project")
	stubEnvShape(t, "bundle-retry-project")
	in := bundleBuildInputs{Release: "v1.4.0", Now: bundleTestNow, errOut: io.Discard}

	first, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, in)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, in)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}

	if first[0].Digest != second[0].Digest {
		t.Errorf("one render must produce one digest: %s then %s", first[0].Digest, second[0].Digest)
	}
	if !first[0].Created {
		t.Error("the first record must create")
	}
	if second[0].Created {
		t.Error("re-recording one digest for one env must report created=false — that is what makes a retry free")
	}
	if n := len(mustBundles(t, dir, "prod")); n != 1 {
		t.Errorf("two identical builds left %d rows, want 1", n)
	}
}

// TestEnvBuildBundle_BundleEnvsWritesOnePerEnv is --bundle-envs. A release is
// env-agnostic and a bundle is not, so one cut must be able to produce the
// manifests for every env it will ship to.
//
// The DIGESTS MUST DIFFER, which is the real assertion: each bundle is its own
// env's render. One digest for two envs would mean the writer rendered once
// and labelled the result twice, and the second env's deploy would apply the
// first env's manifests.
func TestEnvBuildBundle_BundleEnvsWritesOnePerEnv(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-multi-project")
	stubEnvShape(t, "bundle-multi-project")

	written, err := writeEnvBundles(context.Background(), dir, []string{"staging", "prod"}, bundleBuildInputs{
		Release: "v1.4.0", Now: bundleTestNow, errOut: io.Discard,
	})
	if err != nil {
		t.Fatalf("writeEnvBundles: %v", err)
	}
	if len(written) != 2 {
		t.Fatalf("wrote %d bundles, want one per env", len(written))
	}
	if written[0].Env != "staging" || written[1].Env != "prod" {
		t.Errorf("bundles must be written for the envs named, got %s and %s", written[0].Env, written[1].Env)
	}
	if written[0].Digest == written[1].Digest {
		t.Error("two envs must produce two bundles: one digest for both would mean the second env's " +
			"deploy applies the first env's manifests")
	}
	for _, env := range []string{"staging", "prod"} {
		if n := len(mustBundles(t, dir, env)); n != 1 {
			t.Errorf("env %s holds %d bundle rows, want 1", env, n)
		}
	}
}

// TestEnvBuildBundle_HostedEnvPushesAndRecordsTheBytes is the control-plane
// half, and the one that matters most: the server records what it VERIFIES.
//
// So this asserts the whole chain — the bundle is pushed to
// <image_push_base>/bundle.v1/<env>, the recorded reference is that push's,
// and RecordBundle carried the manifest and config BYTES rather than a
// description of them.
//
// It runs against two control planes, because the document forge sends is
// built for its reader: one that decodes every optional document feature gets
// them, and one that predates GetCapabilities — every control plane built
// before forge#850 — gets none of #850's fields, which its strict decoder
// would refuse (`unknown field "commit_subject"`).
func TestEnvBuildBundle_HostedEnvPushesAndRecordsTheBytes(t *testing.T) {
	for _, server := range []struct {
		name    string
		decodes bool
	}{
		{"a control plane that decodes every document feature", true},
		{"a control plane that predates GetCapabilities", false},
	} {
		t.Run(server.name, func(t *testing.T) { hostedEnvPushesAndRecordsTheBytes(t, server.decodes) })
	}
}

func hostedEnvPushesAndRecordsTheBytes(t *testing.T, decodes bool) {
	dir := newLedgerTestProject(t, "bundle-hosted-project")
	stubEnvShape(t, "bundle-hosted-project")
	writeHostedPushBaseFixture(t, dir, "prod", "ghcr.io/acme")

	registry := memory.New()
	prevTarget := bundlePushTarget
	var pushedTo string
	bundlePushTarget = func(_ context.Context, _, _, reference string) (bundle.Pusher, error) {
		pushedTo = reference
		return registry, nil
	}
	t.Cleanup(func() { bundlePushTarget = prevTarget })

	// The fake answers with the digest forge actually pushed. A canned
	// fixture digest would trip the record-vs-artifact check below, which
	// is correct behaviour but not what this test is about — and the
	// mismatch guard has its own test.
	fake := &fakeDSOTCaller{replies: map[string]any{
		// RecordBundle resolves (creating if needed) the env id first:
		// a bundle is recorded for an env the KCL declares, so whichever
		// verb reaches the control plane first creates it.
		procEnsureEnv: map[string]any{"environment": map[string]any{"id": "env_prod"}},
	}}
	fake.reply = func(proc string, body map[string]any) (any, bool) {
		if proc != procRecordBundle {
			return nil, false
		}
		var served map[string]any
		if err := json.Unmarshal([]byte(cpBundleFixture), &served); err != nil {
			t.Fatal(err)
		}
		served["digest"] = bundleDigestOfRequest(t, body)
		return map[string]any{"bundle": served, "created": true}, true
	}
	if decodes {
		fake.replies[procDeployCapabilities] = map[string]any{"bundleDocumentFeatures": release.BundleDocFeatures()}
	} else {
		fake.errs = map[string]error{procDeployCapabilities: &cloud.Error{HTTPStatus: 404, Code: cloud.CodeUnimplemented}}
	}
	prevLedger := bundleLedgerFor
	bundleLedgerFor = func(context.Context, string, string) (envLedger, error) {
		return envLedger{
			Bindings: hostedBindingsStub{}, Releases: hostedReleasesStub{}, Hosted: true,
		}, nil
	}
	t.Cleanup(func() { bundleLedgerFor = prevLedger })
	prevRecorder := bundleRecorderForEnv
	bundleRecorderForEnv = func(context.Context, string, string, envLedger) (bundleRecorder, error) {
		return hostedRecordStore{client: fake, project: "bundle-hosted-project",
			resolver: stubEnvResolver{id: "env_prod"}}, nil
	}
	t.Cleanup(func() { bundleRecorderForEnv = prevRecorder })

	written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0", Now: bundleTestNow, Pushed: true, errOut: io.Discard,
	})
	if err != nil {
		t.Fatalf("writeEnvBundles: %v", err)
	}
	got := written[0]

	if pushedTo != "ghcr.io/acme/bundle.v1/prod" {
		t.Errorf("a hosted env's bundle goes beside its images at <push-base>/bundle.v1/<env>, got %q", pushedTo)
	}
	if !got.Pushed || !strings.HasPrefix(got.Reference, "ghcr.io/acme/bundle.v1/prod@sha256:") {
		t.Errorf("the recorded reference must be the push's: pushed=%v ref=%q", got.Pushed, got.Reference)
	}
	if !got.Recorded {
		t.Error("a hosted env's bundle must be recorded on its control plane")
	}

	// THE BYTES, NOT A DESCRIPTION. This is doc §6.3's rule: the server
	// checks digest = sha256(manifest) and derives the shape from its own
	// decode of the config blob, so a client that could state a shape could
	// record one that disagrees with what it pushed.
	body := fake.body(t, procRecordBundle)
	for _, required := range []string{"manifest", "config", "repository"} {
		if _, present := body[required]; !present {
			t.Errorf("RecordBundle sent no %q — the server records what it verifies", required)
		}
	}
	for _, described := range []string{"shape", "provenance", "configDigest", "digest"} {
		if _, present := body[described]; present {
			t.Errorf("RecordBundle must not describe the bundle (%q): the server derives it from the bytes", described)
		}
	}

	// THE DOCUMENT IS BUILT FOR ITS READER.
	encoded, _ := body["config"].(string)
	rawConfig, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("config bytes: %v", err)
	}
	var top struct {
		Objects    *json.RawMessage           `json:"objects"`
		Provenance map[string]json.RawMessage `json:"provenance"`
	}
	if err := json.Unmarshal(rawConfig, &top); err != nil {
		t.Fatal(err)
	}
	_, subject := top.Provenance["commit_subject"]
	_, author := top.Provenance["commit_author"]
	switch {
	case !decodes && (top.Objects != nil || subject || author):
		t.Errorf("a control plane that names no document feature was sent forge#850 keys (objects=%v subject=%v author=%v); "+
			"its strict decoder refuses the whole bundle for one of them", top.Objects != nil, subject, author)
	case decodes && top.Objects == nil:
		t.Error("a control plane that decodes object summaries was sent none")
	}
}

// TestEnvBuildBundle_AnUnreachableControlPlaneDoesNotFailTheBuild is #404's
// rule applied to the bundle: `forge env build` never needs a reachable
// control plane. Opening a hosted env's ledger (or its records half) needs a
// credential and a reachable server; when the control plane is UNDELIVERABLE —
// no credential, a transport error, Unavailable — the build warns and
// continues, exactly as the declaration step does. An ANSWERED refusal still
// fails the build.
//
// This is control-plane's hosted_deploy fixtures (TestFixtureBuildPushes…),
// which build against an unreachable endpoint on purpose: with F6a's bundle
// write they went red again on "no control-plane credential", the same two
// tests #404 fixed for the declaration.
//
// Mutation: return the seam's error unclassified and the first two cases fail.
func TestEnvBuildBundle_AnUnreachableControlPlaneDoesNotFailTheBuild(t *testing.T) {
	noCred := fmt.Errorf("env %q keeps its release ledger on the control plane at %s: %w",
		"prod", "http://127.0.0.1:9", cloud.ErrNoCredential)
	refused := &cloud.Error{HTTPStatus: 401, Code: cloud.CodeUnauthenticated, Message: "token rejected"}

	for _, tc := range []struct {
		name              string
		ledgerErr, recErr error
		wantErr           bool
		wantSkipped       bool
	}{
		{name: "ledger: no credential", ledgerErr: noCred, wantSkipped: true},
		{name: "records half: no credential", recErr: noCred},
		{name: "ledger: credential refused", ledgerErr: refused, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newLedgerTestProject(t, "bundle-offline-project")
			stubEnvShape(t, "bundle-offline-project")

			prevLedger := bundleLedgerFor
			bundleLedgerFor = func(ctx context.Context, projectDir, env string) (envLedger, error) {
				if tc.ledgerErr != nil {
					return envLedger{}, tc.ledgerErr
				}
				return prevLedger(ctx, projectDir, env)
			}
			t.Cleanup(func() { bundleLedgerFor = prevLedger })
			prevRecorder := bundleRecorderForEnv
			bundleRecorderForEnv = func(ctx context.Context, projectDir, env string, l envLedger) (bundleRecorder, error) {
				if tc.recErr != nil {
					return nil, tc.recErr
				}
				return prevRecorder(ctx, projectDir, env, l)
			}
			t.Cleanup(func() { bundleRecorderForEnv = prevRecorder })

			var warn strings.Builder
			written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
				Now: bundleTestNow, errOut: &warn,
			})
			if tc.wantErr {
				if err == nil {
					t.Fatal("a control plane that ANSWERS and refuses must fail the build")
				}
				return
			}
			if err != nil {
				t.Fatalf("an undeliverable control plane must not fail the build (#404), got: %v", err)
			}
			if len(written) != 1 || written[0].Recorded {
				t.Fatalf("the bundle must be reported as NOT recorded, got %+v", written)
			}
			if written[0].Skipped != tc.wantSkipped {
				t.Errorf("skipped = %v, want %v", written[0].Skipped, tc.wantSkipped)
			}
			if !strings.Contains(warn.String(), "The build continues") {
				t.Errorf("the undeliverable case must warn and say the build continues, got:\n%s", warn.String())
			}
		})
	}
}

// TestEnvBuildBundle_HostedEnvWithNoPushBaseFallsBackLocally: an env whose
// DECLARATION composes no registry subtree still gets a bundle.
//
// NOT an error, deliberately. The images were pushed to their own declared
// references and the build stands; there is simply nowhere to put the bundle.
// Failing here would make a build fail over an address it needs only in order
// to push a RECORD.
//
// The cause changed with F-REGISTRY-DEFAULT and the message had to follow. It
// used to be "the control plane has not reported a base yet", which implied a
// later build would learn one — so the note promised exactly that. The base is
// now composed from the env's own declaration, so there is no "yet": an env
// that declares no organization will not acquire a base by being rebuilt, and
// the note has to say what to declare instead of what to wait for.
func TestEnvBuildBundle_HostedEnvWithNoPushBaseFallsBackLocally(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-nobase-project")
	stubEnvShape(t, "bundle-nobase-project")

	prevLedger := bundleLedgerFor
	bundleLedgerFor = func(context.Context, string, string) (envLedger, error) {
		return envLedger{Bindings: hostedBindingsStub{}, Releases: hostedReleasesStub{}, Hosted: true}, nil
	}
	t.Cleanup(func() { bundleLedgerFor = prevLedger })
	// The RECORD still goes to the machine ledger here, which is what the
	// fallback means: the bytes are local, so the reference naming them is
	// local too.
	prevRecorder := bundleRecorderForEnv
	bundleRecorderForEnv = func(_ context.Context, projectDir, _ string, _ envLedger) (bundleRecorder, error) {
		return recordStoreFor(projectDir)
	}
	t.Cleanup(func() { bundleRecorderForEnv = prevRecorder })

	var warnings strings.Builder
	written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0", Now: bundleTestNow, Pushed: true, errOut: &warnings,
	})
	if err != nil {
		t.Fatalf("a hosted env with no known push base must still write a bundle: %v", err)
	}
	if written[0].Pushed {
		t.Error("with no push base there is nowhere to push, so the bundle must be local")
	}
	if !strings.Contains(warnings.String(), "declares no platform registry subtree") {
		t.Errorf("the fallback must say why it was local; warnings were %q", warnings.String())
	}
	// And it must NOT promise that a later build will learn the address,
	// which was true of the cache and is not true of a declaration.
	if strings.Contains(warnings.String(), "next build") {
		t.Errorf("the note must not promise a later build learns the base — the address comes from "+
			"the declaration, so rebuilding changes nothing. Got %q", warnings.String())
	}
}

// TestEnvBuildBundle_RefusesARecordThatNamesOtherBytes: if the control plane
// records a digest other than the one forge pushed, the two do not describe
// the same bundle and the build must fail.
//
// Silently accepting it would be the worst available outcome, because every
// reader downstream believes the RECORD: Live would show a bundle, a plan
// would be computed against it, and a deploy would apply bytes nobody can
// reconcile with what was pushed. There is no repair forge can make here —
// only a refusal that says the two disagree.
func TestEnvBuildBundle_RefusesARecordThatNamesOtherBytes(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-mismatch-project")
	stubEnvShape(t, "bundle-mismatch-project")

	fake := &fakeDSOTCaller{replies: map[string]any{
		procEnsureEnv: map[string]any{"environment": map[string]any{"id": "env_prod"}},
		// The canned fixture's digest, which is NOT what this render
		// produces.
		procRecordBundle: map[string]any{"bundle": json.RawMessage(cpBundleFixture), "created": true},
	}}
	prevRecorder := bundleRecorderForEnv
	bundleRecorderForEnv = func(context.Context, string, string, envLedger) (bundleRecorder, error) {
		return hostedRecordStore{client: fake, project: "bundle-mismatch-project",
			resolver: stubEnvResolver{id: "env_prod"}}, nil
	}
	t.Cleanup(func() { bundleRecorderForEnv = prevRecorder })

	_, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0", Now: bundleTestNow, errOut: io.Discard,
	})
	if err == nil {
		t.Fatal("a record naming different bytes than were pushed must fail the build: " +
			"every reader downstream believes the record")
	}
	if !strings.Contains(err.Error(), "do not describe the same bytes") {
		t.Errorf("the refusal must say what disagrees; got: %v", err)
	}
}

// TestParseBundleEnvs covers the flag parse, including the two inputs that
// must NOT be read as "bundle an env with no name".
func TestParseBundleEnvs(t *testing.T) {
	t.Parallel()
	for flag, want := range map[string][]string{
		"":                 {"prod"},
		"   ":              {"prod"},
		"staging,prod":     {"staging", "prod"},
		" staging , prod ": {"staging", "prod"},
		"prod,":            {"prod"},
		",":                {"prod"},
		"prod,prod":        {"prod"},
	} {
		got := parseBundleEnvs(flag, "prod")
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("parseBundleEnvs(%q) = %v, want %v", flag, got, want)
		}
	}
}

// bundleDigestOfRequest is the digest of the manifest bytes a RecordBundle
// request carried — what the server computes and records.
//
// Recomputing it here rather than hard-coding one keeps the fake honest: it
// answers about the bytes it was actually sent, so the test cannot pass
// against a request that carried different ones.
func bundleDigestOfRequest(t *testing.T, body map[string]any) string {
	t.Helper()
	encoded, ok := body["manifest"].(string)
	if !ok {
		t.Fatalf("RecordBundle carried no manifest bytes; body = %v", body)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("the manifest must be base64, as proto3 JSON encodes bytes: %v", err)
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// mustBundles reads an env's bundle rows.
func mustBundles(t *testing.T, dir, env string) []release.BundleRecord {
	t.Helper()
	rows, err := testStore(t, dir).Bundles(env)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// writeHostedPushBaseFixture states the env's DECLARED platform registry
// subtree.
//
// It stubs the resolution rather than writing a declaration, because these
// projects have no deploy/kcl tree at all — stubEnvShape points the projection
// at a literal for the same reason. What these tests are about is the
// PLACEMENT (pushed to the env's bundle repository, or written to the local
// layout), not how an address is composed from a declaration, which
// internal/hostedimage owns and tests.
func writeHostedPushBaseFixture(t *testing.T, dir, env, base string) {
	t.Helper()
	prev := bundlePushBaseFor
	bundlePushBaseFor = func(_ context.Context, _, gotEnv string) string {
		if gotEnv != env {
			return ""
		}
		return base
	}
	t.Cleanup(func() { bundlePushBaseFor = prev })
	_ = dir
}

// hostedBindingsStub / hostedReleasesStub stand in for a control-plane ledger
// in the tests whose subject is the BUNDLE, not the promotion or the release.
type hostedBindingsStub struct{}

func (hostedBindingsStub) Current(context.Context, string) (release.Promotion, bool, error) {
	return release.Promotion{}, false, nil
}

func (hostedBindingsStub) Append(context.Context, release.Promotion, appendGuard) (release.Promotion, error) {
	return release.Promotion{}, nil
}

func (hostedBindingsStub) Location() string { return "https://control-plane.test" }

type hostedReleasesStub struct{}

func (hostedReleasesStub) Cut(context.Context, release.Release) (bool, error) { return true, nil }

func (hostedReleasesStub) Get(context.Context, string) (*release.Release, error) { return nil, nil }

func (hostedReleasesStub) List(context.Context) ([]release.Release, error) { return nil, nil }

func (hostedReleasesStub) Location() string { return "https://control-plane.test" }

// TestEnvBuildReachesTheBundleStep is the WIRING, which no producer test can
// cover: it asserts the build CALLS the bundle writer, with the env it built,
// the release it cut, and whether it pushed.
//
// Without this, `forge env build` could stop writing bundles entirely and
// every test above would still pass — they call the writer directly. That is
// the regression that matters most here, because its symptom is not an error
// but an absent record, discovered later by a deploy that has no bundle to
// apply.
func TestEnvBuildReachesTheBundleStep(t *testing.T) {
	var got []bundleBuildInputs
	var gotEnvs [][]string
	prev := writeBundlesFn
	writeBundlesFn = func(_ context.Context, _ string, envs []string, in bundleBuildInputs) ([]bundleWriteOutcome, error) {
		gotEnvs = append(gotEnvs, envs)
		got = append(got, in)
		return nil, nil
	}
	t.Cleanup(func() { writeBundlesFn = prev })

	dir := newLedgerTestProject(t, "bundle-wiring-project")
	t.Chdir(dir)
	if err := writeBuildBundle(context.Background(), buildOptions{
		env: "prod", release: "v1.4.0", push: true, bundleEnvs: "staging,prod",
	}); err != nil {
		t.Fatalf("writeBuildBundle: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("the build called the bundle writer %d times, want once", len(got))
	}
	if strings.Join(gotEnvs[0], ",") != "staging,prod" {
		t.Errorf("--bundle-envs must reach the writer, got %v", gotEnvs[0])
	}
	if got[0].Release != "v1.4.0" {
		t.Errorf("the bundle must name the release the build cut, got %q", got[0].Release)
	}
	if !got[0].Pushed {
		t.Error("a build that pushed must say so: it decides whether the bundle is pushed or written locally")
	}
	if got[0].Now.IsZero() {
		t.Error("the build must stamp the bundle's creation time — bundle.Build never reads a clock")
	}
}

// TestEnvBuildSkipsTheBundleWithNoEnvOrUnderPlan: the two cases that must
// write nothing.
//
// A bundle is ONE env's render, so a compile-only `forge build` has no env to
// render. --plan preflights a build and must not be the thing that changed
// the world — the same rule the declaration step follows.
func TestEnvBuildSkipsTheBundleWithNoEnvOrUnderPlan(t *testing.T) {
	var calls int
	prev := writeBundlesFn
	writeBundlesFn = func(context.Context, string, []string, bundleBuildInputs) ([]bundleWriteOutcome, error) {
		calls++
		return nil, nil
	}
	t.Cleanup(func() { writeBundlesFn = prev })

	for name, opts := range map[string]buildOptions{
		"no env": {env: "", release: "v1"},
		"--plan": {env: "prod", plan: true},
	} {
		calls = 0
		if err := writeBuildBundle(context.Background(), opts); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if calls != 0 {
			t.Errorf("%s must write no bundle, wrote %d", name, calls)
		}
	}
}

// TestEnvBuildBundle_AFailedPushFallsBackLocallyAndDoesNotFailTheBuild is the
// rule a real test run caught me breaking.
//
// A bundle is a RECORD, and a missing one is recoverable — `forge env deploy`
// renders one on demand. So a push that cannot reach the bundle subtree must
// degrade, exactly as a failed RecordBundle does, rather than failing a build
// whose images went to their own declared references and whose release is cut.
//
// Making it fatal meant a build newly depended on the bundle registry being
// reachable AND writable, which is a different fact from the image registry
// being reachable: a registry can admit images and reject an artifact type,
// and a project can push images somewhere the control plane does not name as
// its base. Two existing tests that push images to a stub and have no route to
// the declared registry started failing the whole build.
func TestEnvBuildBundle_AFailedPushFallsBackLocallyAndDoesNotFailTheBuild(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-pushfail-project")
	stubEnvShape(t, "bundle-pushfail-project")
	writeHostedPushBaseFixture(t, dir, "prod", "registry.unreachable.invalid/acme")

	prevTarget := bundlePushTarget
	bundlePushTarget = func(context.Context, string, string, string) (bundle.Pusher, error) {
		return nil, errors.New("dial tcp: lookup registry.unreachable.invalid: no such host")
	}
	t.Cleanup(func() { bundlePushTarget = prevTarget })

	prevLedger := bundleLedgerFor
	bundleLedgerFor = func(context.Context, string, string) (envLedger, error) {
		return envLedger{Bindings: hostedBindingsStub{}, Releases: hostedReleasesStub{}, Hosted: true}, nil
	}
	t.Cleanup(func() { bundleLedgerFor = prevLedger })
	prevRecorder := bundleRecorderForEnv
	bundleRecorderForEnv = func(_ context.Context, projectDir, _ string, _ envLedger) (bundleRecorder, error) {
		return recordStoreFor(projectDir)
	}
	t.Cleanup(func() { bundleRecorderForEnv = prevRecorder })

	var warnings strings.Builder
	written, err := writeEnvBundles(context.Background(), dir, []string{"prod"}, bundleBuildInputs{
		Release: "v1.4.0", Now: bundleTestNow, Pushed: true, errOut: &warnings,
	})
	if err != nil {
		t.Fatalf("a bundle push that cannot reach the registry must not fail the build: %v", err)
	}
	if written[0].Pushed {
		t.Error("the push failed, so the bundle must not claim to have been pushed")
	}
	if !strings.HasPrefix(written[0].Reference, "oci-layout:") {
		t.Errorf("the fallback must record the LOCAL reference, not a registry one it does not hold; got %q",
			written[0].Reference)
	}
	if !strings.Contains(warnings.String(), "could not be pushed") {
		t.Errorf("the degradation must be reported, not silent; warnings were %q", warnings.String())
	}
	if !strings.Contains(warnings.String(), "not recorded") {
		t.Errorf("a hosted ledger must not receive a local-layout bundle; warnings were %q", warnings.String())
	}
	if written[0].Recorded {
		t.Error("a hosted bundle that was not pushed must not be recorded")
	}
	// And the bytes really are local, so a deploy can still fetch them.
	if _, ferr := bundle.Fetch(context.Background(), mustLocalLayout(t, dir), written[0].Digest); ferr != nil {
		t.Errorf("the locally-written bundle must be fetchable: %v", ferr)
	}
}

// mustLocalLayout opens the machine ledger's OCI layout.
func mustLocalLayout(t *testing.T, dir string) *bundle.LocalLayout {
	t.Helper()
	layout, err := bundle.NewLocalLayout(testStore(t, dir).OCIDir())
	if err != nil {
		t.Fatal(err)
	}
	return layout
}

// TestEnvBuildBundle_ReleasedBundleDoesNotDependOnTheWallClock: a plan-only run
// seals a bundle too, and used to stamp it with time.Now, so two runs of one
// render of one release were two digests — two registry pushes, two ledger rows,
// and a promoted bundle that was never the planned one. A released bundle's
// creation time is the RELEASE's.
func TestEnvBuildBundle_ReleasedBundleDoesNotDependOnTheWallClock(t *testing.T) {
	dir := newLedgerTestProject(t, "bundle-clock-project")
	stubEnvShape(t, "bundle-clock-project")
	cutAt := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if err := testCutRelease(t, dir, rel("v1.4.0", cutAt.Format(time.RFC3339), "", false, map[string]string{"api": sha("1")})); err != nil {
		t.Fatalf("cut: %v", err)
	}

	run := func(now time.Time) bundleWriteOutcome {
		t.Helper()
		got, err := writeEnvBundles(context.Background(), dir, []string{"prod"},
			bundleBuildInputs{Release: "v1.4.0", Now: now, errOut: io.Discard})
		if err != nil {
			t.Fatalf("writeEnvBundles at %s: %v", now, err)
		}
		return got[0]
	}
	first := run(time.Date(2026, 10, 7, 6, 0, 0, 0, time.UTC))
	second := run(time.Date(2026, 10, 7, 6, 45, 12, 0, time.UTC))

	if first.Digest != second.Digest {
		t.Fatalf("one render of one release must be one digest regardless of when it runs: %s then %s", first.Digest, second.Digest)
	}
	if second.Created {
		t.Error("the second run recorded a new row for the same digest")
	}
	if n := len(mustBundles(t, dir, "prod")); n != 1 {
		t.Errorf("two runs at different times left %d bundle rows, want 1", n)
	}
}
