// Copyright (c) 2025 Reliant Labs
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
//
//forge:exclude-contract: workflow compile semantics and decision engine; pure
package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

// InvocationMode indicates whether a sub-workflow comes from inline or ref.
type InvocationMode string

const (
	InvocationModeInline InvocationMode = "inline"
	InvocationModeRef    InvocationMode = "ref"
)

// InputPolicy is the normalized sub-workflow input assembly policy.
type InputPolicy string

const (
	InputPolicyInlineInheritParentInputs InputPolicy = "inline_inherit_parent_inputs"
	InputPolicyRefPresetsArgsDefaults    InputPolicy = "ref_presets_args_defaults"
)

// LoadStrategy is the normalized sub-workflow load strategy.
type LoadStrategy string

const (
	LoadStrategyInlineEmbedded    LoadStrategy = "inline_embedded"
	LoadStrategyLoadByWorkflowRef LoadStrategy = "load_by_workflow_ref"
)

// InputAssemblyStage describes the ordered input assembly stages.
type InputAssemblyStage string

const (
	InputAssemblyStageInheritParentInputs InputAssemblyStage = "inherit_parent_inputs"
	InputAssemblyStagePresets             InputAssemblyStage = "presets"
	InputAssemblyStagePassthrough         InputAssemblyStage = "passthrough"
	InputAssemblyStageArgs                InputAssemblyStage = "args"
	InputAssemblyStageDefaults            InputAssemblyStage = "defaults"
)

// SubWorkflowContract is the canonical compile-time contract for each workflow/loop sub-workflow invocation.
type SubWorkflowContract struct {
	NodePath          string
	NodeID            string
	NodeType          string
	InvocationMode    InvocationMode
	WorkflowIdentity  string
	ParentWorkflowRef string
	WorkflowRef       string
	InputPolicy       InputPolicy
	LoadStrategy      LoadStrategy
	InputAssembly     []InputAssemblyStage
	Presets           map[string]string
	Args              map[string]any
	Passthrough       []string
	DefaultInputs     map[string]any
}

// CompiledSemantics is the normalized semantic contract output used across runtime and validation.
type CompiledSemantics struct {
	CanonicalWorkflowRef string
	SubWorkflows         map[string]SubWorkflowContract
	NodeOrder            []string
}

// Compile builds a Program with normalized semantic contracts.
func Compile(workflow *reliantv1.Workflow, options CompileOptions) (*Program, error) {
	if workflow == nil {
		return nil, fmt.Errorf("workflow is required")
	}

	canonicalRef := strings.TrimSpace(options.CanonicalWorkflowRef)
	if canonicalRef == "" {
		canonicalRef = strings.TrimSpace(workflow.GetName())
	}
	if canonicalRef == "" {
		return nil, fmt.Errorf("canonical workflow ref is required")
	}

	semantics := &CompiledSemantics{
		CanonicalWorkflowRef: canonicalRef,
		SubWorkflows:         make(map[string]SubWorkflowContract),
		NodeOrder:            make([]string, 0),
	}

	root := refTrail{expanding: []string{workflowref.RefKey(canonicalRef)}}
	if err := compileWorkflowSemantics(workflow, canonicalRef, "", options, semantics, root); err != nil {
		return nil, err
	}

	return &Program{Workflow: workflow, Semantics: semantics}, nil
}

// ErrRefCycle is the cause of a RefError whose last ref names a workflow that
// is already being expanded on the same chain.
var ErrRefCycle = errors.New("reference cycle: a workflow cannot contain itself")

// RefError is a workflow ref that could not be followed, with every ref that
// led to it from the workflow being compiled. Compile is the walker that
// follows refs transitively for every validator (the CLI's `workflow
// validate` and the app's run-start and builder checks alike), so this is how
// a broken ref two levels down is reported with the path that reaches it.
type RefError struct {
	// Chain is each ref followed from the root, as written; the last is the
	// one that failed.
	Chain []string
	// Node is the qualified path ("to_b/to_c") of the node holding the
	// failing ref, through every workflow on the chain.
	Node string
	Err  error
}

func (e *RefError) Error() string {
	return strings.Join(e.Chain, " → ") + ": " + e.Err.Error()
}

