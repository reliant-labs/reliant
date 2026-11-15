// File: internal/cli/audit/audit_gating_exit_test.go
//
// The EXIT CODE of `forge project audit`, which is the half of the
// unscoped_auth gate that was missing.
//
// The category arms correctly: declaring `forge:owner` on a column turns
// the advisory warning into a StatusError that names the affected RPCs,
// and leaves undeclared tables alone. All of that is pinned next door in
// audit_unscoped_auth_owner_test.go and none of it changes here.
//
// What did not work is that the armed error did not GATE. `forge project
// audit` printed "✗ unscoped_auth — …" and exited 0, so nothing in CI and
// no agent reading an exit status ever saw it. A dogfood run reported
// escaping this with `--strict`; that flag does not exist — cobra rejected
// the unknown flag and the 1 it observed was the parse error, not a
// verdict. So there was no way at all to make this check fail a build.
//
// The rule these tests pin:
//
//	overall status error  → exit non-zero
//	overall status warn   → exit ZERO
//
// The second line is as load-bearing as the first. A fresh scaffold is
// entirely unscoped by construction — forge emits the delegations, the
// user writes the scoping — so an unarmed unscoped_auth warning must not
// fail the build, or forge's own output fails forge's own gate on day
// one. Severity is the whole predicate: the developer's `forge:owner`
// sentence is what moves a finding from advisory to gating, and the exit
// code follows severity rather than adding a second, independent switch.

package audit

import (
	"errors"
	"testing"

	"github.com/reliant-labs/forge/internal/cli/audittype"
)

// TestAuditGate_ArmedAndFailingIsNonZero is the finding. A project that
// declared an owner column and left an authenticated RPC over that table
// unscoped must fail the command, not merely decorate the screen.
func TestAuditGate_ArmedAndFailingIsNonZero(t *testing.T) {
	src := handlerHeader + delegationBody("GetCustomer")
	dir := writeProject(t, "ShopService", map[string]bool{"GetCustomer": true}, src)
	writeOwnerMigration(t, dir, "customers", "company_id")

	cat := auditUnscopedAuth(nil, dir)
	if cat.Status != audittype.StatusError {
		t.Fatalf("precondition: category status = %q, want error (the arming rule is pinned in "+
			"audit_unscoped_auth_owner_test.go; this test is about the EXIT CODE)", cat.Status)
	}

	err := gateOnReport(&Report{
		Categories:    map[string]audittype.Category{"unscoped_auth": cat},
		OverallStatus: audittype.StatusError,
	})
	if err == nil {
		t.Fatal("audit returned nil for a report whose overall status is error.\n" +
			"`forge project audit` then exits 0 with ✗ unscoped_auth on screen — a security-shaped " +
			"check that prints a failure and reports success is the failure mode being fixed.")
	}
	var gate *gateError
	if !errors.As(err, &gate) {
		t.Fatalf("error is %T, want *gateError so the CLI can report it without a stack trace: %v", err, err)
	}
	if len(gate.categories) != 1 || gate.categories[0] != "unscoped_auth" {
		t.Errorf("gate names %v, want [unscoped_auth] — the message must say WHICH category failed, "+
			"or the exit code is as unfalsifiable as the warning it replaces", gate.categories)
	}
}

// TestAuditGate_UnarmedWarningStaysZero is the invariant that keeps the
// gate opt-in. This is the state EVERY greenfield project starts in: the
// finding is real and reported, no table declares an owner, and the build
// must stay green.
func TestAuditGate_UnarmedWarningStaysZero(t *testing.T) {
	src := handlerHeader + delegationBody("GetOrder")
	dir := writeProject(t, "ShopService", map[string]bool{"GetOrder": true}, src)

	cat := auditUnscopedAuth(nil, dir)
	if cat.Status != audittype.StatusWarn {
		t.Fatalf("precondition: category status = %q, want warn — no table declares an owner", cat.Status)
	}
	if names := unscopedMethods(t, cat); len(names) != 1 {
		t.Fatalf("precondition: want the finding still REPORTED, got %v", names)
	}

	if err := gateOnReport(&Report{
		Categories:    map[string]audittype.Category{"unscoped_auth": cat},
		OverallStatus: audittype.StatusWarn,
	}); err != nil {
		t.Fatalf("audit failed on a warn-only report: %v\n"+
			"A fresh scaffold is unscoped by construction, so gating this would make forge's own "+
			"output fail forge's own gate on day one.", err)
	}
}

// TestAuditGate_OKIsZero is the trivial third state, pinned so a future
// change cannot make "nothing to report" fail.
func TestAuditGate_OKIsZero(t *testing.T) {
	if err := gateOnReport(&Report{
		Categories:    map[string]audittype.Category{"unscoped_auth": {Status: audittype.StatusOK}},
		OverallStatus: audittype.StatusOK,
	}); err != nil {
		t.Fatalf("a clean report must exit 0, got %v", err)
	}
}

// TestAuditGate_NamesEveryFailingCategory pins that the gate reports the
// full set. `unscoped_auth` is the category that motivated this, but the
// exit code is a property of the REPORT — a reader who fixes the one
// category named and re-runs must not discover a second failure they were
// never told about.
func TestAuditGate_NamesEveryFailingCategory(t *testing.T) {
	err := gateOnReport(&Report{
		Categories: map[string]audittype.Category{
			"unscoped_auth": {Status: audittype.StatusError, Summary: "2 RPCs over owned data"},
			"version":       {Status: audittype.StatusError, Summary: "no forge.yaml found"},
			"deps":          {Status: audittype.StatusWarn, Summary: "go.sum older than go.mod"},
		},
		OverallStatus: audittype.StatusError,
	})
	var gate *gateError
	if !errors.As(err, &gate) {
		t.Fatalf("want *gateError, got %T (%v)", err, err)
	}
	// Sorted, so the message does not churn on map iteration order.
	if len(gate.categories) != 2 || gate.categories[0] != "unscoped_auth" || gate.categories[1] != "version" {
		t.Errorf("categories = %v, want [unscoped_auth version] sorted — every failing category, "+
			"and no warn-level one", gate.categories)
	}
}
