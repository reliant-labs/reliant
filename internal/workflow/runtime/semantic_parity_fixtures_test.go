package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/builtin"
	"github.com/reliant-labs/reliant/internal/workflow/core"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/validation"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
	"google.golang.org/protobuf/types/known/structpb"
	"gopkg.in/yaml.v3"
)

type semanticParityFixture struct {
	Name                 string                          `yaml:"name"`
	WorkflowRef          string                          `yaml:"workflow_ref"`
	CanonicalWorkflowRef string                          `yaml:"canonical_workflow_ref"`
	WorkflowYAML         string                          `yaml:"workflow_yaml"`
	Core                 semanticParityCoreSection       `yaml:"core"`
	Validation           semanticParityValidationSection `yaml:"validation"`
	Simulator            semanticParitySimulatorSection  `yaml:"simulator"`
	Runtime              semanticParityRuntimeSection    `yaml:"runtime"`
}

type semanticParityCoreSection struct {
	UseWorkflowLoader bool                         `yaml:"use_workflow_loader"`
	ExpectedContracts []semanticParityContractSpec `yaml:"expected_contracts"`
}

type semanticParityContractSpec struct {
	NodePath         string `yaml:"node_path"`
	InputPolicy      string `yaml:"input_policy"`
	WorkflowIdentity string `yaml:"workflow_identity"`
	LoadStrategy     string `yaml:"load_strategy"`
}

type semanticParityValidationSection struct {
	ShouldPass        bool   `yaml:"should_pass"`
	UseWorkflowLoader bool   `yaml:"use_workflow_loader"`
	ErrorContains     string `yaml:"error_contains"`
}

type semanticParitySimulatorSection struct {
	ExpectedStatus string                  `yaml:"expected_status"`
	Scenario       *semanticParityScenario `yaml:"scenario"`
}

type semanticParityScenario struct {
	Name   string                        `yaml:"name"`
	Inputs map[string]interface{}        `yaml:"inputs"`
	Events []semanticParityScenarioEvent `yaml:"events"`
	Expect *semanticParityScenarioExpect `yaml:"expect"`
}

type semanticParityScenarioEvent struct {
	Node      string                   `yaml:"node"`
	Type      string                   `yaml:"type"`
	Text      string                   `yaml:"text"`
	ToolCalls []semanticParityToolCall `yaml:"tool_calls"`
	Output    map[string]interface{}   `yaml:"output"`
}

type semanticParityToolCall struct {
	Name  string                 `yaml:"name"`
	Input map[string]interface{} `yaml:"input"`
}

type semanticParityScenarioExpect struct {
	Outcome string   `yaml:"outcome"`
	Reached []string `yaml:"reached"`
}

type semanticParityRuntimeSection struct {
	Checks []semanticParityRuntimeCheck `yaml:"checks"`
}

type semanticParityRuntimeCheck struct {
	Type                 string                 `yaml:"type"`
	NodePath             string                 `yaml:"node_path"`
	ParentInputs         map[string]interface{} `yaml:"parent_inputs"`
	ExpectedModel        interface{}            `yaml:"expected_model"`
	Inputs               map[string]interface{} `yaml:"inputs"`
	ExpectedSpawnRef     string                 `yaml:"expected_spawn_ref"`
	ExpectedSpawnPresets []string               `yaml:"expected_spawn_presets"`
}

