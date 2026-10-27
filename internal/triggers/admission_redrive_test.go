package triggers

import (
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestAdmissionMemoFencesUnboundHalfStart(t *testing.T) {
	a := core.TriggerAdmission{EventID: "generation", ChatID: "root"}
	payload, err := converter.GetDefaultDataConverter().ToPayload(a.EventID)
	if err != nil {
		t.Fatal(err)
	}
	desc := &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: "root", RunId: "original"}, Memo: &commonpb.Memo{Fields: map[string]*commonpb.Payload{"reliant_admission_event_id": payload}}}}
	if id, ok := admissionMemoRun(desc, a); !ok || id != "original" {
		t.Fatal("accepted-before-callback crash not recoverable")
	}
	a.EventID = "new-generation"
	if _, ok := admissionMemoRun(desc, a); ok {
		t.Fatal("old memo attached to new generation")
	}
	a.EventID = "generation"
	a.ChatID = "another-root"
	if _, ok := admissionMemoRun(desc, a); ok {
		t.Fatal("different root attached")
	}
	a.ChatID = "root"
	desc.WorkflowExecutionInfo.Memo = nil
	if _, ok := admissionMemoRun(desc, a); ok {
		t.Fatal("followup without memo attached")
	}
}

func TestAdmissionOnlyConfirmedTerminalReleases(t *testing.T) {
	for _, s := range []enumspb.WorkflowExecutionStatus{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED, enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW} {
		if admissionTerminal(s) {
			t.Fatalf("nonterminal or continuing execution released: %s", s)
		}
	}
	if !admissionTerminal(enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED) {
		t.Fatal("closed execution not released")
	}
	// Even after DB completion activity, Temporal RUNNING remains nonterminal:
	// pauses, waits and cancellation requests do not alter this authority.
}

func TestAdmissionScopeIsOwnerProjectWorkflowNotActivation(t *testing.T) {
	if admissionScope("u", "p", "Code Review") != admissionScope("u", "p", "code-review") {
		t.Fatal("canonical refs disagree")
	}
	if admissionScope("u", "p", "wf") == admissionScope("v", "p", "wf") || admissionScope("u", "p", "wf") == admissionScope("u", "q", "wf") {
		t.Fatal("scope leaked across owner/project")
	}
}
