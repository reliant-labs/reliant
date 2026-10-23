package cli

// A LEDGER-ONLY env's plan diffs against what forge last applied.
//
// The fixture is control-plane prod's shape: a hosted ledger that converges
// nothing (Hosted + Mixed, no hosted tier), so forge's own apply is the whole
// deploy and the control plane holds no Live. Its server-side PlanDeploy is
// answered here exactly as the real one answers for such an env — over no
// applied bundle — so a test that still reached it reads every object as
// added, which is the 2026-10-10 defect.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

const ledgerOnlyEnvID = "env_prod"

func ledgerOnlyObject(kind, ns, name, config string) release.ShapeObject {
	return release.ShapeObject{
		Cluster: "prod-ctx", APIVersion: "v1", Kind: kind, Namespace: ns, Name: name,
		Hash: sha("hash:" + kind + name + config), ConfigHash: sha("config:" + kind + name + config),
	}
}

// ledgerOnlyShape is the applied render: a Deployment, its Service and a
// ConfigMap, reading two external Secrets.
func ledgerOnlyShape() release.Shape {
	return release.Shape{
		Kind:      release.EnvSelfManaged,
		Workloads: []release.ShapeWorkload{{Name: "api", Runtime: "cluster", Cluster: "prod-ctx"}},
		Secrets: []release.ShapeSecret{
			{Name: "db-credentials", Provider: "external", DeclaredBy: []string{"api"}},
			{Name: "cloudflare-api-token", Provider: "external"},
		},
		Clusters: []string{"prod-ctx"},
		Objects: []release.ShapeObject{
			ledgerOnlyObject("Deployment", "app", "api", "v1"),
			ledgerOnlyObject("Service", "app", "api", "v1"),
			ledgerOnlyObject("ConfigMap", "app", "api-config", "v1"),
		},
	}
}

// ledgerOnlyBundle is one recorded bundle as the control plane holds it.
func ledgerOnlyBundle(id, version string, shape release.Shape) release.BundleRecord {
	return release.BundleRecord{
		ID: id, Env: "prod", Release: version,
		Digest:       sha("bundle:" + id),
		ConfigDigest: sha("bundle-config:" + id),
		Reference:    "registry.example.com/acme/control-plane/bundle.v1/prod@" + sha("bundle:"+id),
		Shape:        shape,
	}
}

func bundleToWire(t *testing.T, b release.BundleRecord) map[string]any {
	t.Helper()
	shape, err := json.Marshal(b.Shape)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"id": b.ID, "environmentId": ledgerOnlyEnvID, "releaseVersion": b.Release,
		"digest": b.Digest, "reference": b.Reference, "configDigest": b.ConfigDigest,
		"shape": json.RawMessage(shape), "createdAt": time.Unix(0, 0).UTC(),
	}
}

// ledgerOnlyControlPlane is a control plane holding these bundles, answering
// PlanDeploy the way the real one does for an env it does not converge: with
// no applied bundle to diff against.
func ledgerOnlyControlPlane(t *testing.T, bundles ...release.BundleRecord) *fakeDSOTCaller {
	t.Helper()
	byDigest := map[string]release.BundleRecord{}
	byID := map[string]release.BundleRecord{}
	for _, b := range bundles {
		byDigest[b.Digest], byID[b.ID] = b, b
	}
	return &fakeDSOTCaller{replies: map[string]any{}, reply: func(proc string, body map[string]any) (any, bool) {
		switch {
		case strings.HasSuffix(proc, "/ListEnvironments"):
			return map[string]any{"environments": []map[string]string{{"id": ledgerOnlyEnvID, "name": "prod"}}}, true
		case proc == procGetBundle:
			digest, _ := body["digest"].(string)
			b, ok := byDigest[digest]
			if !ok {
				return map[string]any{}, true
			}
			return map[string]any{"bundle": bundleToWire(t, b)}, true
		case proc == procPlanDeploy:
			id, _ := body["bundleId"].(string)
			plan, err := release.BuildPlan(release.PlanInput{
				EnvironmentID: ledgerOnlyEnvID, BundleID: id, ReleaseVersion: byID[id].Release,
				Candidate: byID[id].Shape, CandidateConfigDigest: byID[id].ConfigDigest,
			})
			if err != nil {
				t.Fatalf("server plan: %v", err)
			}
			return map[string]any{"plan": planToWireFixture(plan)}, true
		}
		return nil, false
	}}
}

