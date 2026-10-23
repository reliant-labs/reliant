package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardedProject writes a project whose proto declares a guard and whose
// scaffolded edit page may or may not honour it.
//
// The scaffolded page is written by hand rather than generated because that
// is the situation the rule exists for: pages are scaffold-once, so a page
// forge emitted BEFORE the marker was added is never rewritten, and its
// stale mask is exactly what this check has to find.
func guardedProject(t *testing.T, maskPaths string) string {
	t.Helper()
	dir := t.TempDir()

	protoDir := filepath.Join(dir, "proto", "services", "invoices", "v1")
	if err := os.MkdirAll(protoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proto := `syntax = "proto3";
package billing.v1;

service InvoiceService {
  rpc RecordPayment(RecordPaymentRequest) returns (RecordPaymentResponse);
}

message RecordPaymentRequest {
  string invoice_id = 1;
  int64 amount_cents = 2; // forge:guards invoices.amount_paid_cents
}

message RecordPaymentResponse {
  string id = 1;
}
`
	if err := os.WriteFile(filepath.Join(protoDir, "invoices.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}

	pageDir := filepath.Join(dir, "frontends", "web", "src", "app", "invoices", "[id]", "edit")
	if err := os.MkdirAll(pageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := `export default function EditInvoicePage() {
  function onSubmit(values: FormValues) {
    mutation.mutate({
      invoice: { id, ...values },
      updateMask: { paths: [` + maskPaths + `] },
    });
  }
}
`
	if err := os.WriteFile(filepath.Join(pageDir, "page.tsx"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestGuardedFields_FiresWhenPageWritesGuardedColumn is the whole rule: the
// scaffolded edit page still names a guarded column in its update_mask, so
// saving the form writes that column raw and bypasses RecordPayment.
func TestGuardedFields_FiresWhenPageWritesGuardedColumn(t *testing.T) {
	dir := guardedProject(t, `"amount_cents", "amount_paid_cents"`)

	findings, err := collectGuardedFieldFindings(dir, []string{filepath.Join("frontends", "web")})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one for amount_paid_cents", findings)
	}
	f := findings[0]
	if f.Column != "amount_paid_cents" || f.Table != "invoices" {
		t.Errorf("finding targets %s.%s, want invoices.amount_paid_cents", f.Table, f.Column)
	}
	if f.GuardedBy != "RecordPayment" {
		t.Errorf("GuardedBy = %q, want RecordPayment — the message has to name the rpc the page is bypassing", f.GuardedBy)
	}
	// The report points at the PAGE, not the proto. The proto is correct;
	// the stale scaffold is the thing the author has to edit, and a
	// finding that cited the marker would send them to the wrong file.
	if !strings.HasSuffix(f.File, "page.tsx") {
		t.Errorf("File = %q, want the edit page — that is the file to fix", f.File)
	}
	if f.Line == 0 {
		t.Error("Line = 0; the finding must point at the mask line")
	}

	hint := guardedFieldFixHint(f)
	for _, want := range []string{"RecordPayment", "amount_paid_cents", "update_mask"} {
		if !strings.Contains(hint, want) {
			t.Errorf("fix hint missing %q:\n%s", want, hint)
		}
	}
}

// TestGuardedFields_SilentWhenPageHonoursTheGuard is the no-fire arm. A
// freshly generated page already omits the guarded column, and firing on it
// would flag every correct project — the reliable way to earn a blanket
// disable and stop protecting the one case that mattered.
func TestGuardedFields_SilentWhenPageHonoursTheGuard(t *testing.T) {
	dir := guardedProject(t, `"amount_cents"`)

	findings, err := collectGuardedFieldFindings(dir, []string{filepath.Join("frontends", "web")})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none — the page omits the guarded column", findings)
	}
}

// TestGuardedFields_ColumnOnAnotherTableDoesNotFire pins the resolution
// rule the design turns on. A page whose mask names `amount_cents` must not
// be flagged by a guard declared against `payments.amount_cents`: those are
// two columns that share a name, and conflating them is precisely the
// unsound inference this marker was introduced to replace.
func TestGuardedFields_ColumnOnAnotherTableDoesNotFire(t *testing.T) {
	dir := guardedProject(t, `"amount_cents"`)
	// Retarget the marker at a different table, leaving everything else.
	protoPath := filepath.Join(dir, "proto", "services", "invoices", "v1", "invoices.proto")
	data, err := os.ReadFile(protoPath)
	if err != nil {
		t.Fatal(err)
	}
	retargeted := strings.Replace(string(data),
		"forge:guards invoices.amount_paid_cents",
		"forge:guards payments.amount_cents", 1)
	if err := os.WriteFile(protoPath, []byte(retargeted), 0o644); err != nil {
		t.Fatal(err)
	}

	findings, err := collectGuardedFieldFindings(dir, []string{filepath.Join("frontends", "web")})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none — the guard names payments, the page edits invoices", findings)
	}
}

// TestGuardTableForPage_EveryEditRouteShape pins the page paths the rule
// reads a table from: the static Next.js edit route forge scaffolds now
// (`<slug>/edit/page.tsx`), the dynamic one older projects still carry
// (`<slug>/[id]/edit/page.tsx`), and Vite's `<slug>/Edit.tsx`. A shape this
// misses is a page the rule silently stops checking.
func TestGuardTableForPage_EveryEditRouteShape(t *testing.T) {
	for _, tc := range []struct {
		path  string
		table string
		ok    bool
	}{
		{"frontends/web/src/app/invoices/edit/page.tsx", "invoices", true},
		{"frontends/web/src/app/line-items/edit/page.tsx", "line_items", true},
		{"frontends/web/src/app/invoices/[id]/edit/page.tsx", "invoices", true},
		{"frontends/web/src/pages/invoices/Edit.tsx", "invoices", true},
		{"frontends/web/src/app/invoices/view/page.tsx", "", false},
		{"frontends/web/src/app/invoices/page.tsx", "", false},
	} {
		table, ok := guardTableForPage(tc.path)
		if ok != tc.ok || table != tc.table {
			t.Errorf("guardTableForPage(%q) = (%q, %v), want (%q, %v)", tc.path, table, ok, tc.table, tc.ok)
		}
	}
}

// TestGuardedFields_NoProtoTreeIsClean keeps the check quiet for the project
// shapes that have no proto at all (CLI and library projects), matching
// every sibling advisory lint.
func TestGuardedFields_NoProtoTreeIsClean(t *testing.T) {
	findings, err := collectGuardedFieldFindings(t.TempDir(), []string{"frontends/web"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v, want none for a project with no proto tree", findings)
	}
}