func (e *RefError) Unwrap() error { return e.Err }

// refTrail is where compilation is in the ref graph: the refs followed from
// the root, and the identity of every workflow being expanded along them
// (the root's included). An ancestor stack, not a visited set: two siblings
// may ref the same workflow; only a workflow inside itself is a cycle.
type refTrail struct {
	chain     []string
	expanding []string
}

func (t refTrail) enter(ref string) refTrail {
	return refTrail{
		chain:     append(slices.Clone(t.chain), ref),
		expanding: append(slices.Clone(t.expanding), workflowref.RefKey(ref)),
	}
}

func compileWorkflowSemantics(
	workflow *reliantv1.Workflow,
	canonicalWorkflowRef string,
	pathPrefix string,
	options CompileOptions,
	semantics *CompiledSemantics,
	trail refTrail,
) error {
	for _, node := range workflow.GetNodes() {
		if node == nil {
			continue
		}

		nodeID := node.GetId()
		nodePath := nodeID
		if pathPrefix != "" {
			nodePath = pathPrefix + "/" + nodeID
		}

		subWorkflow, mode, ok := subWorkflowFromNode(node)
		if !ok {
			continue
		}

		contract, childWorkflow, childCanonicalRef, childTrail, err := buildSubWorkflowContract(
			node,
			nodePath,
			subWorkflow,
			mode,
			canonicalWorkflowRef,
			options,
			trail,
		)
		if err != nil {
			var refErr *RefError
			if len(trail.chain) > 0 && !errors.As(err, &refErr) {
				// A defect inside a referenced workflow: say which chain of
				// refs reached it.
				return &RefError{Chain: trail.chain, Node: nodePath, Err: err}
			}
			return err
		}

		semantics.SubWorkflows[nodePath] = contract
		semantics.NodeOrder = append(semantics.NodeOrder, nodePath)

		if childWorkflow != nil {
			if err := compileWorkflowSemantics(childWorkflow, childCanonicalRef, nodePath, options, semantics, childTrail); err != nil {
				return err
			}
		}
	}

	return nil
}

// buildSubWorkflowContract builds a node's contract and returns the child
// workflow to expand next (an inline body, or a loaded ref) with the trail it
// is expanded under.
func buildSubWorkflowContract(
	node *reliantv1.Node,
	nodePath string,
	subWorkflow subWorkflowArgs,
	mode InvocationMode,
	parentCanonicalRef string,
	options CompileOptions,
	trail refTrail,
) (SubWorkflowContract, *reliantv1.Workflow, string, refTrail, error) {
	contract := SubWorkflowContract{
		NodePath:          nodePath,
		NodeID:            node.GetId(),
		NodeType:          node.GetType(),
		InvocationMode:    mode,
		ParentWorkflowRef: parentCanonicalRef,
	}

	if mode == InvocationModeInline {
		contract.WorkflowIdentity = parentCanonicalRef
		contract.InputPolicy = InputPolicyInlineInheritParentInputs
		contract.LoadStrategy = LoadStrategyInlineEmbedded
		contract.InputAssembly = []InputAssemblyStage{InputAssemblyStageInheritParentInputs}
		// An inline body is part of the workflow it sits in: no new hop.
		return contract, subWorkflow.Inline, contract.WorkflowIdentity, trail, nil
	}

	ref := strings.TrimSpace(subWorkflow.Ref)
	if ref == "" {
		return SubWorkflowContract{}, nil, "", trail, fmt.Errorf("node %q has empty workflow ref", node.GetId())
	}
	if strings.Contains(ref, "::") {
		return SubWorkflowContract{}, nil, "", trail, fmt.Errorf("node %q has non-canonical workflow ref %q (contains ::)", node.GetId(), ref)
	}

	contract.WorkflowRef = ref
	contract.WorkflowIdentity = ref
	contract.InputPolicy = InputPolicyRefPresetsArgsDefaults
	contract.LoadStrategy = LoadStrategyLoadByWorkflowRef
	contract.InputAssembly = []InputAssemblyStage{
		InputAssemblyStagePresets,
		InputAssemblyStagePassthrough,
		InputAssemblyStageArgs,
		InputAssemblyStageDefaults,
	}
	contract.Presets = copyStringMap(subWorkflow.Presets)
	contract.Args = convertStructpbArgs(subWorkflow.Args)
	contract.Passthrough = subWorkflow.Passthrough

	if options.WorkflowLoader != nil && !isTemplateWorkflowRef(ref) {
		childTrail := trail.enter(ref)
		if slices.Contains(trail.expanding, childTrail.expanding[len(childTrail.expanding)-1]) {
			return SubWorkflowContract{}, nil, "", trail, &RefError{Chain: childTrail.chain, Node: nodePath, Err: ErrRefCycle}
		}
		childWorkflow, err := options.WorkflowLoader(ref)
		if err != nil {
			return SubWorkflowContract{}, nil, "", trail, &RefError{Chain: childTrail.chain, Node: nodePath, Err: err}
		}
		contract.DefaultInputs = extractInputDefaults(childWorkflow.GetInputs())
		return contract, childWorkflow, contract.WorkflowIdentity, childTrail, nil
	}

	return contract, nil, contract.WorkflowIdentity, trail, nil
}