// ledgerOnlyFixture wires the seams: the control plane above, the env's
// promotion history, and the cluster's answer for its Secrets.
func ledgerOnlyFixture(t *testing.T, cp *fakeDSOTCaller, history []release.Promotion, presence map[string]bool) envLedger {
	t.Helper()
	store := newMemBindingStore(map[string]release.Promotion{})
	store.history["prod"] = history
	prevStore, prevPresence, prevFetch := hostedRecordStoreForDeploy, deployPlanSecretPresence, fetchBundleRef
	hostedRecordStoreForDeploy = func(context.Context, string, string) (hostedRecordStore, error) {
		return hostedRecordStoreFor(cp, "control-plane"), nil
	}
	deployPlanSecretPresence = func(context.Context, string, string, release.Shape) map[string]bool { return presence }
	// No registry: the field summary is best-effort and these bundles have
	// no bytes. A test that wants it states the bytes (see realBundle).
	fetchBundleRef = func(_ context.Context, _, _, ref string) (bundle.Fetched, error) {
		return bundle.Fetched{}, errors.New("no registry in this test: " + ref)
	}
	t.Cleanup(func() {
		hostedRecordStoreForDeploy, deployPlanSecretPresence, fetchBundleRef = prevStore, prevPresence, prevFetch
	})
	ledger := envLedger{Bindings: store, Hosted: true, Mixed: true}
	if !ledger.ledgerOnly() {
		t.Fatal("fixture must be a ledger-only env: hosted ledger, applied from here, no hosted tier")
	}
	return ledger
}

// appliedPromotion is a promotion whose client-side apply SUCCEEDED and
// recorded the bundle it shipped — the gate recordApplyOutcome writes.
func appliedPromotion(id, version string, b release.BundleRecord) release.Promotion {
	started := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	return release.Promotion{
		ID: id, Env: "prod", Release: version, Kind: release.KindPromote,
		RecordedGates: []release.Gate{applyOutcomeGate(started, started.Add(4*time.Minute), nil, b.Digest, sha("plan:"+id))},
	}
}

func findingsByCode(p *release.Plan) map[string][]release.PlanFinding {
	out := map[string][]release.PlanFinding{}
	for _, f := range p.Findings {
		out[f.Code] = append(out[f.Code], f)
	}
	return out
}

func planLedgerOnly(t *testing.T, ledger envLedger, candidate release.BundleRecord) *release.Plan {
	t.Helper()
	plan, err := resolveDeployPlan(context.Background(), t.TempDir(), "prod", ledger, candidate.Digest, io.Discard)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan == nil {
		t.Fatal("no plan computed")
	}
	return plan
}

