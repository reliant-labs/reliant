package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/triggers"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const maxApprovalDependencies = 64
const maxApprovalSnapshotBytes = 2 * 1024 * 1024

// A snapshot is request-local, never stored in approval.title/audit. Validation's
// loader walks ALL referenced children (including spawn and inline/failure
// branches); memoization terminates self-spawn cycles. Limits fail closed.
type activationSnapshot struct {
	Workflows       map[string]string
	Presets         map[string]*preset.Preset
	EffectiveInputs map[string]any
	ToolBindings    any
	Connection      any
	// Summary contains only structural counts and tool declaration categories,
	// never a prompt, expression, input value, command or graph literal.
	Summary []string
	// Capabilities are the powers the closure uses (activation_capabilities.go):
	// what the approval policy compares before and after a change. The row's
	// own identity and machine are added by the caller.
	Capabilities automationpolicy.Capabilities
	bytes        int
}

func (s *Service) activationSnapshot(ctx context.Context, user string, def *pb.TriggerDefinition, wf *pb.Workflow) (*activationSnapshot, error) {
	snap := &activationSnapshot{Workflows: map[string]string{}, Presets: map[string]*preset.Preset{}, Capabilities: automationpolicy.Capabilities{}}
	bindings, err := s.deps.Repo.ListSettingsByKey(ctx, user, "tool.bindings.%")
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	snap.ToolBindings = bindings
	if def.GetConnectionId() != "" {
		if source := s.connections(); source != nil {
			c, err := source.GetConnection(ctx, user, def.GetConnectionId())
			if err != nil {
				return nil, svcerr.Wrap(err)
			}
			snap.Connection = c
			snap.Capabilities.Add(connectionCapabilities(c)...)
		}
	}
	// Raw values only enter the memory-only digest, never its safe summary.
	loaded := map[string]*pb.Workflow{}
	var limitErr error
	account := func(data []byte) error {
		snap.bytes += len(data)
		if snap.bytes > maxApprovalSnapshotBytes || len(snap.Workflows)+len(snap.Presets) > maxApprovalDependencies {
			return refusedPrecondition("Activation approval dependency/size limit exceeded")
		}
		return nil
	}
	loadPreset := func(name string) (*preset.Preset, error) {
		if p, ok := snap.Presets[name]; ok {
			return p, nil
		}
		p, err := launch.ResolveRunPreset(ctx, s.deps.Repo, user, def.ProjectId, name)
		if err != nil {
			// Name what did not resolve (a preset name, never its values), so
			// the caller can fix it instead of guessing which dependency.
			return nil, refusedPrecondition(fmt.Sprintf("Could not resolve preset %q for approval", name))
		}
		snap.Presets[name] = p
		raw, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		if err = account(raw); err != nil {
			limitErr = err
			return nil, err
		}
		return p, nil
	}
	load := func(ref string) (*pb.Workflow, error) {
		key := workflowref.RefKey(ref)
		if w := loaded[key]; w != nil {
			return w, nil
		} // cycle detection: fingerprint once, no recursive expansion
		w, err := launch.ResolveRunWorkflowOn(ctx, s.deps.Repo, launch.AutomationScope(user, def.ProjectId, def.DaemonId), ref)
		if err != nil {
			// Resolved on the activation's own machine (def.DaemonId), not the
			// caller's checkout. The ref is named; it is a name, not a value.
			return nil, refusedPrecondition(fmt.Sprintf("Could not resolve referenced workflow %q on the activation's machine for approval", ref))
		}
		loaded[key] = w
		snap.Workflows[key] = canonicalProto(w)
		if err = account([]byte(snap.Workflows[key])); err != nil {
			limitErr = err
			return nil, err
		}
		return w, nil
	}
	key := workflowref.RefKey(def.Workflow)
	loaded[key] = wf
	snap.Workflows[key] = canonicalProto(wf)
	if err := account([]byte(snap.Workflows[key])); err != nil {
		return nil, err
	}
	// Use the same full-tree validator/runtime loaders, but capture everything
	// they read rather than pretending the returned root is the executable tree.
	result := validation.StaticAnalysisWithOptions(wf, &validation.ValidationOptions{CanonicalWorkflowRef: def.Workflow, WorkflowLoader: load, PresetLoader: func(name string) (map[string]interface{}, error) {
		p, err := loadPreset(name)
		if err != nil {
			return nil, err
		}
		return p.Params, nil
	}})
	if limitErr != nil {
		return nil, limitErr
	}
	if result.HasErrors() {
		return nil, refusedPrecondition("Workflow dependency validation failed during approval")
	}
	inputs := map[string]any{}
	groups := make([]string, 0, len(def.Presets))
	for group := range def.Presets {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	for _, group := range groups {
		p, err := loadPreset(def.Presets[group])
		if err != nil {
			return nil, err
		}
		inputs = preset.ApplyToInputs(p, inputs, group)
	}
	for k, v := range def.Params {
		inputs[k] = v.AsInterface()
	}
	snap.EffectiveInputs = runtime.ApplyDefaults(inputs, wf.GetInputs())
	// Inputs the declared trigger fills from each event are resolved once per
	// value the trigger's match allows (activation_event_inputs.go).
	rootContexts, eventLines, err := eventInputContexts(triggers.FindDeclaredTrigger(wf, def.GetWorkflowTrigger()), wf, snap.EffectiveInputs)
	if err != nil {
		return nil, err
	}
	snap.Summary = append(snap.Summary, eventLines...)
	// Explicitly traverse every captured definition to collect preset INPUT
	// defaults and spawn preset declarations too: the validator only loads
	// presets needed to validate child argument contracts, not every choice.
	contexts := map[string][]map[string]any{key: rootContexts}
	contextSeen := map[string]bool{}
	addContext := func(ref string, params map[string]any) error {
		w, err := load(ref)
		if err != nil {
			return err
		}
		effective := runtime.ApplyDefaults(params, w.GetInputs())
		raw, err := json.Marshal(effective)
		if err != nil {
			return err
		}
		ctxKey := workflowref.RefKey(ref) + string(raw)
		if contextSeen[ctxKey] {
			return nil
		}
		contextSeen[ctxKey] = true
		if len(contextSeen) > maxApprovalDependencies {
			return refusedPrecondition("Activation effective privilege context limit exceeded")
		}
		if err := account(raw); err != nil {
			return err
		}
		contexts[workflowref.RefKey(ref)] = append(contexts[workflowref.RefKey(ref)], effective)
		return nil
	}
	visited := map[string]int{}
	for {
		progress := false
		keys := make([]string, 0, len(loaded))
		for k := range loaded {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if len(contexts[k]) == 0 {
				contexts[k] = []map[string]any{runtime.ApplyDefaults(map[string]any{}, loaded[k].GetInputs())}
			}
			if visited[k] >= len(contexts[k]) {
				continue
			}
			progress = true
			valuesForContext := contexts[k][visited[k]]
			visited[k]++
			w := loaded[k]
			checked := validation.StaticAnalysisWithOptions(w, &validation.ValidationOptions{CanonicalWorkflowRef: k, WorkflowLoader: load, PresetLoader: func(name string) (map[string]interface{}, error) {
				p, err := loadPreset(name)
				if err != nil {
					return nil, err
				}
				return p.Params, nil
			}})
			if limitErr != nil {
				return nil, limitErr
			}
			if checked.HasErrors() {
				return nil, refusedPrecondition("Referenced workflow dependency validation failed during approval")
			}
			var walk func(*pb.Workflow, int) error
			walk = func(w *pb.Workflow, depth int) error {
				if depth > 32 {
					return refusedPrecondition("Activation inline workflow depth limit exceeded")
				}
				// Capture preset-input defaults (including groups) as effective choices.
				var walkInputs func(map[string]*pb.Input, map[string]any) error
				walkInputs = func(schema map[string]*pb.Input, values map[string]any) error {
					for name, input := range schema {
						if input.GetType() == "preset" {
							choices := []string{}
							switch value := values[name].(type) {
							case string:
								choices = append(choices, value)
							case []any:
								for _, v := range value {
									if s, ok := v.(string); ok {
										choices = append(choices, s)
									}
								}
							case []string:
								choices = value
							}
							for _, value := range choices {
								if value != "" {
									if strings.Contains(value, "{{") || strings.Contains(value, eventSupplied) {
										return refusedPrecondition("Preset dependencies must resolve before activation approval")
									}
									if _, err := loadPreset(value); err != nil {
										return err
									}
								}
							}
						}
						if input.GetType() == "group" {
							nested, _ := values[name].(map[string]any)
							if err := walkInputs(input.GetGroupInput().GetInputs(), nested); err != nil {
								return err
							}
						}
					}
					return nil
				}
				values := valuesForContext
				tainted := taintedInputs(values)
				if err := walkInputs(w.GetInputs(), values); err != nil {
					return err
				}
				if w.GetOnFailure() != nil {
					if err := walk(w.GetOnFailure(), depth+1); err != nil {
						return err
					}
				}
				if w.GetFinally() != nil {
					if err := walk(w.GetFinally(), depth+1); err != nil {
						return err
					}
				}
				secretSummary, secretCaps := secretDeclarationDisclosure(w, values, k, tainted)
				snap.Summary = append(snap.Summary, secretSummary...)
				snap.Capabilities.Add(secretCaps...)
				if model.DeclaresPermission(w, model.PermissionApprovalsDecide) {
					snap.Summary = append(snap.Summary, "Permission approvals:decide: the workflow declares that its steps may approve or deny requests")
					snap.Capabilities.Add(approvalsDecideCapability)
				}
				counts := map[string]int{}
				for _, node := range w.GetNodes() {
					counts[model.NodeType(node)]++
				}
				// Only known node kinds, never arbitrary authored node IDs.
				for _, kind := range []string{"run", "action", "call_llm", "invoke_tool", "workflow", "loop", "approval"} {
					if counts[kind] > 0 {
						snap.Summary = append(snap.Summary, fmt.Sprintf("Executable node kind %s: %d", kind, counts[kind]))
					}
				}
				for _, node := range w.GetNodes() {
					snap.Summary = append(snap.Summary, nodePrivilegeDisclosure(node, values, k, tainted)...)
					snap.Capabilities.Add(nodeCapabilities(node, values, k, tainted)...)
					if router := node.GetRouter(); router != nil {
						for _, candidate := range router.GetWorkflows() {
							if len(candidate.GetPresets()) == 0 {
								return refusedPrecondition("Router workflow candidates must enumerate preset choices for activation approval")
							}
							for _, name := range candidate.GetPresets() {
								p, err := loadPreset(name)
								if err != nil {
									return err
								}
								if err := addContext(candidate.GetRef(), preset.ApplyToInputs(p, map[string]any{}, "default")); err != nil {
									return err
								}
							}
						}
					}
					// Capture CEL-selected child preset names with the runtime resolver.
					// If event/runtime output decides a dependency we cannot approve a
					// complete closure now; fail closed rather than skip that dependency.
					var childRef string
					var childPresets map[string]string
					if a := node.GetWorkflow(); a != nil && a.GetInline() == nil {
						var args map[string]*structpb.Value
						var err error
						childRef, childPresets, args, err = resolveChildSelection("Workflow", node.GetId(), a.GetRef(), a.GetPresets(), a.GetArgs(), values, k, tainted)
						if err != nil {
							return err
						}
						snap.Summary = append(snap.Summary, childDirectoryDisclosure(a.GetProject(), tainted)...)
						params := map[string]any{}
						groups := make([]string, 0, len(childPresets))
						for group := range childPresets {
							groups = append(groups, group)
						}
						sort.Strings(groups)
						for _, group := range groups {
							name := childPresets[group]
							p, err := loadPreset(name)
							if err != nil {
								return err
							}
							params = preset.ApplyToInputs(p, params, group)
						}
						for _, name := range a.GetPassthrough() {
							if v, ok := values[name]; ok {
								params[name] = v
							}
						}
						for name, v := range args {
							params[name] = v.AsInterface()
						}
						if childRef != "" {
							if err := addContext(childRef, params); err != nil {
								return err
							}
						}
					}
					if childRef != "" {
						if _, err := load(childRef); err != nil {
							return err
						}
					}
					for _, name := range childPresets {
						if name != "" {
							if _, err := loadPreset(name); err != nil {
								return err
							}
						}
					}
					if a := node.GetWorkflow(); a != nil && a.GetInline() != nil {
						if err := walk(a.GetInline(), depth+1); err != nil {
							return err
						}
					}
					if a := node.GetLoop(); a != nil && a.GetInline() == nil {
						ref, loopPresets, args, err := resolveChildSelection("Loop", node.GetId(), a.GetRef(), a.GetPresets(), a.GetArgs(), values, k, tainted)
						if err != nil {
							return err
						}
						snap.Summary = append(snap.Summary, childDirectoryDisclosure(a.GetProject(), tainted)...)
						if ref != "" {
							if _, err := load(ref); err != nil {
								return err
							}
						}
						params := map[string]any{}
						groups := make([]string, 0, len(loopPresets))
						for group := range loopPresets {
							groups = append(groups, group)
						}
						sort.Strings(groups)
						for _, group := range groups {
							name := loopPresets[group]
							if name != "" {
								p, err := loadPreset(name)
								if err != nil {
									return err
								}
								params = preset.ApplyToInputs(p, params, group)
							}
						}
						for _, name := range a.GetPassthrough() {
							if v, ok := values[name]; ok {
								params[name] = v
							}
						}
						for name, v := range args {
							params[name] = v.AsInterface()
						}
						if ref != "" {
							if err := addContext(ref, params); err != nil {
								return err
							}
						}
					}
					if a := node.GetLoop(); a != nil && a.GetInline() != nil {
						if err := walk(a.GetInline(), depth+1); err != nil {
							return err
						}
					}
					if a := model.GetCallLLMArgs(node); a != nil {
						tc := &pb.ToolsConfig{}
						if a.GetToolsConfig() != nil {
							tc = proto.Clone(a.GetToolsConfig()).(*pb.ToolsConfig)
						}
						// Resolve privilege-bearing CEL with the runtime's evaluator, not a
						// bespoke parser. Runtime/event-dependent values that cannot resolve
						// are refused rather than silently omitted from the approval.
						if field := taintedField(&pb.ToolsConfig{PreloadedTools: tc.GetPreloadedTools(), LoadableTools: tc.GetLoadableTools(), Permission: tc.GetPermission(), Spawn: tc.GetSpawn()}, tainted); field != "" {
							return refusedPrecondition(fmt.Sprintf("Agent node %q: tools_config.%s depends on a node output, the trigger event or another run-time value; activation approval requires fixed privilege choices", node.GetId(), field))
						}
						resolved, err := wfcel.ResolveCELFields(tc, runtime.NewCELContextBuilder().WithWorkflow("", k).WithInputs(values))
						if err != nil {
							return refusedPrecondition(fmt.Sprintf("Agent node %q: tools_config does not resolve from the activation's inputs; activation approval requires fixed privilege choices", node.GetId()))
						}
						tc = resolved.(*pb.ToolsConfig)
						declarations := model.CelStringListValue(tc.GetPreloadedTools())
						declarations = append(declarations, model.CelStringListValue(tc.GetLoadableTools())...)
						for _, choice := range append(append([]string{model.CelStringValue(tc.GetPermission())}, declarations...), model.CelStringListValue(tc.GetSpawn())...) {
							if strings.Contains(choice, eventSupplied) || strings.Contains(choice, "{{") {
								return refusedPrecondition("Tool privileges depend on runtime/event values; activation approval requires fixed privilege choices")
							}
						}
						if tc.GetLoadableTools() == nil {
							declarations = append(declarations, "*")
						}
						snap.Summary = append(snap.Summary, "Agent tier: "+tools.NormalizePermission(model.CelStringValue(tc.GetPermission())))
						known := map[string]bool{}
						for _, entry := range tools.GetToolRegistry() {
							known[entry.Name] = true
						}
						allowed := tools.ExpandToolFilter(declarations, nil)
						sort.Strings(allowed)
						nonCatalog := false
						for _, name := range allowed {
							if !known[name] {
								nonCatalog = true
							}
						}
						// A wildcard or MCP declaration cannot be enumerated: its set
						// is whatever MCP servers and runtime-loaded tools exist when
						// the run starts. Say so plainly instead of refusing, so the
						// person approving decides with the widest reading in front
						// of them.
						if tools.FilterReachesMCP(declarations) || nonCatalog || len(allowed) > 256 {
							shown := make([]string, 0, len(declarations))
							for _, d := range declarations {
								if d == "*" {
									shown = append(shown, "* (every tool)")
								} else {
									shown = append(shown, discloseScope(d))
								}
							}
							sort.Strings(shown)
							snap.Summary = append(snap.Summary, fmt.Sprintf("Agent tool privileges: UNRESTRICTED — this workflow's agent has unrestricted tools: every built-in tool, every MCP server tool and any tool loaded at runtime, including tools added after this approval (declared: %s); agent-chosen action targets: dynamic/unbounded (runtime policy still applies)", strings.Join(shown, ", ")))
						} else {
							snap.Summary = append(snap.Summary, fmt.Sprintf("Agent tool privileges: %s; agent-chosen action target repositories/channels/environments: dynamic/unbounded unless a fixed binding below narrows them (runtime policy still applies)", strings.Join(allowed, ", ")))
						}
						boundTools := make([]string, 0, len(tc.GetTools()))
						for name := range tc.GetTools() {
							boundTools = append(boundTools, name)
						}
						sort.Strings(boundTools)
						for _, name := range boundTools {
							if known[name] {
								snap.Summary = append(snap.Summary, "Fixed tool binding: "+name+"; "+targetDisclosure(tc.GetTools()[name].GetFields(), values, k, tainted))
							}
						}
						entries := model.CelStringListValue(tc.GetSpawn())
						if model.CelStringListIsExpr(tc.GetSpawn()) {
							return refusedPrecondition("Dynamic spawn declarations require explicit fixed choices before activation approval")
						}
						for _, entry := range entries {
							if strings.Contains(entry, "{{") {
								return refusedPrecondition("Dynamic spawn declarations require explicit fixed choices before activation approval")
							}
							if strings.HasPrefix(entry, "spawn:") && tools.ParseSpawnEntry(entry) == nil {
								return refusedPrecondition("Spawn dependencies must enumerate fixed preset choices for activation approval")
							}
							if spawn := tools.ParseSpawnEntry(entry); spawn != nil {
								if _, err := load(spawn.Workflow); err != nil {
									return err
								}
								for _, name := range spawn.Presets {
									p, err := loadPreset(name)
									if err != nil {
										return err
									}
									if err := addContext(spawn.Workflow, preset.ApplyToInputs(p, map[string]any{}, "default")); err != nil {
										return err
									}
								}
							}
						}
					}
				}
				return nil
			}
			if err := walk(w, 0); err != nil {
				return nil, err
			}
			snap.Summary = append(snap.Summary, fmt.Sprintf("Workflow dependency: %d nodes; complete definition and its literal privileges fingerprinted (literals withheld)", len(w.Nodes)))
		}
		if !progress {
			break
		}
	}
	if limitErr != nil {
		return nil, limitErr
	}
	raw, err := json.Marshal(snap.EffectiveInputs)
	if err != nil {
		return nil, err
	}
	if err = account(raw); err != nil {
		return nil, err
	}
	// One line per distinct privilege: resolving per event value repeats
	// every line that does not depend on it.
	seen := map[string]bool{}
	unique := snap.Summary[:0]
	for _, line := range snap.Summary {
		if !seen[line] {
			seen[line] = true
			unique = append(unique, line)
		}
	}
	snap.Summary = unique
	snap.Summary = append(snap.Summary, fmt.Sprintf("%d workflow definitions; %d resolved presets; %d effective inputs. Input/preset values and graph literals withheld. Later workflow edits remain live (not a permanently frozen execution).", len(snap.Workflows), len(snap.Presets), len(snap.EffectiveInputs)))
	return snap, nil
}

// childDirectoryDisclosure says when a child's working directory is chosen at
// run time (`project: {path: '{{ nodes.checkout.path }}'}`). It is not
// resolved at approval: it moves the child within the activation's machine,
// whose authority is disclosed already, and picks no workflow or privilege.
func childDirectoryDisclosure(project *pb.ProjectConfig, tainted map[string]bool) []string {
	if project == nil || !referencesTaintedIn(project, tainted) {
		return nil
	}
	return []string{"Child workflow working directory: chosen at run time on the activation's machine (dynamic/unbounded)"}
}

// resolveChildSelection resolves what a workflow or loop node hands its child:
// the child workflow, its presets and its arguments, against the approval's
// inputs. Only those fields pick the child and its inputs, so only they are
// resolved; the node's other config (thread, project, loop items and while)
// routinely reads node outputs and grants nothing. The whole node is still
// fingerprinted.
//
// The ref and presets choose a dependency, so they must be fixed now: one that
// reads a tainted input or a run-time value is refused (referencesTainted),
// never approved for whatever it would resolve to today. An argument that
// reads one is not refused: it reaches the child as the sentinel
// (maskTaintedArgs), where it is tainted in turn, so a child input it feeds is
// disclosed as dynamic, or refused wherever it would choose a privilege. An
// argument that reads only fixed inputs must resolve.
//
// Refusals name the node and the field (declared names, never a value).
func resolveChildSelection(kind, nodeID string, ref *pb.CelString, presets map[string]string, args map[string]*structpb.Value, values map[string]any, identity string, tainted map[string]bool) (string, map[string]string, map[string]*structpb.Value, error) {
	selection := &pb.SubWorkflowArgs{Ref: ref, Presets: presets}
	if field := taintedField(selection, tainted); field != "" {
		return "", nil, nil, refusedPrecondition(fmt.Sprintf("%s node %q: %s depends on a node output, the trigger event or another run-time value, but a child workflow and its presets must resolve before activation approval", kind, nodeID, field))
	}
	eval := runtime.NewCELContextBuilder().WithWorkflow("", identity).WithInputs(values)
	resolved, err := wfcel.ResolveCELFields(selection, eval)
	if err != nil {
		return "", nil, nil, refusedPrecondition(fmt.Sprintf("%s node %q: ref/presets do not resolve from the activation's inputs, but a child workflow and its presets must resolve before activation approval", kind, nodeID))
	}
	selection = resolved.(*pb.SubWorkflowArgs)
	masked := maskTaintedArgs(args, tainted)
	names := make([]string, 0, len(masked))
	for name := range masked {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make(map[string]*structpb.Value, len(masked))
	for _, name := range names {
		arg, err := wfcel.ResolveCELFields(&pb.SubWorkflowArgs{Args: map[string]*structpb.Value{name: masked[name]}}, eval)
		if err != nil {
			return "", nil, nil, refusedPrecondition(fmt.Sprintf("%s node %q: args.%s does not resolve from the activation's inputs, but child inputs that read only inputs must resolve before activation approval", kind, nodeID, name))
		}
		out[name] = arg.(*pb.SubWorkflowArgs).GetArgs()[name]
	}
	return model.CelStringValue(selection.GetRef()), selection.GetPresets(), out, nil
}
