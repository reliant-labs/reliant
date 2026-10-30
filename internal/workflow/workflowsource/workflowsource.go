// Copyright (c) 2025 Reliant Labs

// Package workflowsource feeds workflowref the app's workflows: the caller's
// own, from the workflow catalog, and the project's, from the
// .reliant/workflows the daemon synced into the project config record.
//
// It is the app half of the one resolution rule. Run start (launch), the
// runtime's LoadWorkflow activity, the scenario RPCs and the scenario agent
// tools all resolve a ref through Resolve or Loader, and the CLI resolves
// through workflowref.Resolve against the same index built from disk, so a ref
// cannot resolve one way in a scenario test and another in a run.
//
//forge:exclude-contract: free functions over Store, a narrow interface declared here at its consumer; StoreError's methods are the error interface
package workflowsource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// ProjectStore reads a project's synced workflows.
//
// It is the one column, not the whole config record: the record also carries
// every skill body the daemon indexed, which ran to 18 MB in prod and made each
// resolution on the send path cost hundreds of milliseconds.
type ProjectStore interface {
	GetProjectWorkflowsJSON(ctx context.Context, projectID string) (*string, error)
}

// MachineStore reads one machine's synced catalog of a project: what a run on
// that machine executes against. synced is false when the machine has never
// synced the project, which is not the same as having synced nothing.
type MachineStore interface {
	GetProjectMachineWorkflowsJSON(ctx context.Context, projectID, daemonID string) (workflows *string, synced bool, err error)
	GetProjectMachinePresetsJSON(ctx context.Context, projectID, daemonID string) (presets *string, synced bool, err error)
}

// Store is every repository read resolution makes.
type Store interface {
	ProjectStore
	GetUsableWorkflowBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
	GetWorkflowDraftBySlug(ctx context.Context, userID, slug string) (*db.WorkflowDraft, error)
}

// StoreError is a failure to READ a workflow, as opposed to one that is
// absent or broken. The first is retryable; a verdict is not.
type StoreError struct{ Err error }

func (e *StoreError) Error() string { return e.Err.Error() }
func (e *StoreError) Unwrap() error { return e.Err }

// Options says whose workflows, in which project, a ref resolves against.
type Options struct {
	// UserID owns the run; their own workflows shadow the project's. Empty
	// resolves against the project alone.
	UserID string
	// ProjectID is the project whose synced workflows are consulted. Empty
	// means none.
	ProjectID string
	// DaemonID is the machine the run executes on. When set, the project's
	// workflows are that machine's own catalog rather than the union of every
	// machine's (see config.MachineCatalog): a run executes on its machine,
	// so a workflow only another machine provides is a miss that says so. A
	// machine that has never synced the project falls back to the union, so
	// a suspended machine that predates per-machine catalogs still wakes for
	// its run and syncs.
	DaemonID string
	// DraftRoot names the one workflow that may resolve to an unfinished
	// draft: the root of a builder test run, which runs what the builder
	// just saved. Every other ref, including the root's children, needs a
	// complete workflow.
	DraftRoot string
	// DraftRootOptional lets DraftRoot fall through to the project's workflow
	// of that name when the user has no workflow of it at all: a read of a
	// workflow's input schema for whatever is about to start, as opposed to
	// a builder test run, which must run the draft or nothing.
	DraftRootOptional bool
}

// ProjectIndex indexes the workflows synced for a project: nil when there is
// no project, or nothing has been synced for it.
func ProjectIndex(ctx context.Context, store ProjectStore, projectID string) (*workflowref.Index, error) {
	if projectID == "" || store == nil {
		return nil, nil
	}
	workflowsJSON, err := store.GetProjectWorkflowsJSON(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, &StoreError{Err: fmt.Errorf("read project workflows: %w", err)}
	}
	workflows, err := config.ParseStoredWorkflows(workflowsJSON)
	if err != nil {
		return nil, err
	}
	return config.ProjectWorkflowIndex(workflows), nil
}

