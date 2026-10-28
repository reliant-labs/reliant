package validation

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/require"
)

func TestWorkflowConcurrencyRequiresPositiveLimit(t *testing.T) {
	wf := &reliantv1.Workflow{Name: "bounded", Concurrency: &reliantv1.WorkflowConcurrency{}}
	result := StaticAnalysis(wf, nil)
	found := false
	for _, issue := range result.Errors() {
		if issue.Field == "concurrency.max_running" {
			found = true
		}
	}
	require.True(t, found, "proto/builder callers must not bypass the YAML policy check")
}
