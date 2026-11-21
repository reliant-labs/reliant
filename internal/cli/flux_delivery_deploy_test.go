package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/internal/exitcode"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

const testFluxDigest = "sha256:" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"

// fluxRelease is a release of one cluster split into the three phases, as
// the bundle records its layout.
func fluxRelease(identities bool) *fluxDeliveryRelease {
	fd := deliveryEntity("namespace")
	fd.ApplyIdentities = identities
	fd.FluxVerify = FluxDeliveryControl{Requested: "enforce", Effective: "audit"}
	tree := func(phase release.Phase, deps []string, health ...release.BundleHealthCheck) release.BundleTree {
		return release.BundleTree{
			Cluster: testCtx, Unit: release.DefaultDeliveryUnit, Phase: phase,
			Path:      release.BundleTreePath(testCtx, release.DefaultDeliveryUnit, phase),
			Documents: 1, DependsOn: deps, TimeoutSeconds: 90, RetryIntervalSeconds: 15, Health: health,
		}
	}
	pre := release.BundleTreePath(testCtx, release.DefaultDeliveryUnit, release.PhasePre)
	rollout := release.BundleTreePath(testCtx, release.DefaultDeliveryUnit, release.PhaseRollout)
	return &fluxDeliveryRelease{
		fd: fd, project: "acme", version: "v2", digest: testFluxDigest,
		pushTo: "localhost:5050/acme/bundle.v1/prod", sourceURL: "oci://host.k3d.internal:5050/acme/bundle.v1/prod",
		doc: release.BundleDoc{Trees: []release.BundleTree{
			tree(release.PhasePre, nil, release.BundleHealthCheck{Kind: "Job", Name: "migrate-abc", Namespace: "acme-prod"}),
			tree(release.PhaseRollout, []string{pre}, release.BundleHealthCheck{Kind: "Deployment", Name: "api", Namespace: "acme-prod"}),
			tree(release.PhasePost, []string{rollout}),
		}},
	}
}

// TestFluxDeliveryInputFromTheBundle: the pointer's input is one in-cluster
// applier per tree, named and chained as the bundle records them, each under
// its phase's identity, and it passes every check the writer repeats.
func TestFluxDeliveryInputFromTheBundle(t *testing.T) {
	t.Parallel()
	for _, identities := range []bool{true, false} {
		rel := fluxRelease(identities)
		in, err := rel.input("3", "promo-3")
		if err != nil {
			t.Fatal(err)
		}
		if err := checkFluxDeliveryInput(in, rel.fd); err != nil {
			t.Fatalf("identities=%t: the input fails the writer's checks: %v", identities, err)
		}
		if len(in.Appliers) != 3 {
			t.Fatalf("appliers = %d, want one per tree", len(in.Appliers))
		}
		if in.Source.Digest != testFluxDigest || in.Source.URL != rel.sourceURL || !in.Source.Insecure {
			t.Errorf("source = %+v", in.Source)
		}
		if in.Source.Verify.Mode != fluxdelivery.VerifyAudit {
			t.Errorf("verify = %q, want the capped audit", in.Source.Verify.Mode)
		}
		rollout := in.Appliers[1]
		if len(rollout.DependsOn) != 1 || rollout.DependsOn[0] != in.Appliers[0].Name {
			t.Errorf("rollout dependsOn = %v, want the pre tree %s", rollout.DependsOn, in.Appliers[0].Name)
		}
		if rollout.Timeout != "1m30s" || rollout.RetryInterval != "15s" {
			t.Errorf("rollout timeout/retry = %s/%s", rollout.Timeout, rollout.RetryInterval)
		}
		want := map[release.Phase]string{release.PhasePre: fluxdelivery.PreServiceAccount, release.PhaseRollout: fluxdelivery.ApplyServiceAccount, release.PhasePost: fluxdelivery.ApplyServiceAccount}
		for _, a := range in.Appliers {
			sa := ""
			if identities {
				sa = want[a.Phase]
			}
			if !a.InCluster || a.ServiceAccountName != sa {
				t.Errorf("identities=%t: %s applies in-cluster=%t as %q, want %q", identities, a.Name, a.InCluster, a.ServiceAccountName, sa)
			}
		}
	}
}

func TestFluxDeliveryInputRefusesABundleWithNothingForTheCluster(t *testing.T) {
	t.Parallel()
	rel := fluxRelease(true)
	rel.fd.Cluster = "some-other-context"
	if _, err := rel.input("1", "p"); err == nil || !strings.Contains(err.Error(), "carries no tree for cluster") {
		t.Fatalf("err = %v, want the no-tree refusal", err)
	}
}

func TestFluxDeliveryRegistry(t *testing.T) {
	t.Parallel()
	declared := &KCLEntities{FluxDelivery: &FluxDeliveryEntity{Registry: "ghcr.io/acme/"}}
	if got := fluxRegistryBase(declared); got != "ghcr.io/acme" {
		t.Errorf("declared registry = %q", got)
	}
	if got := fluxRegistryBase(&KCLEntities{FluxDelivery: &FluxDeliveryEntity{}}); got != "" {
		t.Errorf("no registry and no workload: %q, want none", got)
	}
	for ref, want := range map[string]string{
		"localhost:5050/acme/bundle.v1/prod": "host.k3d.internal:5050/acme/bundle.v1/prod",
		"127.0.0.1:5050/acme":                "host.k3d.internal:5050/acme",
		"ghcr.io/acme/bundle.v1/prod":        "ghcr.io/acme/bundle.v1/prod",
	} {
		if got := clusterReachableRegistry(ref); got != want {
			t.Errorf("clusterReachableRegistry(%q) = %q, want %q", ref, got, want)
		}
	}
	for ref, want := range map[string]bool{
		"oci://host.k3d.internal:5050/acme": true, "oci://registry.localhost:5000/a": true, "oci://localhost:5050/a": true,
		"oci://ghcr.io/acme": false, "oci://localhost.evil.com/a": false,
		// A k3d-looking name is not evidence of a local container: a public
		// host may carry it, with a real certificate.
		"oci://k3d-registry.example.com/a": false, "oci://k3d-reg:5000/a": false,
	} {
		if got := insecureRegistry(ref); got != want {
			t.Errorf("insecureRegistry(%q) = %t, want %t", ref, got, want)
		}
	}
}

// fakePointer is the pointer's cluster I/O, in memory, with the API server's
// preconditions: create fails on an existing pointer, update on a stale (or
// deleted) resourceVersion. before, when set, runs ahead of every write, so a
// test can interleave another writer between a read and a write.
type fakePointer struct {
	live    *unstructured.Unstructured
	rv      int
	applied []map[string]any
	before  func(f *fakePointer)
}

