// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
)

// procedureScopes is the scope a narrow access token must hold to call a
// procedure. It is forge's documented mapping (pkg/accesstoken/runs_triggers.go).
//
// It binds ONLY machine credentials. A browser session, the self-host shared
// key and an OAuth client token carry no principal and never consult it, and
// reliant:api permits every scope here (Set.Permits), so a token minted before
// these scopes existed behaves exactly as it did.
//
// A procedure absent from the map needs reliant:api. That is the fail-closed
// default: a narrow token reaches nothing it was not explicitly granted,
// including the trigger reads that have no narrow scope yet (GetTrigger,
// ListTriggers, ListTriggerEvents, GetLaunchEvent, ResolveTriggerSenders,
// ListReplayEvents, PreviewReplay) and DraftAutomation.
//
// The inbox slice (forge pkg/accesstoken/inbox.go) is mapped the same way:
// ListInbox (which is also the counts call, limit 0) needs inbox:read,
// Dismiss and Restore need inbox:manage, ResolveQuestion needs
// questions:answer. Approve, Deny, BatchApprove and BatchDeny need
// approvals:decide (owner decision 2026-11-06), EXPLICITLY: reliant:api does not
// satisfy them here (explicitScopeProcedures), though Set.Permits implies it
// elsewhere. The approval handler repeats the check and also refuses machine-bound
// tokens (auth.RequireApprovalDecider). CleanupStorage and ForgetStorageWorktree
// (they manage a machine) need reliant:api.
var procedureScopes = map[string]fat.Scope{
	reliantv1connect.RunServiceStartRunProcedure:           fat.ScopeRunsStart,
	reliantv1connect.RunServiceGetRunProcedure:             fat.ScopeRunsRead,
	reliantv1connect.RunServiceListRunsProcedure:           fat.ScopeRunsRead,
	reliantv1connect.RunServiceLastRunPerWorkflowProcedure: fat.ScopeRunsRead,
	reliantv1connect.RunServiceSignalRunProcedure:          fat.ScopeRunsControl,
	reliantv1connect.RunServicePauseRunProcedure:           fat.ScopeRunsControl,
	reliantv1connect.RunServiceResumeRunProcedure:          fat.ScopeRunsControl,
	reliantv1connect.RunServiceCancelRunProcedure:          fat.ScopeRunsControl,
	reliantv1connect.RunServiceInterruptRunProcedure:       fat.ScopeRunsControl,

	reliantv1connect.TriggerServiceFireTriggerProcedure: fat.ScopeTriggersFire,

	reliantv1connect.TriggerServiceCreateTriggerProcedure:      fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceUpdateTriggerProcedure:      fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceDeleteTriggerProcedure:      fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceSetTriggerEnabledProcedure:  fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceRotateWebhookTokenProcedure: fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceEnableWorkflowProcedure:     fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceDisableWorkflowProcedure:    fat.ScopeTriggersManage,

	reliantv1connect.InboxServiceListInboxProcedure:          fat.ScopeInboxRead,
	reliantv1connect.InboxServiceDismissInboxItemProcedure:   fat.ScopeInboxManage,
	reliantv1connect.InboxServiceRestoreInboxItemProcedure:   fat.ScopeInboxManage,
	reliantv1connect.QuestionServiceResolveQuestionProcedure: fat.ScopeQuestionsAnswer,

	reliantv1connect.ApprovalServiceApproveProcedure:      fat.ScopeApprovalsDecide,
	reliantv1connect.ApprovalServiceDenyProcedure:         fat.ScopeApprovalsDecide,
	reliantv1connect.ApprovalServiceBatchApproveProcedure: fat.ScopeApprovalsDecide,
	reliantv1connect.ApprovalServiceBatchDenyProcedure:    fat.ScopeApprovalsDecide,
}

// explicitScopeProcedures are the procedures whose scope must be GRANTED on the
// token: reliant:api, which Set.Permits lets stand in for the inbox scopes,
// does not satisfy them. A token that predates the scope stays refused.
var explicitScopeProcedures = map[string]bool{
	reliantv1connect.ApprovalServiceApproveProcedure:      true,
	reliantv1connect.ApprovalServiceDenyProcedure:         true,
	reliantv1connect.ApprovalServiceBatchApproveProcedure: true,
	reliantv1connect.ApprovalServiceBatchDenyProcedure:    true,
}

// ScopeForProcedure is the scope a machine credential needs for procedure:
// the narrow scope forge maps it to, else reliant:api.
func ScopeForProcedure(procedure string) fat.Scope {
	if s, ok := procedureScopes[procedure]; ok {
		return s
	}
	return fat.ScopeReliantAPI
}

// requireProcedureScope refuses a machine credential that lacks the scope its
// procedure needs: PermissionDenied with reason scope_missing and the scope
// named in the error metadata.
func requireProcedureScope(procedure string, p *fat.Principal) error {
	scope := ScopeForProcedure(procedure)
	if explicitScopeProcedures[procedure] {
		if auth.MachineBound(p) {
			return auth.MachineCredentialRefusal()
		}
		if p == nil || !p.Scopes.Has(scope) {
			return auth.ScopeMissing(scope)
		}
		return nil
	}
	return p.Require(scope)
}
