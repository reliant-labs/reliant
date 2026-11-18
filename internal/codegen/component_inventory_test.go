package codegen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// What these pin: discovery reads each component kind off the artifact that
// declares it, so `forge scaffold crd --operator X` and
// `forge scaffold webhook --service Y` resolve X and Y on a project where
// they were just created, and the shared name-uniqueness gate sees them all.

func writeGo(t *testing.T, path, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func componentByName(comps []config.ComponentConfig, name string) (config.ComponentConfig, bool) {
	for _, c := range comps {
		if c.Name == name {
			return c, true
		}
	}
	return config.ComponentConfig{}, false
}

func TestDiscoverProjectComponents_WorkersOperatorsBinaries(t *testing.T) {
	dir := t.TempDir()

	// A long-running worker and a cron worker (the Schedule constant is the
	// cron template's own declaration of its kind).
	writeGo(t, filepath.Join(dir, "internal", "workers", "mailer", "worker.go"),
		"package mailer\n\ntype Worker struct{}\n")
	writeGo(t, filepath.Join(dir, "internal", "workers", "nightly", "worker.go"),
		"package nightly\n\nconst Schedule = \"0 3 * * *\"\n\ntype Worker struct{}\n")

	// An operator with recorded API coordinates and one CRD shim.
	writeGo(t, filepath.Join(dir, "internal", "operators", "fleet", "operator.go"),
		"package fleet\n\nconst (\n\tAPIGroup   = \"acme.io\"\n\tAPIVersion = \"v1beta1\"\n)\n\ntype Controller struct{}\n")
	writeGo(t, filepath.Join(dir, "internal", "operators", "fleet", "gadget_controller.go"),
		"package fleet\n\ntype GadgetController struct{}\n")
	writeGo(t, filepath.Join(dir, "internal", "operators", "fleet", "gadget_controller_test.go"),
		"package fleet\n")

	// The primary binary plus one `scaffold binary` secondary.
	writeGo(t, filepath.Join(dir, "cmd", "demo", "main.go"), "package main\n\nfunc main() {}\n")
	writeGo(t, filepath.Join(dir, "cmd", "proxy", "main.go"), "package main\n\nfunc main() {}\n")

	comps := DiscoverProjectComponents(dir, "demo")

	mailer, ok := componentByName(comps, "mailer")
	if !ok || mailer.EffectiveKind() != config.ComponentKindWorker {
		t.Errorf("mailer = %+v (found=%v), want kind=worker", mailer, ok)
	}
	nightly, ok := componentByName(comps, "nightly")
	if !ok || nightly.EffectiveKind() != config.ComponentKindCron {
		t.Errorf("nightly = %+v (found=%v), want kind=cron", nightly, ok)
	}

	fleet, ok := componentByName(comps, "fleet")
	if !ok {
		t.Fatalf("operator fleet not discovered: %+v", comps)
	}
	if !fleet.IsOperator() {
		t.Errorf("fleet kind = %q, want operator", fleet.EffectiveKind())
	}
	if fleet.Group != "acme.io" || fleet.Version != "v1beta1" {
		t.Errorf("fleet API coordinates = %q/%q, want acme.io/v1beta1", fleet.Group, fleet.Version)
	}
	if len(fleet.CRDs) != 1 || fleet.CRDs[0].Name != "Gadget" {
		t.Errorf("fleet CRDs = %+v, want one named Gadget", fleet.CRDs)
	}
	if fleet.CRDs[0].Group != "acme.io" {
		t.Errorf("CRD inherited group = %q, want acme.io", fleet.CRDs[0].Group)
	}

	if _, ok := componentByName(comps, "proxy"); !ok {
		t.Errorf("secondary binary proxy not discovered: %+v", comps)
	}
	if _, ok := componentByName(comps, "demo"); ok {
		t.Errorf("the primary binary must not be reported as an `scaffold binary` component: %+v", comps)
	}
}

// TestDiscoverProjectComponents_OperatorWithoutRecordedCoordinates: an
// operator scaffolded before forge recorded APIGroup/APIVersion reports empty
// coordinates, which is what leaves `scaffold crd` on its documented default
// instead of inventing a wrong group.
func TestDiscoverProjectComponents_OperatorWithoutRecordedCoordinates(t *testing.T) {
	dir := t.TempDir()
	writeGo(t, filepath.Join(dir, "internal", "operators", "legacy", "operator.go"),
		"package legacy\n\n// APIGroup is only mentioned in this comment.\ntype Controller struct{}\n")

	comps := DiscoverProjectComponents(dir, "demo")
	legacy, ok := componentByName(comps, "legacy")
	if !ok {
		t.Fatalf("operator legacy not discovered: %+v", comps)
	}
	if legacy.Group != "" || legacy.Version != "" {
		t.Errorf("coordinates = %q/%q, want empty (a comment mention is not a declaration)", legacy.Group, legacy.Version)
	}
}
