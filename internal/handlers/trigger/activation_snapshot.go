package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/launch"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/preset"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	runtime "github.com/reliant-labs/reliant/internal/workflow/runtime"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	"github.com/reliant-labs/reliant/internal/workflow/workflowref"
	"google.golang.org/protobuf/proto"
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
	bytes   int
}

func (s *Service) activationSnapshot(ctx context.Context, user string, def *pb.TriggerDefinition, wf *pb.Workflow) (*activationSnapshot, error) {
	snap := &activationSnapshot{Workflows: map[string]string{}, Presets: map[string]*preset.Preset{}}
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
			return nil, refusedPrecondition("Could not resolve an activation preset for approval")
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
		w, err := launch.ResolveRunWorkflow(ctx, s.deps.Repo, user, ref, def.ProjectId)
		if err != nil {
			return nil, refusedPrecondition("Could not resolve a referenced workflow for approval")
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
	// Explicitly traverse every captured definition to collect preset INPUT
	// defaults and spawn preset declarations too: the validator only loads
	// presets needed to validate child argument contracts, not every choice.
	contexts := map[string][]map[string]any{key: {snap.EffectiveInputs}}
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
									if strings.Contains(value, "{{") {
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
				if err := walkInputs(w.GetInputs(), values); err != nil {
					return err
				}
				if w.GetOnFailure() != nil {
					if err := walk(w.GetOnFailure(), depth+1); err != nil {
						return err
					}
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
					snap.Summary = append(snap.Summary, nodePrivilegeDisclosure(node, values, k)...)
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
						resolved, err := wfcel.ResolveCELFields(a, runtime.NewCELContextBuilder().WithWorkflow("", k).WithInputs(values))
						if err != nil {
							return refusedPrecondition("Child workflow/preset dependencies must resolve before activation approval")
						}
						a = resolved.(*pb.SubWorkflowArgs)
						childRef = model.CelStringValue(a.GetRef())
						childPresets = a.GetPresets()
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
						for name, v := range a.GetArgs() {
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
						resolved, err := wfcel.ResolveCELFields(a, runtime.NewCELContextBuilder().WithWorkflow("", k).WithInputs(values))
						if err != nil {
							return refusedPrecondition("Loop workflow/preset dependencies must resolve before activation approval")
						}
						a = resolved.(*pb.LoopArgs)
						if ref := model.CelStringValue(a.GetRef()); ref != "" {
							if _, err := load(ref); err != nil {
								return err
							}
						}
						params := map[string]any{}
						groups := make([]string, 0, len(a.GetPresets()))
						for group := range a.GetPresets() {
							groups = append(groups, group)
						}
						sort.Strings(groups)
						for _, group := range groups {
							name := a.GetPresets()[group]
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
						for name, v := range a.GetArgs() {
							params[name] = v.AsInterface()
						}
						if ref := model.CelStringValue(a.GetRef()); ref != "" {
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
						resolved, err := wfcel.ResolveCELFields(tc, runtime.NewCELContextBuilder().WithWorkflow("", k).WithInputs(values))
						if err != nil {
							return refusedPrecondition("Tool privileges depend on runtime/event values; activation approval requires fixed privilege choices")
						}
						tc = resolved.(*pb.ToolsConfig)
						declarations := model.CelStringListValue(tc.GetPreloadedTools())
						for _, declaration := range append(append([]string{}, declarations...), model.CelStringListValue(tc.GetLoadableTools())...) {
							if strings.HasPrefix(strings.TrimPrefix(declaration, "!"), "mcp__") {
								return refusedPrecondition("MCP privilege dependencies require explicit review in the app")
							}
						}
						declarations = append(declarations, model.CelStringListValue(tc.GetLoadableTools())...)
						if tc.GetLoadableTools() == nil {
							declarations = append(declarations, "*")
						}
						snap.Summary = append(snap.Summary, "Agent tier: "+tools.NormalizePermission(model.CelStringValue(tc.GetPermission())))
						if tools.FilterReachesMCP(declarations) {
							return refusedPrecondition("MCP or wildcard privilege dependencies require explicit fixed catalog choices or review in the app")
						}
						allowed := tools.ExpandToolFilter(declarations, nil)
						sort.Strings(allowed)
						known := map[string]bool{}
						for _, entry := range tools.GetToolRegistry() {
							known[entry.Name] = true
						}
						for _, name := range allowed {
							if !known[name] {
								return refusedPrecondition("Non-catalog tool privileges require explicit review in the app")
							}
						}
						if len(allowed) > 256 {
							return refusedPrecondition("Tool privilege display limit exceeded")
						}
						snap.Summary = append(snap.Summary, fmt.Sprintf("Agent tool privileges: %s; agent-chosen action target repositories/channels/environments: dynamic/unbounded unless a fixed binding below narrows them (runtime policy still applies)", strings.Join(allowed, ", ")))
						boundTools := make([]string, 0, len(tc.GetTools()))
						for name := range tc.GetTools() {
							boundTools = append(boundTools, name)
						}
						sort.Strings(boundTools)
						for _, name := range boundTools {
							if known[name] {
								snap.Summary = append(snap.Summary, "Fixed tool binding: "+name+"; "+targetDisclosure(tc.GetTools()[name].GetFields(), values, k))
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
			snap.Summary = append(snap.Summary, fmt.Sprintf("Workflow dependency %d: %d nodes; complete definition and its literal privileges fingerprinted (literals withheld)", len(snap.Summary)+1, len(w.Nodes)))
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
	snap.Summary = append(snap.Summary, fmt.Sprintf("%d workflow definitions; %d resolved presets; %d effective inputs. Input/preset values and graph literals withheld. Later workflow edits remain live (not a permanently frozen execution).", len(snap.Workflows), len(snap.Presets), len(snap.EffectiveInputs)))
	return snap, nil
}