func (f *fakePointer) install(t *testing.T) {
	t.Helper()
	prev := fluxDeliveryPointerIO
	t.Cleanup(func() { fluxDeliveryPointerIO = prev })
	fluxDeliveryPointerIO.read = func(context.Context, FluxDeliveryEntity) (*unstructured.Unstructured, error) {
		if f.live == nil {
			return nil, nil
		}
		return f.live.DeepCopy(), nil
	}
	fluxDeliveryPointerIO.create = func(_ context.Context, _ FluxDeliveryEntity, p map[string]any) error {
		f.hook()
		if f.live != nil {
			return errFluxPointerConflict
		}
		f.store(p)
		return nil
	}
	fluxDeliveryPointerIO.update = func(_ context.Context, _ FluxDeliveryEntity, p map[string]any) error {
		f.hook()
		u := unstructured.Unstructured{Object: p}
		if f.live == nil || u.GetResourceVersion() != f.live.GetResourceVersion() {
			return errFluxPointerConflict
		}
		f.store(p)
		return nil
	}
}

func (f *fakePointer) hook() {
	if b := f.before; b != nil {
		f.before = nil
		b(f)
	}
}

// store is a write the server accepted: a new resourceVersion.
func (f *fakePointer) store(p map[string]any) {
	f.applied = append(f.applied, p)
	u := (&unstructured.Unstructured{Object: p}).DeepCopy()
	f.rv++
	u.SetResourceVersion(fmt.Sprintf("rv-%d", f.rv))
	f.live = u
}

