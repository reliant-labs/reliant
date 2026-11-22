package cluster

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func safetyFixture(t *testing.T) (*fakeCluster, HelmChartSpec, func(string)) {
	t.Helper()
	if testing.Short() {
		t.Skip("fake kubectl subprocess sequence; runs in task test")
	}
	cl := newFakeCluster(t)
	set := fakeHelmReleases(t)
	spec := fluxLikeSpec()
	set(stream(docDeployment, docWebhookPolicy))
	applyRelease(t, spec)
	return cl, spec, set
}
func policyLive(t *testing.T, cl *fakeCluster) liveChartObject {
	t.Helper()
	var o liveChartObject
	b, _ := json.Marshal(cl.find("NetworkPolicy", "flux-system", "allow-webhooks"))
	if err := json.Unmarshal(b, &o); err != nil {
		t.Fatal(err)
	}
	return o
}
func TestChartPrune_ReplacedUIDIsNeverDeleted(t *testing.T) {
	cl, spec, set := safetyFixture(t)
	o := jsonRoundTrip(cl.find("NetworkPolicy", "flux-system", "allow-webhooks"))
	strMap(o["metadata"])["uid"] = "foreign-replacement"
	s := cl.state()
	s.upsert(o)
	saveFakeState(cl.path, s)
	set(docDeployment)
	applyRelease(t, spec)
	if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
		t.Fatal("deleted a replacement UID")
	}
	if len(cl.deletes()) != 0 {
		t.Fatalf("deleted %v", cl.deletes())
	}
}
func TestChartPrune_DeletePreconditionsProtectConcurrentReplacementAndAnnotation(t *testing.T) {
	for _, change := range []string{"replacement", "prune-disabled", "disowned"} {
		t.Run(change, func(t *testing.T) {
			cl, spec, set := safetyFixture(t)
			changed := jsonRoundTrip(cl.find("NetworkPolicy", "flux-system", "allow-webhooks"))
			md := strMap(changed["metadata"])
			switch change {
			case "replacement":
				md["uid"] = "new-foreign-uid"
			case "prune-disabled":
				md["annotations"] = map[string]any{PruneAnnotation: PruneDisabled}
			case "disowned":
				delete(strMap(md["labels"]), ChartOwnerLabel)
			}
			// Fake injects after GET but immediately before DELETE, advancing RV.
			cl.injectOn("delete", changed)
			set(docDeployment)
			rc := renderRelease(t, spec)
			if err := applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{rc}, true); err == nil {
				t.Fatal("conditional delete conflict must surface")
			}
			if !cl.has("NetworkPolicy", "flux-system", "allow-webhooks") {
				t.Fatal("deleted concurrently modified object")
			}
			if len(cl.deletes()) != 0 {
				t.Fatalf("unexpected deletes %v", cl.deletes())
			}
		})
	}
}
func TestChartPrune_FailedApplyLeavesInventoryByteIdentical(t *testing.T) {
	cl, spec, set := safetyFixture(t)
	inventoryData := func() any {
		o := cl.find("ConfigMap", chartStateNamespace, chartInventoryName(spec.Name, spec.Owner))
		if o == nil {
			t.Fatal("fixture: inventory missing")
		}
		return strMap(o["data"])[chartInventoryDataKey]
	}
	before := inventoryData()
	set(docDeployment)
	t.Setenv("FORGE_FAKE_FAIL_APPLY", "1")
	rc := renderRelease(t, spec)
	if err := applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{rc}, true); err == nil {
		t.Fatal("apply unexpectedly succeeded")
	}
	if !reflect.DeepEqual(before, inventoryData()) {
		t.Fatal("failed apply changed serialized inventory payload or recorded UIDs")
	}
	if len(cl.deletes()) != 0 {
		t.Fatal("failed apply pruned")
	}
}
func TestChartPrune_LockNeverExpiresAndAppliesNothingWhenHeld(t *testing.T) {
	cl, spec, set := safetyFixture(t)
	set(docDeployment)
	rc := renderRelease(t, spec)
	p, err := beginChartPrune(context.Background(), "k3d-test", rc, "old-holder")
	if err != nil {
		t.Fatal(err)
	}
	// Age has no role in acquisition; even an ancient started timestamp is not
	// evidence the process is dead. No timer permits takeover.
	s := cl.state()
	for _, o := range s.Objects {
		if strMap(o["metadata"])["name"] == chartLockName(spec) {
			strMap(o["data"])["started"] = "1970-01-01T00:00:00Z"
		}
	}
	saveFakeState(cl.path, s)
	before := cl.state().Applies
	err = applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{rc}, true)
	if err == nil || !strings.Contains(err.Error(), "no timeout takeover") {
		t.Fatalf("missing recovery refusal: %v", err)
	}
	if cl.state().Applies != before {
		t.Fatal("held lock admitted a chart write")
	}
	if !cl.has("ConfigMap", chartStateNamespace, chartLockName(spec)) {
		t.Fatal("another applier removed old lock")
	}
	// Simulate the OLD writer resuming; its lock is still valid since nobody
	// stole it, so normal release is safe after it exits its critical section.
	if err := p.release(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestChartPrune_CorruptInventoryFailsBeforeChartWrites(t *testing.T) {
	cl, spec, set := safetyFixture(t)
	s := cl.state()
	for _, o := range s.Objects {
		if strMap(o["metadata"])["name"] == chartInventoryName(spec.Name, spec.Owner) {
			strMap(o["data"])[chartInventoryDataKey] = "not json"
		}
	}
	saveFakeState(cl.path, s)
	before := cl.state().Applies
	set(docDeployment)
	rc := renderRelease(t, spec)
	if err := applyRenderedCharts(context.Background(), "k3d-test", []renderedChart{rc}, true); err == nil {
		t.Fatal("accepted corrupt inventory")
	}
	if cl.state().Applies != before || len(cl.deletes()) != 0 {
		t.Fatal("corrupt inventory permitted chart write/prune")
	}
}
func TestChartPrune_RecoveryRequiresInspectedIdentityAndHolder(t *testing.T) {
	cl, spec, set := safetyFixture(t)
	set(docDeployment)
	p, err := beginChartPrune(context.Background(), "k3d-test", renderRelease(t, spec), "holder-A")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ uid, rv, confirm string }{
		{p.lock.Metadata.UID, p.lock.Metadata.ResourceVersion, ""},
		{p.lock.Metadata.UID, p.lock.Metadata.ResourceVersion, "terminated:other-holder"},
		{"stale-uid", p.lock.Metadata.ResourceVersion, "terminated:holder-A"},
		{p.lock.Metadata.UID, "stale-rv", "terminated:holder-A"},
	} {
		if err := RecoverChartLock(context.Background(), "k3d-test", spec.Owner, spec.Name, tc.uid, tc.rv, tc.confirm); err == nil {
			t.Fatal("unconfirmed or stale recovery succeeded")
		}
	}
	if !cl.has("ConfigMap", chartStateNamespace, chartLockName(spec)) {
		t.Fatal("invalid recovery cleared lock")
	}
	if err := RecoverChartLock(context.Background(), "k3d-test", spec.Owner, spec.Name, p.lock.Metadata.UID, p.lock.Metadata.ResourceVersion, "terminated:holder-A"); err != nil {
		t.Fatal(err)
	}
	if cl.has("ConfigMap", chartStateNamespace, chartLockName(spec)) {
		t.Fatal("confirmed recovery did not clear old lock")
	}
	if err := p.checkLock(context.Background()); err == nil {
		t.Fatal("obsolete writer accepted recovered lock")
	}
}

func TestChartPrune_PlanUsesRecordedUIDAndGuards(t *testing.T) {
	cl, spec, set := safetyFixture(t)
	set(docDeployment)
	rc := renderRelease(t, spec)
	del, _, ok, err := chartPruneCandidates(context.Background(), "k3d-test", rc)
	if err != nil || !ok || len(del) != 1 {
		t.Fatalf("plan: %v %v %v", del, ok, err)
	}
	o := policyLive(t, cl)
	changed := jsonRoundTrip(o.raw)
	strMap(changed["metadata"])["uid"] = "new"
	s := cl.state()
	s.upsert(changed)
	saveFakeState(cl.path, s)
	del, _, _, err = chartPruneCandidates(context.Background(), "k3d-test", rc)
	if err != nil || len(del) != 0 {
		t.Fatalf("plan offered foreign UID for prune: %v %v", del, err)
	}
}
