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

// A PATCH body must leave out fields the caller did not pass: sending
// `"body": null` to GitHub's issue update clears the body. CEL's optional
// syntax (`?"key": params.?key`) expresses "only if present".
func TestOptionalSyntaxOmitsAbsentKeys(t *testing.T) {
	expr := `{"title": params.title, ?"body": params.?body, ?"labels": params.?labels}`
	if err := ValidateExpr(expr); err != nil {
		t.Fatalf("optional syntax must compile: %v", err)
	}
	got, err := EvalExpr(expr, map[string]any{"params": map[string]any{"title": "t", "labels": []any{"x"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"title": "t", "labels": []any{"x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
	got, err = EvalExpr(`response.?user.?login.orValue(null)`, map[string]any{"response": map[string]any{"user": nil}})
	if err != nil || got != nil {
		t.Fatalf("a null object on the path must yield null, got %#v, %v", got, err)
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

// GitHub's contents API sends a file base64-encoded, wrapped at 60 columns.
// base64.decode undoes it, newlines and all, and .text() yields the string
// only when the bytes are text: an image is an absent value, never an error
// that would fail the whole output.
func TestBase64DecodeAndText(t *testing.T) {
	expr := `base64.decode(response.content).text().orValue(null)`
	for _, tc := range []struct {
		name    string
		content string
		want    any
	}{
		{"wrapped text", "cGFja2FnZSBt\nYWluCg==\n", "package main\n"},
		{"unpadded", "aGk", "hi"},
		{"empty file", "", ""},
		{"utf-8", "aMOpbGxv", "héllo"},
		{"invalid utf-8", "/w==", nil},
		{"nul byte", "YQBi", nil},
	} {
		got, err := EvalExpr(expr, map[string]any{"response": map[string]any{"content": tc.content}})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %#v want %#v", tc.name, got, tc.want)
		}
	}
	if _, err := EvalExpr(`base64.decode("not base64!")`, nil); err == nil {
		t.Error("malformed base64 is an error")
	}
	got, err := EvalExpr(`base64.encode(b"hi")`, nil)
	if err != nil || got != "aGk=" {
		t.Errorf("base64.encode: got %#v, %v", got, err)
	}
}

func TestHasExpr(t *testing.T) {
	if !HasExpr("a{{b}}") || HasExpr("plain") {
		t.Fatal("HasExpr wrong")
	}
}