type subWorkflowArgs struct {
	Ref         string
	Inline      *reliantv1.Workflow
	Args        map[string]*structpb.Value
	Presets     map[string]string
	Passthrough []string
}

func subWorkflowFromNode(node *reliantv1.Node) (subWorkflowArgs, InvocationMode, bool) {
	if node.GetWorkflow() != nil {
		args := node.GetWorkflow()
		if args.GetInline() != nil {
			return subWorkflowArgs{Inline: args.GetInline()}, InvocationModeInline, true
		}
		return subWorkflowArgs{
			Ref:         model.CelStringRaw(args.GetRef()),
			Args:        args.GetArgs(),
			Presets:     args.GetPresets(),
			Passthrough: args.GetPassthrough(),
		}, InvocationModeRef, true
	}

	if node.GetLoop() != nil {
		args := node.GetLoop()
		if args.GetInline() != nil {
			return subWorkflowArgs{Inline: args.GetInline()}, InvocationModeInline, true
		}
		return subWorkflowArgs{
			Ref:         model.CelStringRaw(args.GetRef()),
			Args:        args.GetArgs(),
			Presets:     args.GetPresets(),
			Passthrough: args.GetPassthrough(),
		}, InvocationModeRef, true
	}

	// Router nodes: only workflow-routing routers produce sub-workflow contracts.
	// Node-routing routers dispatch to sibling nodes, not child workflows.
	if node.GetRouter() != nil {
		args := node.GetRouter()
		candidates := args.GetWorkflows()
		if len(candidates) == 0 {
			// Node routing mode — no child workflow to load
			return subWorkflowArgs{}, "", false
		}
		// Workflow routing mode — use first candidate ref as placeholder identity.
		// The real ref is determined at runtime by the routing LLM.
		return subWorkflowArgs{
			Ref: candidates[0].GetRef(),
		}, InvocationModeRef, true
	}

	return subWorkflowArgs{}, "", false
}

func isTemplateWorkflowRef(ref string) bool {
	return strings.Contains(ref, "{{") && strings.Contains(ref, "}}")
}

func convertStructpbArgs(raw map[string]*structpb.Value) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	converted := make(map[string]any, len(raw))
	for key, value := range raw {
		if value == nil {
			converted[key] = nil
			continue
		}
		converted[key] = value.AsInterface()
	}
	return converted
}

func extractInputDefaults(inputs map[string]*reliantv1.Input) map[string]any {
	if len(inputs) == 0 {
		return nil
	}

	defaults := make(map[string]any)
	for inputName, input := range inputs {
		if input == nil {
			continue
		}

		if nested := model.GetGroupInputs(input); nested != nil {
			nestedDefaults := extractInputDefaults(nested)
			if len(nestedDefaults) > 0 {
				defaults[inputName] = nestedDefaults
			}
			continue
		}

		defaultValue := model.GetInputDefault(input)
		if defaultValue != nil {
			defaults[inputName] = defaultValue
		}
	}

	if len(defaults) == 0 {
		return nil
	}
	return defaults
}

func copyStringMap(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}
