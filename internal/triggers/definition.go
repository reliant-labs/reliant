// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"errors"
	"fmt"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"github.com/reliant-labs/reliant/internal/workflow/workflowsource"
)

// DefinitionReader is a WorkflowResolver that can also say WHICH definition a
// trigger's runs resolve to now: the file, its version, the machine and git
// state it was synced from, and why it cannot run when it cannot. Every
// firing records it (TriggerEvent.Definition), and a firing whose definition
// is Broken is refused. LaunchWorkflows and CachedWorkflows over it are
// DefinitionReaders.
type DefinitionReader interface {
	TriggerDefinition(ctx context.Context, t *core.Trigger) (*core.TriggerEventDefinition, error)
}

// workflowProvenanceStore says which machine's copy of a project workflow is
// which (db.Repo.ProjectWorkflowProvenance).
type workflowProvenanceStore interface {
	ProjectWorkflowProvenance(ctx context.Context, projectID string) ([]config.WorkflowProvenance, error)
}

var (
	_ DefinitionReader = LaunchWorkflows{}
	_ DefinitionReader = (*CachedWorkflows)(nil)
)

// TriggerDefinition implements DefinitionReader.
func (w LaunchWorkflows) TriggerDefinition(ctx context.Context, t *core.Trigger) (*core.TriggerEventDefinition, error) {
	return describeDefinition(ctx, w.Repo, w, t)
}

// TriggerDefinition implements DefinitionReader, once per workflow, project
// and machine for the life of the cache (a list of fifty activations of one
// workflow describes and validates it once).
func (c *CachedWorkflows) TriggerDefinition(ctx context.Context, t *core.Trigger) (*core.TriggerEventDefinition, error) {
	if t == nil {
		return nil, nil
	}
	key := [4]string{t.UserID, t.Workflow, t.ProjectID, t.DaemonID}
	if hit, ok := c.defs[key]; ok {
		return hit.def, hit.err
	}
	var (
		def *core.TriggerEventDefinition
		err error
	)
	switch inner := c.inner.(type) {
	case LaunchWorkflows:
		def, err = describeDefinition(ctx, inner.Repo, c, t)
	case DefinitionReader:
		def, err = inner.TriggerDefinition(ctx, t)
	}
	c.defs[key] = cachedDefinition{def: def, err: err}
	return def, err
}

// BrokenDefinition is the broken definition t's firings are refused against,
// or nil when its workflow resolves and validates (or is not a file: a miss,
// a builtin). It resolves through workflows' cache first, so a healthy
// trigger costs no more than the resolution its health already makes; only a
// failing one is described. An error is a failure to read.
func BrokenDefinition(ctx context.Context, workflows *CachedWorkflows, t *core.Trigger) (*core.TriggerEventDefinition, error) {
	if t == nil || t.Workflow == "" {
		return nil, nil
	}
	if _, err := resolveTriggerWorkflow(ctx, workflows, t); err == nil {
		return nil, nil
	} else if retryableRead(err) {
		return nil, err
	}
	def, err := workflows.TriggerDefinition(ctx, t)
	if err != nil || !def.Broken() {
		return nil, err
	}
	return def, nil
}

// DefinitionOf is the definition t's firing resolves, nil when workflows
// cannot say (a resolver that is not a DefinitionReader). An error is a
// failure to READ, which a retry can fix. Every firing path records it and
// refuses a Broken one.
func DefinitionOf(ctx context.Context, workflows WorkflowResolver, t *core.Trigger) (*core.TriggerEventDefinition, error) {
	reader, ok := workflows.(DefinitionReader)
	if !ok || t == nil {
		return nil, nil
	}
	def, err := reader.TriggerDefinition(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("read the definition of trigger %s's workflow %q: %w", t.ID, t.Workflow, err)
	}
	return def, nil
}

