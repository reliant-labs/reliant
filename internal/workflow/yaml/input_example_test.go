package wfyaml

import (
	"strings"
	"testing"
)

// An input's `example:` is what the Run and Activate forms show in an empty
// field. It is part of the definition, so it has to survive YAML both ways,
// and it never becomes the input's default.
func TestInputExampleRoundTrips(t *testing.T) {
	doc := `
name: test
inputs:
  topic:
    type: string
    description: What to research
    example: the history of the transistor
  skills:
    type: array
    default: []
    example: "[forge/db, code-review]"
nodes:
  - id: n1
    type: save_message
    args:
      role: assistant
      content: "{{inputs.topic}}"
`
	wf, err := ParseWorkflow([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	topic := wf.GetInputs()["topic"].GetStringInput()
	if got := topic.GetBase().GetExample(); got != "the history of the transistor" {
		t.Fatalf("topic example = %q", got)
	}
	if topic.Default != nil {
		t.Errorf("an example must not become a default, got %q", topic.GetDefault())
	}
	if got := wf.GetInputs()["skills"].GetArrayInput().GetBase().GetExample(); got != "[forge/db, code-review]" {
		t.Errorf("skills example = %q", got)
	}

	out, err := MarshalWorkflow(wf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "example: the history of the transistor") {
		t.Fatalf("marshalled YAML lost the example:\n%s", out)
	}
	again, err := ParseWorkflow(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.GetInputs()["topic"].GetStringInput().GetBase().GetExample(); got != "the history of the transistor" {
		t.Errorf("round-tripped example = %q", got)
	}
}
