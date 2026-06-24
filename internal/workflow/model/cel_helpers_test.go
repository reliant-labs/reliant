package model

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// --- CelString tests ---

func TestCelStringIsSet(t *testing.T) {
	tests := []struct {
		name string
		c    *reliantv1.CelString
		want bool
	}{
		{"nil", nil, false},
		{"empty", &reliantv1.CelString{}, false},
		{"literal", &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello"}}, true},
		{"expr", &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "inputs.x"}}, true},
		{"empty literal", &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: ""}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CelStringIsSet(tt.c); got != tt.want {
				t.Errorf("CelStringIsSet() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCelStringIsExpr(t *testing.T) {
	tests := []struct {
		name string
		c    *reliantv1.CelString
		want bool
	}{
		{"nil", nil, false},
		{"literal", &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello"}}, false},
		{"expr", &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "inputs.x"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CelStringIsExpr(tt.c); got != tt.want {
				t.Errorf("CelStringIsExpr() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCelStringValue(t *testing.T) {
	tests := []struct {
		name string
		c    *reliantv1.CelString
		want string
	}{
		{"nil", nil, ""},
		{"literal", &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello"}}, "hello"},
		{"expr returns empty", &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "inputs.x"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CelStringValue(tt.c); got != tt.want {
				t.Errorf("CelStringValue() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCelStringExpr(t *testing.T) {
	tests := []struct {
		name string
		c    *reliantv1.CelString
		want string
	}{
		{"nil", nil, ""},
		{"literal returns empty", &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello"}}, ""},
		{"expr", &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "inputs.x"}}, "inputs.x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CelStringExpr(tt.c); got != tt.want {
				t.Errorf("CelStringExpr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCelStringRaw(t *testing.T) {
	tests := []struct {
		name string
		c    *reliantv1.CelString
		want string
	}{
		{"nil", nil, ""},
		{"literal", &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "5m"}}, "5m"},
		{"expr", &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "inputs.timeout"}}, "inputs.timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CelStringRaw(tt.c); got != tt.want {
				t.Errorf("CelStringRaw() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCelStringField(t *testing.T) {
	tests := []struct {
		name     string
		c        *reliantv1.CelString
		wantVal  string
		wantExpr bool
		wantSet  bool
	}{
		{"nil", nil, "", false, false},
		{"literal", &reliantv1.CelString{Value: &reliantv1.CelString_Literal{Literal: "hello"}}, "hello", false, true},
		{"expr", &reliantv1.CelString{Value: &reliantv1.CelString_Expr{Expr: "inputs.x"}}, "inputs.x", true, true},
		{"empty no value", &reliantv1.CelString{}, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, isExpr, isSet := CelStringField(tt.c)
			if val != tt.wantVal {
				t.Errorf("val = %q, want %q", val, tt.wantVal)
			}
			if isExpr != tt.wantExpr {
				t.Errorf("isExpr = %v, want %v", isExpr, tt.wantExpr)
			}
			if isSet != tt.wantSet {
				t.Errorf("isSet = %v, want %v", isSet, tt.wantSet)
			}
		})
	}
}

// --- CelBool tests ---

func TestCelBoolIsSet(t *testing.T) {
	tests := []struct {
		name string
		c    *reliantv1.CelBool
		want bool
	}{
		{"nil", nil, false},
		{"empty", &reliantv1.CelBool{}, false},
		{"literal true", &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}, true},
		{"literal false", &reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: false}}, true},
		{"expr", &reliantv1.CelBool{Value: &reliantv1.CelBool_Expr{Expr: "inputs.flag"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CelBoolIsSet(tt.c); got != tt.want {
				t.Errorf("CelBoolIsSet() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCelBoolIsExpr(t *testing.T) {
	if CelBoolIsExpr(nil) {
		t.Error("CelBoolIsExpr(nil) = true")
	}
	if CelBoolIsExpr(&reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}) {
		t.Error("CelBoolIsExpr(literal) = true")
	}
	if !CelBoolIsExpr(&reliantv1.CelBool{Value: &reliantv1.CelBool_Expr{Expr: "inputs.flag"}}) {
		t.Error("CelBoolIsExpr(expr) = false")
	}
}

func TestCelBoolValue(t *testing.T) {
	if CelBoolValue(nil) != false {
		t.Error("CelBoolValue(nil) != false")
	}
	if CelBoolValue(&reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}) != true {
		t.Error("CelBoolValue(true) != true")
	}
	if CelBoolValue(&reliantv1.CelBool{Value: &reliantv1.CelBool_Expr{Expr: "x"}}) != false {
		t.Error("CelBoolValue(expr) != false")
	}
}

func TestCelBoolExpr(t *testing.T) {
	if CelBoolExpr(nil) != "" {
		t.Error("CelBoolExpr(nil) != empty")
	}
	if CelBoolExpr(&reliantv1.CelBool{Value: &reliantv1.CelBool_Literal{Literal: true}}) != "" {
		t.Error("CelBoolExpr(literal) != empty")
	}
	if CelBoolExpr(&reliantv1.CelBool{Value: &reliantv1.CelBool_Expr{Expr: "inputs.flag"}}) != "inputs.flag" {
		t.Error("CelBoolExpr(expr) != inputs.flag")
	}
}

// --- CelDouble tests ---

func TestCelDoubleIsSet(t *testing.T) {
	if CelDoubleIsSet(nil) {
		t.Error("nil should not be set")
	}
	if !CelDoubleIsSet(&reliantv1.CelDouble{Value: &reliantv1.CelDouble_Literal{Literal: 0.5}}) {
		t.Error("literal should be set")
	}
	if !CelDoubleIsSet(&reliantv1.CelDouble{Value: &reliantv1.CelDouble_Expr{Expr: "x"}}) {
		t.Error("expr should be set")
	}
}

func TestCelDoubleIsExpr(t *testing.T) {
	if CelDoubleIsExpr(nil) {
		t.Error("nil should not be expr")
	}
	if CelDoubleIsExpr(&reliantv1.CelDouble{Value: &reliantv1.CelDouble_Literal{Literal: 0.5}}) {
		t.Error("literal should not be expr")
	}
	if !CelDoubleIsExpr(&reliantv1.CelDouble{Value: &reliantv1.CelDouble_Expr{Expr: "x"}}) {
		t.Error("expr should be expr")
	}
}

func TestCelDoubleValue(t *testing.T) {
	if CelDoubleValue(nil) != 0 {
		t.Error("nil should be 0")
	}
	if CelDoubleValue(&reliantv1.CelDouble{Value: &reliantv1.CelDouble_Literal{Literal: 0.7}}) != 0.7 {
		t.Error("literal should be 0.7")
	}
}

func TestCelDoubleExpr(t *testing.T) {
	if CelDoubleExpr(nil) != "" {
		t.Error("nil should be empty")
	}
	if CelDoubleExpr(&reliantv1.CelDouble{Value: &reliantv1.CelDouble_Expr{Expr: "inputs.temp"}}) != "inputs.temp" {
		t.Error("expr should be inputs.temp")
	}
}

// --- CelInt tests ---

func TestCelIntIsSet(t *testing.T) {
	if CelIntIsSet(nil) {
		t.Error("nil should not be set")
	}
	if !CelIntIsSet(&reliantv1.CelInt{Value: &reliantv1.CelInt_Literal{Literal: 100}}) {
		t.Error("literal should be set")
	}
}

func TestCelIntIsExpr(t *testing.T) {
	if CelIntIsExpr(nil) {
		t.Error("nil should not be expr")
	}
	if !CelIntIsExpr(&reliantv1.CelInt{Value: &reliantv1.CelInt_Expr{Expr: "inputs.max"}}) {
		t.Error("expr should be expr")
	}
}

func TestCelIntValue(t *testing.T) {
	if CelIntValue(nil) != 0 {
		t.Error("nil should be 0")
	}
	if CelIntValue(&reliantv1.CelInt{Value: &reliantv1.CelInt_Literal{Literal: 42}}) != 42 {
		t.Errorf("literal should be 42, got %d", CelIntValue(&reliantv1.CelInt{Value: &reliantv1.CelInt_Literal{Literal: 42}}))
	}
}

func TestCelIntExpr(t *testing.T) {
	if CelIntExpr(nil) != "" {
		t.Error("nil should be empty")
	}
	if got := CelIntExpr(&reliantv1.CelInt{Value: &reliantv1.CelInt_Expr{Expr: "inputs.max"}}); got != "inputs.max" {
		t.Errorf("got %q, want inputs.max", got)
	}
}

// --- CelStringList tests ---

func TestCelStringListIsSet(t *testing.T) {
	if CelStringListIsSet(nil) {
		t.Error("nil should not be set")
	}
	if CelStringListIsSet(&reliantv1.CelStringList{}) {
		t.Error("empty should not be set")
	}
	if !CelStringListIsSet(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"view"}}}}) {
		t.Error("literal should be set")
	}
	if !CelStringListIsSet(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Expr{Expr: "{{inputs.tools}}"}}) {
		t.Error("expr should be set")
	}
}

func TestCelStringListIsExpr(t *testing.T) {
	if CelStringListIsExpr(nil) {
		t.Error("nil should not be expr")
	}
	if CelStringListIsExpr(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"view"}}}}) {
		t.Error("literal should not be expr")
	}
	if !CelStringListIsExpr(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Expr{Expr: "{{inputs.tools}}"}}) {
		t.Error("expr should be expr")
	}
}

func TestCelStringListValue(t *testing.T) {
	if CelStringListValue(nil) != nil {
		t.Error("nil should return nil")
	}
	if CelStringListValue(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Expr{Expr: "{{inputs.tools}}"}}) != nil {
		t.Error("expr should return nil value")
	}
	got := CelStringListValue(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"view", "edit"}}}})
	if len(got) != 2 || got[0] != "view" || got[1] != "edit" {
		t.Errorf("unexpected value: %v", got)
	}
}