func TestSemanticParityFixtures(t *testing.T) {
	fixtures, err := loadSemanticParityFixtures("testdata/semantic_parity")
	if err != nil {
		t.Fatalf("failed to load semantic parity fixtures: %v", err)
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Name, func(t *testing.T) {
			workflowDef, err := loadWorkflowForSemanticFixture(fixture)
			validationErr := err
			if validationErr != nil && fixture.Validation.ShouldPass {
				t.Fatalf("failed to load workflow for fixture %q: %v", fixture.Name, validationErr)
			}
			if workflowDef == nil {
				workflowDef, err = loadWorkflowForSemanticFixtureWithoutValidation(fixture)
				if err != nil {
					t.Fatalf("failed to parse workflow for fixture %q without validation: %v", fixture.Name, err)
				}
			}

			canonicalRef := strings.TrimSpace(fixture.CanonicalWorkflowRef)
			if canonicalRef == "" {
				canonicalRef = strings.TrimSpace(workflowDef.GetName())
			}
			builtinLoader := semanticParityBuiltinWorkflowLoader()

			t.Run("core_compile_output_semantics", func(t *testing.T) {
				compileOptions := core.CompileOptions{CanonicalWorkflowRef: canonicalRef}
				if fixture.Core.UseWorkflowLoader {
					compileOptions.WorkflowLoader = builtinLoader
				}
				program, compileErr := core.Compile(workflowDef, compileOptions)
				if compileErr != nil {
					t.Fatalf("core compile failed: %v", compileErr)
				}

				for _, expectedContract := range fixture.Core.ExpectedContracts {
					contract, ok := program.Semantics.SubWorkflows[expectedContract.NodePath]
					if !ok {
						t.Fatalf("expected core contract at node path %q", expectedContract.NodePath)
					}
					if expectedContract.InputPolicy != "" && string(contract.InputPolicy) != expectedContract.InputPolicy {
						t.Fatalf("contract %q input policy mismatch: got %q want %q", expectedContract.NodePath, contract.InputPolicy, expectedContract.InputPolicy)
					}
					if expectedContract.WorkflowIdentity != "" && contract.WorkflowIdentity != expectedContract.WorkflowIdentity {
						t.Fatalf("contract %q workflow identity mismatch: got %q want %q", expectedContract.NodePath, contract.WorkflowIdentity, expectedContract.WorkflowIdentity)
					}
					if expectedContract.LoadStrategy != "" && string(contract.LoadStrategy) != expectedContract.LoadStrategy {
						t.Fatalf("contract %q load strategy mismatch: got %q want %q", expectedContract.NodePath, contract.LoadStrategy, expectedContract.LoadStrategy)
					}
				}
			})

			t.Run("validation_acceptance_rejection", func(t *testing.T) {
				if !fixture.Validation.ShouldPass && validationErr != nil {
					if fixture.Validation.ErrorContains != "" && !strings.Contains(validationErr.Error(), fixture.Validation.ErrorContains) {
						t.Fatalf("expected validation error containing %q, got: %s", fixture.Validation.ErrorContains, validationErr.Error())
					}
					return
				}
				validationOptions := &validation.ValidationOptions{}
				if fixture.Validation.UseWorkflowLoader {
					validationOptions.WorkflowLoader = builtinLoader
				}
				result := validation.StaticAnalysisWithOptions(workflowDef, validationOptions)
				if fixture.Validation.ShouldPass && result.HasErrors() {
					t.Fatalf("expected validation to pass, got errors: %s", result.Error())
				}
				if !fixture.Validation.ShouldPass {
					if !result.HasErrors() {
						t.Fatalf("expected validation to fail")
					}
					if fixture.Validation.ErrorContains != "" && !strings.Contains(result.Error(), fixture.Validation.ErrorContains) {
						t.Fatalf("expected validation error containing %q, got: %s", fixture.Validation.ErrorContains, result.Error())
					}
				}
			})

			if fixture.Simulator.Scenario != nil {
				t.Run("simulator_execution_expectations", func(t *testing.T) {
					if validationErr != nil && !fixture.Validation.ShouldPass {
						if fixture.Simulator.ExpectedStatus != "error" {
							t.Fatalf("simulator status mismatch: got %q want %q (%s)", "error", fixture.Simulator.ExpectedStatus, validationErr.Error())
						}
						return
					}
					status, mismatch := runSemanticParityScenario(workflowDef, canonicalRef, builtinLoader, fixture.Simulator.Scenario)
					if status != fixture.Simulator.ExpectedStatus {
						t.Fatalf("simulator status mismatch: got %q want %q (%s)", status, fixture.Simulator.ExpectedStatus, mismatch)
					}
				})
			}

			if len(fixture.Runtime.Checks) > 0 {
				t.Run("runtime_targeted_semantic_paths", func(t *testing.T) {
					semantics, semErr := CompileRuntimeSemantics(workflowDef, canonicalRef)
					if semErr != nil {
						t.Fatalf("CompileRuntimeSemantics failed: %v", semErr)
					}

					for _, runtimeCheck := range fixture.Runtime.Checks {
						runtimeCheck := runtimeCheck
						t.Run(runtimeCheck.Type, func(t *testing.T) {
							switch runtimeCheck.Type {
							case "one_ring_nested_plan_inherits_model":
								runOneRingNestedPlanInheritsModelRuntimeCheck(t, workflowDef, semantics, runtimeCheck)
							case "spawn_workflow_name_presets":
								runSpawnWorkflowNamePresetsRuntimeCheck(t, workflowDef, semantics, canonicalRef, runtimeCheck)
							default:
								t.Fatalf("unknown runtime check type %q", runtimeCheck.Type)
							}
						})
					}
				})
			}
		})
	}
}

