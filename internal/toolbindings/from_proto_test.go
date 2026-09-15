// Copyright (c) 2025 Reliant Labs
package toolbindings

import (
	"testing"

	"google.golang.org/protobuf/types/known/structpb"
)

func mustStruct(t *testing.T, fields map[string]interface{}) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(fields)
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

func TestFromProtoStructs_ConvertsEachToolsParameters(t *testing.T) {
	got := FromProtoStructs(map[string]*structpb.Struct{
		"generate_image": mustStruct(t, map[string]interface{}{
			"size":    "1536x1024",
			"quality": "high",
		}),
		"view": mustStruct(t, map[string]interface{}{"limit": 100}),
	})

	if len(got) != 2 {
		t.Fatalf("expected bindings for 2 tools, got %d", len(got))
	}
	if v := got["generate_image"]["size"].Literal; v != "1536x1024" {
		t.Errorf("size: got %v", v)
	}
	// structpb numbers decode as float64; the binding must carry the value
	// through unchanged rather than coercing it to a string.
	if v := got["view"]["limit"].Literal; v != float64(100) {
		t.Errorf("limit: got %v (%T), want float64(100)", v, v)
	}
}

// Values arrive as literals, never expressions: CEL has already resolved any
// {{...}} template before these structs reach this function.
func TestFromProtoStructs_ProducesLiteralsNotExpressions(t *testing.T) {
	got := FromProtoStructs(map[string]*structpb.Struct{
		"generate_image": mustStruct(t, map[string]interface{}{"size": "1536x1024"}),
	})
	if got["generate_image"]["size"].IsExpr() {
		t.Error("binding should be a literal; an expression here would be re-evaluated by a weaker engine")
	}
}

func TestFromProtoStructs_NestedObjectSurvives(t *testing.T) {
	got := FromProtoStructs(map[string]*structpb.Struct{
		"generate_image": mustStruct(t, map[string]interface{}{
			"model": map[string]interface{}{
				"tags":      []interface{}{"image-gen"},
				"providers": []interface{}{"codex"},
			},
		}),
	})

	model, ok := got["generate_image"]["model"].Literal.(map[string]interface{})
	if !ok {
		t.Fatalf("model should stay a nested object, got %T", got["generate_image"]["model"].Literal)
	}
	tags, ok := model["tags"].([]interface{})
	if !ok || len(tags) != 1 || tags[0] != "image-gen" {
		t.Errorf("model.tags: got %v", model["tags"])
	}
}

// Nil rather than an empty map, so Scopes.ToolNames does not report a tool
// nobody configured and the logs stay honest about what was applied.
func TestFromProtoStructs_EmptyInputsProduceNoBindings(t *testing.T) {
	for name, input := range map[string]map[string]*structpb.Struct{
		"nil map":             nil,
		"empty map":           {},
		"tool with no params": {"generate_image": mustStruct(t, map[string]interface{}{})},
	} {
		t.Run(name, func(t *testing.T) {
			if got := FromProtoStructs(input); got != nil {
				t.Errorf("expected nil, got %v", got)
			}
		})
	}
}
