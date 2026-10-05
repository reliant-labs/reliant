// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// MachineRequirement is why a workflow cannot run without the user's machine.
// It is the authoring-time half of no-machine runs (research/DAEMONLESS_RUNS.md):
// the tool menu filters what a no-machine run is offered, and this tells an
// author, before anything fires, that a workflow will not work without one.
//
// It is separate from RequiresDaemon on purpose. RequiresDaemon runs inside
// workflow code (the preflight step), so its answer is part of every run's
// replay and cannot change; this runs only at launch and at trigger write.
type MachineRequirement struct {
	// Hard reasons name nodes that cannot run at all without a machine: a
	// shell `run`, a `create_worktree`, a daemon-placed action, an invoke_tool
	// of a machine tool, an explicit `daemon:` target.
	Hard []string
	// Tools name agent tool lists that statically reach a machine tool. A
	// no-machine run still starts — the menu drops those tools — but for an
	// unattended automation that is a silent downgrade, so trigger writes
	// refuse it. Lists written as CEL cannot be resolved here and are left to
	// the menu filter.
	Tools []string
}

// None reports whether the workflow needs no machine at all.
func (r MachineRequirement) None() bool { return len(r.Hard) == 0 && len(r.Tools) == 0 }

// Summary is the reasons in one sentence fragment, hard ones first.
func (r MachineRequirement) Summary() string {
	return strings.Join(append(append([]string(nil), r.Hard...), r.Tools...), "; ")
}

// WorkflowRefLoader resolves a `ref:` sub-workflow for MachineRequirements.
// A nil loader, or one that fails, leaves that ref unanalysed; the runtime
// refusals still stop it if it reaches for a machine.
type WorkflowRefLoader func(ref string) (*reliantv1.Workflow, error)

// MachineRequirements analyses wf for what keeps it from running without a
// machine. inputs are the values the run will start with (a trigger's params);
// a `tools`-typed input falls back to its schema default when absent, which is
// how the builtin agent declares its tools. cfg is the same injected tool
// classification RequiresDaemon uses; nil analyses nodes only.
func MachineRequirements(wf *reliantv1.Workflow, inputs map[string]any, loader WorkflowRefLoader, cfg *PreflightConfig) MachineRequirement {
	a := &machineAnalysis{cfg: cfg, loader: loader, visited: map[string]bool{}}
	a.workflow(wf, inputs, "")
	return MachineRequirement{Hard: a.hard, Tools: a.tools}
}

type machineAnalysis struct {
	cfg     *PreflightConfig
	loader  WorkflowRefLoader
	visited map[string]bool
	hard    []string
	tools   []string
}

func (a *machineAnalysis) workflow(wf *reliantv1.Workflow, inputs map[string]any, path string) {
	if wf == nil {
		return
	}
	if wf.GetDaemon() != nil {
		a.hard = append(a.hard, fmt.Sprintf("%starget a machine with `daemon:`", prefix(path)))
	}
	a.toolInputs(wf, inputs, path)
	for _, node := range wf.GetNodes() {
		a.node(node, joinNodePath(path, node.GetId()))
	}
}

// toolInputs reports `tools`-typed workflow inputs whose value reaches a
// machine tool. The input is what an agent node's CEL tool list reads (the
// builtin agent's `preloaded_tools: "{{... inputs.tools}}"`), so this is how a
// trigger on the stock agent learns its configured tools need a machine.
func (a *machineAnalysis) toolInputs(wf *reliantv1.Workflow, inputs map[string]any, path string) {
	names := make([]string, 0, len(wf.GetInputs()))
	for name := range wf.GetInputs() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		input := wf.GetInputs()[name]
		if model.GetInputType(input) != "tools" {
			continue
		}
		value, ok := inputs[name]
		if !ok {
			value = model.GetInputDefault(input)
		}
		if bound := a.machineTools(stringList(value)); len(bound) > 0 {
			a.tools = append(a.tools, fmt.Sprintf("%sinput `%s` includes %s", prefix(path), name, strings.Join(bound, ", ")))
		}
	}
}