// RefusalDetail is the recorded outcome of a firing refused because its
// definition is broken: which version of which file, from where, and why.
func RefusalDetail(def *core.TriggerEventDefinition) string {
	return fmt.Sprintf("refused: the newest definition of workflow %q, %s, cannot run, and no older version runs in its place: %s",
		def.Workflow, def.Describe(), def.Error)
}

// describeDefinition resolves t's workflow as its runs do (the automation
// precedence, on its machine) and says which definition that is, validating
// it through validate. store reads the catalogs.
func describeDefinition(ctx context.Context, store launch.WorkflowResolver, validate WorkflowResolver, t *core.Trigger) (*core.TriggerEventDefinition, error) {
	if t == nil || t.Workflow == "" {
		// The owner's default workflow, resolved by the launcher: nothing
		// the trigger names to describe.
		return nil, nil
	}
	def := &core.TriggerEventDefinition{Workflow: t.Workflow}
	scope := launch.AutomationScope(t.UserID, t.ProjectID, t.DaemonID)
	desc, err := launch.DescribeRunWorkflow(ctx, store, scope, t.Workflow)
	if err != nil {
		if retryableRead(err) {
			return nil, err
		}
		def.Error = err.Error()
		var fileErr *workflowref.FileError
		if errors.As(err, &fileErr) && fileErr.Path != "" {
			def.Source = string(workflowref.SourceProject)
			def.Path = fileErr.Path
			def.ContentHash = config.DefinitionHash(fileErr.Content)
			attributeMachine(ctx, store, t, def, false)
		}
		return def, nil
	}
	def.Source = string(desc.Source)
	def.ContentHash = config.DefinitionHash(desc.YAML)
	if desc.Source == workflowref.SourceProject {
		def.Path = workflowref.ProjectRelative(desc.Path)
		attributeMachine(ctx, store, t, def, desc.MachineCatalog)
	}
	// What resolves must also validate, children included: the same gate
	// the launch applies, so a definition that parses but cannot run is
	// refused as broken too.
	if _, err := resolveTriggerWorkflow(ctx, validate, t); err != nil {
		if retryableRead(err) {
			return nil, err
		}
		def.Error = err.Error()
	}
	return def, nil
}

// retryableRead reports a failure to read a workflow, as opposed to a
// verdict about it.
func retryableRead(err error) bool {
	var storeErr *workflowsource.StoreError
	return errors.As(err, &storeErr) || isRetryableLookup(err)
}

// attributeMachine fills in which machine's copy of def.Path was read and the
// git state it was synced at. fromMachine says the resolution read t's
// machine's own catalog; otherwise it read the union of every machine's, and
// the copy is the one whose content is the version that resolved. A machine
// that cannot be named leaves the fields empty: provenance is evidence, and a
// failure to read it never decides a firing.
func attributeMachine(ctx context.Context, store any, t *core.Trigger, def *core.TriggerEventDefinition, fromMachine bool) {
	provenance, ok := store.(workflowProvenanceStore)
	if !ok || t.ProjectID == "" {
		return
	}
	copies, err := provenance.ProjectWorkflowProvenance(ctx, t.ProjectID)
	if err != nil {
		logging.WarnContext(ctx, "Could not read which machine synced a trigger's workflow; recording the definition without it",
			"trigger_id", t.ID, "path", def.Path, "error", err)
		return
	}
	var match *config.WorkflowProvenance
	for i := range copies {
		c := &copies[i]
		if c.RelativePath != def.Path || c.ContentHash != def.ContentHash {
			continue
		}
		if c.DaemonID == t.DaemonID {
			match = c
			break
		}
		if !fromMachine && match == nil {
			match = c
		}
	}
	if match == nil {
		return
	}
	def.DaemonID, def.Machine = match.DaemonID, match.Machine
	if def.Machine == "" {
		def.Machine = match.DaemonID
	}
	if match.Repo != nil {
		def.Repo, def.Branch, def.Commit, def.Dirty = match.Repo.Repo, match.Repo.Branch, match.Repo.Commit, match.Repo.Dirty
	}
}
