package cli

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type nsCall struct{ kctx, doc string }

func stubNamespaceCluster(t *testing.T, existing map[string]bool) *[]nsCall {
	t.Helper()
	var applied []nsCall
	prevExists, prevApply := namespaceExistsFn, applyNamespaceFn
	namespaceExistsFn = func(_ context.Context, kctx, name string) (bool, error) {
		return existing[kctx+"/"+name], nil
	}
	applyNamespaceFn = func(_ context.Context, kctx, doc string) error {
		applied = append(applied, nsCall{kctx, doc})
		return nil
	}
	t.Cleanup(func() { namespaceExistsFn, applyNamespaceFn = prevExists, prevApply })
	return &applied
}

// control-plane's prod v1.7.89-rc.2 (forge bug ac69bea3): the release adds the
// claimable-dns namespace on the daemon cluster and grants the prerequisite
// observer a Role in it, and the mint ran before the apply that created it.
// The deploy now creates the missing namespace first, exactly as rendered, on
// its own cluster, and leaves every existing namespace alone.
func TestApplyMissingRenderedNamespacesCreatesOnlyTheAbsentOnesAsRendered(t *testing.T) {
	applied := stubNamespaceCluster(t, map[string]bool{
		"daemon/kata-prepull":    true,
		"hub/control-plane-prod": true,
	})
	e := &KCLEntities{ManifestNamespaces: []RenderedNamespaceEntity{
		{Cluster: "daemon", Name: "claimable-dns", Labels: map[string]string{
			"forge.dev/cluster":                  "daemon",
			"pod-security.kubernetes.io/enforce": "restricted",
		}},
		{Cluster: "daemon", Name: "kata-prepull", Labels: map[string]string{"forge.dev/cluster": "daemon"},
			Annotations: map[string]string{"kustomize.toolkit.fluxcd.io/prune": "disabled"}},
		{Cluster: "hub", Name: "control-plane-prod"},
		{Cluster: "hosted", Name: "never-a-cluster"},
	}}
	if err := applyMissingRenderedNamespaces(context.Background(), e, false); err != nil {
		t.Fatal(err)
	}
	if len(*applied) != 1 {
		t.Fatalf("applied %d namespaces, want only the absent claimable-dns: %+v", len(*applied), *applied)
	}
	got := (*applied)[0]
	if got.kctx != "daemon" {
		t.Errorf("claimable-dns went to %q, want its own cluster daemon", got.kctx)
	}
	var doc struct {
		Kind     string
		Metadata struct {
			Name   string
			Labels map[string]string
		}
	}
	if err := yaml.Unmarshal([]byte(got.doc), &doc); err != nil {
		t.Fatalf("not YAML: %v\n%s", err, got.doc)
	}
	if doc.Kind != "Namespace" || doc.Metadata.Name != "claimable-dns" ||
		doc.Metadata.Labels["pod-security.kubernetes.io/enforce"] != "restricted" {
		t.Errorf("not the rendered Namespace:\n%s", got.doc)
	}
}

// A dry run contacts no cluster.
func TestApplyMissingRenderedNamespacesDryRunTouchesNothing(t *testing.T) {
	applied := stubNamespaceCluster(t, nil)
	namespaceExistsFn = func(context.Context, string, string) (bool, error) {
		t.Fatal("a dry run read a cluster")
		return false, nil
	}
	e := &KCLEntities{ManifestNamespaces: []RenderedNamespaceEntity{{Cluster: "daemon", Name: "claimable-dns"}}}
	out := captureStdout(t, func() {
		if err := applyMissingRenderedNamespaces(context.Background(), e, true); err != nil {
			t.Fatal(err)
		}
	})
	if len(*applied) != 0 || !strings.Contains(out, "claimable-dns") {
		t.Errorf("dry run applied %+v, printed %q", *applied, out)
	}
}

// The render keeps each stamped Namespace with its labels and annotations.
func TestRenderedNamespacesKeepsTheStampedNamespaces(t *testing.T) {
	var ns, cm rawManifest
	ns.Kind = "Namespace"
	ns.Metadata.Name = "claimable-dns"
	ns.Metadata.Labels = map[string]string{"forge.dev/cluster": "daemon", "a": "b"}
	ns.Metadata.Annotations = map[string]string{"x": "y"}
	cm.Kind = "ConfigMap"
	cm.Metadata.Name = "cfg"
	cm.Metadata.Labels = map[string]string{"forge.dev/cluster": "daemon"}
	var unstamped rawManifest
	unstamped.Kind = "Namespace"
	unstamped.Metadata.Name = "nowhere"
	got := renderedNamespaces([]rawManifest{cm, ns, unstamped})
	if len(got) != 1 || got[0].Cluster != "daemon" || got[0].Name != "claimable-dns" || got[0].Annotations["x"] != "y" {
		t.Fatalf("renderedNamespaces = %+v", got)
	}
}
