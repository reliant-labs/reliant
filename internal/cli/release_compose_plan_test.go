package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/pkg/release"
)

func TestCompositionSelectionIndependentOfSharedImage(t *testing.T) {
	consumers := []release.Consumer{{Key: release.ConsumerKey{Cluster: "prod", Workload: "api"}}, {Key: release.ConsumerKey{Cluster: "prod", Workload: "worker"}}}
	got, err := compositionSelection(consumers, []string{" worker ", "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Workload != "worker" {
		t.Fatalf("selection expanded: %+v", got)
	}
	for _, selectors := range [][]string{{"missing"}, {"all", "worker"}, {""}} {
		if _, err := compositionSelection(consumers, selectors); err == nil {
			t.Fatalf("accepted %v", selectors)
		}
	}
	consumers = append(consumers, release.Consumer{Key: release.ConsumerKey{Cluster: "other", Workload: "worker"}})
	if _, err := compositionSelection(consumers, []string{"worker"}); err == nil {
		t.Fatal("accepted ambiguous cluster")
	}
}

func TestCompositionBuildRequiresCapturedBaseBeforePreflight(t *testing.T) {
	consumers := []release.Consumer{{Key: release.ConsumerKey{Cluster: "prod", Workload: "api"}}, {Key: release.ConsumerKey{Cluster: "prod", Workload: "worker"}}}
	_, err := planCompositionBuild(context.Background(), &config.ProjectConfig{Name: "p"}, &KCLEntities{}, consumers, []string{"worker"}, nil, buildOptions{}, planInputs{})
	if err == nil || !strings.Contains(err.Error(), "captured base") {
		t.Fatalf("got %v", err)
	}
}

func TestCompositionBuildUnitConflictAndNoRepositoryDedupe(t *testing.T) {
	a := WorkloadEntity{Name: "api", Image: "shared", Build: BuildConfigEntity{Unit: "reliant", Type: "shell", Shell: &ShellBuild{Cmd: "build"}}}
	b := a
	b.Name = "worker"
	units, err := compositionBuildUnits(&KCLEntities{Workloads: []WorkloadEntity{a, b}})
	if err != nil || len(units) != 1 || len(units[0]) != 2 {
		t.Fatalf("units %+v: %v", units, err)
	}
	b.Build.Shell = &ShellBuild{Cmd: "different"}
	if _, err := compositionBuildUnits(&KCLEntities{Workloads: []WorkloadEntity{a, b}}); err == nil {
		t.Fatal("accepted conflicting unit")
	}
	a.Build.Unit = ""
	b.Build.Unit = ""
	units, err = compositionBuildUnits(&KCLEntities{Workloads: []WorkloadEntity{a, b}})
	if err != nil || len(units) != 2 {
		t.Fatalf("deduped merely equal repository: %+v %v", units, err)
	}
}

func TestCompositionBuildDeclarationDecode(t *testing.T) {
	b, err := dispatchBuild("worker", []byte(`{"type":"shell","cmd":"build","unit":"reliant","inputs_file":"actual.json"}`))
	if err != nil || b.Unit != "reliant" || b.InputsFile != "actual.json" {
		t.Fatalf("%+v %v", b, err)
	}
}
