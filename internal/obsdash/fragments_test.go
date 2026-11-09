package obsdash

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// The shared project part must not change when an unrelated service is added.
// Alert inputs already have one file per service; dashboard RPC ownership comes
// from the selected environment's rendered processes, not a project-wide list.
func TestFragmentsContainOnlyProjectInputs(t *testing.T) {
	in := fixture()
	in.Funnels = []FrontendFunnel{{ID: "checkout", Funnel: Funnel{Steps: []string{"cart", "paid"}, Window: "7d"}}}
	project, _ := BuildParts(in)
	more := in
	more.Services = append(append([]Service(nil), in.Services...), Service{
		Name: "extra", Package: "extra.v1", ProtoService: "ExtraService", Methods: []Method{{Name: "Ping"}},
	})
	added, _ := BuildParts(more)
	if !reflect.DeepEqual(project, added) {
		t.Fatal("adding a service changed the shared project part")
	}

	dir := t.TempDir()
	if _, err := Write(dir, in, &checksums.FileChecksums{}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadFragments(dir)
	if err != nil || !ok {
		t.Fatalf("ReadFragments: ok=%v err=%v", ok, err)
	}
	want := Input{Project: in.Project, Frontends: in.Frontends, HasDatabase: in.HasDatabase, Funnels: in.Funnels}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("project fragments did not round-trip: got %+v, want %+v", got, want)
	}
	// Per-env process identities can be supplied without the service inventory
	// and produce exactly the dashboards a direct build would produce.
	got.Processes = in.Processes
	if !reflect.DeepEqual(Build(got), Build(in)) {
		t.Fatal("selected-env assembly lost dashboard content")
	}
	alerts, ok, err := ReadAlertInput(dir)
	if err != nil || !ok || !reflect.DeepEqual(alerts, AlertInputOf(in)) {
		t.Fatalf("service alert inputs lost: got %+v, ok=%v, err=%v", alerts, ok, err)
	}
}

// Parts written by the old writer embedded an entire Input. Accept those
// project fields without reviving the stale service or process inventory.
func TestFragmentsReadPreviouslyWrittenInput(t *testing.T) {
	in := fixture()
	dir := t.TempDir()
	raw, err := json.Marshal(struct {
		Version string `json:"version"`
		Project string `json:"project"`
		Input   Input  `json:"input"`
	}{PartsVersion, in.Project, in})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, SpecDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ProjectSpecFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadFragments(dir)
	if err != nil || !ok {
		t.Fatalf("ReadFragments: ok=%v err=%v", ok, err)
	}
	want := Input{Project: in.Project, Frontends: in.Frontends, HasDatabase: in.HasDatabase, Funnels: in.Funnels}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy project fragments: got %+v, want %+v", got, want)
	}
}
