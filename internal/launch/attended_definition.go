// Copyright (c) 2026 Reliant Labs
package launch

import (
	"context"
	"errors"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// catalogPinner is the launcher's narrow dependency on the same snapshot
// operation the runtime and firing service already use.
type catalogPinner interface {
	PinProjectCatalog(context.Context, db.CatalogPinScope) (string, error)
}

// pinAttendedDefinition runs after project authorization and machine selection.
// No request field selects either the pin or the hash, and the live catalog is
// never read to produce the hash. The pin includes the attended user's account
// workflows, so its normal user-first precedence is maintained.
func (l *Launcher) pinAttendedDefinition(ctx context.Context, chat *db.Chat, ref string) (string, *core.TriggerEventDefinition, error) {
	if parsed, err := workflowref.Parse(ref); err != nil || parsed.Kind == workflowref.Builtin {
		return "", nil, nil // the regular admission gate handles this ref
	}
	pinner, ok := l.repo.(catalogPinner)
	if !ok {
		return "", nil, &InternalError{Reason: "cannot pin the catalog for an attended workflow launch"}
	}
	scope := Scope{UserID: chat.UserID, ProjectID: chat.ProjectID, DaemonID: RunMachine(chat), ProjectFirst: AutomationPrecedence(chat)}
	pin, err := pinner.PinProjectCatalog(ctx, db.CatalogPinScope{
		ProjectID: scope.ProjectID, DaemonID: scope.DaemonID, UserID: scope.UserID,
	})
	if err != nil || pin == "" {
		return "", nil, &InternalError{Reason: "cannot pin the catalog for an attended workflow launch", Err: err}
	}
	scope.CatalogPin = pin
	desc, err := DescribeRunWorkflow(ctx, l.workflowReads(), scope, ref)
	if err != nil {
		var unreadable *workflowsource.StoreError
		if errors.As(err, &unreadable) {
			return "", nil, &InternalError{Reason: "cannot read the pinned attended workflow", Err: err}
		}
		return pin, nil, nil // regular admission reports the same pinned verdict
	}
	// Record the pin for retries of both account and project workflows. Only
	// a project definition supplies the CEL evidence used to gate its repo.
	def := &core.TriggerEventDefinition{Workflow: ref, Source: string(desc.Source), CatalogPin: pin, ContentHash: config.DefinitionHash(desc.YAML)}
	if desc.Source == workflowref.SourceProject {
		def.Path = workflowref.ProjectRelative(desc.Path)
	}
	return pin, def, nil
}
