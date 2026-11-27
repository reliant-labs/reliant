package schemagen

import (
	"encoding/json"
	"strings"
	"testing"
)

func compile(t *testing.T, schema string) *Compiled {
	t.Helper()
	c, err := Compile([]byte(schema))
	if err != nil {
		t.Fatalf("Compile(%s): %v", schema, err)
	}
	return c
}

// Each row is one schema keyword and a document that breaks it, with the
// message a user reads back.
func TestValidateKeywordFailures(t *testing.T) {
	cases := []struct {
		name, schema, doc, want string
	}{
		{"type mismatch", `{"type":"string"}`, `1`, "must be string, not integer"},
		{"type list names alternatives", `{"type":["string","boolean"]}`, `1`, "must be string or boolean, not integer"},
		{"type list with null says so", `{"type":["string","null"]}`, `true`, "must be string (or null), not boolean"},
		{"number kind is distinct from integer", `{"type":"integer"}`, `1.5`, "must be integer, not number"},
		{"integer accepts a whole float", `{"type":"integer"}`, `2.0`, ""},
		{"integer accepts int64", `{"type":"integer"}`, `9007199254740993`, ""},
		{"integer accepts a huge exponent that is whole", `{"type":"integer"}`, `1e30`, ""},
		{"number accepts integers", `{"type":"number"}`, `3`, ""},
		{"boolean", `{"type":"boolean"}`, `"x"`, "must be boolean, not string"},
		{"array", `{"type":"array"}`, `{}`, "must be array, not object"},
		{"object", `{"type":"object"}`, `[]`, "must be object, not array"},
		{"null", `{"type":"null"}`, `0`, "must be null, not integer"},
		{"unknown type name matches nothing", `{"type":"banana"}`, `1`, "must be banana, not integer"},
		{"const mismatch", `{"const":"a"}`, `"b"`, `must be "a", not "b"`},
		{"const match", `{"const":1}`, `1`, ""},
		{"enum miss lists the choices", `{"enum":["a","b"]}`, `"c"`, `"c" is not one of "a", "b"`},
		{"enum hit", `{"enum":["a",2]}`, `2`, ""},
		{"minimum", `{"minimum":0}`, `-1`, "-1 is less than the minimum 0"},
		{"minimum equal passes", `{"minimum":0}`, `0`, ""},
		{"minimum ignores non-numbers", `{"minimum":0}`, `"x"`, ""},
		{"items validated per index", `{"type":"array","items":{"type":"string"}}`, `["a",2,"c",true]`, "[1]: must be string, not integer"},
		{"additionalProperties schema applies to extras", `{"type":"object","additionalProperties":{"type":"integer"}}`, `{"a":1,"b":"x"}`, "b: must be integer, not string"},
		{"additionalProperties true allows extras", `{"type":"object","additionalProperties":true}`, `{"a":1}`, ""},
		{"closed object with no properties says so", `{"type":"object","additionalProperties":false}`, `{"a":1}`, "a: unknown field (this object declares no fields)"},
		{"missing $ref target accepts anything", `{"$ref":"#/$defs/nope"}`, `1`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validate(t, compile(t, tc.schema), tc.doc)
			if tc.want == "" {
				if got != "" {
					t.Errorf("rejected a valid document: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("errors:\n%s\nwant one containing %q", got, tc.want)
			}
		})
	}
}

// An unknown key is answered with the closest declared name, or with the
// list of what is declared (truncated when long).
func TestValidateUnknownFieldSuggestions(t *testing.T) {
	few := compile(t, `{"type":"object","additionalProperties":false,"properties":{"alpha":{},"beta":{}}}`)
	if got := validate(t, few, `{"gamma":1}`); got != "  - gamma: unknown field (known: alpha, beta)" {
		t.Errorf("short list: %q", got)
	}
	if got := validate(t, few, `{"ALPHA":1}`); !strings.Contains(got, `did you mean "alpha"?`) {
		t.Errorf("suggestion should ignore case: %q", got)
	}

	props := make([]string, 15)
	for i := range props {
		props[i] = `"p` + strings.Repeat("x", i+1) + `":{}`
	}
	many := compile(t, `{"type":"object","additionalProperties":false,"properties":{`+strings.Join(props, ",")+`}}`)
	got := validate(t, many, `{"zzzzzzzzzzzzzzzzzzzzzz":1}`)
	if !strings.Contains(got, "… 3 more)") || strings.Count(got, "px") < 1 {
		t.Errorf("long list should be cut to 12 names plus a count: %q", got)
	}
}

func TestValidateRequiredReportsMissingAndNull(t *testing.T) {
	c := compile(t, `{"type":"object","required":["a","b"],"properties":{"a":{},"b":{}}}`)
	got := validate(t, c, `{"b":null}`)
	for _, want := range []string{"a: required, and missing", "b: required, and null"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// An optional value is anyOf [X, null]: null passes, a wrong value reports
// the real branch's error (the one with fewest problems), not "matched none".
func TestValidateAnyOf(t *testing.T) {
	c := compile(t, `{"properties":{"v":{"anyOf":[
		{"type":"object","required":["a","b"],"properties":{"a":{"type":"string"},"b":{"type":"string"}}},
		{"type":"object","required":["a"],"properties":{"a":{"type":"integer"}}},
		{"type":"null"}]}}}`)
	if got := validate(t, c, `{"v":null}`); got != "" {
		t.Errorf("null should satisfy the null branch: %s", got)
	}
	if got := validate(t, c, `{"v":{"a":1}}`); got != "" {
		t.Errorf("second branch should match: %s", got)
	}
	got := validate(t, c, `{"v":{"a":"x"}}`)
	// branch 1 has one error (b missing), branch 2 has one (a not integer): the first fewest wins.
	if got != "  - v.b: required, and missing" {
		t.Errorf("best branch's errors expected, got %q", got)
	}
	got = validate(t, c, `{"v":{"a":"x","b":"y"}}`)
	if got != "" {
		t.Errorf("first branch matches fully: %s", got)
	}

	onlyNull := compile(t, `{"anyOf":[{"type":"null"}]}`)
	if got := validate(t, onlyNull, `1`); got != "  - must be null, not integer" {
		t.Errorf("a union of only null: %q", got)
	}
}

// A oneOf that is not a tagged union is checked generically.
func TestValidateGenericOneOf(t *testing.T) {
	c := compile(t, `{"oneOf":[{"type":"string"},{"type":"integer"}]}`)
	if got := validate(t, c, `"s"`); got != "" {
		t.Errorf("exactly one branch matches: %s", got)
	}
	if got := validate(t, c, `true`); got != "  - must match exactly one of 2 alternatives (matched 0)" {
		t.Errorf("matched none: %q", got)
	}
	both := compile(t, `{"oneOf":[{"type":"number"},{"type":"integer"}]}`)
	if got := validate(t, both, `1`); got != "  - must match exactly one of 2 alternatives (matched 2)" {
		t.Errorf("matched both: %q", got)
	}
}

// Branches that are not all distinct-const objects on one property are not a
// tagged union and fall back to the generic check.
func TestValidateOneOfFallsBackWhenNotATaggedUnion(t *testing.T) {
	cases := map[string]string{
		"duplicate const":        `{"oneOf":[{"type":"object","properties":{"k":{"const":"a"}}},{"type":"object","properties":{"k":{"const":"a"}}}]}`,
		"different tags":         `{"oneOf":[{"type":"object","properties":{"k":{"const":"a"}}},{"type":"object","properties":{"j":{"const":"b"}}}]}`,
		"a branch with no const": `{"oneOf":[{"type":"object","properties":{"k":{"const":"a"}}},{"type":"object","properties":{"k":{"type":"string"}}}]}`,
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			got := validate(t, compile(t, schema), `{"k":"a"}`)
			if !strings.Contains(got, "must match exactly one of 2 alternatives") {
				t.Errorf("expected the generic oneOf report, got %q", got)
			}
		})
	}
}

func TestValidateTaggedUnionDispatch(t *testing.T) {
	c := compile(t, `{"$defs":{
		"A":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"const":"a"},"x":{"type":"integer"}}},
		"B":{"type":"object","additionalProperties":false,"required":["kind"],"properties":{"kind":{"const":"b"}}}},
		"properties":{"u":{"oneOf":[{"$ref":"#/$defs/A"},{"$ref":"#/$defs/B"}]}}}`)
	cases := []struct{ doc, want string }{
		{`{"u":{"kind":"a","x":1}}`, ""},
		{`{"u":{"kind":"a","x":"s"}}`, "u.x: must be integer, not string"},
		{`{"u":{"kind":"c"}}`, `u.kind: "c" is not one of: a, b`},
		{`{"u":{"kind":null}}`, "u.kind: required, and missing (one of: a, b)"},
		{`{"u":{}}`, "u.kind: required, and missing (one of: a, b)"},
		{`{"u":[]}`, "u: must be object, not array"},
		{`{"u":{"kind":7}}`, `u.kind: 7 is not one of: a, b`},
	}
	for _, tc := range cases {
		got := validate(t, c, tc.doc)
		if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
			t.Errorf("%s:\n got %q\nwant %q", tc.doc, got, tc.want)
		}
	}
}