func runSemanticParityScenario(
	workflowDef *reliantv1.Workflow,
	canonicalRef string,
	loader func(string) (*reliantv1.Workflow, error),
	scenario *semanticParityScenario,
) (string, string) {
	eventByNode := make(map[string][]semanticParityScenarioEvent)
	for _, event := range scenario.Events {
		eventByNode[event.Node] = append(eventByNode[event.Node], event)
	}
	consumedByNode := make(map[string]int)

	hasInternalEvents := func(prefix string) bool {
		for nodeID := range eventByNode {
			if strings.HasPrefix(nodeID, prefix) {
				return true
			}
		}
		return false
	}

	stepMocker := func(stepID string, _ map[string]interface{}) map[string]interface{} {
		events := eventByNode[stepID]
		index := consumedByNode[stepID]
		if index >= len(events) {
			return map[string]interface{}{}
		}
		consumedByNode[stepID] = index + 1
		return semanticParityEventToOutput(events[index])
	}

	sim := NewWorkflowSimulator(workflowDef, SimulatorConfig{
		WorkflowInputs:       scenario.Inputs,
		MaxIterations:        100,
		HasInternalEvents:    hasInternalEvents,
		WorkflowLoader:       loader,
		CanonicalWorkflowRef: canonicalRef,
	})

	runErr := sim.Run(stepMocker)

	if scenario.Expect != nil {
		if scenario.Expect.Outcome == "error" {
			if runErr == nil {
				return "failed", "expected error outcome but simulation completed"
			}
			return "passed", ""
		}
		if runErr != nil {
			return "error", runErr.Error()
		}
		visited := sim.GetVisitedSteps()
		for _, expectedNode := range scenario.Expect.Reached {
			if !stringSliceContains(visited, expectedNode) {
				return "failed", fmt.Sprintf("expected reached node %q, got %v", expectedNode, visited)
			}
		}
	}

	for nodeID, events := range eventByNode {
		if consumedByNode[nodeID] < len(events) {
			return "failed", fmt.Sprintf("unconsumed events for %s", nodeID)
		}
	}

	if runErr != nil {
		return "error", runErr.Error()
	}
	return "passed", ""
}

func semanticParityEventToOutput(event semanticParityScenarioEvent) map[string]interface{} {
	if event.Output != nil {
		return event.Output
	}

	switch event.Type {
	case "llm_response":
		toolCalls := make([]interface{}, 0, len(event.ToolCalls))
		for _, toolCall := range event.ToolCalls {
			inputValue, _ := structpb.NewStruct(toolCall.Input)
			toolCalls = append(toolCalls, map[string]interface{}{
				"name":  toolCall.Name,
				"input": inputValue.AsMap(),
			})
		}
		return map[string]interface{}{
			"message": map[string]interface{}{
				"role": "assistant",
				"text": event.Text,
			},
			"response_text": event.Text,
			"tool_calls":    toolCalls,
		}
	default:
		return map[string]interface{}{}
	}
}

