package validation

import (
	"reflect"
	"strings"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/types/known/structpb"
)

// makeResponseSchemaStruct builds a structpb.Value from a JSON Schema map for use in workflow args.
func makeResponseSchemaStruct(schema map[string]interface{}) *structpb.Value {
	s, err := structpb.NewValue(schema)
	if err != nil {
		panic(err)
	}
	return s
}

// TestIterItemTypeInference_ResponseSchema tests that iter.item fields are validated
// when the loop items come from a workflow node with a response_schema arg.
func TestIterItemTypeInference_ResponseSchema(t *testing.T) {
	// Build a response schema with waves array containing has_frontend boolean
	responseSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"waves": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name":         map[string]interface{}{"type": "string"},
						"goal":         map[string]interface{}{"type": "string"},
						"has_frontend": map[string]interface{}{"type": "boolean"},
						"features": map[string]interface{}{
							"type":  "array",
							"items": map[string]interface{}{"type": "string"},
						},
					},
				},
			},
		},
	}

	wf := &reliantv1.Workflow{
		Name:  "test-iter-type",
		Entry: []string{"scope_loop"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "scope_loop",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{
					Workflow: &reliantv1.SubWorkflowArgs{
						Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://structured-agent"}},
						Args: map[string]*structpb.Value{
							"response_schema": makeResponseSchemaStruct(responseSchema),
						},
					},
				},
			},
			{
				Id:   "wave_loop",
				Type: "loop",
				Args: &reliantv1.Node_Loop{
					Loop: &reliantv1.LoopArgs{
						Items: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "nodes.scope_loop.response.waves"}},
						Ref:   &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://some-wave-workflow"}},
						While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 10"},
						Args: map[string]*structpb.Value{
							// Valid field access — should pass
							"wave_name":    structpb.NewStringValue("{{iter.item.name}}"),
							"wave_goal":    structpb.NewStringValue("{{iter.item.goal}}"),
							"has_frontend": structpb.NewStringValue("{{iter.item.has_frontend}}"),
						},
					},
				},
			},
		},
		Edges: []*reliantv1.Edge{
			{From: "scope_loop", Default: []string{"wave_loop"}},
		},
	}

	result := StaticAnalysis(wf, nil)

	// Should NOT have errors about iter.item.name, iter.item.goal, iter.item.has_frontend
	for _, e := range result.Errors() {
		if e.Severity == SeverityError && strings.Contains(e.Message, "iter.item") {
			t.Errorf("unexpected error about iter.item access: %s (path: %s)", e.Message, pathToString(e.Path))
		}
	}
}

// TestIterItemTypeInference_InvalidField tests that accessing a nonexistent field
// on iter.item produces a validation error when the item type is known.
func TestIterItemTypeInference_InvalidField(t *testing.T) {
	responseSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"waves": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name": map[string]interface{}{"type": "string"},
						"goal": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}

	wf := &reliantv1.Workflow{
		Name:  "test-iter-type-invalid",
		Entry: []string{"scope_loop"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "scope_loop",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{
					Workflow: &reliantv1.SubWorkflowArgs{
						Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://structured-agent"}},
						Args: map[string]*structpb.Value{
							"response_schema": makeResponseSchemaStruct(responseSchema),
						},
					},
				},
			},
			{
				Id:   "wave_loop",
				Type: "loop",
				Args: &reliantv1.Node_Loop{
					Loop: &reliantv1.LoopArgs{
						Items: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "nodes.scope_loop.response.waves"}},
						Ref:   &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://some-wave-workflow"}},
						While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 10"},
						Args: map[string]*structpb.Value{
							// Invalid field — has_frontend doesn't exist in this schema
							"has_frontend": structpb.NewStringValue("{{iter.item.has_frontend}}"),
						},
					},
				},
			},
		},
		Edges: []*reliantv1.Edge{
			{From: "scope_loop", Default: []string{"wave_loop"}},
		},
	}

	result := StaticAnalysis(wf, nil)

	// Debug: print all errors to understand what's happening
	for _, e := range result.Errors() {
		t.Logf("[%s] %s: %s", e.Severity, pathToString(e.Path), e.Message)
	}

	// Should have an error about iter.item.has_frontend being invalid
	found := false
	for _, e := range result.Errors() {
		if strings.Contains(e.Message, "has_frontend") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected validation error about iter.item.has_frontend, got errors: %v", formatErrors(result))
	}
}

// TestIterItemTypeInference_FallbackToDyn tests that when iter.item type can't be
// inferred (no response_schema), validation falls back to dyn and allows any field.
func TestIterItemTypeInference_FallbackToDyn(t *testing.T) {
	wf := &reliantv1.Workflow{
		Name:  "test-iter-type-dyn",
		Entry: []string{"data_source"},
		Nodes: []*reliantv1.Node{
			{
				Id:   "data_source",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{
					Workflow: &reliantv1.SubWorkflowArgs{
						Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://some-workflow"}},
						// No response_schema arg
					},
				},
			},
			{
				Id:   "my_loop",
				Type: "loop",
				Args: &reliantv1.Node_Loop{
					Loop: &reliantv1.LoopArgs{
						Items: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "nodes.data_source.items"}},
						Ref:   &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://worker"}},
						While: &reliantv1.DirectCelBool{Expr: "iter.iteration < 10"},
						Args: map[string]*structpb.Value{
							// Any field access should be fine with dyn
							"whatever": structpb.NewStringValue("{{iter.item.anything_goes}}"),
						},
					},
				},
			},
		},
		Edges: []*reliantv1.Edge{
			{From: "data_source", Default: []string{"my_loop"}},
		},
	}

	result := StaticAnalysis(wf, nil)

	// Should NOT have errors about iter.item field access (dyn allows anything)
	for _, e := range result.Errors() {
		if e.Severity == SeverityError && strings.Contains(e.Message, "iter.item") {
			t.Errorf("unexpected error about iter.item access with dyn fallback: %s", e.Message)
		}
	}
}

