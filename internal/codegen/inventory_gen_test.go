package codegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateInventory_DataOnlyRowsAndMount(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing", "\tLogger *slog.Logger")
	writeComponentDeps(t, dir, "internal/handlers", "user", "user", "\tLogger *slog.Logger")

	err := GenerateInventory(InventoryGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services: []ServiceDef{
			{Name: "BillingService", Package: "billing.v1", ModulePath: "example.com/proj"},
			{Name: "UserService", Package: "user.v1", ModulePath: "example.com/proj"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateInventory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "internal", "app", "mounts_services_gen.go"))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	out := string(data)

	// Data-only descriptor rows: Name (display) + ConnectPath + version-aware
	// BaseService/Version metadata + Kind. NO Mount closure on the row.
	for _, want := range []string{
		// Descriptor TYPE lives in forge/pkg/mountkit/inventory now.
		`"github.com/reliant-labs/forge/pkg/mountkit/inventory"`,
		`var Inventory = []mountinv.ComponentInfo{`,
		`Name:        "billing",`,
		`Name:        "user",`,
		`BaseService: "billing",`,
		`BaseService: "user",`,
		`Version:     "v1",`,
		`Kind:        "service",`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("inventory missing %q:\n%s", want, out)
		}
	}

	// TYPED MOUNTS — the run path. One typed method per service + MountAll.
	// No string->func map: single-service selection is a typed subcommand
	// naming the Mount<Svc> method directly.
	for _, want := range []string{
		`func (c *Components) MountBilling(mux *http.ServeMux`,
		`func (c *Components) MountUser(mux *http.ServeMux`,
		`func (c *Components) MountAll(mux *http.ServeMux`,
		`c.Billing.Register(`,
		`c.User.Register(`,
		`c.Billing.RegisterHTTP(`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("typed mounts missing %q:\n%s", want, out)
		}
	}

	// No string-keyed mount registry — the run path must not reconstruct a
	// name->func lookup table.
	if strings.Contains(out, "MountByName") {
		t.Fatalf("mount surface must not emit a string-keyed MountByName map:\n%s", out)
	}

	// The descriptor row must NOT carry a Mount closure anymore.
	if strings.Contains(out, `Mount: func(`) {
		t.Fatalf("ComponentInfo row must be data-only (no Mount closure):\n%s", out)
	}
}

// TestGenerateInventory_VersionMetadata: the version-aware seam records the
// proto API version as EXPLICIT metadata derived from the descriptor's proto
// package, while leaving today's v1 mount path/keying untouched. A
// higher-version package (v2beta1) flows through as Version metadata without
// any change to identity — the precondition for additive multi-version
// support. An unversioned package records an empty Version.
func TestGenerateInventory_VersionMetadata(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "shop", "shop", "\tLogger *slog.Logger")
	writeComponentDeps(t, dir, "internal/handlers", "legacy", "legacy", "\tLogger *slog.Logger")

	err := GenerateInventory(InventoryGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services: []ServiceDef{
			{Name: "ShopService", Package: "acme.shop.v2beta1", ModulePath: "example.com/proj"},
			{Name: "LegacyService", Package: "legacy", ModulePath: "example.com/proj"}, // unversioned
		},
	})
	if err != nil {
		t.Fatalf("GenerateInventory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "internal", "app", "mounts_services_gen.go"))
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	out := string(data)
	for _, want := range []string{
		`BaseService: "shop",`,
		`Version:     "v2beta1",`,
		`BaseService: "legacy",`,
		`Version:     "",`, // unversioned proto package
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("inventory missing version metadata %q:\n%s", want, out)
		}
	}
}