// liveInput is the pointer's input as the cluster holds it.
func (f *fakePointer) liveInput(t *testing.T) fluxdelivery.BindingInput {
	t.Helper()
	in, err := fluxdelivery.LiveInput(f.live)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// stubRetireApplied makes the renderer's retire observation report done
// after `after` reads, recording the names it was asked about.
func stubRetireApplied(t *testing.T, after int, asked *[]string) {
	t.Helper()
	prev := fluxDeliveryRetireApplied
	t.Cleanup(func() { fluxDeliveryRetireApplied = prev })
	reads := 0
	fluxDeliveryRetireApplied = func(_ context.Context, _ FluxDeliveryEntity, names []string) (bool, error) {
		reads++
		*asked = append([]string(nil), names...)
		return reads > after, nil
	}
}

var quietNotice = func(string, ...any) {}

// TestWriteFluxDeliveryPointer is the writer's refusal matrix against a live
// pointer. Not parallel: it swaps the pointer seam.
func TestWriteFluxDeliveryPointer(t *testing.T) {
	rel := fluxRelease(true)
	f := &fakePointer{}
	f.install(t)

	first, _ := rel.input("4", "promo-4")
	if err := writeFluxDeliveryPointer(context.Background(), rel.fd, nil, "", first, "v1"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if md := f.applied[0]["metadata"].(map[string]any); md["resourceVersion"] != nil {
		t.Errorf("a first write carries a resourceVersion precondition: %v", md["resourceVersion"])
	}
	live := f.liveInput(t)

	next, _ := rel.input("5", "promo-5")
	if err := writeFluxDeliveryPointer(context.Background(), rel.fd, &live, f.live.GetResourceVersion(), next, "v1"); err != nil {
		t.Fatalf("second write: %v", err)
	}
	if md := f.applied[1]["metadata"].(map[string]any); md["resourceVersion"] != "rv-1" {
		t.Errorf("the second write is not conditional on the pointer it read: resourceVersion %v", md["resourceVersion"])
	}
	live = f.liveInput(t)
	rv := f.live.GetResourceVersion()

	stale, _ := rel.input("3", "promo-3")
	if err := writeFluxDeliveryPointer(context.Background(), rel.fd, &live, rv, stale, "v1"); !errors.Is(err, fluxdelivery.ErrStaleFence) {
		t.Fatalf("an older fence: err = %v, want ErrStaleFence", err)
	}
	reused, _ := rel.input("5", "promo-other")
	if err := writeFluxDeliveryPointer(context.Background(), rel.fd, &live, rv, reused, "v1"); !errors.Is(err, fluxdelivery.ErrStaleFence) {
		t.Fatalf("an equal fence for another promotion: err = %v, want ErrStaleFence", err)
	}
	dropped, _ := rel.input("6", "promo-6")
	dropped.Appliers = dropped.Appliers[:2]
	if err := writeFluxDeliveryPointer(context.Background(), rel.fd, &live, rv, dropped, "v1"); !errors.Is(err, fluxdelivery.ErrUnretiredApplierDropped) {
		t.Fatalf("a dropped tree: err = %v, want ErrUnretiredApplierDropped", err)
	}
	if len(f.applied) != 2 {
		t.Errorf("a refused write reached the cluster: %d writes, want 2", len(f.applied))
	}
	if err := writeFluxDeliveryPointer(context.Background(), rel.fd, nil, "", first, "v1"); !errors.Is(err, errFluxPointerConflict) {
		t.Fatalf("a create over an existing pointer: err = %v, want the conflict", err)
	}
}

// TestPointFluxDeliveryFenceIsTheClusters: two deployers with independent
// ledgers (nothing but the cluster in common) each write the next fence in
// the cluster's sequence, so neither is stuck behind the other and neither
// can reuse the other's fence. Not parallel: it swaps the pointer seam.
func TestPointFluxDeliveryFenceIsTheClusters(t *testing.T) {
	f := &fakePointer{}
	f.install(t)
	ctx := context.Background()

	ci, admin := fluxRelease(true), fluxRelease(true)
	for i, step := range []struct {
		rel       *fluxDeliveryRelease
		promotion string
	}{{ci, "ci-1"}, {admin, "admin-1"}, {ci, "ci-2"}} {
		in, err := pointFluxDelivery(ctx, step.rel, step.promotion, time.Minute, quietNotice)
		if err != nil {
			t.Fatalf("write %d (%s): %v", i+1, step.promotion, err)
		}
		want := strconv.Itoa(i + 1)
		if got := f.liveInput(t); got.Fence != want || got.Promotion != step.promotion || in.Fence != want {
			t.Errorf("write %d: live fence %s promotion %s, want fence %s promotion %s", i+1, got.Fence, got.Promotion, want, step.promotion)
		}
	}
}

// TestPointFluxDeliveryInterleavedFirstWrites: two deploys both read "no
// pointer". The other one creates it first; this one's create-only write
// fails, and it re-reads and checks the transition against what won rather
// than overwriting it. Not parallel: it swaps the pointer seam.
func TestPointFluxDeliveryInterleavedFirstWrites(t *testing.T) {
	f := &fakePointer{}
	f.install(t)
	ctx := context.Background()
	rel := fluxRelease(true)

	newer := fluxRelease(true)
	newerIn, _ := newer.input("1", "newer")
	f.before = func(f *fakePointer) {
		p, err := fluxdelivery.InputProvider(newerIn)
		if err != nil {
			t.Fatal(err)
		}
		f.store(p)
	}
	in, err := pointFluxDelivery(ctx, rel, "older", time.Minute, quietNotice)
	if err != nil {
		t.Fatalf("interleaved first write: %v", err)
	}
	if len(f.applied) != 2 {
		t.Fatalf("writes = %d, want the other deploy's create and this one's conditional update", len(f.applied))
	}
	if md := f.applied[1]["metadata"].(map[string]any); md["resourceVersion"] != "rv-1" {
		t.Errorf("the retry is not conditional on the pointer that won: resourceVersion %v", md["resourceVersion"])
	}
	if in.Fence != "2" || f.liveInput(t).Promotion != "older" {
		t.Errorf("after the conflict: fence %s promotion %s, want fence 2 over the winner's 1", in.Fence, f.liveInput(t).Promotion)
	}

	// The winner's pointer is one this write may not follow (it drops a
	// tree the winner holds, and the retire never lands): the retry is
	// CHECKED, not forced.
	g := &fakePointer{}
	g.install(t)
	wide := fluxRelease(true)
	wide.doc.Trees = append(wide.doc.Trees, release.BundleTree{
		Cluster: testCtx, Unit: "jobs", Phase: release.PhaseRollout,
		Path:      release.BundleTreePath(testCtx, "jobs", release.PhaseRollout),
		Documents: 1, TimeoutSeconds: 90, RetryIntervalSeconds: 15,
	})
	wideIn, _ := wide.input("1", "wide")
	g.before = func(g *fakePointer) {
		p, _ := fluxdelivery.InputProvider(wideIn)
		g.store(p)
	}
	var asked []string
	stubRetireApplied(t, 1<<30, &asked)
	_, err = pointFluxDelivery(ctx, rel, "narrow", time.Nanosecond, quietNotice)
	if exitcode.Of(err) != exitTimedOut {
		t.Fatalf("a retry over a pointer whose retire never lands: err = %v (exit %d), want the retire timeout", err, exitcode.Of(err))
	}
	if got := g.liveInput(t); got.Source.Digest != wideIn.Source.Digest || got.Promotion != "wide" {
		t.Errorf("the retry moved the winner's source: %+v", got.Source)
	}
}

// TestPointFluxDeliveryRetiresDroppedTrees: a release that drops its last
// post Job (the post tree), or a whole delivery unit, retires the dropped
// trees under the LIVE source first, waits for the renderer to apply that,
// then ships. Not parallel: it swaps the pointer and retire seams.
func TestPointFluxDeliveryRetiresDroppedTrees(t *testing.T) {
	withUnit := func() *fluxDeliveryRelease {
		r := fluxRelease(true)
		r.doc.Trees = append(r.doc.Trees, release.BundleTree{
			Cluster: testCtx, Unit: "jobs", Phase: release.PhaseRollout,
			Path:      release.BundleTreePath(testCtx, "jobs", release.PhaseRollout),
			Documents: 1, TimeoutSeconds: 90, RetryIntervalSeconds: 15,
		})
		return r
	}
	dropPost := func() *fluxDeliveryRelease {
		r := fluxRelease(true)
		r.doc.Trees = r.doc.Trees[:2]
		return r
	}
	for _, tc := range []struct {
		name     string
		previous *fluxDeliveryRelease
		next     *fluxDeliveryRelease
		dropped  int
	}{
		{"the last post Job", fluxRelease(true), dropPost(), 1},
		{"a whole delivery unit", withUnit(), fluxRelease(true), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePointer{}
			f.install(t)
			ctx := context.Background()
			if _, err := pointFluxDelivery(ctx, tc.previous, "promo-1", time.Minute, quietNotice); err != nil {
				t.Fatalf("previous release: %v", err)
			}
			before := f.liveInput(t)
			tc.next.digest = "sha256:" + strings.Repeat("e", 64)
			var asked []string
			stubRetireApplied(t, 0, &asked)

			in, err := pointFluxDelivery(ctx, tc.next, "promo-2", time.Minute, quietNotice)
			if err != nil {
				t.Fatalf("a release that drops a tree: %v", err)
			}
			if len(f.applied) != 3 {
				t.Fatalf("writes = %d, want previous, retire, release", len(f.applied))
			}
			retire, err := fluxdelivery.LiveInput(&unstructured.Unstructured{Object: f.applied[1]})
			if err != nil {
				t.Fatal(err)
			}
			if retire.Source.Digest != before.Source.Digest || retire.Promotion != before.Promotion || retire.Fence != "2" {
				t.Errorf("the retire moved more than the lifecycle: digest %s promotion %s fence %s", retire.Source.Digest, retire.Promotion, retire.Fence)
			}
			var retired []string
			for _, a := range retire.Appliers {
				if a.Retire == fluxdelivery.RetireOrphan {
					retired = append(retired, a.Name)
				}
			}
			if len(retired) != tc.dropped || len(asked) != tc.dropped || retired[0] != asked[0] {
				t.Errorf("retired %v, waited on %v, want the %d dropped tree(s)", retired, asked, tc.dropped)
			}
			if in.Fence != "3" || in.Source.Digest != tc.next.digest || len(in.Appliers) != len(before.Appliers)-tc.dropped {
				t.Errorf("the release write: fence %s digest %s appliers %d", in.Fence, in.Source.Digest, len(in.Appliers))
			}
			for _, a := range in.Appliers {
				if a.Name == retired[0] {
					t.Errorf("the release still carries the retired %s", a.Name)
				}
			}
		})
	}
}

// TestWaitFluxDeliveryCurrentJSON: `env status <flux-env> --wait --json`
// writes the wait document to stdout and its progress to stderr, for success,
// failure and a missing pointer. Not parallel: it swaps the seams.
func TestWaitFluxDeliveryCurrentJSON(t *testing.T) {
	rel := fluxRelease(true)
	f := &fakePointer{}
	f.install(t)
	in, _ := rel.input("1", "promo-1")
	ctx := context.Background()

	var out, progress bytes.Buffer
	err := waitFluxDeliveryCurrent(ctx, "prod", rel.fd, time.Minute, envWaitOptions{JSON: true}, &out, &progress)
	var doc envWaitReport
	if jerr := json.Unmarshal(out.Bytes(), &doc); jerr != nil {
		t.Fatalf("no pointer: stdout is not one JSON document: %v\n%s", jerr, out.String())
	}
	if exitcode.Of(err) != exitUndetermined || doc.OK || doc.ExitCode != exitUndetermined || doc.Phase != "unknown" {
		t.Errorf("no pointer: err %v, doc %+v", err, doc)
	}

	if err := writeFluxDeliveryPointer(ctx, rel.fd, nil, "", in, "v1"); err != nil {
		t.Fatal(err)
	}
	prev := fluxDeliveryObserve
	t.Cleanup(func() { fluxDeliveryObserve = prev })
	d := in.Source.Digest
	for _, tc := range []struct {
		name  string
		state fluxObjectStatus
		phase string
		code  int
	}{
		{"converged", fluxObjectStatus{Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}, "succeeded", exitOK},
		{"failing", fluxObjectStatus{Found: true, Ready: "False", Reason: "HealthCheckFailed", Message: "Job failed", Current: true, LastAttempted: d}, "degraded", exitWrong},
	} {
		fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
			return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
				s := tc.state
				s.Name = a.Name
				return s
			}), nil
		}
		out.Reset()
		progress.Reset()
		// A budget spent at the first poll: the failure is not fail-fast, so
		// it is the verdict only once the deadline passes still failing.
		err := waitFluxDeliveryCurrent(ctx, "prod", rel.fd, time.Nanosecond, envWaitOptions{JSON: true}, &out, &progress)
		var doc envWaitReport
		if jerr := json.Unmarshal(out.Bytes(), &doc); jerr != nil {
			t.Fatalf("%s: stdout is not one JSON document: %v\n%s", tc.name, jerr, out.String())
		}
		if exitcode.Of(err) != tc.code || doc.ExitCode != tc.code || doc.Phase != tc.phase {
			t.Errorf("%s: err %v (exit %d), doc exit %d phase %q, want exit %d phase %q", tc.name, err, exitcode.Of(err), doc.ExitCode, doc.Phase, tc.code, tc.phase)
		}
		if doc.Promotion == nil || doc.Promotion.ID != "promo-1" || len(doc.Workloads) != len(in.Appliers) || doc.Workloads[0].Phase != tc.phase {
			t.Errorf("%s: doc = %+v", tc.name, doc)
		}
		if !strings.Contains(progress.String(), "Waiting for env prod") || strings.Contains(out.String(), "Waiting for") {
			t.Errorf("%s: progress went to stdout:\nstdout %s\nstderr %s", tc.name, out.String(), progress.String())
		}
	}
}

