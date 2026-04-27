package activities

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
	"github.com/stretchr/testify/require"
)

func TestAskQuestionNodeSchemaUsesAskQuestionOutput(t *testing.T) {
	defaults := schema.GetOutputDefaults("AskQuestion")
	require.NotNil(t, defaults)
	require.Contains(t, defaults, "has_feedback")
	require.Contains(t, defaults, "response")
}