// MachineProjectIndex is ProjectIndex for the catalog one machine synced, or
// the project's union when daemonID is empty or that machine has never synced
// the project. A store that cannot read a machine's catalog is refused, not
// silently answered with the union: the caller asked what will run THERE.
func MachineProjectIndex(ctx context.Context, store ProjectStore, projectID, daemonID string) (*workflowref.Index, error) {
	if daemonID == "" || projectID == "" || store == nil {
		return ProjectIndex(ctx, store, projectID)
	}
	machines, ok := store.(MachineStore)
	if !ok {
		return nil, &StoreError{Err: fmt.Errorf("this store cannot read machine %s's workflows", daemonID)}
	}
	workflowsJSON, synced, err := machines.GetProjectMachineWorkflowsJSON(ctx, projectID, daemonID)
	if err != nil {
		return nil, &StoreError{Err: fmt.Errorf("read machine %s's project workflows: %w", daemonID, err)}
	}
	if !synced {
		warnUnsyncedMachine(ctx, projectID, daemonID, "workflows")
		return ProjectIndex(ctx, store, projectID)
	}
	workflows, err := config.ParseStoredWorkflows(workflowsJSON)
	if err != nil {
		return nil, err
	}
	return config.ProjectWorkflowIndex(workflows), nil
}

// warnUnsyncedMachine says that a read for one machine is answered with the
// union of every machine's catalog, because that machine has never synced the
// project: what runs is then whatever machine pushed last, not what the run's
// machine holds, and that is worth a line in the log every time it happens.
func warnUnsyncedMachine(ctx context.Context, projectID, daemonID, what string) {
	logging.WarnContext(ctx, "[workflowsource] machine has never synced this project; resolving against every machine's catalog instead",
		"project_id", projectID, "daemon_id", daemonID, "catalog", what)
}

// ProjectPresets reads a project's synced presets: one machine's when
// daemonID is set and that machine has synced the project (MachineProjectIndex's
// rule), otherwise the union of every machine's.
func ProjectPresets(ctx context.Context, store interface {
	GetProjectPresetsJSON(ctx context.Context, projectID string) (*string, error)
}, projectID, daemonID string) ([]config.StoredPreset, error) {
	if projectID == "" || store == nil {
		return nil, nil
	}
	if daemonID != "" {
		machines, ok := store.(MachineStore)
		if !ok {
			return nil, &StoreError{Err: fmt.Errorf("this store cannot read machine %s's presets", daemonID)}
		}
		presetsJSON, synced, err := machines.GetProjectMachinePresetsJSON(ctx, projectID, daemonID)
		if err != nil {
			return nil, &StoreError{Err: fmt.Errorf("read machine %s's project presets: %w", daemonID, err)}
		}
		if synced {
			return config.ParseStoredPresets(presetsJSON)
		}
		warnUnsyncedMachine(ctx, projectID, daemonID, "presets")
	}
	presetsJSON, err := store.GetProjectPresetsJSON(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, &StoreError{Err: fmt.Errorf("read project presets: %w", err)}
	}
	return config.ParseStoredPresets(presetsJSON)
}

// Sources assembles what a ref resolves against.
func Sources(ctx context.Context, store Store, opts Options) (workflowref.Sources, error) {
	project, err := MachineProjectIndex(ctx, store, opts.ProjectID, opts.DaemonID)
	if err != nil {
		return workflowref.Sources{}, err
	}
	sources := workflowref.Sources{Project: project}
	if opts.UserID != "" && store != nil {
		sources.User = userWorkflows(ctx, store, opts)
	}
	return sources, nil
}

// userWorkflows looks a slug up among the caller's own workflows: only a
// complete one resolves, except the draft root. A draft that is not complete
// is a *db.WorkflowDraftNotRunnableError — final, so it is reported rather
// than a project workflow of the same name silently running in its place.
func userWorkflows(ctx context.Context, store Store, opts Options) func(slug string) ([]byte, error) {
	draftRoot := workflowref.ProjectSlug(opts.DraftRoot)
	return func(slug string) ([]byte, error) {
		if draftRoot != "" && slug == draftRoot {
			draft, err := store.GetWorkflowDraftBySlug(ctx, opts.UserID, slug)
			if err != nil {
				return nil, &StoreError{Err: fmt.Errorf("look up workflow %q: %w", slug, err)}
			}
			if draft == nil || draft.IsHidden {
				if opts.DraftRootOptional {
					return nil, nil
				}
				return nil, fmt.Errorf("%w: you have no workflow named %q to test", workflowref.ErrNotFound, slug)
			}
			return []byte(draft.Definition), nil
		}
		draft, err := store.GetUsableWorkflowBySlug(ctx, opts.UserID, slug)
		if err != nil {
			var notRunnable *db.WorkflowDraftNotRunnableError
			if errors.As(err, &notRunnable) {
				return nil, err
			}
			return nil, &StoreError{Err: fmt.Errorf("look up workflow %q: %w", slug, err)}
		}
		if draft == nil {
			return nil, nil
		}
		return []byte(draft.Definition), nil
	}
}

