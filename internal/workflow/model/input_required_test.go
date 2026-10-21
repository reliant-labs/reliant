package model

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestIsInputRequiredHonoursTheRequiredFlag(t *testing.T) {
	def := structpb.NewStringValue("x")
	tests := []struct {
		name  string
		input *reliantv1.Input
		want  bool
	}{
		{"string without a default", &reliantv1.Input{Type: "string", Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{}}}, true},
		{"string with a default", &reliantv1.Input{Type: "string", Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{Default: strPtr("x")}}}, false},
		{"array with an empty default is optional", &reliantv1.Input{Type: "array", Config: &reliantv1.Input_ArrayInput{ArrayInput: &reliantv1.ArrayInputConfig{Default: structpb.NewListValue(&structpb.ListValue{})}}}, false},
		{"array with an empty default and required: true", &reliantv1.Input{Type: "array", Config: &reliantv1.Input_ArrayInput{ArrayInput: &reliantv1.ArrayInputConfig{
			Base: &reliantv1.InputBase{Required: true}, Default: structpb.NewListValue(&structpb.ListValue{})}}}, true},
		{"integration input without a default", &reliantv1.Input{Type: "github.repository", Config: &reliantv1.Input_IntegrationInput{IntegrationInput: &reliantv1.IntegrationInputConfig{}}}, true},
		{"integration input with a default", &reliantv1.Input{Type: "github.repository", Config: &reliantv1.Input_IntegrationInput{IntegrationInput: &reliantv1.IntegrationInputConfig{Default: def}}}, false},
		{"integration input with required: true", &reliantv1.Input{Type: "github.repository", Config: &reliantv1.Input_IntegrationInput{IntegrationInput: &reliantv1.IntegrationInputConfig{Base: &reliantv1.InputBase{Required: true}}}}, true},
		{"nil", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsInputRequired(tt.input); got != tt.want {
				t.Errorf("IsInputRequired = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetInputBaseCoversEveryConfigArm(t *testing.T) {
	base := &reliantv1.InputBase{Description: "d", Required: true}
	inputs := map[string]*reliantv1.Input{
		"string":      {Config: &reliantv1.Input_StringInput{StringInput: &reliantv1.StringInputConfig{Base: base}}},
		"number":      {Config: &reliantv1.Input_NumberInput{NumberInput: &reliantv1.NumberInputConfig{Base: base}}},
		"integer":     {Config: &reliantv1.Input_IntegerInput{IntegerInput: &reliantv1.IntegerInputConfig{Base: base}}},
		"boolean":     {Config: &reliantv1.Input_BooleanInput{BooleanInput: &reliantv1.BooleanInputConfig{Base: base}}},
		"enum":        {Config: &reliantv1.Input_EnumInput{EnumInput: &reliantv1.EnumInputConfig{Base: base}}},
		"model":       {Config: &reliantv1.Input_ModelInput{ModelInput: &reliantv1.ModelInputConfig{Base: base}}},
		"message":     {Config: &reliantv1.Input_MessageInput{MessageInput: &reliantv1.MessageInputConfig{Base: base}}},
		"attachments": {Config: &reliantv1.Input_AttachmentsInput{AttachmentsInput: &reliantv1.AttachmentsInputConfig{Base: base}}},
		"tools":       {Config: &reliantv1.Input_ToolsInput{ToolsInput: &reliantv1.ToolsInputConfig{Base: base}}},
		"array":       {Config: &reliantv1.Input_ArrayInput{ArrayInput: &reliantv1.ArrayInputConfig{Base: base}}},
		"object":      {Config: &reliantv1.Input_ObjectInput{ObjectInput: &reliantv1.ObjectInputConfig{Base: base}}},
		"any":         {Config: &reliantv1.Input_AnyInput{AnyInput: &reliantv1.AnyInputConfig{Base: base}}},
		"group":       {Config: &reliantv1.Input_GroupInput{GroupInput: &reliantv1.GroupInputConfig{Base: base}}},
		"preset":      {Config: &reliantv1.Input_PresetInput{PresetInput: &reliantv1.PresetInputConfig{Base: base}}},
		"integration": {Config: &reliantv1.Input_IntegrationInput{IntegrationInput: &reliantv1.IntegrationInputConfig{Base: base}}},
	}
	for name, in := range inputs {
		if got := GetInputBase(in); got != base {
			t.Errorf("%s: GetInputBase did not return the arm's base", name)
		}
	}
}

func TestSplitIntegrationType(t *testing.T) {
	tests := []struct {
		ref         string
		integration string
		typeID      string
		ok          bool
	}{
		{"github.repository", "github", "repository", true},
		{"string", "", "", false},
		{"a.b.c", "", "", false},
		{".b", "", "", false},
		{"a.", "", "", false},
	}
	for _, tt := range tests {
		i, ty, ok := SplitIntegrationType(tt.ref)
		if i != tt.integration || ty != tt.typeID || ok != tt.ok {
			t.Errorf("SplitIntegrationType(%q) = %q, %q, %v", tt.ref, i, ty, ok)
		}
	}
}

func strPtr(s string) *string { return &s }
