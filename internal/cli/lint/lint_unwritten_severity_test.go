// File: internal/cli/lint/lint_unwritten_severity_test.go
//
// Severity parity and pending-stub mode for the two unwritten-column rules
// (forgeconv-computed-field-unwritten, forgeconv-read-only-field-unwritten).
//
// The defect being pinned: an unwritten forge:computed field only WARNED
// while an unwritten forge:read-only field FAILED — backwards, since
// computed is the stronger promise. And the read-only rule failed the gate
// immediately after `forge scaffold`, while the rpc that would write the
// column was still forge's own unwired stub.

package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// estimateServiceProto declares a SERVICE — pending-stub mode resolves the
// handler package through the service block in the entity's proto
// directory, exactly as `forge generate` does.
const estimateServiceProto = `syntax = "proto3";

package services.estimates.v1;

service EstimatesService {
  rpc RecalculateEstimate(RecalculateEstimateRequest) returns (RecalculateEstimateResponse);
}

// forge:entity
message Estimate {
  string id = 1;
  string title = 2;
  int64 subtotal_cents = 3; // forge:computed
  int64 total_cents = 4; // forge:read-only
}

message RecalculateEstimateRequest {
  string id = 1;
}

message RecalculateEstimateResponse {
  Estimate estimate = 1;
}
`

const estimateServiceMigration = `CREATE TABLE estimates (
    id              TEXT PRIMARY KEY,
    title           TEXT NOT NULL DEFAULT '',
    subtotal_cents  BIGINT NOT NULL DEFAULT 0,
    total_cents     BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// The rpc as `forge scaffold` leaves it: a placeholder carrying the marker.
const recalculateStub = `package estimates

// RecalculateEstimate implements the RecalculateEstimate RPC.
// FORGE_SCAFFOLD: implement business logic; remove this marker when done.
// forge:gen unwired-stub symbol=estimates.RecalculateEstimate
func (s *Service) RecalculateEstimate() error {
	return nil
}
`

// The same rpc once a person has implemented it — and, deliberately, still
// not writing either column. The marker is gone, so nothing holds the
// finding any more: it must be an error again.
const recalculateImplemented = `package estimates