func TestCelStringListExpr(t *testing.T) {
	if CelStringListExpr(nil) != "" {
		t.Error("nil should be empty")
	}
	if CelStringListExpr(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Literal{Literal: &reliantv1.StringList{Values: []string{"view"}}}}) != "" {
		t.Error("literal should return empty expression")
	}
	if got := CelStringListExpr(&reliantv1.CelStringList{Value: &reliantv1.CelStringList_Expr{Expr: "{{inputs.tools}}"}}); got != "{{inputs.tools}}" {
		t.Errorf("got %q", got)
	}
}

// --- CelModelSelector tests ---

func TestCelModelSelectorIsSet(t *testing.T) {
	if CelModelSelectorIsSet(nil) {
		t.Error("nil should not be set")
	}
	ms := &reliantv1.CelModelSelector{
		Value: &reliantv1.CelModelSelector_Literal{
			Literal: &reliantv1.ModelSelector{Tags: []string{"flagship"}},
		},
	}
	if !CelModelSelectorIsSet(ms) {
		t.Error("literal should be set")
	}
}

func TestCelModelSelectorIsExpr(t *testing.T) {
	if CelModelSelectorIsExpr(nil) {
		t.Error("nil should not be expr")
	}
	ms := &reliantv1.CelModelSelector{
		Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"},
	}
	if !CelModelSelectorIsExpr(ms) {
		t.Error("expr should be expr")
	}
}