// TestGenerateInventory_NoServicesEmptyInventory: with no services, the
// inventory file is STILL emitted as a valid empty []mountinv.ComponentInfo. The
// generated cmd/server.go imports internal/app and references app.Inventory
// unconditionally, so the symbol must exist even on a service-less tree —
// otherwise the package would be empty and `go mod tidy` would 404 trying to
// resolve the local import remotely (the §2 regression this guards against).
func TestGenerateInventory_NoServicesEmptyInventory(t *testing.T) {
	dir := newInjectProject(t)
	if err := GenerateInventory(InventoryGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
	}); err != nil {
		t.Fatalf("GenerateInventory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "internal", "app", "mounts_services_gen.go"))
	if err != nil {
		t.Fatalf("no-services run should still emit mounts_services.go: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, `var Inventory = []mountinv.ComponentInfo{`) {
		t.Fatalf("empty inventory should still declare Inventory:\n%s", out)
	}
	// No service rows in the empty case.
	if strings.Contains(out, `Kind:        "service"`) {
		t.Fatalf("no-services inventory should have no rows:\n%s", out)
	}
}

// TestGenerateInventory_MountOptions: per-service mount options are a plain
// value the owned serve code builds — a generated struct with one field per
// service — never a method forge discovers by name. Its methods are the
// mounts (each a MountFunc as a method value), every service's handler
// options are resolved through mountkit after the shared chain, and the
// option-less (*Components).Mount<Svc> / MountAll method expressions the
// server and per-service subcommands name are unchanged.
func TestGenerateInventory_MountOptions(t *testing.T) {
	dir := newInjectProject(t)
	writeComponentDeps(t, dir, "internal/handlers", "billing", "billing", "\tLogger *slog.Logger")
	writeComponentDeps(t, dir, "internal/handlers", "user", "user", "\tLogger *slog.Logger")
	// An owned method named like the retired seam must not change the output:
	// nothing is discovered by name.
	if err := os.MkdirAll(filepath.Join(dir, "internal", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := "package app\n\nfunc (c *Components) BillingMountOptions() []int { return nil }\n"
	if err := os.WriteFile(filepath.Join(dir, "internal", "app", "decoy.go"), []byte(decoy), 0o644); err != nil {
		t.Fatal(err)
	}
	err := GenerateInventory(InventoryGenInput{
		GenContext: GenContext{ProjectDir: dir, ModulePath: "example.com/proj"},
		Services: []ServiceDef{
			{Name: "BillingService", Package: "billing.v1", ModulePath: "example.com/proj"},
			{Name: "UserService", Package: "user.v1", ModulePath: "example.com/proj"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateInventory: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "internal", "app", "mounts_services_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	if strings.Contains(out, "BillingMountOptions") {
		t.Errorf("a method named like the retired seam was discovered:\n%s", out)
	}
	for _, want := range []string{
		`"github.com/reliant-labs/forge/pkg/mountkit"`,
		"type MountOptions struct {\n\tBilling []mountkit.Option\n\tUser    []mountkit.Option\n}",
		"func (o MountOptions) MountBilling(c *Components, mux *http.ServeMux, cfg *config.Config, logger *slog.Logger, opts ...connect.HandlerOption) []string {",
		"func (o MountOptions) MountAll(c *Components, mux *http.ServeMux, cfg *config.Config, logger *slog.Logger, opts ...connect.HandlerOption) []string {",
		// The option-less method expressions stay, so the generated server
		// and per-service subcommands compile unchanged.
		"func (c *Components) MountBilling(mux *http.ServeMux, cfg *config.Config, logger *slog.Logger, opts ...connect.HandlerOption) []string {",
		"func (c *Components) MountAll(mux *http.ServeMux, cfg *config.Config, logger *slog.Logger, opts ...connect.HandlerOption) []string {",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("mounts missing %q:\n%s", want, out)
		}
	}
	billing := between(out, "func (o MountOptions) MountBilling(", "\n}\n")
	for _, want := range []string{
		"m := mountkit.Resolve(opts, fmw.HTTPStack(logger, middleware.ClaimsFromContext), o.Billing...)",
		"c.Billing.Register(mux, m.HandlerOptions...)",
		"c.Billing.RegisterHTTP(mux, m.HTTPStack)",
	} {
		if !strings.Contains(billing, want) {
			t.Errorf("MountOptions.MountBilling missing %q:\n%s", want, billing)
		}
	}
	if all := between(out, "func (o MountOptions) MountAll(", "\n}\n"); !strings.Contains(all, "o.MountBilling(c, mux, cfg, logger, opts...)") ||
		!strings.Contains(all, "o.MountUser(c, mux, cfg, logger, opts...)") {
		t.Errorf("MountOptions.MountAll must mount each service with its own options:\n%s", all)
	}
	if plain := between(out, "func (c *Components) MountBilling(", "\n}\n"); !strings.Contains(plain, "return MountOptions{}.MountBilling(c, mux, cfg, logger, opts...)") {
		t.Errorf("(*Components).MountBilling must be the zero-options mount:\n%s", plain)
	}
}

// between returns s from the first start through the next end after it.
func between(s, start, end string) string {
	i := strings.Index(s, start)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i:], end)
	if j < 0 {
		return s[i:]
	}
	return s[i : i+j+len(end)]
}
