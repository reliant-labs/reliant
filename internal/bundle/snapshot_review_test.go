package bundle

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/forge/internal/releasecompose"
	"github.com/reliant-labs/forge/pkg/release"
)

func copySnapshotDoc(t *testing.T, d release.BundleDoc) release.BundleDoc {
	t.Helper()
	b, e := json.Marshal(d)
	if e != nil {
		t.Fatal(e)
	}
	var out release.BundleDoc
	if e := json.Unmarshal(b, &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func reviewedBundle(t *testing.T) Bundle {
	t.Helper()
	in, decl := snapshotFixture(t)
	in.Shape.Manifests += "# cluster: cluster\napiVersion: v1\nkind: Service\nmetadata:\n  name: external\n  namespace: shop\nspec:\n  type: LoadBalancer\n  loadBalancerIP: 192.0.2.1\n---\n# cluster: cluster\napiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migrate-abcd1234\n  namespace: shop\nspec:\n  activeDeadlineSeconds: 300\n  template:\n    spec:\n      restartPolicy: Never\n      containers:\n        - name: api\n          image: registry.test/reliant@" + snapDigest("a") + "\n---\n"
	s, err := SnapshotInventory(context.Background(), in, decl)
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot = &s
	b, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSnapshotRejectsForgedPlanningProjection(t *testing.T) {
	b := reviewedBundle(t)
	layer, _ := b.Layer(release.BundleManifestsLayer)
	tests := []struct {
		name   string
		mutate func(*release.BundleDoc)
	}{
		{"omitted object", func(d *release.BundleDoc) { d.Shape.Objects = d.Shape.Objects[1:] }},
		{"stale hash", func(d *release.BundleDoc) { d.Shape.Objects[0].Hash = snapDigest("f") }},
		{"config hash", func(d *release.BundleDoc) { d.Shape.Objects[0].ConfigHash = snapDigest("f") }},
		{"image projection", func(d *release.BundleDoc) { d.Shape.Objects[0].Images = map[string]string{"invented": snapDigest("f")} }},
		{"tree projection", func(d *release.BundleDoc) { d.Shape.Objects[0].Tree = "other/rollout" }},
		{"LB identity", func(d *release.BundleDoc) {
			for i := range d.Shape.Objects {
				if d.Shape.Objects[i].Kind == "Service" {
					d.Shape.Objects[i].Identity = nil
				}
			}
		}},
		{"config digest", func(d *release.BundleDoc) { d.ConfigDigest = snapDigest("f") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := copySnapshotDoc(t, b.Doc)
			tt.mutate(&doc)
			// Honest unchanged resource layer is not enough to authorize lying plan metadata.
			if err := release.ValidateSnapshotLayer(*doc.Snapshot, layer); err != nil {
				t.Fatal(err)
			}
			if err := doc.Validate(); err == nil {
				t.Fatal("forged planning metadata admitted")
			}
		})
	}
}

func TestSnapshotRejectsWeakenedDeliveryMetadata(t *testing.T) {
	b := reviewedBundle(t)
	// Exercise the seal after an actual partial composition from a recorded
	// base as well, not only a full bootstrap manifest.
	base := *b.Doc.Snapshot
	hash, _ := base.Digest()
	selected := []release.ConsumerKey{base.Consumers[1].Key}
	partial, err := releasecompose.Compose(releasecompose.Input{Base: &base, Candidate: base, BaseRef: &release.SnapshotBase{PromotionID: "p1", SnapshotDigest: hash, BundleDigest: b.Digest}, Selected: selected})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := snapshotFixture(t)
	in.Snapshot = &partial
	b, err = Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		mutate func(*release.BundleDoc)
	}{
		{"health removed", func(d *release.BundleDoc) {
			for i := range d.Trees {
				d.Trees[i].Health = nil
			}
		}},
		{"dependencies removed", func(d *release.BundleDoc) {
			for i := range d.Trees {
				d.Trees[i].DependsOn = nil
			}
		}},
		{"budget changed", func(d *release.BundleDoc) { d.Trees[0].BudgetSeconds++ }},
		{"timeout changed", func(d *release.BundleDoc) { d.Trees[0].TimeoutSeconds++ }},
		{"retry changed", func(d *release.BundleDoc) { d.Trees[0].RetryIntervalSeconds++ }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			doc := copySnapshotDoc(t, b.Doc)
			before := doc.SnapshotDigest
			tt.mutate(&doc)
			hash, err := doc.Snapshot.Digest()
			if err != nil || hash != before {
				t.Fatal("test changed semantic snapshot")
			}
			if err := doc.Validate(); err == nil {
				t.Fatal("weakened actuator metadata admitted")
			}
		})
	}
}

func TestFrontendDeclarationAssetSurvivesBootstrapCarryBundle(t *testing.T) {
	in, decl := snapshotFixture(t)
	asset := []byte(`{"schema":"forge.dev/frontend-declarations/v1","frontends":[]}`)
	in.Observability = []ObservabilityFile{{Path: release.BundleFrontendDeclarationsPath, Data: asset}}
	base, err := SnapshotInventory(context.Background(), in, decl)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := base.Digest()
	worker := decl.Consumers[1].Key
	next, err := releasecompose.Compose(releasecompose.Input{Base: &base, Candidate: base, BaseRef: &release.SnapshotBase{PromotionID: "p1", SnapshotDigest: hash, BundleDigest: snapDigest("d")}, Selected: []release.ConsumerKey{worker}})
	if err != nil {
		t.Fatal(err)
	}
	in.Snapshot = &next
	built, err := Build(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	layer, _ := built.Layer(release.BundleManifestsLayer)
	if err := release.ValidateSnapshotLayer(next, layer); err != nil {
		t.Fatal(err)
	}
	assets, err := release.ReadSnapshotAssets(layer)
	if err != nil || len(assets) != 1 || string(assets[0].Data) != string(asset) {
		t.Fatalf("frontend asset lost: %+v %v", assets, err)
	}
	for _, name := range []string{"forge/arbitrary.json", "forge/frontend-declarations.json/escape.json", "forge/../other.json"} {
		bad, err := writeTarGz([]tarEntry{{name: name, data: asset}})
		if err != nil {
			continue
		}
		if _, err := release.ReadSnapshotAssets(bad); err == nil {
			t.Fatalf("arbitrary non-applied path admitted %s", name)
		}
	}
}
