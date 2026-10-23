package cluster

import (
	"reflect"
	"strings"
	"testing"
)

// ownedPrepullStream is what forge renders for an image-less RenderedWorkload
// named `kata-prepull` that owns three manifests (control-plane prod at
// 7ec9e9c4). The DaemonSet carries its OWN `app.kubernetes.io/name`, the key its
// selector matches on, so the owner rides WorkloadLabel instead. A peer
// workload on another cluster rounds it out.
const ownedPrepullStream = `apiVersion: v1
kind: Namespace
metadata:
  name: kata-prepull
  labels:
    app.kubernetes.io/name: kata-prepull
    forge.dev/workload: kata-prepull
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata:
  name: workspace-base-prepull
  labels:
    app.kubernetes.io/name: kata-prepull
    forge.dev/workload: kata-prepull
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: workspace-base-prepull
  namespace: kata-prepull
  labels:
    app.kubernetes.io/name: workspace-base-prepull
    forge.dev/workload: kata-prepull
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: workspace-base-prepull
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: admin-server
  namespace: control-plane-prod
  labels:
    app.kubernetes.io/name: admin-server`

// TestSelectManifestsByGroup_RoutesByTheOwnerNotTheObjectsOwnName is D1's
// selection half: `--target kata-prepull` must ship all three owned objects,
// including the DaemonSet whose own name label says otherwise. On the old
// reader (AppNameLabel only) the DaemonSet is dropped — 2 of 3.
func TestSelectManifestsByGroup_RoutesByTheOwnerNotTheObjectsOwnName(t *testing.T) {
	got := SelectManifestsByGroup(ownedPrepullStream, []string{"kata-prepull"})
	docs := SplitManifestDocs(got)
	if len(docs) != 3 {
		t.Fatalf("--target kata-prepull kept %d object(s), want 3 (Namespace, PriorityClass, DaemonSet):\n%s", len(docs), got)
	}
	if !strings.Contains(got, "kind: DaemonSet") {
		t.Errorf("the owned DaemonSet was dropped:\n%s", got)
	}
	// And its own name is NOT a target: a name `--list` never attributes an
	// object to must select nothing.
	if s := SelectManifestsByGroup(ownedPrepullStream, []string{"workspace-base-prepull"}); strings.TrimSpace(s) != "" {
		t.Errorf("the DaemonSet's own name label acted as a group:\n%s", s)
	}
}

// TestScopeManifestsToGroup_RoutesByTheOwnerNotTheObjectsOwnName is D1's
// routing half. The DaemonSet belongs to kata-prepull, which the daemon
// cluster owns; on the old reader its app label (`workspace-base-prepull`) is
// in NEITHER set, so it was kept on every cluster — the hub included.
func TestScopeManifestsToGroup_RoutesByTheOwnerNotTheObjectsOwnName(t *testing.T) {
	hub := GroupScope{
		OwnApps:   map[string]struct{}{"admin-server": {}},
		OtherApps: map[string]struct{}{"kata-prepull": {}},
	}
	got := ScopeManifestsToGroup(ownedPrepullStream, hub)
	if strings.Contains(got, "kind: DaemonSet") {
		t.Errorf("the daemon cluster's DaemonSet was routed to the hub:\n%s", got)
	}
	daemon := GroupScope{
		OwnApps:   map[string]struct{}{"kata-prepull": {}},
		OtherApps: map[string]struct{}{"admin-server": {}},
	}
	if got := ScopeManifestsToGroup(ownedPrepullStream, daemon); len(SplitManifestDocs(got)) != 3 {
		t.Errorf("daemon cluster kept %d object(s), want its 3:\n%s", len(SplitManifestDocs(got)), got)
	}
}

// TestManifestGroup_OwnerKeyWinsAndNameIsTheFallback pins the reader: the
// forge-owned key when present, else the recommended name label (what every
// object forge's builders render carries), else nothing.
func TestManifestGroup_OwnerKeyWinsAndNameIsTheFallback(t *testing.T) {
	for _, tc := range []struct {
		labels map[string]string
		want   string
	}{
		{map[string]string{WorkloadLabel: "owner", AppNameLabel: "self"}, "owner"},
		{map[string]string{AppNameLabel: "api"}, "api"},
		{map[string]string{"team": "x"}, ""},
		{nil, ""},
	} {
		if got := ManifestGroup(tc.labels); got != tc.want {
			t.Errorf("ManifestGroup(%v) = %q, want %q", tc.labels, got, tc.want)
		}
	}
	if got, want := ManifestGroups(ownedPrepullStream), []string{"admin-server", "kata-prepull"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ManifestGroups = %v, want %v", got, want)
	}
}