func (a *machineAnalysis) node(node *reliantv1.Node, path string) {
	if node == nil {
		return
	}
	if node.GetDaemon() != nil {
		a.hard = append(a.hard, fmt.Sprintf("node `%s` targets a machine with `daemon:`", path))
	}
	switch node.GetType() {
	case model.NodeTypeRun:
		a.hard = append(a.hard, fmt.Sprintf("node `%s` runs a shell command", path))
	case model.NodeTypeCreateWorktree:
		a.hard = append(a.hard, fmt.Sprintf("node `%s` creates a git worktree", path))
	case model.NodeTypeAction:
		if actionNeedsMachine(node.GetAction()) {
			a.hard = append(a.hard, fmt.Sprintf("node `%s` uses an action that runs on the user's machine", path))
		}
	case model.NodeTypeInvokeTool:
		tool := node.GetInvokeTool().GetTool()
		if model.CelStringIsExpr(tool) || a.needsMachine(model.CelStringRaw(tool)) {
			a.hard = append(a.hard, fmt.Sprintf("node `%s` invokes `%s`, which needs the user's machine", path, model.CelStringRaw(tool)))
		}
	case model.NodeTypeCallLLM:
		// Only the PRELOADED list counts: it is what the author hands the
		// agent, so a machine tool there is a definite contradiction.
		// loadable_tools is a ceiling on discovery — `["*"]` means "reach
		// what you need" — and a no-machine run narrows it rather than
		// refusing the workflow.
		if lit := node.GetCallLlm().GetToolsConfig().GetPreloadedTools().GetLiteral(); lit != nil {
			if bound := a.machineTools(lit.GetValues()); len(bound) > 0 {
				a.tools = append(a.tools, fmt.Sprintf("node `%s` is given %s", path, strings.Join(bound, ", ")))
			}
		}
	case model.NodeTypeWorkflow:
		args := node.GetWorkflow()
		a.sub(args.GetInline(), args.GetRef(), structArgs(args.GetArgs()), path)
	case model.NodeTypeLoop:
		args := node.GetLoop()
		a.sub(args.GetInline(), args.GetRef(), structArgs(args.GetArgs()), path)
	}
}

func (a *machineAnalysis) sub(inline *reliantv1.Workflow, ref *reliantv1.CelString, args map[string]any, path string) {
	if inline != nil {
		a.workflow(inline, args, path)
		return
	}
	if ref == nil || model.CelStringIsExpr(ref) || a.loader == nil {
		return
	}
	name := model.CelStringRaw(ref)
	if name == "" || a.visited[name] {
		return
	}
	a.visited[name] = true
	if loaded, err := a.loader(name); err == nil {
		a.workflow(loaded, args, path)
	}
}

// machineTools expands a tool list and returns the machine tools it reaches,
// sorted. Exclusions in the list are honoured by the expansion.
func (a *machineAnalysis) machineTools(filter []string) []string {
	if len(filter) == 0 || a.cfg == nil || a.cfg.ExpandToolFilter == nil {
		return nil
	}
	var bound []string
	for _, name := range a.cfg.ExpandToolFilter(filter) {
		if a.needsMachine(name) {
			bound = append(bound, name)
		}
	}
	return dedupe(bound)
}

func (a *machineAnalysis) needsMachine(name string) bool {
	if a.cfg == nil || a.cfg.NeedsMachine == nil {
		return true // unknown classification: assume the worst
	}
	return a.cfg.NeedsMachine(name)
}

func actionNeedsMachine(args *reliantv1.ActionArgs) bool {
	if args == nil || model.CelStringIsExpr(args.GetUses()) {
		return true
	}
	resolved, err := catalog.MustBuiltin().Resolve(model.CelStringRaw(args.GetUses()))
	if err != nil {
		return true
	}
	return resolved.Spec.GetPlacement() == manifest.PlacementDaemon
}

func prefix(path string) string {
	if path == "" {
		return ""
	}
	return "`" + path + "`: "
}

func stringList(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// structArgs converts a sub-workflow's literal args to plain values. A CEL
// template arrives as a string and is not a tool list, so it simply does not
// match; the sub-workflow's default applies instead.
func structArgs(args map[string]*structpb.Value) map[string]any {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v.AsInterface()
	}
	return out
}

func dedupe(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	out := names[:1]
	for _, n := range names[1:] {
		if n != out[len(out)-1] {
			out = append(out, n)
		}
	}
	return out
}
