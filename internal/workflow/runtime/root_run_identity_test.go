package runtime

import (
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRootExecutionIdentitySurvivesChildAndIteration(t *testing.T) {
	root := NewExecutionContext("wf", "chat", "test", "thread")
	root.RootFirstRunID = "first-root"
	child := root.ForChildWorkflow("child", "step", model.ThreadModeInherit, "nested", false)
	require.Equal(t, "first-root", child.RootFirstRunID)
	require.Equal(t, "first-root", child.ForIteration(2, true).RootFirstRunID)
	require.Equal(t, "first-root", root.Clone().RootFirstRunID)
}