// pointerWrittenAt is when observed's pointer was last written.
var pointerWrittenAt = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// observed builds an observation of in's appliers, each in the given state.
func observed(in fluxdelivery.BindingInput, each func(a fluxdelivery.Applier) fluxObjectStatus) fluxDeliveryObservation {
	obs := fluxDeliveryObservation{
		Pointer:          fluxPointerIdentity{Found: true, Promotion: in.Promotion, Fence: in.Fence, Digest: in.Source.Digest},
		PointerWrittenAt: pointerWrittenAt,
		Renderer:         fluxObjectStatus{Name: "delivery.prod", Found: true, Ready: "True", Current: true},
		Source: fluxObjectStatus{Name: in.Source.Name, Found: true, Ready: "True", Current: true,
			RequestedDigest: in.Source.Digest, LastApplied: "latest@" + in.Source.Digest, Fence: in.Fence},
	}
	for _, a := range in.Appliers {
		s := each(a)
		s.SpecMatches = s.Found
		obs.Appliers = append(obs.Appliers, s)
	}
	return obs
}

const forbiddenMessage = `CustomResourceDefinition/gadgets.acme.dev dry-run failed (Forbidden): customresourcedefinitions.apiextensions.k8s.io "gadgets.acme.dev" is forbidden: ` +
	`User "system:serviceaccount:forge-delivery-acme-prod:forge-delivery-pre" cannot patch resource "customresourcedefinitions" in API group "apiextensions.k8s.io" at the cluster scope` + "\n" +
	`Widget/acme-prod/w dry-run failed (Forbidden): widgets.acme.dev "w" is forbidden: ` +
	`User "system:serviceaccount:forge-delivery-acme-prod:forge-delivery-pre" cannot patch resource "widgets" in API group "acme.dev" in the namespace "acme-prod"`

func TestFluxDeliveryForbidden(t *testing.T) {
	t.Parallel()
	got := fluxDeliveryForbidden(forbiddenMessage)
	rows := fluxDeliveryGrantRows(got)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Resource != "acme.dev/widgets" || rows[0].Scope != "(namespace acme-prod)" ||
		rows[1].Resource != "apiextensions.k8s.io/customresourcedefinitions" || rows[1].Scope != "(cluster)" {
		t.Errorf("rows = %+v", rows)
	}
	other := `User "system:serviceaccount:kube-system:somebody" cannot patch resource "secrets" in API group "" in the namespace "x"`
	if got := fluxDeliveryForbidden(other); len(got) != 0 {
		t.Errorf("a Forbidden for an identity forge does not grant was mapped: %v", got)
	}
}

// TestFluxDeliveryObservationVerdicts: converged, a Forbidden mapped to the
// grant refusal (exit 1), any other failure in Flux's words (exit 1), and a
// failure from the PREVIOUS revision still progressing.
func TestFluxDeliveryObservationVerdicts(t *testing.T) {
	t.Parallel()
	in, _ := fluxRelease(true).input("2", "promo-2")
	d := in.Source.Digest

	ready := observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
		return fluxObjectStatus{Name: a.Name, Found: true, Ready: "True", Current: true, LastApplied: "latest@" + d, LastAttempted: "latest@" + d}
	})
	if !ready.converged(in) || ready.failure("prod", "v2", in) != nil {
		t.Fatalf("a fully applied release is not converged: %+v", ready)
	}

	failing := func(reason, message, attempted string) fluxDeliveryObservation {
		return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			if a.Phase != release.PhasePre {
				return fluxObjectStatus{Name: a.Name, Found: true, Ready: "False", Reason: "DependencyNotReady", Current: true}
			}
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "False", Reason: reason, Message: message, Current: true, LastAttempted: attempted}
		})
	}

	err := failing("ReconciliationFailed", forbiddenMessage, d).failure("prod", "v2", in)
	var refusal *fluxDeliveryGrantRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("a Forbidden for an apply identity: err = %v, want the grant refusal", err)
	}
	if exitcode.Of(err) != exitWrong || refusal.Reporter != in.Appliers[0].Name ||
		!strings.Contains(err.Error(), "forge env deploy prod --target flux-delivery") {
		t.Errorf("mapped refusal = %v (exit %d)", err, exitcode.Of(err))
	}

	err = failing("HealthCheckFailed", "timeout waiting for: [Job/acme-prod/migrate-abc status: 'Failed']", d).failure("prod", "v2", in)
	if exitcode.Of(err) != exitWrong || !strings.Contains(err.Error(), "HealthCheckFailed") || errors.As(err, &refusal) {
		t.Errorf("a health failure: %v (exit %d), want Flux's own words, exit 1", err, exitcode.Of(err))
	}

	old := "sha256:" + strings.Repeat("0", 64)
	if err := failing("ReconciliationFailed", forbiddenMessage, "latest@"+old).failure("prod", "v2", in); err != nil {
		t.Errorf("a failure of the PREVIOUS revision decided this one: %v", err)
	}

	stalled := ready
	stalled.Renderer.Stalled, stalled.Renderer.Reason = true, "BuildFailed"
	stalled.Renderer.StalledSince = pointerWrittenAt.Add(time.Second)
	if err := stalled.failure("prod", "v2", in); err == nil || !strings.Contains(err.Error(), "renderer has not applied the promoted inputs") {
		t.Errorf("a renderer stalled since the pointer was written: %v", err)
	}
	if stalled.converged(in) {
		t.Errorf("a stalled renderer is converged")
	}
}

