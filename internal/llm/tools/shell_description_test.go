package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// The model populates `description`; the wrapper decodes with
// DisallowUnknownFields, so an undeclared field would be a hard error.
func TestShellDescriptionAccepted(t *testing.T) {
	in := `{"command":"git status","description":"Show working tree status"}`
	var p ShellParams
	dec := json.NewDecoder(strings.NewReader(in))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if p.Description != "Show working tree status" {
		t.Fatalf("description not decoded: %q", p.Description)
	}
	if p.Command != "git status" {
		t.Fatalf("command mangled: %q", p.Command)
	}
}

// Omitting it must still work: old transcripts and models that skip it.
func TestShellDescriptionOptional(t *testing.T) {
	var p ShellParams
	dec := json.NewDecoder(strings.NewReader(`{"command":"ls"}`))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("decode without description failed: %v", err)
	}
	if p.Description != "" {
		t.Fatalf("expected empty description, got %q", p.Description)
	}
}

// The description must survive re-serialization, which is how it reaches the UI.
func TestShellDescriptionRoundTrips(t *testing.T) {
	p := ShellParams{Command: "ls", Description: "List files in current directory"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"description":"List files in current directory"`) {
		t.Fatalf("description lost on marshal: %s", string(b))
	}
}

// Real line breaks, not literal backslash-n, must reach the model.
func TestShellDescriptionSchemaHasRealNewlines(t *testing.T) {
	s := NewShellTool().ParamSchema()
	prop, ok := s.Properties.Get("description")
	if !ok {
		t.Fatal("description property missing from schema")
	}
	if !strings.Contains(prop.Description, "\n") {
		t.Error("schema description has no real newlines")
	}
	if strings.Contains(prop.Description, `\n`) {
		t.Error("schema description contains literal backslash-n")
	}
	if !strings.Contains(prop.Description, "active voice") {
		t.Error("schema description missing expected guidance")
	}
	for _, req := range s.Required {
		if req == "description" {
			t.Error("description must stay optional")
		}
	}
}
