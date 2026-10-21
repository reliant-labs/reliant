// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const reviewSchema = `{
	"type": "object",
	"required": ["verdict", "defects", "risks"],
	"properties": {
		"verdict": {"type": "string"},
		"defects": {"type": "integer"},
		"risks":   {"type": "integer"},
		"score":   {"type": "number"},
		"blocking": {"type": "boolean"},
		"either":  {"type": ["string", "integer"]},
		"nested":  {"type": "object", "properties": {"count": {"type": "integer"}}},
		"counts":  {"type": "array", "items": {"type": "integer"}}
	}
}`

func TestValidateJSONWithRepair_ScalarStrings(t *testing.T) {
	t.Parallel()
	base := `"verdict": "ok", "risks": 0`
	tests := []struct {
		name    string
		input   string
		want    string // canonical JSON of the repaired input; "" = expect error
		wantErr string
	}{
		{name: "integer string", input: `{` + base + `, "defects": "2"}`, want: `{"verdict":"ok","risks":0,"defects":2}`},
		{name: "both integers stringified (the prod shape)", input: `{"verdict":"ok","defects":"2","risks":"0"}`, want: `{"verdict":"ok","defects":2,"risks":0}`},
		{name: "negative integer", input: `{` + base + `, "defects": "-3"}`, want: `{"verdict":"ok","risks":0,"defects":-3}`},
		{name: "integral float string for integer", input: `{` + base + `, "defects": "2.0"}`, want: `{"verdict":"ok","risks":0,"defects":2}`},
		{name: "exponent integral for integer", input: `{` + base + `, "defects": "2e2"}`, want: `{"verdict":"ok","risks":0,"defects":200}`},
		{name: "number string", input: `{` + base + `, "defects": 1, "score": "2.5"}`, want: `{"verdict":"ok","risks":0,"defects":1,"score":2.5}`},
		{name: "bool true", input: `{` + base + `, "defects": 1, "blocking": "true"}`, want: `{"verdict":"ok","risks":0,"defects":1,"blocking":true}`},
		{name: "bool false", input: `{` + base + `, "defects": 1, "blocking": "false"}`, want: `{"verdict":"ok","risks":0,"defects":1,"blocking":false}`},
		{name: "nested object", input: `{` + base + `, "defects": 1, "nested": {"count": "7"}}`, want: `{"verdict":"ok","risks":0,"defects":1,"nested":{"count":7}}`},
		{name: "array items", input: `{` + base + `, "defects": 1, "counts": ["1", "2", 3]}`, want: `{"verdict":"ok","risks":0,"defects":1,"counts":[1,2,3]}`},
		{name: "stringified array of stringified ints", input: `{` + base + `, "defects": 1, "counts": "[\"1\", \"2\"]"}`, want: `{"verdict":"ok","risks":0,"defects":1,"counts":[1,2]}`},
		{name: "string-or-integer keeps the string", input: `{` + base + `, "defects": 1, "either": "5"}`, want: `{"verdict":"ok","risks":0,"defects":1,"either":"5"}`},

		{name: "trailing text", input: `{` + base + `, "defects": "2abc"}`, wantErr: "defects"},
		{name: "empty string", input: `{` + base + `, "defects": ""}`, wantErr: "defects"},
		{name: "whitespace", input: `{` + base + `, "defects": " 2"}`, wantErr: "defects"},
		{name: "leading plus", input: `{` + base + `, "defects": "+2"}`, wantErr: "defects"},
		{name: "leading zero", input: `{` + base + `, "defects": "02"}`, wantErr: "defects"},
		{name: "fractional for integer", input: `{` + base + `, "defects": "2.5"}`, wantErr: "defects"},
		{name: "integer overflow", input: `{` + base + `, "defects": "99999999999999999999"}`, wantErr: "defects"},
		{name: "float overflow", input: `{` + base + `, "defects": 1, "score": "1e999"}`, wantErr: "score"},
		{name: "NaN", input: `{` + base + `, "defects": 1, "score": "NaN"}`, wantErr: "score"},
		{name: "bool capitalised", input: `{` + base + `, "defects": 1, "blocking": "True"}`, wantErr: "blocking"},
		{name: "number string for boolean", input: `{` + base + `, "defects": 1, "blocking": "1"}`, wantErr: "blocking"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateJSONWithRepair("submit_review", tt.input, []byte(reviewSchema))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error, got repaired %s", got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q should name %q", err, tt.wantErr)
				}
				if got != tt.input {
					t.Errorf("on error the original input must be returned, got %s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var gotV, wantV interface{}
			if err := json.Unmarshal([]byte(got), &gotV); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tt.want), &wantV); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotV, wantV) {
				t.Errorf("repaired = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestValidateJSONWithRepair_ErrorNamesFieldTypeAndValue(t *testing.T) {
	t.Parallel()
	_, err := ValidateJSONWithRepair("submit_review", `{"verdict":"ok","defects":"2abc","risks":"0"}`, []byte(reviewSchema))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"defects must be of type integer", `string "2abc"`, "without quotes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
	// The one that WAS coerced is not blamed.
	if strings.Contains(err.Error(), "risks must be") {
		t.Errorf("error should not blame a repaired field: %v", err)
	}
}

func TestValidateJSONWithRepair_MissingFieldHasNoTypeHint(t *testing.T) {
	t.Parallel()
	_, err := ValidateJSONWithRepair("submit_review", `{"verdict":"ok","defects":1}`, []byte(reviewSchema))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "Fix:") {
		t.Errorf("no type mismatch, so no hint expected: %v", err)
	}
}