// TestFluxDeliveryObservationFreshness: nothing decides a promotion on
// evidence that is not about it — Kustomizations Ready on the digest with no
// renderer (or a stalled one, or one that never rendered the input), a stall
// older than the pointer's write, a source error from before it requested the
// digest, an applier failure from a generation it has not observed.
func TestFluxDeliveryObservationFreshness(t *testing.T) {
	t.Parallel()
	in, _ := fluxRelease(true).input("2", "promo-2")
	d := in.Source.Digest
	old := "sha256:" + strings.Repeat("0", 64)
	ready := func() fluxDeliveryObservation {
		return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(o *fluxDeliveryObservation)
	}{
		{"absent renderer", func(o *fluxDeliveryObservation) { o.Renderer = fluxObjectStatus{Name: o.Renderer.Name} }},
		{"stalled renderer", func(o *fluxDeliveryObservation) { o.Renderer.Stalled = true }},
		{"stale renderer generation", func(o *fluxDeliveryObservation) { o.Renderer.Current = false }},
		{"renderer not ready", func(o *fluxDeliveryObservation) { o.Renderer.Ready = "Unknown" }},
		{"source still requests the old digest", func(o *fluxDeliveryObservation) { o.Source.RequestedDigest = old }},
		{"source not fetched the digest", func(o *fluxDeliveryObservation) { o.Source.LastApplied = "latest@" + old }},
		{"source status of an old generation", func(o *fluxDeliveryObservation) { o.Source.Current = false }},
		{"stale applier generation", func(o *fluxDeliveryObservation) { o.Appliers[0].Current = false }},
		{"source rendered from the previous fence", func(o *fluxDeliveryObservation) { o.Source.Fence = "1" }},
		{"applier spec of a previous input", func(o *fluxDeliveryObservation) { o.Appliers[0].SpecMatches = false }},
	} {
		o := ready()
		tc.mutate(&o)
		if o.converged(in) {
			t.Errorf("%s: converged", tc.name)
		}
	}

	// A stall from before the pointer's write is about the previous input.
	o := ready()
	o.Renderer.Stalled, o.Renderer.Reason = true, "BuildFailed"
	o.Renderer.StalledSince = pointerWrittenAt.Add(-time.Minute)
	if err := o.failure("prod", "v2", in); err != nil {
		t.Errorf("a stall older than the pointer's write decided this promotion: %v", err)
	}
	if !strings.Contains(o.summary(in), "Stalled") {
		t.Errorf("the progress line hides the stall: %s", o.summary(in))
	}
	// One that cannot be dated is not attributed either.
	o.Renderer.StalledSince, o.PointerWrittenAt = time.Time{}, time.Time{}
	if err := o.failure("prod", "v2", in); err != nil {
		t.Errorf("an undated stall decided this promotion: %v", err)
	}

	// The source's error from the digest it requested before the renderer
	// updated it is not this release's failure; at the promoted digest it is.
	o = ready()
	o.Source.RequestedDigest, o.Source.Ready, o.Source.Reason, o.Source.Message = old, "False", "OCIArtifactPullFailed", "manifest unknown"
	if err := o.failure("prod", "v2", in); err != nil {
		t.Errorf("a source error from the previous digest decided this promotion: %v", err)
	}
	o.Source.RequestedDigest = d
	if err := o.failure("prod", "v2", in); exitcode.Of(err) != exitWrong {
		t.Errorf("a source error at the promoted digest: %v (exit %d), want exit 1", err, exitcode.Of(err))
	}

	// An applier failure from a generation it has not observed is stale.
	o = observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
		return fluxObjectStatus{Name: a.Name, Found: true, Ready: "False", Reason: "HealthCheckFailed", Message: "Job failed", Current: false, LastAttempted: d}
	})
	if err := o.failure("prod", "v2", in); err != nil {
		t.Errorf("an applier failure of an unobserved generation decided this promotion: %v", err)
	}
}

// TestFluxDeliverySameDigestNewFence: a re-deploy at an unchanged digest is
// a new promotion at a new fence. Until the renderer stamps that fence on the
// source, the previous promotion's Ready (or failing) objects at the same
// digest decide nothing.
func TestFluxDeliverySameDigestNewFence(t *testing.T) {
	t.Parallel()
	prevIn, _ := fluxRelease(true).input("2", "promo-2")
	in, _ := fluxRelease(true).input("3", "promo-3")
	d := in.Source.Digest
	if prevIn.Source.Digest != d {
		t.Fatalf("fixture: digests differ")
	}
	stale := observed(prevIn, func(a fluxdelivery.Applier) fluxObjectStatus {
		return fluxObjectStatus{Name: a.Name, Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}
	})
	stale.Pointer = fluxPointerIdentity{Found: true, Promotion: in.Promotion, Fence: in.Fence, Digest: d}
	if stale.converged(in) {
		t.Errorf("the previous promotion's Ready objects at the same digest converged the new one")
	}
	failing := observed(prevIn, func(a fluxdelivery.Applier) fluxObjectStatus {
		return fluxObjectStatus{Name: a.Name, Found: true, Ready: "False", Reason: "HealthCheckFailed", Message: "Job failed", Current: true, LastAttempted: d}
	})
	if err := failing.failure("prod", "v3", in); err != nil {
		t.Errorf("the previous promotion's failure at the same digest decided the new one: %v", err)
	}
	stale.Source.Fence = in.Fence
	if !stale.converged(in) {
		t.Errorf("not converged once the renderer stamped the new fence: %+v", stale)
	}
}

// TestFluxSpecMatches: a Kustomization matches only the whole spec the input
// renders for it — suspension, dependencies and health checks included — and
// a field the render omits matches only absent or zero.
func TestFluxSpecMatches(t *testing.T) {
	t.Parallel()
	in, _ := fluxRelease(true).input("2", "promo-2")
	a := in.Appliers[0]
	specs := func(in fluxdelivery.BindingInput) map[string]map[string]any {
		t.Helper()
		s, err := fluxApplierSpecs(in)
		if err != nil {
			t.Fatalf("fluxApplierSpecs: %v", err)
		}
		return s
	}
	want := specs(in)[a.Name]
	// live is the object as kubectl returns it: JSON-decoded, with the
	// fields the API server defaults.
	live := func(spec map[string]any) map[string]any {
		t.Helper()
		b, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		decoded["force"] = false
		return map[string]any{"spec": decoded}
	}
	if !fluxApplierSpecMatches(want, live(want)) {
		t.Fatalf("the rendered spec does not match itself: %v", want)
	}
	o := live(want)
	o["spec"].(map[string]any)["path"] = "elsewhere"
	if fluxApplierSpecMatches(want, o) {
		t.Errorf("another path matched")
	}
	torn := in
	torn.Retire = fluxdelivery.RetireTeardown
	if fluxApplierSpecMatches(specs(torn)[a.Name], live(want)) {
		t.Errorf("an Orphan deletionPolicy matched a teardown")
	}
	// A field only the live object holds counts unless it is zero.
	for field, value := range map[string]any{
		"suspend":      true,
		"dependsOn":    []any{map[string]any{"name": "previous"}},
		"healthChecks": []any{map[string]any{"kind": "Job", "name": "migrate"}},
	} {
		o := live(want)
		o["spec"].(map[string]any)[field] = value
		if fluxApplierSpecMatches(want, o) {
			t.Errorf("a live %s the input does not render matched", field)
		}
	}
	if fluxApplierSpecMatches(nil, live(want)) {
		t.Errorf("an applier the input does not render matched")
	}
}

