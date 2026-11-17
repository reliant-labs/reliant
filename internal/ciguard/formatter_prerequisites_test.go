package ciguard

import (
	"os"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

func TestFormattersRemainAvailableAfterEarlierCheckFailure(t *testing.T) {
	raw, err := os.ReadFile(ciWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var workflow stepsWorkflow
	if err = yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, step := range workflow.Jobs["checks"].Steps {
		switch step.Name {
		case "Install buf", "Install codegen tools", "pre-commit":
			found[step.Name] = true
			if step.If != "${{ !cancelled() }}" {
				t.Errorf("%s skipped on an earlier API gate refusal: %q", step.Name, step.If)
			}
		}
	}
	if len(found) != 3 {
		t.Fatalf("missing formatter prerequisites: %v", found)
	}
}
