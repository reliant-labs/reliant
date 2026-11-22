// Copyright (c) 2025 Reliant Labs
package handlers

import "github.com/reliant-labs/forge/pkg/orm"

// workflowStatusDeps is the WorkflowStatusActivity's deps over a store: the
// repository's own rows, and the run records over its ORM handle when the store
// is (or wraps) a *db.Repo. A mock store brings its own MockRunRecords.
func workflowStatusDeps(repo WorkflowStatusStore) WorkflowStatusDeps {
	d := WorkflowStatusDeps{Repo: repo}
	if o, ok := repo.(interface{ ORM() *orm.Client }); ok && o.ORM() != nil {
		d.Runs = NewRunRecords(o.ORM())
	}
	return d
}
