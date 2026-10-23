package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLegacyComposition_ServiceWorkerCollisionAgreesEverywhere pins the one
// field name a service gets in a legacy-composed project, across the three
// files that spell it: compose.go declares it, mounts_services_gen.go reads
// it in Mount<Field>, and the service's cmd group calls that mount.
//
// compose.go counts every role when it resolves collisions
// (assembleBuildComponents), so a service whose handler package a worker
// shares is SvcBilling there. The inventory and the cmd group used to count
// services and packages only, so they read c.Billing and called MountBilling:
// a field and a method that do not exist, and a project that stops compiling
// the moment it adds `forge scaffold worker billing`.
func TestLegacyComposition_ServiceWorkerCollisionAgreesEverywhere(t *testing.T) {
	dir := newInjectProject(t)
	markLegacyComposition(t, dir)
	writeTestGoMod(t, dir, "example.com/proj")
	writeInfra(t, dir, "")
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing", "")
	writeFallibleComponentDeps(t, dir, "internal/workers", "billing", "billing", "")

	services := []ServiceDef{{Name: "BillingService", Package: "billing.v1", ModulePath: "example.com/proj"}}
	workers := []BootstrapWorkerData{{
		Name: "billing", Package: "billing", ImportPath: "billing", FieldName: "Billing", VarName: "billing", Fallible: true,
	}}
	gc := GenContext{ProjectDir: dir, ModulePath: "example.com/proj"}

	if err := GenerateCompose(InjectGenInput{GenContext: gc, Services: services, Workers: workers}); err != nil {
		t.Fatalf("GenerateCompose: %v", err)
	}
	compose := readInject(t, dir)
	if !strings.Contains(compose, "SvcBilling ") {
		t.Fatalf("precondition: compose.go names the colliding service SvcBilling:\n%s", compose)
	}

	if err := GenerateInventory(InventoryGenInput{GenContext: gc, Services: services, Workers: workers}); err != nil {
		t.Fatalf("GenerateInventory: %v", err)
	}
	mounts := mustReadFile(t, filepath.Join(dir, "internal", "app", "mounts_services_gen.go"))
	if !strings.Contains(mounts, "func (c *Components) MountSvcBilling(") || !strings.Contains(mounts, "c.SvcBilling.Register(") {
		t.Errorf("mounts_services_gen.go must mount the field compose.go declares (c.SvcBilling, MountSvcBilling):\n%s", mounts)
	}
	if strings.Contains(mounts, "c.Billing.") {
		t.Errorf("mounts_services_gen.go reads c.Billing, which compose.go does not declare:\n%s", mounts)
	}

	// The cmd group renders only beside a serve.go.
	mustWriteFile(t, filepath.Join(dir, "cmd", "proj", "cmd", "serve.go"), "package cmd\n")
	if err := GenerateCmdGroups(CmdServiceGroupInput{Bin: "proj", Services: []string{"BillingService"}, Workers: workers}, dir, nil); err != nil {
		t.Fatalf("GenerateCmdGroups: %v", err)
	}
	mount := mustReadFile(t, filepath.Join(dir, "cmd", "proj", "cmd", "services", "billing_mount_gen.go"))
	if !strings.Contains(mount, "(*app.Components).MountSvcBilling") {
		t.Errorf("billing_mount_gen.go must call MountSvcBilling, the mount the inventory emits:\n%s", mount)
	}
	if _, err := os.Stat(filepath.Join(dir, "cmd", "proj", "cmd", "services", "billing.go")); err != nil {
		t.Errorf("the billing subcommand was not scaffolded: %v", err)
	}
}
