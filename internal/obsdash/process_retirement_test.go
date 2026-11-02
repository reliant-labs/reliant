package obsdash

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestProcessGenerationRetiresOnlyPristineServiceArtifacts(t *testing.T) {
	dir := t.TempDir()
	cs := &checksums.FileChecksums{}
	old := []string{
		DashboardsDir + "/shop-service-orders_gen.json",
		DashboardsDir + "/shop-service-orders_gen.ndjson",
		SpecDir + "/service-orders_gen.json",
	}
	for _, rel := range old {
		if _, err := checksums.WriteGeneratedFile(dir, rel, []byte("{}\n"), cs, true); err != nil {
			t.Fatal(err)
		}
	}
	owned := DashboardsDir + "/shop-service-orders.json"
	changed := SpecDir + "/service-owned_gen.json"
	if _, err := checksums.WriteGeneratedFile(dir, changed, []byte("{}\n"), cs, true); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{owned, changed} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("{\"owned\":true}\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Write(dir, fixture(), cs); err != nil {
		t.Fatal(err)
	}
	for _, rel := range old {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("obsolete generated artifact survived: %s (%v)", rel, err)
		}
	}
	for _, rel := range []string{owned, changed} {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || string(b) != "{\"owned\":true}\n" {
			t.Fatalf("owned artifact modified: %s (%v)", rel, err)
		}
	}
	// Kept hand-edited old spec parts must not be assembled into the generated
	// process set. They are retained on disk for their owner, never revived.
	sp, ok, err := ReadSpec(dir)
	if err != nil || !ok {
		t.Fatalf("read generated parts: %v", err)
	}
	for _, d := range sp.Dashboards {
		if len(d.ID) > 8 && d.ID[:8] == "service-" {
			t.Fatalf("stale service dashboard revived: %s", d.ID)
		}
	}
}

func TestResolvedBundleSetExcludesStalePartsAndKeepsOwnedOverride(t *testing.T) {
	dir := t.TempDir()
	if _, err := Write(dir, fixture(), &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	selected := fixture()
	selected.Processes = selected.Processes[:1]
	selected.Processes[0].ServiceNames = []string{"selected-alias"}
	sp := Build(selected)
	owned := Dashboard{ID: "process-shop-api", Title: "Owned selected API", Panels: runtimePanels()}
	raw, _ := json.Marshal(owned)
	if err := os.WriteFile(filepath.Join(dir, OverridePath("shop", owned)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	set, err := CollectBundleSet(BundleSetInput{ProjectDir: dir, Resolved: &sp})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range set.Dashboards {
		if d.Dashboard.ID == "process-shop-worker" {
			t.Fatal("stale other-environment disk process leaked into selected bundle")
		}
		if d.Dashboard.ID == owned.ID {
			found = true
			if d.Dashboard.Title != owned.Title {
				t.Fatal("owned process override lost")
			}
		}
	}
	if !found {
		t.Fatal("selected owned process missing")
	}
}

func TestOverviewHealthIsMetricBackedAndBounded(t *testing.T) {
	d := Build(fixture()).Dashboards[0]
	if len(d.Panels) > 15 {
		t.Fatalf("overview has %d panels", len(d.Panels))
	}
	for _, id := range []string{"requests", "errors", "p95", "request-rate", "error-rate", "latency"} {
		p, ok := findPanel(d, id)
		if !ok || p.Signal != Metrics {
			t.Fatalf("%s not backed by unsampled metrics: %+v", id, p)
		}
	}
	// Every emitted dashboard must still pass the shared public contract.
	for _, d := range Build(fixture()).Dashboards {
		for _, f := range d.Filters {
			if f.ID != "rpc-procedure" && f.ID != "rpc-service" && f.ID != "rpc-method" {
				continue
			}
			if len(f.PanelIDs) == 0 {
				t.Fatalf("RPC filter applies globally: %s", f.ID)
			}
			for _, id := range f.PanelIDs {
				p, ok := findPanel(d, id)
				if !ok || p.Section != "rpc" {
					t.Fatalf("RPC filter targets non-RPC panel %q", id)
				}
			}
		}
		b, _ := json.Marshal(d)
		if _, err := ParseDashboard(d.ID, b); err != nil {
			t.Fatal(err)
		}
	}
}