// THE DEFECT. After an apply, re-planning the same config must report
// nothing added and nothing removed — and must say what it diffed against.
func TestLedgerOnlyPlan_AfterAnApplyAnUnchangedConfigIsClean(t *testing.T) {
	applied := ledgerOnlyBundle("bnd_1", "v1", ledgerOnlyShape())
	candidate := ledgerOnlyBundle("bnd_2", "v2", ledgerOnlyShape())
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, applied, candidate),
		[]release.Promotion{appliedPromotion("p-1", "v1", applied)},
		map[string]bool{"db-credentials": true, "cloudflare-api-token": true})

	plan := planLedgerOnly(t, ledger, candidate)
	got := findingsByCode(plan)
	for _, code := range []string{release.FindingObjectAdded, release.FindingObjectRemoved, release.FindingObjectChanged,
		release.FindingConfigChanged, release.FindingSecretNeeded} {
		if len(got[code]) > 0 {
			t.Errorf("%d %s finding(s) for an unchanged config: %+v", len(got[code]), code, got[code])
		}
	}
	for _, f := range got[release.FindingUnknown] {
		if f.Section == release.SectionObjects {
			t.Errorf("the plan says it had nothing to diff against, after an apply recorded one: %+v", f)
		}
	}
	if plan.LiveBasis.AppliedBundleID != applied.ID {
		t.Errorf("computed against applied bundle %q, want %q (the last apply's)", plan.LiveBasis.AppliedBundleID, applied.ID)
	}
	if plan.LiveBasis.CurrentPromotionID != "p-1" {
		t.Errorf("basis promotion = %q, want p-1", plan.LiveBasis.CurrentPromotionID)
	}
}

// Removing an object from the KCL is visible, and only as a removal.
func TestLedgerOnlyPlan_RemovedObjectIsReported(t *testing.T) {
	applied := ledgerOnlyBundle("bnd_1", "v1", ledgerOnlyShape())
	without := ledgerOnlyShape()
	without.Objects = without.Objects[:2] // api-config dropped from the render
	candidate := ledgerOnlyBundle("bnd_2", "v2", without)
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, applied, candidate),
		[]release.Promotion{appliedPromotion("p-1", "v1", applied)}, nil)

	got := findingsByCode(planLedgerOnly(t, ledger, candidate))
	removed := got[release.FindingObjectRemoved]
	if len(removed) != 1 || removed[0].Subject != "prod-ctx/ConfigMap/app/api-config" {
		t.Fatalf("object_removed = %+v, want exactly prod-ctx/ConfigMap/app/api-config", removed)
	}
	// forge's direct apply does not prune: the reviewer must be told the
	// object will keep running, not that the deploy removes it.
	if !strings.Contains(removed[0].Detail, "does NOT delete it") {
		t.Errorf("removal detail = %q, want it to say the direct apply leaves the object running", removed[0].Detail)
	}
	if len(got[release.FindingObjectAdded]) != 0 {
		t.Errorf("object_added = %+v, want none", got[release.FindingObjectAdded])
	}
}

// A changed field is a change to that one object. With both sides carrying a
// config hash the plan names it a config change; when the split cannot be
// made it is an object change. Either way it is never an add.
func TestLedgerOnlyPlan_ChangedFieldIsReported(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*release.ShapeObject)
		wantCode string
	}{
		{"config hash on both sides", func(o *release.ShapeObject) {
			*o = ledgerOnlyObject("Deployment", "app", "api", "v2")
		}, release.FindingConfigChanged},
		{"no config hash to split on", func(o *release.ShapeObject) {
			o.Hash, o.ConfigHash = sha("hash:changed"), ""
		}, release.FindingObjectChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applied := ledgerOnlyBundle("bnd_1", "v1", ledgerOnlyShape())
			changed := ledgerOnlyShape()
			tc.mutate(&changed.Objects[0])
			candidate := ledgerOnlyBundle("bnd_2", "v2", changed)
			ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, applied, candidate),
				[]release.Promotion{appliedPromotion("p-1", "v1", applied)}, nil)

			got := findingsByCode(planLedgerOnly(t, ledger, candidate))
			if f := got[tc.wantCode]; len(f) != 1 || f[0].Subject != "prod-ctx/Deployment/app/api" {
				t.Fatalf("%s = %+v, want exactly prod-ctx/Deployment/app/api (all: %+v)", tc.wantCode, f, got)
			}
			if len(got[release.FindingObjectAdded])+len(got[release.FindingObjectRemoved]) != 0 {
				t.Errorf("a changed field reported as an add or a removal: %+v", got)
			}
		})
	}
}