// TestFluxDeliveryReactivatedApplierAtSameDigest: reactivating a suspended
// applier at an unchanged digest. The renderer writes the source and the
// Kustomizations separately, so the source can carry the new fence while the
// Kustomization is still the previous input's, suspended, with that input's
// Ready (or failing) status. Neither decides the new promotion.
func TestFluxDeliveryReactivatedApplierAtSameDigest(t *testing.T) {
	t.Parallel()
	prevIn, _ := fluxRelease(true).input("2", "promo-2")
	in, _ := fluxRelease(true).input("3", "promo-3")
	prevIn.Appliers = append([]fluxdelivery.Applier(nil), prevIn.Appliers...)
	prevIn.Appliers[0].Suspended = true
	d := in.Source.Digest
	prevSpecs, err := fluxApplierSpecs(prevIn)
	if err != nil {
		t.Fatal(err)
	}
	wantSpecs, err := fluxApplierSpecs(in)
	if err != nil {
		t.Fatal(err)
	}
	a := in.Appliers[0]
	if suspend, _ := prevSpecs[a.Name]["suspend"].(bool); !suspend {
		t.Fatalf("fixture: the previous input does not render %s suspended: %v", a.Name, prevSpecs[a.Name])
	}
	stale := map[string]any{"spec": prevSpecs[a.Name]}
	if fluxApplierSpecMatches(wantSpecs[a.Name], stale) {
		t.Fatalf("the previous input's suspended Kustomization matched the reactivated applier")
	}
	at := func(ready, reason string) fluxDeliveryObservation {
		o := observed(in, func(b fluxdelivery.Applier) fluxObjectStatus {
			if b.Name != a.Name {
				return fluxObjectStatus{Name: b.Name, Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}
			}
			return fluxObjectStatus{Name: b.Name, Found: true, Ready: ready, Reason: reason, Message: "Job failed", Current: true, LastApplied: d, LastAttempted: d}
		})
		// The source already carries in's fence (observed's default); the
		// first applier is still the previous input's.
		o.Appliers[0].SpecMatches = fluxApplierSpecMatches(wantSpecs[a.Name], stale)
		return o
	}
	if o := at("True", ""); o.converged(in) {
		t.Errorf("the previous input's suspended Ready applier converged the reactivation")
	}
	if err := at("False", "HealthCheckFailed").failure("prod", "v3", in); err != nil {
		t.Errorf("the previous input's failure decided the reactivation: %v", err)
	}
	fresh := at("False", "HealthCheckFailed")
	fresh.Appliers[0].SpecMatches = true
	if err := fresh.failure("prod", "v3", in); err == nil {
		t.Errorf("the reactivated applier's own failure was not reported")
	}
}

// TestFluxKustomizationCRDDefaultsAreZero pins fluxApplierSpecMatches' premise: a
// field the render omits must be absent or zero live, which the API server
// keeps only while every default the Kustomization CRD declares is a zero
// value. A Flux upgrade that adds a non-zero default must widen the match.
func TestFluxKustomizationCRDDefaultsAreZero(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "pkg", "fluxdelivery", "testdata", "crds", "kustomization-kustomize-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	spec, _ := nested(schema, "properties", "spec").(map[string]any)
	if spec == nil {
		t.Fatal("the CRD schema has no spec")
	}
	var walk func(path string, v any)
	defaults := 0
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			if d, ok := x["default"]; ok {
				defaults++
				if !fluxSpecZero(d) {
					t.Errorf("spec%s defaults to %v, which a render that omits it would not match", path, d)
				}
			}
			for k, c := range x {
				walk(path+"."+k, c)
			}
		case []any:
			for i, c := range x {
				walk(fmt.Sprintf("%s[%d]", path, i), c)
			}
		}
	}
	walk("", spec)
	if defaults == 0 {
		t.Fatal("found no defaults: the walk is not reading the schema")
	}
}

// TestLastWrittenAt: the pointer's write time is its latest managedFields
// entry.
func TestLastWrittenAt(t *testing.T) {
	t.Parallel()
	obj := map[string]any{"metadata": map[string]any{"managedFields": []any{
		map[string]any{"manager": "forge", "time": "2026-03-01T12:00:00Z"},
		map[string]any{"manager": "flux", "time": "2026-03-01T12:05:00Z"},
		map[string]any{"manager": "x", "time": "not a time"},
	}}}
	if got, want := lastWrittenAt(obj), time.Date(2026, 3, 1, 12, 5, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("lastWrittenAt = %s, want %s", got, want)
	}
	if !lastWrittenAt(nil).IsZero() {
		t.Errorf("a missing pointer has a write time")
	}
}

// TestWaitFluxDelivery: the wait's three outcomes, through the observe seam.
// Not parallel: it swaps the seam.
func TestWaitFluxDelivery(t *testing.T) {
	rel := fluxRelease(true)
	in, _ := rel.input("2", "promo-2")
	d := in.Source.Digest
	prev := fluxDeliveryObserve
	t.Cleanup(func() { fluxDeliveryObserve = prev })
	quiet := func(string, ...any) {}

	calls := 0
	fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
		calls++
		return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}
		}), nil
	}
	if err := waitFluxDelivery(context.Background(), "prod", "v2", rel.fd, in, time.Minute, fluxWaitPace{}, quiet); err != nil || calls != 1 {
		t.Fatalf("converged: err = %v after %d reads", err, calls)
	}

	calls = 0
	fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
		calls++
		return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "False", Reason: "ReconciliationFailed", Message: forbiddenMessage, Current: true, LastAttempted: d}
		}), nil
	}
	var refusal *fluxDeliveryGrantRefusal
	if err := waitFluxDelivery(context.Background(), "prod", "v2", rel.fd, in, time.Minute, fluxWaitPace{failFast: true}, quiet); !errors.As(err, &refusal) || calls != 1 {
		t.Fatalf("forbidden, --fail-fast: err = %v after %d reads, want the grant refusal at the first", err, calls)
	}
	// Without --fail-fast a failure still reported at the deadline is the
	// verdict (exit 1, in Flux's words), not a timeout.
	if err := waitFluxDelivery(context.Background(), "prod", "v2", rel.fd, in, time.Nanosecond, fluxWaitPace{}, quiet); !errors.As(err, &refusal) || exitcode.Of(err) != exitWrong {
		t.Fatalf("forbidden at the deadline: err = %v (exit %d), want the grant refusal, exit 1", err, exitcode.Of(err))
	}

	fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
		return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "Unknown", Reason: "Progressing", Current: true}
		}), nil
	}
	if err := waitFluxDelivery(context.Background(), "prod", "v2", rel.fd, in, time.Nanosecond, fluxWaitPace{}, quiet); exitcode.Of(err) != exitTimedOut {
		t.Fatalf("still progressing at the deadline: err = %v (exit %d), want exit 8", err, exitcode.Of(err))
	}
}