func runOneRingNestedPlanInheritsModelRuntimeCheck(
	t *testing.T,
	workflowDef *reliantv1.Workflow,
	semantics *RuntimeSemantics,
	check semanticParityRuntimeCheck,
) {
	t.Helper()

	nodePathParts := strings.Split(strings.TrimSpace(check.NodePath), "/")
	if len(nodePathParts) < 2 {
		t.Fatalf("one-ring runtime check requires nested node_path, got %q", check.NodePath)
	}

	parentPath := strings.Join(nodePathParts[:len(nodePathParts)-1], "/")
	parentNodeID := nodePathParts[len(nodePathParts)-2]
	parentContract, ok := semantics.ContractForNode(parentNodeID)
	if !ok {
		t.Fatalf("missing parent runtime contract for node %q", parentNodeID)
	}
	if parentContract.NodePath != "" && parentContract.NodePath != parentPath {
		t.Fatalf("runtime contract node path mismatch for %q: got %q want %q", parentNodeID, parentContract.NodePath, parentPath)
	}

	executor := &InlineWorkflowExecutor{workflowInputs: check.ParentInputs, invocationContract: &parentContract}
	inheritedInputs := executor.buildSubWorkflowInputs()

	targetNode, err := findNodeBySlashPath(workflowDef, check.NodePath)
	if err != nil {
		t.Fatalf("failed to find target node %q: %v", check.NodePath, err)
	}

	if inheritedInputs["model"] != check.ExpectedModel {
		t.Fatalf("inline inherited inputs missing model: got %#v want %#v (inputs: %#v)", inheritedInputs["model"], check.ExpectedModel, inheritedInputs)
	}

	evaluatedNode, err := EvaluateNodeConfig(targetNode, map[string]interface{}{}, "runtime-check", workflowDef.GetName(), inheritedInputs, nil, nil, nil)
	if err != nil {
		t.Fatalf("EvaluateNodeConfig failed for node %q: %v", check.NodePath, err)
	}

	resolvedInputs := model.NodeMergedSubWorkflowInputs(evaluatedNode)
	if resolvedInputs["model"] != check.ExpectedModel {
		t.Fatalf("expected nested plan arg model to resolve to %#v, got %#v", check.ExpectedModel, resolvedInputs["model"])
	}
}

func runSpawnWorkflowNamePresetsRuntimeCheck(
	t *testing.T,
	workflowDef *reliantv1.Workflow,
	semantics *RuntimeSemantics,
	canonicalRef string,
	check semanticParityRuntimeCheck,
) {
	t.Helper()
	if len(check.ExpectedSpawnPresets) == 0 {
		t.Fatalf("spawn runtime check requires expected_spawn_presets")
	}

	loopNodeID := strings.Split(strings.TrimSpace(check.NodePath), "/")[0]
	loopNode, err := findNodeBySlashPath(workflowDef, loopNodeID)
	if err != nil {
		t.Fatalf("failed to find loop node %q: %v", loopNodeID, err)
	}

	loopContract, ok := semantics.ContractForNode(loopNodeID)
	if !ok {
		t.Fatalf("missing runtime contract for loop node %q", loopNodeID)
	}

	loopExecutor := &InlineLoopExecutor{
		loopID:             loopNodeID,
		loopStep:           &core.TriggeredNode{Node: loopNode},
		iteration:          0,
		workflowID:         "runtime-check",
		workflowName:       canonicalRef,
		workflowInputs:     check.Inputs,
		nodeOutputs:        map[string]interface{}{},
		subWorkflow:        loopNode.GetLoop().GetInline(),
		invocationContract: &loopContract,
	}

	iterationInputs, err := loopExecutor.buildIterationInputs()
	if err != nil {
		t.Fatalf("buildIterationInputs failed: %v", err)
	}

	targetNode, err := findNodeBySlashPath(workflowDef, check.NodePath)
	if err != nil {
		t.Fatalf("failed to find target node %q: %v", check.NodePath, err)
	}

	callLLMArgs := targetNode.GetCallLlm()
	if callLLMArgs == nil {
		t.Fatalf("spawn runtime check target node %q is missing call_llm args", check.NodePath)
	}
	tc := callLLMArgs.GetToolsConfig()
	if tc == nil {
		t.Fatalf("spawn runtime check target node %q is missing call_llm.tools_config", check.NodePath)
	}
	// Spawn entries are in tools_config.spawn (not filter)
	resolvedSpawn := tc.GetSpawn()
	spawnExpr := model.CelStringListExpr(resolvedSpawn)
	if spawnExpr == "" {
		spawnValues := model.CelStringListValue(resolvedSpawn)
		if len(spawnValues) == 0 {
			t.Fatalf("spawn runtime check target node %q is missing call_llm.tools_config.spawn", check.NodePath)
		}
		spawnExpr = spawnValues[0]
	}

	builder := NewCELContextBuilder().
		WithWorkflow("runtime-check", canonicalRef).
		WithInputs(iterationInputs).
		WithNodeOutputs(map[string]interface{}{})

	evaluatedSpawnRaw, err := builder.EvalString(spawnExpr)
	if err != nil {
		t.Fatalf("EvalString failed for spawn config: %v", err)
	}

	evaluatedSpawn, ok := evaluatedSpawnRaw.([]interface{})
	if !ok {
		t.Fatalf("expected evaluated tools_config.spawn array, got %#v", evaluatedSpawnRaw)
	}

	expectedSpawn := fmt.Sprintf("spawn:%s(%s)", check.ExpectedSpawnRef, strings.Join(check.ExpectedSpawnPresets, ","))
	if !stringSliceContains(toStringSlice(evaluatedSpawn), expectedSpawn) {
		t.Fatalf("expected evaluated tools_config.spawn to include %q, got %#v", expectedSpawn, evaluatedSpawn)
	}
}