// realBundle builds a real bundle over a one-Deployment render, and returns
// its record (as the control plane would hold it) and its fetched bytes.
func realBundle(t *testing.T, id, version string, replicas int) (release.BundleRecord, bundle.Fetched) {
	t.Helper()
	stream := "# cluster: prod-ctx\napiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n  namespace: app\n" +
		"spec:\n  replicas: " + strconv.Itoa(replicas) + "\n  template:\n    spec:\n      containers:\n        - name: api\n          image: ghcr.io/acme/api:v1\n"
	built, err := bundle.Build(context.Background(), bundle.BuildInput{
		Project: "control-plane", Env: "prod", Release: version,
		Shape:     bundle.ShapeInput{Kind: release.EnvSelfManaged, Clusters: []string{"prod-ctx"}, Manifests: stream},
		CreatedAt: time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("build bundle: %v", err)
	}
	layer, _ := built.Layer(release.BundleManifestsLayer)
	rec := release.BundleRecord{
		ID: id, Env: "prod", Release: version, Digest: built.Digest, ConfigDigest: built.Doc.ConfigDigest,
		Reference: "registry.example.com/acme/control-plane/bundle.v1/prod@" + built.Digest, Shape: built.Doc.Shape,
	}
	return rec, bundle.Fetched{Digest: built.Digest, Doc: built.Doc, Manifests: layer}
}

// A changed object's finding names the fields that changed — read from the
// two bundles — and that is prose for the reviewer, not part of what the
// approval names: the digest is the one the unannotated plan would carry.
func TestLedgerOnlyPlan_ChangedObjectNamesItsFields(t *testing.T) {
	applied, appliedBytes := realBundle(t, "bnd_1", "v1", 2)
	candidate, candidateBytes := realBundle(t, "bnd_2", "v2", 3)
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, applied, candidate),
		[]release.Promotion{appliedPromotion("p-1", "v1", applied)}, nil)
	prev := fetchBundleRef
	fetchBundleRef = func(_ context.Context, _, _, ref string) (bundle.Fetched, error) {
		switch ref {
		case applied.Reference:
			return appliedBytes, nil
		case candidate.Reference:
			return candidateBytes, nil
		}
		return bundle.Fetched{}, errors.New("no such bundle: " + ref)
	}
	t.Cleanup(func() { fetchBundleRef = prev })

	plan := planLedgerOnly(t, ledger, candidate)
	changed := findingsByCode(plan)[release.FindingConfigChanged]
	if len(changed) != 1 || changed[0].Subject != "prod-ctx/Deployment/app/api" {
		t.Fatalf("config_changed = %+v, want the api Deployment", changed)
	}
	if !strings.HasPrefix(changed[0].Detail, "fields: spec.replicas") {
		t.Errorf("detail = %q, want it to name spec.replicas", changed[0].Detail)
	}
	if ok, err := plan.VerifyDigest(); err != nil || !ok {
		t.Fatalf("an annotated plan must keep its digest: ok=%v err=%v", ok, err)
	}
}

// NEWEST SUCCEEDED: a failed apply after the last good one does not describe
// what runs, so it is not the baseline — the good one is.
func TestLedgerOnlyPlan_BaselineSkipsAFailedApply(t *testing.T) {
	good := ledgerOnlyBundle("bnd_1", "v1", ledgerOnlyShape())
	half := ledgerOnlyShape()
	half.Objects = half.Objects[:1]
	failed := ledgerOnlyBundle("bnd_2", "v2", half)
	candidate := ledgerOnlyBundle("bnd_3", "v3", ledgerOnlyShape())
	failedPromotion := release.Promotion{ID: "p-2", Env: "prod", Release: "v2", Kind: release.KindPromote,
		RecordedGates: []release.Gate{applyOutcomeGate(time.Now(), time.Now(), errors.New("preflight: image missing"), failed.Digest, "")}}
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, good, failed, candidate),
		[]release.Promotion{appliedPromotion("p-1", "v1", good), failedPromotion}, nil)

	plan := planLedgerOnly(t, ledger, candidate)
	if plan.LiveBasis.AppliedBundleID != good.ID {
		t.Fatalf("baseline = %q, want the last SUCCEEDED apply's bundle %q", plan.LiveBasis.AppliedBundleID, good.ID)
	}
}