// TestWaitFluxDeliveryToleratesARecoveredFailure: the default wait is not
// fail-fast (owner ruling Q4) — a health check that fails once and then
// settles converges — while --fail-fast ends it at the first failure, and a
// renderer Stalled on the input ends it either way. Not parallel: it swaps
// the seam.
func TestWaitFluxDeliveryToleratesARecoveredFailure(t *testing.T) {
	rel := fluxRelease(true)
	in, _ := rel.input("2", "promo-2")
	d := in.Source.Digest
	prev := fluxDeliveryObserve
	t.Cleanup(func() { fluxDeliveryObserve = prev })
	quiet := func(string, ...any) {}
	polls := 0
	fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
		polls++
		if polls == 1 {
			return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
				return fluxObjectStatus{Name: a.Name, Found: true, Ready: "False", Reason: "HealthCheckFailed",
					Message: "timeout waiting for: [Deployment/acme-prod/api status: 'InProgress']", Current: true, LastAttempted: d}
			}), nil
		}
		return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}
		}), nil
	}
	pace := fluxWaitPace{interval: time.Millisecond}
	if _, err := waitFluxDeliveryObserved(context.Background(), "prod", "v2", rel.fd, in, time.Minute, pace, quiet); err != nil || polls != 2 {
		t.Fatalf("default wait: err = %v after %d polls, want convergence at the second", err, polls)
	}
	polls = 0
	pace.failFast = true
	if _, err := waitFluxDeliveryObserved(context.Background(), "prod", "v2", rel.fd, in, time.Minute, pace, quiet); exitcode.Of(err) != exitWrong || polls != 1 {
		t.Fatalf("--fail-fast: err = %v (exit %d) after %d polls, want exit 1 at the first", err, exitcode.Of(err), polls)
	}

	polls = 0
	fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
		polls++
		o := observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "Unknown", Reason: "Progressing", Current: true}
		})
		o.Renderer.Stalled, o.Renderer.Reason, o.Renderer.StalledSince = true, "BuildFailed", pointerWrittenAt.Add(time.Second)
		return o, nil
	}
	if _, err := waitFluxDeliveryObserved(context.Background(), "prod", "v2", rel.fd, in, time.Minute, fluxWaitPace{interval: time.Millisecond}, quiet); exitcode.Of(err) != exitWrong || polls != 1 {
		t.Fatalf("a renderer stalled on the input: err = %v (exit %d) after %d polls, want exit 1 at the first", err, exitcode.Of(err), polls)
	}
}

// TestWaitFluxDeliverySupersededMidWait: a pointer replaced after the wait
// started — even at the same digest — ends the wait superseded (exit 6) at
// the next poll, never as the replacement's success. Not parallel: it swaps
// the seam.
func TestWaitFluxDeliverySupersededMidWait(t *testing.T) {
	rel := fluxRelease(true)
	in, _ := rel.input("2", "promo-2")
	d := in.Source.Digest
	prev := fluxDeliveryObserve
	t.Cleanup(func() { fluxDeliveryObserve = prev })
	quiet := func(string, ...any) {}
	for _, tc := range []struct {
		name    string
		pointer fluxPointerIdentity
	}{
		{"same digest, newer promotion", fluxPointerIdentity{Found: true, Promotion: "promo-3", Fence: "3", Digest: d}},
		{"another digest", fluxPointerIdentity{Found: true, Promotion: "promo-3", Fence: "3", Digest: "sha256:" + strings.Repeat("f", 64)}},
		{"removed", fluxPointerIdentity{}},
	} {
		polls := 0
		fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
			polls++
			if polls == 1 {
				return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
					return fluxObjectStatus{Name: a.Name, Found: true, Ready: "Unknown", Reason: "Progressing", Current: true}
				}), nil
			}
			// The replacement is rendered and Ready at its digest.
			next := in
			next.Promotion, next.Fence = tc.pointer.Promotion, tc.pointer.Fence
			o := observed(next, func(a fluxdelivery.Applier) fluxObjectStatus {
				return fluxObjectStatus{Name: a.Name, Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}
			})
			o.Pointer = tc.pointer
			return o, nil
		}
		_, err := waitFluxDeliveryObserved(context.Background(), "prod", "v2", rel.fd, in, time.Minute, fluxWaitPace{interval: time.Millisecond}, quiet)
		if exitcode.Of(err) != exitSuperseded || polls != 2 {
			t.Errorf("%s: err = %v (exit %d) after %d polls, want exit 6 after 2", tc.name, err, exitcode.Of(err), polls)
		}
	}
}

// TestPointFluxDeliveryObservesAnEarlierRetire: a deploy that marked a retire
// and then timed out (or crashed) leaves the marker on the pointer. The retry
// must still OBSERVE that retire applied before it drops the applier, not
// treat the marker as proof. Not parallel: it swaps the pointer and retire
// seams.
func TestPointFluxDeliveryObservesAnEarlierRetire(t *testing.T) {
	f := &fakePointer{}
	f.install(t)
	ctx := context.Background()
	prev := fluxRelease(true)
	if _, err := pointFluxDelivery(ctx, prev, "promo-1", time.Minute, quietNotice); err != nil {
		t.Fatalf("previous release: %v", err)
	}
	next := fluxRelease(true)
	next.doc.Trees = next.doc.Trees[:2]
	next.digest = "sha256:" + strings.Repeat("e", 64)

	// The interrupted deploy: its retire is written, never observed.
	var asked []string
	stubRetireApplied(t, 1<<30, &asked)
	if _, err := pointFluxDelivery(ctx, next, "promo-2", time.Nanosecond, quietNotice); exitcode.Of(err) != exitTimedOut {
		t.Fatalf("the interrupted deploy: err = %v (exit %d), want the retire timeout", err, exitcode.Of(err))
	}
	marked := f.liveInput(t)
	if marked.Promotion != "promo-1" || marked.Source.Digest != testFluxDigest {
		t.Fatalf("the interrupted deploy moved the source: %+v", marked)
	}
	writes := len(f.applied)

	// The retry finds the marker already written. Its retire is still not
	// applied, so the retry must wait and drop nothing.
	asked = nil
	if _, err := pointFluxDelivery(ctx, next, "promo-2", time.Nanosecond, quietNotice); exitcode.Of(err) != exitTimedOut {
		t.Fatalf("a retry over an unobserved retire: err = %v (exit %d), want the retire timeout", err, exitcode.Of(err))
	}
	if len(asked) != 1 || len(f.applied) != writes {
		t.Fatalf("the retry dropped an unobserved retire: asked %v, writes %d (want %d)", asked, len(f.applied), writes)
	}
	if got := f.liveInput(t); got.Source.Digest != testFluxDigest || len(got.Appliers) != 3 {
		t.Fatalf("the retry moved the source or dropped the applier: %+v", got)
	}

	// Once the retire is applied, the retry drops it without marking again.
	stubRetireApplied(t, 0, &asked)
	in, err := pointFluxDelivery(ctx, next, "promo-2", time.Minute, quietNotice)
	if err != nil {
		t.Fatalf("the retry once the retire is applied: %v", err)
	}
	if len(f.applied) != writes+1 || in.Source.Digest != next.digest || len(in.Appliers) != 2 || len(asked) != 1 {
		t.Errorf("the retry: writes %d (want %d), digest %s, appliers %d, asked %v", len(f.applied), writes+1, in.Source.Digest, len(in.Appliers), asked)
	}
}

