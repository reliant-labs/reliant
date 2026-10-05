package wfyaml

import (
	"strings"
	"testing"
)

// title and hidden are presentation metadata on a workflow definition: the
// display name a picker shows, and whether a picker shows it at all. Both
// must survive a parse → marshal → parse round trip, because the workflow
// builder saves definitions by marshalling the parsed proto — a field the
// marshaller drops is silently erased the first time someone edits the file.
func TestWorkflowTitleAndHidden_RoundTrip(t *testing.T) {
	src := []byte(`name: scope-conversation
title: Scope a Conversation
hidden: true
apiVersion: "0.0.5"
entry: [a]
nodes:
  - id: a
    type: compact
edges: []
`)

	wf, err := ParseWorkflow(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if wf.Title != "Scope a Conversation" {
		t.Errorf("Title = %q, want %q", wf.Title, "Scope a Conversation")
	}
	if !wf.Hidden {
		t.Errorf("Hidden = false, want true")
	}

	out, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), "title: Scope a Conversation") {
		t.Errorf("marshalled YAML lost title:\n%s", out)
	}
	if !strings.Contains(string(out), "hidden: true") {
		t.Errorf("marshalled YAML lost hidden:\n%s", out)
	}

	again, err := ParseWorkflow(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if again.Title != wf.Title || again.Hidden != wf.Hidden {
		t.Errorf("round trip changed metadata: title %q→%q hidden %v→%v",
			wf.Title, again.Title, wf.Hidden, again.Hidden)
	}
}

// A visible, untitled workflow must marshal with neither key: emitting
// `hidden: false` / `title: ""` into every saved user workflow is noise that
// shows up as a diff on files nobody changed.
func TestWorkflowTitleAndHidden_OmittedWhenUnset(t *testing.T) {
	wf, err := ParseWorkflow([]byte(`name: plain
entry: [a]
nodes:
  - id: a
    type: compact
edges: []
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"title:", "hidden:"} {
		if strings.Contains(string(out), key) {
			t.Errorf("unset %s emitted:\n%s", key, out)
		}
	}
}

// hidden is a boolean, and a typo'd value must fail loudly rather than parse
// as false and quietly put a building block back in every picker.
func TestWorkflowHidden_RejectsNonBoolean(t *testing.T) {
	_, err := ParseWorkflow([]byte(`name: x
hidden: yes-please
entry: [a]
nodes:
  - id: a
    type: compact
edges: []
`))
	if err == nil {
		t.Fatal("expected an error for a non-boolean hidden value")
	}
}