// RecalculateEstimate implements the RecalculateEstimate RPC.
func (s *Service) RecalculateEstimate() error {
	return nil
}
`

// unwrittenProject writes the shared fixture: the proto, the migration, a
// handler package for the estimates service, and (optionally) the
// recalculate rpc file.
func unwrittenProject(t *testing.T, protoBody, rpcFile string) string {
	t.Helper()
	root := readOnlyProject(t, protoBody, estimateServiceMigration, "package estimates\n\ntype Service struct{}\n")
	if rpcFile != "" {
		writeRPCFile(t, root, "estimates", rpcFile)
	}
	return root
}

func writeRPCFile(t *testing.T, root, svcDir, body string) {
	t.Helper()
	dir := filepath.Join(root, "internal", "handlers", svcDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rpc_recalculate_estimate.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// unwrittenFindings runs both rules' JSON collectors.
func unwrittenFindings(t *testing.T, root string) (computed, readOnly []lintJSONFinding) {
	t.Helper()
	computed, err := collectComputedFieldsJSON(root)
	if err != nil {
		t.Fatalf("computed collect: %v", err)
	}
	readOnly, err = collectReadOnlyFieldsJSON(root, filepath.Join("db", "migrations"))
	if err != nil {
		t.Fatalf("read-only collect: %v", err)
	}
	if len(computed) != 1 || len(readOnly) != 1 {
		t.Fatalf("fixture must yield one finding per rule; got computed=%+v read-only=%+v", computed, readOnly)
	}
	return computed, readOnly
}

// TestComputedFields_GatesAtLeastAsHardAsReadOnly is the parity fix: both
// steps gate, and with no forge placeholders in the service both rules'
// findings are errors that flip the verdict.
func TestComputedFields_GatesAtLeastAsHardAsReadOnly(t *testing.T) {
	for _, name := range []string{"computed-fields lint", "read-only-fields lint"} {
		if !lintStepNamed(t, name).gates {
			t.Errorf("%s must gate: an unwritten forge:computed field is the stronger promise broken, "+
				"so it fails at least as hard as an unwritten forge:read-only one", name)
		}
	}

	root := unwrittenProject(t, estimateServiceProto, "")
	computed, readOnly := unwrittenFindings(t, root)
	for _, f := range append(computed, readOnly...) {
		if f.Severity != lintSevError {
			t.Errorf("%s is %q with no forge stubs in the service; want %q", f.Rule, f.Severity, lintSevError)
		}
		if strings.Contains(f.Message, "pending:") {
			t.Errorf("%s claims to be pending with no stub in the service: %s", f.Rule, f.Message)
		}
	}
	if !anyErrorFinding(computed) || !anyErrorFinding(readOnly) {
		t.Error("neither rule's findings would flip the JSON verdict")
	}

}

// TestUnwrittenFields_PendingStubsHoldAtWarning is pending-stub mode: right
// after scaffold, the rpc that will write the column is still forge's own
// placeholder, so both rules warn and NAME the stubs instead of failing.
func TestUnwrittenFields_PendingStubsHoldAtWarning(t *testing.T) {
	root := unwrittenProject(t, estimateServiceProto, recalculateStub)
	computed, readOnly := unwrittenFindings(t, root)
	for _, f := range append(computed, readOnly...) {
		if f.Severity != lintSevWarning {
			t.Errorf("%s is %q while forge's unwired stub is still in the service; want %q",
				f.Rule, f.Severity, lintSevWarning)
		}
		for _, want := range []string{"pending: implement RecalculateEstimate", "internal/handlers/estimates"} {
			if !strings.Contains(f.Message, want) {
				t.Errorf("%s message does not name %q:\n%s", f.Rule, want, f.Message)
			}
		}
	}
	if anyErrorFinding(computed) || anyErrorFinding(readOnly) {
		t.Error("a pending finding flipped the verdict")
	}

}

// TestUnwrittenFields_ImplementingTheStubRestoresTheError pins the other
// edge of the mode: once no stub remains, the finding is an error again.
func TestUnwrittenFields_ImplementingTheStubRestoresTheError(t *testing.T) {
	root := unwrittenProject(t, estimateServiceProto, recalculateStub)
	if computed, readOnly := unwrittenFindings(t, root); anyErrorFinding(computed) || anyErrorFinding(readOnly) {
		t.Fatal("precondition: the stubbed project should be pending, not failing")
	}

	writeRPCFile(t, root, "estimates", recalculateImplemented)
	computed, readOnly := unwrittenFindings(t, root)
	for _, f := range append(computed, readOnly...) {
		if f.Severity != lintSevError {
			t.Errorf("%s stayed %q after the last stub was implemented; want %q", f.Rule, f.Severity, lintSevError)
		}
	}
}

// TestUnwrittenFields_AnotherServicesStubsDoNotHold pins the mapping: only
// the stubs of the service whose proto declares the entity count. A stub in
// a different handler package says nothing about who writes this column.
func TestUnwrittenFields_AnotherServicesStubsDoNotHold(t *testing.T) {
	root := unwrittenProject(t, estimateServiceProto, "")
	writeRPCFile(t, root, "jobs", strings.ReplaceAll(recalculateStub, "package estimates", "package jobs"))

	computed, readOnly := unwrittenFindings(t, root)
	for _, f := range append(computed, readOnly...) {
		if f.Severity != lintSevError {
			t.Errorf("%s was held at %q by a stub in ANOTHER service's package", f.Rule, f.Severity)
		}
	}
}

// TestUnwrittenFields_NoServiceBlockIsNotPending pins determinism: a proto
// directory that declares no service has no handler package to consult, so
// nothing holds its findings — even if a same-named handler dir has stubs.
func TestUnwrittenFields_NoServiceBlockIsNotPending(t *testing.T) {
	noService := strings.Replace(estimateServiceProto,
		"service EstimatesService {\n  rpc RecalculateEstimate(RecalculateEstimateRequest) returns (RecalculateEstimateResponse);\n}\n", "", 1)
	if noService == estimateServiceProto {
		t.Fatal("fixture edit did not remove the service block")
	}
	root := unwrittenProject(t, noService, recalculateStub)

	computed, readOnly := unwrittenFindings(t, root)
	for _, f := range append(computed, readOnly...) {
		if f.Severity != lintSevError {
			t.Errorf("%s was held at %q with no service block to map it to a handler package", f.Rule, f.Severity)
		}
	}
}

// TestCollectSingleLinterJSON_ReadOnlyFieldsIsTargeted pins a dispatch gap
// found alongside: `--read-only-fields --json` was missing from the single-
// linter table and fell through to the WHOLE suite.
func TestCollectSingleLinterJSON_ReadOnlyFieldsIsTargeted(t *testing.T) {
	root := unwrittenProject(t, estimateServiceProto, "")
	report, handled, err := collectSingleLinterJSON(t.Context(), lintFlags{readOnlyFields: true}, nil, root, nil, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !handled {
		t.Fatal("--read-only-fields was not handled as a targeted lane; it would run the whole suite")
	}
	if report.OK {
		t.Error("an unpopulated read-only column reported ok=true")
	}
	for _, f := range report.Findings {
		if f.Rule != readOnlyFieldRuleID {
			t.Errorf("targeted --read-only-fields reported another lane's finding: %+v", f)
		}
	}
}
