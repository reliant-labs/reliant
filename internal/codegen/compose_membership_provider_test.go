package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// An internal/app provider that returns a contract package's own type
// supplies that package: the package is not also made a node, which would give
// the type two sources (forge bug 811f1d28).
func TestProviderSuppliedComponentsProviderWins(t *testing.T) {
	const module = "example.com/x"
	comps := []BuildComponent{
		{FieldName: "OrgMove", Alias: "orgmove", ImportPath: "internal/orgmove", compRoleRoot: "internal"},
		{FieldName: "Billing", Alias: "billing", ImportPath: "internal/billing", compRoleRoot: "internal", compFieldType: "*billing.Service"},
		{FieldName: "Audit", Alias: "audit", ImportPath: "internal/audit", compRoleRoot: "internal"},
		{FieldName: "Tools", Alias: "tools", ImportPath: "internal/handlers/tools", compRoleRoot: "internal/handlers"},
	}
	providers := []graphProvider{
		{Name: "OrgMove", Result: "orgmove.Service", Optional: true, Imports: map[string]string{"orgmove": module + "/internal/orgmove"}},
		{Name: "Billing", Result: "*bill.Service", Imports: map[string]string{"bill": module + "/internal/billing"}},
		// A wrapper over audit is another type: audit stays a node.
		{Name: "AuditView", Result: "auditview.View", Imports: map[string]string{"auditview": module + "/internal/audit"}},
		{Name: "Tools", Result: "tools.Service", Imports: map[string]string{"tools": module + "/internal/handlers/tools"}},
	}
	kept, supplied := providerSuppliedComponents("", module, comps, providers)
	var names []string
	for _, c := range kept {
		names = append(names, c.FieldName)
	}
	if len(names) != 2 || names[0] != "Audit" || names[1] != "Tools" {
		t.Fatalf("kept = %v, want [Audit Tools]", names)
	}
	if supplied["internal/orgmove"] != "OrgMove" || supplied["internal/billing"] != "Billing" || len(supplied) != 2 {
		t.Fatalf("suppliedBy = %v", supplied)
	}
	if kept, supplied := providerSuppliedComponents("", module, comps, nil); len(kept) != len(comps) || supplied != nil {
		t.Fatalf("no providers changes nothing, got %d kept, %v", len(kept), supplied)
	}
}

// reliant's changetools: the package's Service only embeds the consumer's
// interface, and the internal/app provider returns that interface. The
// provider still supplies the package (it is the same type), so the package
// is neither a second node nor reported unreachable.
func TestProviderSuppliedComponentsEmbeddedConsumerInterface(t *testing.T) {
	const module = "example.com/x"
	dir := t.TempDir()
	pkg := filepath.Join(dir, "internal", "changetools")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "package changetools\n\nimport \"example.com/x/internal/llm/tools\"\n\ntype Service interface {\n\ttools.ChangeController\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	comps := []BuildComponent{
		{FieldName: "Changetools", Alias: "changetools", ImportPath: "internal/changetools", compRoleRoot: "internal"},
	}
	providers := []graphProvider{
		{Name: "ChangeController", Result: "tools.ChangeController", Optional: true, Imports: map[string]string{"tools": module + "/internal/llm/tools"}},
	}
	kept, supplied := providerSuppliedComponents(dir, module, comps, providers)
	if len(kept) != 0 || supplied["internal/changetools"] != "ChangeController" {
		t.Fatalf("kept = %v, supplied = %v; want changetools supplied by ChangeController", kept, supplied)
	}
	// A Service that adds a method of its own is a different type.
	src = "package changetools\n\nimport \"example.com/x/internal/llm/tools\"\n\ntype Service interface {\n\ttools.ChangeController\n\tExtra()\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "contract.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if kept, _ := providerSuppliedComponents(dir, module, comps, providers); len(kept) != 1 {
		t.Fatalf("a Service with its own method must stay a node, kept = %v", kept)
	}
}