// An apply recorded before the gate named its bundle cannot be a baseline,
// and neither can anything older than it: the walk stops there and the plan
// says Live is unknown rather than diffing against what no longer runs.
func TestLedgerOnlyPlan_UnidentifiableNewestApplyIsUnknown(t *testing.T) {
	old := ledgerOnlyBundle("bnd_1", "v1", ledgerOnlyShape())
	candidate := ledgerOnlyBundle("bnd_3", "v3", ledgerOnlyShape())
	legacy := release.Promotion{ID: "p-2", Env: "prod", Release: "v2", Kind: release.KindPromote,
		RecordedGates: []release.Gate{{Name: applyGateName, Status: release.GateStatusPassed, Summary: "applied from this machine"}}}
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, old, candidate),
		[]release.Promotion{appliedPromotion("p-1", "v1", old), legacy}, nil)

	plan := planLedgerOnly(t, ledger, candidate)
	if plan.LiveBasis.AppliedBundleID != "" {
		t.Fatalf("baseline = %q; an older apply must not stand in for the one that cannot be named", plan.LiveBasis.AppliedBundleID)
	}
	unknown := false
	for _, f := range plan.Findings {
		unknown = unknown || (f.Code == release.FindingUnknown && f.Section == release.SectionObjects)
	}
	if !unknown {
		t.Fatal("an unknown Live must be a warn finding, never an empty diff")
	}
}

// Secret presence comes from the cluster: a Secret declared at the last apply
// and still there is silent; one deleted since is named MISSING; a newly
// declared, present one is info.
func TestLedgerOnlyPlan_SecretPresenceFromTheCluster(t *testing.T) {
	applied := ledgerOnlyBundle("bnd_1", "v1", ledgerOnlyShape())
	withNew := ledgerOnlyShape()
	withNew.Secrets = append(withNew.Secrets, release.ShapeSecret{Name: "sentry-dsn", Provider: "external"})
	candidate := ledgerOnlyBundle("bnd_2", "v2", withNew)
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, applied, candidate),
		[]release.Promotion{appliedPromotion("p-1", "v1", applied)},
		map[string]bool{"db-credentials": true, "cloudflare-api-token": false, "sentry-dsn": true})

	secrets := map[string]release.PlanFinding{}
	for _, f := range findingsByCode(planLedgerOnly(t, ledger, candidate))[release.FindingSecretNeeded] {
		secrets[f.Subject] = f
	}
	if f, ok := secrets["db-credentials"]; ok {
		t.Errorf("present and present last time is not a finding: %+v", f)
	}
	if f := secrets["cloudflare-api-token"]; f.Class != release.ClassWarn || !strings.Contains(f.Detail, "MISSING") {
		t.Errorf("a declared Secret missing from the cluster = %+v, want a warn naming it MISSING", f)
	}
	if f := secrets["sentry-dsn"]; f.Class != release.ClassInfo {
		t.Errorf("newly declared and present = %+v, want info", f)
	}
}