// TestPointFluxDeliveryRetireInterleavedWriter: while this deploy waits for a
// retire to be applied, another writer re-points the env at a release that
// still carries the tree (un-retiring it). This deploy's drop is conditional
// on the pointer it read before the wait, so it fails, re-reads, marks the
// retire again and observes it again before it drops. Not parallel: it swaps
// the pointer and retire seams.
func TestPointFluxDeliveryRetireInterleavedWriter(t *testing.T) {
	f := &fakePointer{}
	f.install(t)
	ctx := context.Background()
	prev := fluxRelease(true)
	if _, err := pointFluxDelivery(ctx, prev, "promo-1", time.Minute, quietNotice); err != nil {
		t.Fatalf("previous release: %v", err)
	}
	next := fluxRelease(true)
	next.doc.Trees = next.doc.Trees[:2]
	next.digest = "sha256:" + strings.Repeat("e", 64)

	other := fluxRelease(true)
	other.digest = "sha256:" + strings.Repeat("f", 64)
	waits := 0
	prevRetire := fluxDeliveryRetireApplied
	t.Cleanup(func() { fluxDeliveryRetireApplied = prevRetire })
	fluxDeliveryRetireApplied = func(context.Context, FluxDeliveryEntity, []string) (bool, error) {
		waits++
		if waits == 1 {
			// The other writer lands while this one observes.
			live := f.liveInput(t)
			fence, _ := nextFluxDeliveryFence(&live)
			otherIn, _ := other.input(fence, "other")
			p, err := fluxdelivery.InputProvider(otherIn)
			if err != nil {
				t.Fatal(err)
			}
			f.store(p)
		}
		return true, nil
	}
	in, err := pointFluxDelivery(ctx, next, "promo-2", time.Minute, quietNotice)
	if err != nil {
		t.Fatalf("interleaved: %v", err)
	}
	if waits != 2 {
		t.Errorf("the retire was observed %d times, want once per marking (2)", waits)
	}
	// previous, retire, other's re-point, retire again, release.
	if len(f.applied) != 5 {
		t.Fatalf("writes = %d, want 5", len(f.applied))
	}
	remark, err := fluxdelivery.LiveInput(&unstructured.Unstructured{Object: f.applied[3]})
	if err != nil {
		t.Fatal(err)
	}
	if remark.Promotion != "other" || remark.Source.Digest != other.digest {
		t.Errorf("the second retire is not over the writer that won: %+v", remark.Source)
	}
	if in.Fence != "5" || in.Source.Digest != next.digest || len(in.Appliers) != 2 {
		t.Errorf("the release write: fence %s digest %s appliers %d", in.Fence, in.Source.Digest, len(in.Appliers))
	}
}

// TestWaitFluxDeliveryCurrentGuards: `env status <flux-env> --wait` enforces
// --release and --promotion against the live pointer before it waits, and
// refuses the options it cannot honour. Not parallel: it swaps the seams.
func TestWaitFluxDeliveryCurrentGuards(t *testing.T) {
	rel := fluxRelease(true)
	f := &fakePointer{}
	f.install(t)
	ctx := context.Background()
	in, _ := rel.input("1", "promo-1")
	if err := writeFluxDeliveryPointer(ctx, rel.fd, nil, "", in, "v2"); err != nil {
		t.Fatal(err)
	}
	prev := fluxDeliveryObserve
	t.Cleanup(func() { fluxDeliveryObserve = prev })
	observes := 0
	d := in.Source.Digest
	fluxDeliveryObserve = func(context.Context, FluxDeliveryEntity, fluxdelivery.BindingInput) (fluxDeliveryObservation, error) {
		observes++
		return observed(in, func(a fluxdelivery.Applier) fluxObjectStatus {
			return fluxObjectStatus{Name: a.Name, Found: true, Ready: "True", Current: true, LastApplied: d, LastAttempted: d}
		}), nil
	}
	for _, tc := range []struct {
		name string
		opts envWaitOptions
		code int
	}{
		{"the pointed release", envWaitOptions{Release: "v2"}, exitOK},
		{"the pointed promotion", envWaitOptions{PromotionID: "promo-1"}, exitOK},
		{"another release", envWaitOptions{Release: "v1"}, exitSuperseded},
		{"another promotion", envWaitOptions{PromotionID: "promo-0"}, exitSuperseded},
		{"--stable-for", envWaitOptions{StableFor: time.Minute}, exitWrong},
		{"--watch-json", envWaitOptions{WatchJSON: true}, exitWrong},
		{"--include-unpinned", envWaitOptions{IncludeUnpinned: true}, exitWrong},
		{"both names", envWaitOptions{Release: "v2", PromotionID: "promo-1"}, exitWrong},
	} {
		observes = 0
		var out bytes.Buffer
		err := waitFluxDeliveryCurrent(ctx, "prod", rel.fd, time.Minute, tc.opts, &out, &out)
		if exitcode.Of(err) != tc.code {
			t.Errorf("%s: err = %v (exit %d), want exit %d", tc.name, err, exitcode.Of(err), tc.code)
		}
		if tc.code != exitOK && observes != 0 {
			t.Errorf("%s: waited on a pointer it refused", tc.name)
		}
	}

	// A pointer that does not record its release cannot prove one.
	md := f.live.Object["metadata"].(map[string]any)
	delete(md["annotations"].(map[string]any), fluxDeliveryReleaseAnnotation)
	var out bytes.Buffer
	if err := waitFluxDeliveryCurrent(ctx, "prod", rel.fd, time.Minute, envWaitOptions{Release: "v2"}, &out, &out); exitcode.Of(err) != exitUndetermined {
		t.Errorf("an unrecorded release: err = %v (exit %d), want exit 2", err, exitcode.Of(err))
	}
}