func TestCelModelSelectorValue(t *testing.T) {
	if CelModelSelectorValue(nil) != nil {
		t.Error("nil should return nil")
	}
	expected := &reliantv1.ModelSelector{Tags: []string{"flagship"}}
	ms := &reliantv1.CelModelSelector{
		Value: &reliantv1.CelModelSelector_Literal{Literal: expected},
	}
	got := CelModelSelectorValue(ms)
	if got == nil || len(got.Tags) != 1 || got.Tags[0] != "flagship" {
		t.Errorf("unexpected value: %v", got)
	}
}

func TestCelModelSelectorExpr(t *testing.T) {
	if CelModelSelectorExpr(nil) != "" {
		t.Error("nil should be empty")
	}
	ms := &reliantv1.CelModelSelector{
		Value: &reliantv1.CelModelSelector_Expr{Expr: "inputs.model"},
	}
	if CelModelSelectorExpr(ms) != "inputs.model" {
		t.Errorf("got %q", CelModelSelectorExpr(ms))
	}
}

// --- DirectCelBool tests ---

func TestDirectCelExpr(t *testing.T) {
	if DirectCelExpr(nil) != "" {
		t.Error("nil should be empty")
	}
	if DirectCelExpr(&reliantv1.DirectCelBool{Expr: "inputs.run"}) != "inputs.run" {
		t.Error("should be inputs.run")
	}
	if DirectCelExpr(&reliantv1.DirectCelBool{}) != "" {
		t.Error("empty should be empty")
	}
}

func TestDirectCelIsSet(t *testing.T) {
	if DirectCelIsSet(nil) {
		t.Error("nil should not be set")
	}
	if DirectCelIsSet(&reliantv1.DirectCelBool{}) {
		t.Error("empty should not be set")
	}
	if !DirectCelIsSet(&reliantv1.DirectCelBool{Expr: "true"}) {
		t.Error("non-empty should be set")
	}
}