// A `hosted` secret's presence is the control plane's store's to answer, as it
// was when the server computed this plan: moving the plan to forge must not
// turn those into "not verifiable".
func TestLedgerOnlyPlan_HostedSecretsAreReadFromTheStore(t *testing.T) {
	hostedSecrets := func(names ...string) []release.ShapeSecret {
		out := make([]release.ShapeSecret, 0, len(names))
		for _, n := range names {
			out = append(out, release.ShapeSecret{Name: n, Provider: "hosted"})
		}
		return out
	}
	appliedShape := ledgerOnlyShape()
	appliedShape.Secrets = hostedSecrets("API_KEY", "GONE")
	candidateShape := ledgerOnlyShape()
	candidateShape.Secrets = hostedSecrets("API_KEY", "GONE", "NEW_TOKEN")
	applied := ledgerOnlyBundle("bnd_1", "v1", appliedShape)
	candidate := ledgerOnlyBundle("bnd_2", "v2", candidateShape)
	cp := ledgerOnlyControlPlane(t, applied, candidate)
	cp.replies[procListSecrets] = map[string]any{"secrets": []map[string]any{
		{"name": "API_KEY", "currentVersion": 3}, {"name": "NEW_TOKEN", "currentVersion": 1},
	}}
	ledger := ledgerOnlyFixture(t, cp, []release.Promotion{appliedPromotion("p-1", "v1", applied)}, nil)

	secrets := map[string]release.PlanFinding{}
	for _, f := range findingsByCode(planLedgerOnly(t, ledger, candidate))[release.FindingSecretNeeded] {
		secrets[f.Subject] = f
	}
	if f, ok := secrets["API_KEY"]; ok {
		t.Errorf("a hosted secret set in the store and declared last time is not a finding: %+v", f)
	}
	if f := secrets["GONE"]; f.Class != release.ClassWarn || !strings.Contains(f.Detail, "MISSING") {
		t.Errorf("a declared hosted secret absent from the store = %+v, want a warn naming it MISSING", f)
	}
	if f := secrets["NEW_TOKEN"]; f.Class != release.ClassInfo {
		t.Errorf("newly declared and set in the store = %+v, want info", f)
	}
}

// The plan forge computes for a ledger-only env is forge's own, so its digest
// is NOT sent for the control plane to recompute (it would refuse every one as
// stale); every other hosted env's plan is the server's and travels on the
// write.
func TestServerPlanDigest_OnlyAServerPlanTravels(t *testing.T) {
	plan := &release.Plan{Digest: sha("plan")}
	if got := serverPlanDigest(plan, envLedger{Hosted: true, Mixed: true}); got != "" {
		t.Errorf("ledger-only env sent %q to its control plane, want nothing", got)
	}
	if got := serverPlanDigest(plan, envLedger{Hosted: true}); got != plan.Digest {
		t.Errorf("hosted env sent %q, want the server's plan digest", got)
	}
	if got := serverPlanDigest(plan, envLedger{}); got != plan.Digest {
		t.Errorf("file-ledger env recorded %q, want the plan digest", got)
	}
}

// A scoped apply shipped part of its bundle; naming the bundle would make the
// next plan treat the skipped objects as applied. Only a whole-env apply is a
// baseline.
func TestAppliedBundleDigest_OnlyAWholeEnvApplyNamesItsBundle(t *testing.T) {
	noteRecordedBundles("v9", []bundleWriteOutcome{{Env: "prod", Digest: sha("v9-bundle"), Recorded: true}})
	if got := appliedBundleDigest("prod", "v9", deployOptions{}); got != sha("v9-bundle") {
		t.Errorf("whole-env apply = %q, want the bundle this deploy recorded", got)
	}
	if got := appliedBundleDigest("prod", "v9", deployOptions{targets: []string{"api"}}); got != "" {
		t.Errorf("--target apply named bundle %q, want none", got)
	}
	if got := appliedBundleDigest("prod", "v9", deployOptions{frontendsOnly: true}); got != "" {
		t.Errorf("--frontends-only apply named bundle %q, want none", got)
	}
}