// A long value in a message is cut, so one bad document cannot flood the
// output.
func TestShowTruncatesLongValues(t *testing.T) {
	long := strings.Repeat("x", 100)
	got := show(long)
	if len(got) != 60 || !strings.HasSuffix(got, "...") {
		t.Errorf("show = %q (len %d), want 57 bytes plus ...", got, len(got))
	}
	if got := show("short"); got != `"short"` {
		t.Errorf("show = %q", got)
	}
	if got := show(make(chan int)); !strings.HasPrefix(got, "0x") {
		t.Errorf("an unmarshalable value falls back to fmt: %q", got)
	}
	if got := showList([]any{"a", json.Number("2"), nil}); got != `"a", 2, null` {
		t.Errorf("showList = %q", got)
	}
}

func TestJSONKindNamesEveryShape(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "null"}, {true, "boolean"}, {"s", "string"}, {map[string]any{}, "object"}, {[]any{}, "array"},
		{json.Number("1"), "integer"}, {json.Number("1.5"), "number"}, {json.Number("1e400"), "number"}, {int(1), "int"},
	}
	for _, tc := range cases {
		if got := jsonKind(tc.in); got != tc.want {
			t.Errorf("jsonKind(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDescribeTypeFallsBackToFmt(t *testing.T) {
	if got := describeType(42); got != "42" {
		t.Errorf("describeType(42) = %q", got)
	}
	if typeMatches(42, "x") {
		t.Error("a type keyword that is neither a string nor a list matches nothing")
	}
}

func TestErrorsFormatting(t *testing.T) {
	if got := (Error{Message: "bad"}).Error(); got != "bad" {
		t.Errorf("document-level error = %q", got)
	}
	es := Errors{{Path: "a.b", Message: "x"}, {Message: "y"}}
	if got := es.Error(); got != "  - a.b: x\n  - y" {
		t.Errorf("Errors = %q", got)
	}
}

func TestCompileErrors(t *testing.T) {
	if _, err := Compile([]byte(`{`)); err == nil || !strings.Contains(err.Error(), "parse schema") {
		t.Errorf("malformed schema: %v", err)
	}
	// An unimplemented keyword is named with where it was found, however deep.
	for _, schema := range []string{
		`{"properties":{"a":{"pattern":"x"}}}`,
		`{"$defs":{"a":{"pattern":"x"}}}`,
		`{"items":{"pattern":"x"}}`,
		`{"additionalProperties":{"pattern":"x"}}`,
		`{"oneOf":[{"pattern":"x"}]}`,
		`{"anyOf":[{},{"pattern":"x"}]}`,
	} {
		_, err := Compile([]byte(schema))
		if err == nil || !strings.Contains(err.Error(), `keyword "pattern" is not implemented`) {
			t.Errorf("Compile(%s) = %v, want the pattern keyword refused", schema, err)
		}
	}
	// Keyword-shaped values of the wrong JSON type are skipped, not crashed on.
	if _, err := Compile([]byte(`{"properties":1,"items":true,"oneOf":1,"anyOf":[1]}`)); err != nil {
		t.Errorf("odd-typed keyword values: %v", err)
	}
}

func TestResolveGivesUpOnReferenceCycles(t *testing.T) {
	c := compile(t, `{"$ref":"#/$defs/a","$defs":{"a":{"$ref":"#/$defs/b"},"b":{"$ref":"#/$defs/a"}}}`)
	if got := validate(t, c, `1`); got != "" {
		t.Errorf("a ref cycle should end, not loop: %q", got)
	}
}