func loadSemanticParityFixtures(dir string) ([]semanticParityFixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	fixtures := make([]semanticParityFixture, 0)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		rawFixture, readErr := os.ReadFile(filepath.Join(dir, name))
		if readErr != nil {
			return nil, readErr
		}
		var fixture semanticParityFixture
		if unmarshalErr := yaml.Unmarshal(rawFixture, &fixture); unmarshalErr != nil {
			return nil, fmt.Errorf("unmarshal fixture %s: %w", name, unmarshalErr)
		}
		if strings.TrimSpace(fixture.Name) == "" {
			fixture.Name = strings.TrimSuffix(name, filepath.Ext(name))
		}
		fixtures = append(fixtures, fixture)
	}

	if len(fixtures) == 0 {
		return nil, fmt.Errorf("no semantic parity fixtures found in %s", dir)
	}
	return fixtures, nil
}

func loadWorkflowForSemanticFixture(fixture semanticParityFixture) (*reliantv1.Workflow, error) {
	if strings.TrimSpace(fixture.WorkflowYAML) != "" {
		return ParseWorkflowProtoBytes([]byte(fixture.WorkflowYAML))
	}
	if strings.TrimSpace(fixture.WorkflowRef) == "" {
		return nil, fmt.Errorf("fixture must set workflow_ref or workflow_yaml")
	}
	return semanticParityBuiltinWorkflowLoader()(fixture.WorkflowRef)
}

func loadWorkflowForSemanticFixtureWithoutValidation(fixture semanticParityFixture) (*reliantv1.Workflow, error) {
	if strings.TrimSpace(fixture.WorkflowYAML) != "" {
		return ParseWorkflowProtoBytesNoValidation([]byte(fixture.WorkflowYAML))
	}
	if strings.TrimSpace(fixture.WorkflowRef) == "" {
		return nil, fmt.Errorf("fixture must set workflow_ref or workflow_yaml")
	}
	return semanticParityBuiltinWorkflowLoader()(fixture.WorkflowRef)
}

func semanticParityBuiltinWorkflowLoader() func(string) (*reliantv1.Workflow, error) {
	return func(ref string) (*reliantv1.Workflow, error) {
		workflowName := strings.TrimSpace(ref)
		workflowName = strings.TrimPrefix(workflowName, "builtin://")
		if workflowName == "" {
			return nil, fmt.Errorf("empty workflow reference")
		}
		workflowData, readErr := builtin.BuiltinWorkflowsFS.ReadFile(workflowName + ".yaml")
		if readErr != nil {
			return nil, fmt.Errorf("workflow not found: %s", ref)
		}
		parsedWorkflow, parseErr := wfyaml.ParseWorkflow(workflowData)
		if parseErr != nil {
			return nil, fmt.Errorf("parse workflow %s: %w", ref, parseErr)
		}
		return parsedWorkflow, nil
	}
}

