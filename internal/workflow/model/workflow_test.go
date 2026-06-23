package model

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestGetEntryNodes(t *testing.T) {
	if GetEntryNodes(nil) != nil {
		t.Error("nil workflow should return nil")
	}

	wf := &reliantv1.Workflow{}
	if GetEntryNodes(wf) != nil {
		t.Error("no entry should return nil")
	}

	wf = &reliantv1.Workflow{
		Entry: []string{"node-1", "node-2"},
	}
	got := GetEntryNodes(wf)
	if len(got) != 2 || got[0] != "node-1" || got[1] != "node-2" {
		t.Errorf("got %v", got)
	}
}

func TestFindEdgesFrom(t *testing.T) {
	if FindEdgesFrom(nil, "x") != nil {
		t.Error("nil workflow should return nil")
	}

	e1 := &reliantv1.Edge{From: "a", Default: []string{"b"}}
	e2 := &reliantv1.Edge{From: "b", Default: []string{"c"}}
	e3 := &reliantv1.Edge{From: "a", Default: []string{"d"}}

	wf := &reliantv1.Workflow{
		Edges: []*reliantv1.Edge{e1, e2, e3},
	}

	edges := FindEdgesFrom(wf, "a")
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges from 'a', got %d", len(edges))
	}
	if edges[0] != e1 || edges[1] != e3 {
		t.Error("wrong edges returned")
	}

	edges = FindEdgesFrom(wf, "b")
	if len(edges) != 1 || edges[0] != e2 {
		t.Error("wrong edges for 'b'")
	}

	edges = FindEdgesFrom(wf, "nonexistent")
	if len(edges) != 0 {
		t.Error("should find no edges for nonexistent")
	}
}

func TestGetInputType(t *testing.T) {
	if GetInputType(nil) != "" {
		t.Error("nil should return empty")
	}
	input := &reliantv1.Input{Type: "string"}
	if GetInputType(input) != "string" {
		t.Errorf("got %q", GetInputType(input))
	}
}

func TestIsInputRequired(t *testing.T) {
	// No default → required
	input := &reliantv1.Input{
		Type: "string",
		Config: &reliantv1.Input_StringInput{
			StringInput: &reliantv1.StringInputConfig{},
		},
	}
	if !IsInputRequired(input) {
		t.Error("no default should be required")
	}

	// With default → not required
	defaultVal := "hello"
	input = &reliantv1.Input{
		Type: "string",
		Config: &reliantv1.Input_StringInput{
			StringInput: &reliantv1.StringInputConfig{
				Default: &defaultVal,
			},
		},
	}
	if IsInputRequired(input) {
		t.Error("with default should not be required")
	}
}

func TestGetInputDefault(t *testing.T) {
	// nil
	if GetInputDefault(nil) != nil {
		t.Error("nil should return nil")
	}

	// String with default
	defaultStr := "hello"
	input := &reliantv1.Input{
		Type: "string",
		Config: &reliantv1.Input_StringInput{
			StringInput: &reliantv1.StringInputConfig{Default: &defaultStr},
		},
	}
	if got := GetInputDefault(input); got != "hello" {
		t.Errorf("string default = %v", got)
	}

	// Number with default
	defaultNum := 3.14
	input = &reliantv1.Input{
		Type: "number",
		Config: &reliantv1.Input_NumberInput{
			NumberInput: &reliantv1.NumberInputConfig{Default: &defaultNum},
		},
	}
	if got := GetInputDefault(input); got != 3.14 {
		t.Errorf("number default = %v", got)
	}

	// Integer with default
	defaultInt := int64(42)
	input = &reliantv1.Input{
		Type: "integer",
		Config: &reliantv1.Input_IntegerInput{
			IntegerInput: &reliantv1.IntegerInputConfig{Default: &defaultInt},
		},
	}
	if got := GetInputDefault(input); got != int64(42) {
		t.Errorf("integer default = %v", got)
	}

	// Boolean with default
	defaultBool := true
	input = &reliantv1.Input{
		Type: "boolean",
		Config: &reliantv1.Input_BooleanInput{
			BooleanInput: &reliantv1.BooleanInputConfig{Default: &defaultBool},
		},
	}
	if got := GetInputDefault(input); got != true {
		t.Errorf("boolean default = %v", got)
	}

	// Enum with default
	enumDefault, _ := structpb.NewValue("option_a")
	input = &reliantv1.Input{
		Type: "enum",
		Config: &reliantv1.Input_EnumInput{
			EnumInput: &reliantv1.EnumInputConfig{Default: enumDefault},
		},
	}
	if got := GetInputDefault(input); got != "option_a" {
		t.Errorf("enum default = %v", got)
	}

	// Any with default
	anyDefault, _ := structpb.NewValue(map[string]interface{}{"key": "val"})
	input = &reliantv1.Input{
		Type: "any",
		Config: &reliantv1.Input_AnyInput{
			AnyInput: &reliantv1.AnyInputConfig{Default: anyDefault},
		},
	}
	got := GetInputDefault(input)
	if m, ok := got.(map[string]interface{}); !ok || m["key"] != "val" {
		t.Errorf("any default = %v", got)
	}

	// Model with default selector
	input = &reliantv1.Input{
		Type: "model",
		Config: &reliantv1.Input_ModelInput{
			ModelInput: &reliantv1.ModelInputConfig{Default: &reliantv1.ModelSelector{
				Id:        "gpt-5",
				Tags:      []string{"flagship"},
				Providers: []string{"openai"},
			}},
		},
	}
	modelDefault, ok := GetInputDefault(input).(map[string]interface{})
	if !ok {
		t.Fatalf("model default expected map, got %T", GetInputDefault(input))
	}
	if modelDefault["id"] != "gpt-5" {
		t.Errorf("model id default = %v", modelDefault["id"])
	}

	// No config (e.g., model input without default)
	input = &reliantv1.Input{
		Type: "model",
		Config: &reliantv1.Input_ModelInput{
			ModelInput: &reliantv1.ModelInputConfig{},
		},
	}
	if GetInputDefault(input) != nil {
		t.Error("model input without default should return nil")
	}
}

func TestGetInputDefault_ModelSelectorEdgeCases(t *testing.T) {
	t.Run("empty selector returns nil", func(t *testing.T) {
		input := &reliantv1.Input{
			Type: "model",
			Config: &reliantv1.Input_ModelInput{
				ModelInput: &reliantv1.ModelInputConfig{Default: &reliantv1.ModelSelector{}},
			},
		}
		if got := GetInputDefault(input); got != nil {
			t.Fatalf("expected nil for empty selector, got %#v", got)
		}
	})

	t.Run("selector with only providers still materializes provider defaults", func(t *testing.T) {
		input := &reliantv1.Input{
			Type: "model",
			Config: &reliantv1.Input_ModelInput{
				ModelInput: &reliantv1.ModelInputConfig{Default: &reliantv1.ModelSelector{
					Providers: []string{"openai", "anthropic"},
				}},
			},
		}

		selector, ok := GetInputDefault(input).(map[string]interface{})
		if !ok {
			t.Fatalf("expected selector map for providers-only default, got %T", GetInputDefault(input))
		}
		providers, ok := selector["providers"].([]interface{})
		if !ok {
			t.Fatalf("expected providers array in selector default, got %#v", selector)
		}
		if len(providers) != 2 || providers[0] != "openai" || providers[1] != "anthropic" {
			t.Fatalf("unexpected provider defaults: %#v", providers)
		}
	})
}