// Resolve resolves one ref.
//
// A builtin:// ref is answered from the embedded catalog without reading the
// store: no user or project workflow can shadow it, so reading them only adds
// round trips to every run start and send of the default workflow.
func Resolve(ctx context.Context, store Store, opts Options, ref string) (*workflowref.Resolved, error) {
	if parsed, err := workflowref.Parse(ref); err != nil || parsed.Kind == workflowref.Builtin {
		return workflowref.Resolve(ref, workflowref.Sources{})
	}
	sources, err := Sources(ctx, store, opts)
	if err != nil {
		return nil, err
	}
	resolved, resolveErr := workflowref.Resolve(ref, sources)
	if errors.Is(resolveErr, workflowref.ErrNotFound) {
		// Optional diagnostic read only on a miss: no full config payload or
		// liveness lookup on the successful run-start path.
		if diagnostics, ok := store.(SourceDiagnostics); ok {
			if hint, err := diagnostics.WorkflowSourceDiagnostic(ctx, opts.ProjectID, opts.DaemonID, ref); err == nil && hint != "" {
				resolveErr = &diagnosedMiss{hint: hint, err: resolveErr}
			}
		}
	}
	return resolved, resolveErr
}

// SourceDiagnostics says where a project workflow that missed is provided:
// on which other machine, at which path, or where it was last synced from.
// It is read only on a miss, never on the run-start path.
type SourceDiagnostics interface {
	WorkflowSourceDiagnostic(ctx context.Context, projectID, daemonID, ref string) (string, error)
}

// diagnosedMiss is a miss with the diagnostic that explains it: the index's
// reason first (it may name a near miss), then where the workflow is
// provided ("machine "cicd" has no workflow "release"; it is defined on
// machine "default" at control-plane/.reliant/workflows/release.yaml").
type diagnosedMiss struct {
	hint string
	err  error
}

func (e *diagnosedMiss) Error() string { return e.err.Error() + "; " + e.hint }
func (e *diagnosedMiss) Unwrap() error { return e.err }

// DraftLoader resolves refs while validating one of the user's own workflows
// outside any project (the workflow builder, the workflow agent tools). The
// workflow resolves to itself, so a draft that spawns itself validates; a
// builtin:// or user ref resolves by the rule; and a project ref that names
// none of the user's workflows is left unresolved (nil, nil) rather than
// failed — it may name a project workflow, which run start resolves and
// validates in the project the run belongs to.
func DraftLoader(ctx context.Context, store Store, userID string, self *reliantv1.Workflow) func(ref string) (*reliantv1.Workflow, error) {
	load := Loader(ctx, store, Options{UserID: userID})
	selfKey := ""
	if self != nil && self.GetName() != "" {
		selfKey = workflowref.RefKey(self.GetName())
	}
	return func(ref string) (*reliantv1.Workflow, error) {
		if selfKey != "" && workflowref.RefKey(ref) == selfKey {
			return self, nil
		}
		wf, err := load(ref)
		if err != nil {
			parsed, parseErr := workflowref.Parse(ref)
			if parseErr == nil && parsed.Kind == workflowref.Project && errors.Is(err, workflowref.ErrNotFound) {
				return nil, nil
			}
			return nil, err
		}
		return wf, nil
	}
}

// Loader is a ref → workflow function for validation and the scenario
// runner. The project's workflows are read once, when it is built.
func Loader(ctx context.Context, store Store, opts Options) func(ref string) (*reliantv1.Workflow, error) {
	sources, sourcesErr := Sources(ctx, store, opts)
	return func(ref string) (*reliantv1.Workflow, error) {
		if sourcesErr != nil {
			return nil, sourcesErr
		}
		resolved, err := workflowref.Resolve(ref, sources)
		if err != nil {
			return nil, err
		}
		return resolved.Workflow, nil
	}
}