// The apply's evidence names what it shipped and what approved it — the two
// facts the next plan and an incident review need — and still records a
// failure as a failure.
func TestApplyOutcomeGate_RecordsTheAppliedBundle(t *testing.T) {
	started := time.Date(2026, 10, 10, 7, 0, 0, 0, time.UTC)
	passed := applyOutcomeGate(started, started.Add(time.Minute), nil, sha("bundle"), sha("plan"))
	if passed.Status != release.GateStatusPassed || passed.Name != applyGateName {
		t.Fatalf("gate = %+v, want a passed %q gate", passed, applyGateName)
	}
	if gateDetail(passed, applyGateBundleDigest) != sha("bundle") || gateDetail(passed, applyGatePlanDigest) != sha("plan") {
		t.Fatalf("details = %v, want the bundle and plan digests", passed.Details)
	}
	if err := passed.Validate(); err != nil {
		t.Fatalf("the gate must be writable: %v", err)
	}
	failed := applyOutcomeGate(started, started.Add(time.Minute), errors.New("rollout: api not ready"), sha("bundle"), "")
	if failed.Status != release.GateStatusFailed || !strings.Contains(failed.Summary, "api not ready") {
		t.Fatalf("failed gate = %+v", failed)
	}
	if _, ok := failed.Details[applyGatePlanDigest]; ok {
		t.Fatal("an absent plan digest must not be recorded as an empty one")
	}
	if g := applyOutcomeGate(started, started, nil, "", ""); g.Details != nil {
		t.Fatalf("no digests = %v, want no details at all", g.Details)
	}
}

// A hash-named one-shot Job is "removed" by every release that changes its
// spec, and the apply prunes the superseded ones itself. Telling the reviewer to
// "delete it by hand" there would be wrong advice on every deploy.
func TestRemovalNote_JobsAreNotToBeDeletedByHand(t *testing.T) {
	for _, tc := range []struct {
		subject string
		wantJob bool
	}{
		{"prod-ctx/Job/control-plane-prod/control-plane-migrate-11e21c4446", true},
		{"gke_proj_us-central1_prod/Job/ns/dbrole-provision-litellm-abcdef0123", true},
		{"prod-ctx/ConfigMap/app/api-config", false},
		{"prod-ctx/CronJob/app/nightly", false},
		{"prod-ctx/Deployment/app/api", false},
		{"Job", false}, // not an object key
	} {
		got := removalNote(tc.subject)
		if tc.wantJob {
			if got != directApplyJobRemovalNote || strings.Contains(got, "by hand") {
				t.Errorf("%s: note = %q, want the Job note (the apply prunes it; no 'by hand')", tc.subject, got)
			}
		} else if got != directApplyRemovalNote {
			t.Errorf("%s: note = %q, want the generic removal note", tc.subject, got)
		}
	}
	if !strings.Contains(directApplyJobRemovalNote, "3") {
		t.Errorf("the Job note must state how many finished Jobs are kept: %q", directApplyJobRemovalNote)
	}
}

func TestLedgerOnlyPlan_RemovedJobIsNotToBeDeletedByHand(t *testing.T) {
	job := func(name string) release.ShapeObject {
		o := ledgerOnlyObject("Job", "app", name, "")
		o.APIVersion = "batch/v1"
		return o
	}
	applied := ledgerOnlyShape()
	applied.Objects = append(applied.Objects, job("app-migrate-aaaaaaaaaa"))
	candidateShape := ledgerOnlyShape()
	candidateShape.Objects = append(candidateShape.Objects, job("app-migrate-bbbbbbbbbb"))

	a := ledgerOnlyBundle("bnd_1", "v1", applied)
	c := ledgerOnlyBundle("bnd_2", "v2", candidateShape)
	ledger := ledgerOnlyFixture(t, ledgerOnlyControlPlane(t, a, c),
		[]release.Promotion{appliedPromotion("p-1", "v1", a)}, nil)

	got := findingsByCode(planLedgerOnly(t, ledger, c))
	removed := got[release.FindingObjectRemoved]
	if len(removed) != 1 || removed[0].Subject != "prod-ctx/Job/app/app-migrate-aaaaaaaaaa" {
		t.Fatalf("object_removed = %+v, want exactly the superseded Job", removed)
	}
	if removed[0].Detail != directApplyJobRemovalNote {
		t.Errorf("removal detail = %q, want the Job note", removed[0].Detail)
	}
}
