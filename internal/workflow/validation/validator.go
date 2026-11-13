// Copyright (c) 2025 Reliant Labs
// Package validation provides unified workflow validation for v2 workflows.
//
// The validation system has two phases:
//
//  1. StaticAnalysis - Run at workflow load/parse time.
//  2. ValidateInputs - Run before execution when input values are bound.
//
// All functions are stateless - pass what you need per call.
package validation

import (
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/requirements"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
)

// WorkflowLoader loads a workflow by name. Used for cross-workflow validation.
type WorkflowLoader func(workflowName string) (*reliantv1.Workflow, error)

// PresetLoader loads a preset by name and returns its params.
// Used for cross-workflow validation to simulate runtime preset merging.
type PresetLoader func(presetName string) (map[string]interface{}, error)

// ValidationOptions configures optional validation behaviors.
type ValidationOptions struct {
	// WorkflowLoader loads child workflows by reference.
	// Required for cross-workflow validation.
	WorkflowLoader WorkflowLoader

	// PresetLoader loads presets by name.
	// When provided, enables preset-aware validation that simulates runtime merging.
	PresetLoader PresetLoader

	// CanonicalWorkflowRef provides the loadable workflow identity for workflow.name
	// semantic validation (for example: "builtin://agent").
	// When empty, validation falls back to wf.name.
	CanonicalWorkflowRef string

	// RootLabel names the workflow under validation where an error traces a
	// chain of refs from it ("content-weekly.yaml → project://content-next →
	// ..."). The CLI passes the file it validates; empty means wf.name.
	RootLabel string

	// SkillResolver resolves the skill names a workflow declares against a real
	// catalog. When provided, enables the skill-reference layer that fails a
	// workflow naming a skill nothing resolves.
	SkillResolver *SkillResolver

	// ValueTypes resolves the input types integrations define
	// (`type: github.repository`). Nil means the embedded catalog's.
	ValueTypes ValueTypes

	// Authoring is set where a definition is being written or checked (save,
	// `reliant workflow validate`, the workflow tools), not run. Findings that
	// depend on this release's vocabulary rather than the definition's
	// correctness (CategoryToolTag) are errors only then: a workflow saved
	// before a tag was renamed still starts, with a warning.
	Authoring bool
}

// =============================================================================
// STATIC ANALYSIS (Load-time)
// =============================================================================

// StaticAnalysis performs all load-time validation on a workflow.
// Pass options to enable cross-workflow validation (child workflow contracts)
// and preset-aware validation.
func StaticAnalysis(wf *reliantv1.Workflow, loader WorkflowLoader) *Result {
	opts := &ValidationOptions{
		WorkflowLoader: loader,
	}
	return StaticAnalysisWithOptions(wf, opts)
}

// StaticAnalysisWithOptions performs all load-time validation on a workflow.
// Pass options to enable cross-workflow validation and preset-aware validation.
func StaticAnalysisWithOptions(wf *reliantv1.Workflow, opts *ValidationOptions) *Result {
	if opts == nil || opts.WorkflowLoader == nil || wf == nil {
		return staticAnalysisOne(wf, opts)
	}
	// Compiling a parent's contracts loads referenced workflows, but does not
	// check every child's structure. Validate each loaded definition as well.
	// Cache by ref for this pass only: cycles terminate and every check sees
	// the same definition rather than re-reading a changing project index.
	type definition struct {
		ref string
		wf  *reliantv1.Workflow
		err error
	}
	rootRef := opts.CanonicalWorkflowRef
	key := workflowref.RefKey
	queue := []*definition{{ref: rootRef, wf: wf}}
	loaded := map[string]*definition{}
	if rootRef != "" {
		loaded[key(rootRef)] = queue[0]
	}
	visited := map[*reliantv1.Workflow]bool{wf: true}
	options := *opts
	options.WorkflowLoader = func(ref string) (*reliantv1.Workflow, error) {
		if hit, ok := loaded[key(ref)]; ok {
			return hit.wf, hit.err
		}
		child, err := opts.WorkflowLoader(ref)
		entry := &definition{ref: ref, wf: child, err: err}
		loaded[key(ref)] = entry
		if child != nil && err == nil && !visited[child] {
			visited[child] = true
			queue = append(queue, entry)
		}
		return child, err
	}
	result := NewResult()
	for i := 0; i < len(queue); i++ {
		options.CanonicalWorkflowRef = queue[i].ref
		checked := staticAnalysisOne(queue[i].wf, &options)
		if i == 0 {
			result.Merge(checked)
			continue
		}
		// Admission propagates blocking child errors. Advisory findings stay
		// on the child's own validation surface: they are not findings on the
		// parent canvas (whose node IDs and edit locations differ).
		for _, issue := range checked.Errors() {
			result.Add(issue)
		}
	}
	return result
}

func staticAnalysisOne(wf *reliantv1.Workflow, opts *ValidationOptions) *Result {
	result := NewResult()

	if wf == nil {
		result.AddError(CategoryStructure, nil, "", "workflow is nil")
		return result
	}

	if opts == nil {
		opts = &ValidationOptions{}
	}

	// Layer 7 runs first and regardless of the graph: a declared trigger is
	// independent of the nodes, and one save should show every finding.
	validateTriggers(wf, opts, catalogIntegrations{}, result)
	validateConcurrency(wf.GetConcurrency(), result)
	validateAttentionTree(wf, result)
	validateSecrets(wf, result)
	validatePermissions(wf, result)
	for _, msg := range requirements.Validate(wf.GetRequires(), func(id string) bool {
		_, _, ok := catalogIntegrations{}.TriggerTypes(id)
		return ok
	}) {
		result.AddError(CategoryStructure, nil, "requires", msg)
	}

	valueTypes := opts.valueTypes()

	// Layer 1: Structural validation
	validateStructure(wf, valueTypes, result)
	if result.HasErrors() {
		return result
	}

	// Layer 2: CEL expression validation (compilation-based)
	validateCELWithTypes(wf, result, opts.WorkflowLoader, valueTypes)

	// Layer 3: Parallel write detection
	validateParallelWrites(wf, result)

	// Layer 4: Cross-workflow validation via core semantic contracts.
	// Loader is optional: compile contracts are still validated even without ref loading.
	validateCrossWorkflow(wf, opts, result)

	// Layer 5: Skill references resolve against a real catalog.
	// Resolver is optional; without one the layer is a no-op.
	validateSkillReferences(wf, opts, result)

	// Layer 6: tools_config.tools bindings name real tools and real bindable
	// parameters. Unconditional — the catalog is generated and always present,
	// so unlike the skill layer there is no optional dependency to skip on.
	validateToolsConfigBindings(wf, result)

	if !opts.Authoring {
		result.demote(CategoryToolTag)
	}
	return result
}

// =============================================================================
// INPUT VALIDATION (Pre-execution)
// =============================================================================

// ValidateInputs validates actual input values against the workflow's input schema.
// Call this right before workflow execution when input values are bound.
func ValidateInputs(wf *reliantv1.Workflow, inputs map[string]any) *Result {
	return ValidateInputsWith(wf, inputs, nil)
}

// ValidateInputsWith is ValidateInputs with the resolver for integration input
// types; nil means the embedded catalog's.
func ValidateInputsWith(wf *reliantv1.Workflow, inputs map[string]any, types ValueTypes) *Result {
	result := NewResult()

	if wf == nil {
		result.AddError(CategoryInput, nil, "", "workflow is nil")
		return result
	}

	validateBoundInputs(wf, inputs, typesOrDefault(types), result)
	return result
}

// =============================================================================
// HELPERS
// =============================================================================
