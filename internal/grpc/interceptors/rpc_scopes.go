// Copyright (c) 2025 Reliant Labs
package interceptors

import (
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// rpcScopes is the scope each rpc needs from an `rlat_` access token, for
// the rpcs an SDK caller may reach with a slice of the API instead of the
// person's whole API. It is the table forge/pkg/accesstoken documents
// ("Runs and triggers", runs_triggers.go), enforced here at the
// authentication boundary for every unary and streaming rpc.
//
// An rpc not listed needs reliant:api (procedureScope), so a narrow token
// that reaches any other rpc — including a new one nobody listed — is
// refused. reliant:api permits every scope below (fat.Set.Permits), so a
// reliant:api token keeps every rpc it had. Sessions are not access tokens
// and are not affected.
//
// TestRPCScopes_CoverRunAndTriggerServices holds this table to the protos:
// every RunService and TriggerService rpc is either listed here or named in
// reliantAPIOnly.
var rpcScopes = map[string]fat.Scope{
	reliantv1connect.RunServiceStartRunProcedure: fat.ScopeRunsStart,

	reliantv1connect.RunServiceGetRunProcedure:             fat.ScopeRunsRead,
	reliantv1connect.RunServiceListRunsProcedure:           fat.ScopeRunsRead,
	reliantv1connect.RunServiceLastRunPerWorkflowProcedure: fat.ScopeRunsRead,

	reliantv1connect.RunServiceSignalRunProcedure:    fat.ScopeRunsControl,
	reliantv1connect.RunServicePauseRunProcedure:     fat.ScopeRunsControl,
	reliantv1connect.RunServiceResumeRunProcedure:    fat.ScopeRunsControl,
	reliantv1connect.RunServiceCancelRunProcedure:    fat.ScopeRunsControl,
	reliantv1connect.RunServiceInterruptRunProcedure: fat.ScopeRunsControl,

	reliantv1connect.TriggerServiceFireTriggerProcedure: fat.ScopeTriggersFire,

	reliantv1connect.TriggerServiceCreateTriggerProcedure:      fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceUpdateTriggerProcedure:      fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceDeleteTriggerProcedure:      fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceSetTriggerEnabledProcedure:  fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceRotateWebhookTokenProcedure: fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceEnableWorkflowProcedure:     fat.ScopeTriggersManage,
	reliantv1connect.TriggerServiceDisableWorkflowProcedure:    fat.ScopeTriggersManage,
}

// procedureScope is the scope an access token needs to call procedure:
// its rpcScopes entry, or reliant:api.
func procedureScope(procedure string) fat.Scope {
	if s, ok := rpcScopes[procedure]; ok {
		return s
	}
	return fat.ScopeReliantAPI
}