func findNodeBySlashPath(workflowDef *reliantv1.Workflow, slashPath string) (*reliantv1.Node, error) {
	pathParts := strings.Split(strings.TrimSpace(slashPath), "/")
	if len(pathParts) == 0 || pathParts[0] == "" {
		return nil, fmt.Errorf("empty node path")
	}

	currentWorkflow := workflowDef
	var node *reliantv1.Node
	for index, pathPart := range pathParts {
		node = nil
		for _, candidateNode := range currentWorkflow.GetNodes() {
			if candidateNode.GetId() == pathPart {
				node = candidateNode
				break
			}
		}
		if node == nil {
			return nil, fmt.Errorf("node %q not found at path segment %d", pathPart, index)
		}
		if index == len(pathParts)-1 {
			return node, nil
		}
		if node.GetWorkflow() != nil && node.GetWorkflow().GetInline() != nil {
			currentWorkflow = node.GetWorkflow().GetInline()
			continue
		}
		if node.GetLoop() != nil && node.GetLoop().GetInline() != nil {
			currentWorkflow = node.GetLoop().GetInline()
			continue
		}
		return nil, fmt.Errorf("node %q is not inline and cannot descend to %q", pathPart, pathParts[index+1])
	}
	return node, nil
}

func toStringSlice(values []interface{}) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if stringValue, ok := value.(string); ok {
			result = append(result, stringValue)
		}
	}
	return result
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestLoadSemanticParityFixtures_FailureBehavior(t *testing.T) {
	t.Run("returns explicit error when directory has no yaml fixtures", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("not a fixture"), 0o600)
		if err != nil {
			t.Fatalf("failed to seed temp fixture dir: %v", err)
		}

		_, loadErr := loadSemanticParityFixtures(dir)
		if loadErr == nil {
			t.Fatalf("expected no-fixture error")
		}
		if !strings.Contains(loadErr.Error(), "no semantic parity fixtures found") {
			t.Fatalf("expected explicit no-fixture error, got: %v", loadErr)
		}
	})

	t.Run("returns fixture-scoped parse error for invalid yaml", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("name: [unterminated"), 0o600)
		if err != nil {
			t.Fatalf("failed to write malformed fixture: %v", err)
		}

		_, loadErr := loadSemanticParityFixtures(dir)
		if loadErr == nil {
			t.Fatalf("expected yaml parse failure")
		}
		if !strings.Contains(loadErr.Error(), "unmarshal fixture broken.yaml") {
			t.Fatalf("expected fixture filename in parse error, got: %v", loadErr)
		}
	})
}

func TestFindNodeBySlashPath_FailureBehavior(t *testing.T) {
	workflowDef := &reliantv1.Workflow{
		Name: "root",
		Nodes: []*reliantv1.Node{{
			Id:   "outer",
			Type: "workflow",
			Args: &reliantv1.Node_Workflow{Workflow: &reliantv1.SubWorkflowArgs{
				Inline: &reliantv1.Workflow{Nodes: []*reliantv1.Node{{Id: "inner", Type: "call_llm"}}},
			}},
		}},
	}

	t.Run("rejects empty path", func(t *testing.T) {
		_, err := findNodeBySlashPath(workflowDef, "   ")
		if err == nil {
			t.Fatalf("expected empty node path error")
		}
		if !strings.Contains(err.Error(), "empty node path") {
			t.Fatalf("expected empty path context, got: %v", err)
		}
	})

	t.Run("reports missing segment and index", func(t *testing.T) {
		_, err := findNodeBySlashPath(workflowDef, "outer/missing")
		if err == nil {
			t.Fatalf("expected missing segment error")
		}
		if !strings.Contains(err.Error(), "node \"missing\" not found at path segment 1") {
			t.Fatalf("expected indexed missing segment context, got: %v", err)
		}
	})

	t.Run("rejects descent through non-inline node", func(t *testing.T) {
		flatWorkflow := &reliantv1.Workflow{Nodes: []*reliantv1.Node{{Id: "leaf", Type: "call_llm"}}}
		_, err := findNodeBySlashPath(flatWorkflow, "leaf/child")
		if err == nil {
			t.Fatalf("expected non-inline descent error")
		}
		if !strings.Contains(err.Error(), "is not inline and cannot descend") {
			t.Fatalf("expected non-inline descent context, got: %v", err)
		}
	})
}
