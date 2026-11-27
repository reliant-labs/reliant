// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// workflowStatusDeps is the WorkflowStatusActivity's deps over a store: the
// repository's own rows, and the run records over its ORM handle when the store
// is (or wraps) a *db.Repo. A mock store brings its own MockRunRecords.
func workflowStatusDeps(repo WorkflowStatusStore) WorkflowStatusDeps {
	d := WorkflowStatusDeps{Repo: repo}
	if o, ok := repo.(interface{ ORM() *orm.Client }); ok && o.ORM() != nil {
		d.Triggers = NewTriggerReads(o.ORM())
		d.Runs = NewRunRecords(o.ORM())
		d.Workflows = NewWorkflowRuns(o.ORM())
		d.Attention = NewAttentionRecorder(o.ORM())
		d.Pauser = store.NewTriggerWritesExtStore(o.ORM())
		d.RunEvents = runevents.NewStore(o.ORM())
	}
	return d
}

// workflowRunsOf is the workflow roles over repo's ORM handle: a *db.Repo, or a
// fake embedding one.
func workflowRunsOf(repo interface{ ORM() *orm.Client }) WorkflowRuns {
	return NewWorkflowRuns(repo.ORM())
}

// approvalCreateDeps wires the approval lookup through the generated store.
func approvalCreateDeps(repo ApprovalCreateStore) ApprovalCreateDeps {
	d := ApprovalCreateDeps{Repo: repo}
	if o, ok := repo.(interface{ ORM() *orm.Client }); ok && o.ORM() != nil {
		d.Approvals = store.NewApprovalExtStore(o.ORM())
		d.RunEvents = runevents.NewStore(o.ORM())
	}
	return d
}

// workspaceRowsOf is the workspace rows over repo's ORM handle: a *db.Repo, or
// a fake embedding one. Anything else (a mock store, nil) has no database, so
// there are no rows to read.
func workspaceRowsOf(repo any) WorkspaceRows {
	if o, ok := repo.(interface{ ORM() *orm.Client }); ok && o.ORM() != nil {
		return NewWorkspaceRows(o.ORM())
	}
	return nil
}

// toolCallsOf is the tool-call roles over repo's ORM handle: a *db.Repo, or a
// fake embedding one. Anything else (a mock store, nil) has no database, so
// there are no tool-call rows to read.
func toolCallsOf(repo any) ToolCalls {
	if o, ok := repo.(interface{ ORM() *orm.Client }); ok && o.ORM() != nil {
		return NewToolCalls(o.ORM())
	}
	return nil
}
