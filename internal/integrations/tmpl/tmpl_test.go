package tmpl

import (
	"reflect"
	"testing"
)

func TestRenderStringInterpolation(t *testing.T) {
	got, err := Render("/repos/{{ params.owner }}/{{ params.repo }}", map[string]any{"params": map[string]any{"owner": "a b", "repo": "r"}}, Options{EscapePath: true})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/repos/a%20b/r" {
		t.Fatalf("got %v", got)
	}
}

func TestSingleExprKeepsNativeType(t *testing.T) {
	vars := map[string]any{"params": map[string]any{"labels": []any{"x", "y"}, "n": 3.0, "ok": true}}
	for expr, want := range map[string]any{
		"{{ params.labels }}": []any{"x", "y"},
		"{{ params.n }}":      3.0,
		"{{ params.ok }}":     true,
	} {
		got, err := Render(expr, vars, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %#v want %#v", expr, got, want)
		}
	}
}

func TestMixedExprStringifies(t *testing.T) {
	got, err := Render("n={{ params.n }}", map[string]any{"params": map[string]any{"n": 3.0}}, Options{})
	if err != nil || got != "n=3" {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestMissingParamRendersAsError(t *testing.T) {
	if _, err := Render("{{ params.nope }}", map[string]any{"params": map[string]any{}}, Options{}); err == nil {
		t.Fatal("referencing an absent param is an error")
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("{{ params.a + }}"); err == nil {
		t.Error("syntax error must fail")
	}
	if err := Validate("{{ params.a"); err == nil {
		t.Error("unterminated must fail")
	}
	if err := Validate("plain {{ params.a }} {{ response.b }}"); err != nil {
		t.Error(err)
	}
}

func TestEvalExpr(t *testing.T) {
	got, err := EvalExpr(`response.items.map(i, i.id)`, map[string]any{"response": map[string]any{"items": []any{map[string]any{"id": 1.0}, map[string]any{"id": 2.0}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []any{1.0, 2.0}) {
		t.Fatalf("got %#v", got)
	}
}

func TestHasExpr(t *testing.T) {
	if !HasExpr("a{{b}}") || HasExpr("plain") {
		t.Fatal("HasExpr wrong")
	}
}