// TestInferLoopItemFields_ParseNodeFieldPath tests the expression parser.
func TestInferLoopItemFields_ParseNodeFieldPath(t *testing.T) {
	tests := []struct {
		expr     string
		wantNil  bool
		wantNode string
		wantPath []string
	}{
		{"nodes.scope_loop.response.waves", false, "scope_loop", []string{"response", "waves"}},
		{"nodes.my_node.items", false, "my_node", []string{"items"}},
		{"nodes.a-b.x.y.z", false, "a-b", []string{"x", "y", "z"}},
		{"inputs.items", true, "", nil},   // not a nodes.* expression
		{"something_else", true, "", nil}, // not a nodes.* expression
		{"nodes.foo", true, "", nil},      // no field path
		{"", true, "", nil},               // empty
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			result := parseNodeFieldPath(tt.expr)
			if tt.wantNil {
				if result != nil {
					t.Errorf("expected nil, got %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatalf("expected non-nil, got nil")
			}
			if result.nodeID != tt.wantNode {
				t.Errorf("nodeID: got %q, want %q", result.nodeID, tt.wantNode)
			}
			if len(result.fieldPath) != len(tt.wantPath) {
				t.Fatalf("fieldPath length: got %d, want %d", len(result.fieldPath), len(tt.wantPath))
			}
			for i, p := range result.fieldPath {
				if p != tt.wantPath[i] {
					t.Errorf("fieldPath[%d]: got %q, want %q", i, p, tt.wantPath[i])
				}
			}
		})
	}
}

// TestInferLoopItemFields_Direct tests the inference function directly.
func TestInferLoopItemFields_Direct(t *testing.T) {
	responseSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"waves": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"name":         map[string]interface{}{"type": "string"},
						"has_frontend": map[string]interface{}{"type": "boolean"},
					},
				},
			},
		},
	}

	wf := &reliantv1.Workflow{
		Name: "test",
		Nodes: []*reliantv1.Node{
			{
				Id:   "scope_loop",
				Type: "workflow",
				Args: &reliantv1.Node_Workflow{
					Workflow: &reliantv1.SubWorkflowArgs{
						Ref: &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "builtin://structured-agent"}},
						Args: map[string]*structpb.Value{
							"response_schema": makeResponseSchemaStruct(responseSchema),
						},
					},
				},
			},
		},
	}

	loopArgs := &reliantv1.LoopArgs{
		Items: &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "nodes.scope_loop.response.waves"}},
	}

	fields := inferLoopItemFields(loopArgs, wf, nil)
	if fields == nil {
		t.Fatal("expected non-nil fields, got nil")
	}

	// Should have 'name' and 'has_frontend' fields
	if _, ok := fields["name"]; !ok {
		t.Error("expected 'name' field in inferred iter.item fields")
	}
	if _, ok := fields["has_frontend"]; !ok {
		t.Error("expected 'has_frontend' field in inferred iter.item fields")
	}
	// Should not have a nonexistent field
	if _, ok := fields["nonexistent"]; ok {
		t.Error("unexpected 'nonexistent' field in inferred iter.item fields")
	}
}

// TestTypedIterCELCompilation directly tests that the CEL environment rejects
// unknown fields on iter.item when the type is known.
func TestTypedIterCELCompilation(t *testing.T) {
	typeCtx := &WorkflowTypeContext{
		InputFields:  make(map[string]*FieldInfo),
		InputGroups:  make(map[string]map[string]*FieldInfo),
		NodeOutputs:  make(map[string]map[string]*FieldInfo),
		OutputFields: make(map[string]*FieldInfo),
		NodeTypes:    make(map[string]string),
		Registry:     sharedRegistry,
		IterItemFields: map[string]*FieldInfo{
			"name": {Name: "name", Kind: reflect.String},
			"goal": {Name: "goal", Kind: reflect.String},
		},
	}

	env, err := newValidationCELEnv([]wfcel.CELNamespace{
		wfcel.CELInputs,
		wfcel.CELWorkflow,
		wfcel.CELNodes,
		wfcel.CELIter,
	}, typeCtx)
	if err != nil {
		t.Fatalf("failed to create CEL env: %v", err)
	}

	// Valid field access should compile
	_, issues := env.Compile("iter.item.name")
	if issues != nil && issues.Err() != nil {
		t.Errorf("iter.item.name should compile, got error: %v", issues.Err())
	}

	// Invalid field access should fail
	_, issues = env.Compile("iter.item.has_frontend")
	if issues == nil || issues.Err() == nil {
		t.Error("iter.item.has_frontend should fail compilation when not in schema")
	}

	// iter.iteration and iter.key should work
	_, issues = env.Compile("iter.iteration")
	if issues != nil && issues.Err() != nil {
		t.Errorf("iter.iteration should compile, got error: %v", issues.Err())
	}

	_, issues = env.Compile("iter.key")
	if issues != nil && issues.Err() != nil {
		t.Errorf("iter.key should compile, got error: %v", issues.Err())
	}
}

func formatErrors(r *Result) string {
	var sb strings.Builder
	for _, e := range r.Errors() {
		sb.WriteString("\n  - [")
		sb.WriteString(e.Severity.String())
		sb.WriteString("] ")
		sb.WriteString(pathToString(e.Path))
		sb.WriteString(": ")
		sb.WriteString(e.Message)
	}
	return sb.String()
}
