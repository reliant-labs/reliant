package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/reliant-labs/forge/pkg/stsresize"
)

const preflightClickhouseManifest = `
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: clickhouse
  namespace: observability-data
spec:
  serviceName: clickhouse
  selector:
    matchLabels: {app: clickhouse}
  volumeClaimTemplates:
    - metadata: {name: data}
      spec:
        accessModes: [ReadWriteOnce]
        storageClassName: standard-rwo
        resources: {requests: {storage: 20Gi}}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: stateless, namespace: observability-data}
spec: {serviceName: stateless, selector: {matchLabels: {app: stateless}}}
`

// fakeVolumeResizeChecker answers per StatefulSet name, and records who was asked on
// which cluster.
type fakeVolumeResizeChecker struct {
	mu      sync.Mutex
	verdict map[string]error
	asked   []string
}

func (f *fakeVolumeResizeChecker) Judge(_ context.Context, kctx string, d *appsv1.StatefulSet) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, kctx+"|"+d.Namespace+"/"+d.Name)
	return f.verdict[d.Name]
}

func TestPreflight_VolumeShrinkBlocksAndNamesTheStatefulSet(t *testing.T) {
	fake := &fakeVolumeResizeChecker{verdict: map[string]error{
		"clickhouse": fmt.Errorf("%w: StatefulSet observability-data/clickhouse template %q declares 20Gi, the live template has 100Gi", stsresize.ErrShrink, "data"),
	}}
	err := Preflight(context.Background(), PreflightOpts{
		Manifests: preflightClickhouseManifest, Context: "gke_prod", Namespace: "observability-data", VolumeResize: fake,
	})
	if err == nil {
		t.Fatal("a shrink must BLOCK the deploy")
	}
	msg := err.Error()
	for _, want := range []string{"observability-data/clickhouse", "cannot shrink", "100Gi"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the refusal must name %q; got:\n%s", want, msg)
		}
	}
	// A StatefulSet with no volumeClaimTemplates has nothing to judge.
	if len(fake.asked) != 1 || fake.asked[0] != "gke_prod|observability-data/clickhouse" {
		t.Fatalf("judged %v, want only clickhouse on gke_prod", fake.asked)
	}
}

func TestPreflight_VolumeGrowthAndNoChangePass(t *testing.T) {
	fake := &fakeVolumeResizeChecker{}
	if err := Preflight(context.Background(), PreflightOpts{
		Manifests: preflightClickhouseManifest, Context: "gke_prod", Namespace: "observability-data", VolumeResize: fake,
	}); err != nil {
		t.Fatalf("a growable volume must pass: %v", err)
	}
}

func TestPreflight_VolumeUnreadableWarnsAndDoesNotBlock(t *testing.T) {
	fake := &fakeVolumeResizeChecker{verdict: map[string]error{"clickhouse": errors.New("kubectl get statefulset: connection refused")}}
	res, err := PreflightReport(context.Background(), PreflightOpts{
		Manifests: preflightClickhouseManifest, Context: "gke_prod", Namespace: "observability-data", VolumeResize: fake,
	})
	if err != nil {
		t.Fatalf("an unreadable live StatefulSet says nothing about the declaration, so it must not block: %v", err)
	}
	if len(res.VolumeResizeWarnings) != 1 || !strings.Contains(res.VolumeResizeWarnings[0], "observability-data/clickhouse") {
		t.Fatalf("warnings = %v", res.VolumeResizeWarnings)
	}
}

func TestPreflight_VolumeIsJudgedOnEveryClusterTheDocumentLandsOn(t *testing.T) {
	fake := &fakeVolumeResizeChecker{verdict: map[string]error{"clickhouse": fmt.Errorf("%w", stsresize.ErrTemplateChanged)}}
	res, err := PreflightReport(context.Background(), PreflightOpts{
		Manifests: preflightClickhouseManifest, Context: "hub", Namespace: "observability-data", VolumeResize: fake,
		DocumentContexts: func(string) []string { return []string{"hub", "daemon"} },
	})
	if err == nil || len(res.VolumeResizeRefusals) != 2 {
		t.Fatalf("refusals = %v, err = %v", res.VolumeResizeRefusals, err)
	}
	for _, r := range res.VolumeResizeRefusals {
		if !strings.HasPrefix(r, "hub:") && !strings.HasPrefix(r, "daemon:") {
			t.Fatalf("a multi-cluster finding must say which cluster: %q", r)
		}
	}
}

func TestPreflight_VolumeCheckNeedsAContext(t *testing.T) {
	fake := &fakeVolumeResizeChecker{verdict: map[string]error{"clickhouse": stsresize.ErrShrink}}
	if err := Preflight(context.Background(), PreflightOpts{Manifests: preflightClickhouseManifest, VolumeResize: fake}); err != nil {
		t.Fatalf("with no cluster there is nothing to compare against: %v", err)
	}
	if len(fake.asked) != 0 {
		t.Fatalf("asked %v with no context", fake.asked)
	}
}

func TestIsVolumeRefusal(t *testing.T) {
	for _, e := range []error{stsresize.ErrShrink, stsresize.ErrNotExpandable, stsresize.ErrTemplateChanged, stsresize.ErrOtherImmutable} {
		if !isVolumeRefusal(fmt.Errorf("wrapped: %w", e)) {
			t.Errorf("%v is a refusal", e)
		}
	}
	if isVolumeRefusal(errors.New("connection refused")) {
		t.Error("a read failure is not a refusal")
	}
}
