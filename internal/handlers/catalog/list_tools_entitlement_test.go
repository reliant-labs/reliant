package catalog_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"

	"github.com/reliant-labs/reliant/internal/featuregate"
	catalog "github.com/reliant-labs/reliant/internal/handlers/catalog"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// A feature-gated tool is listed only to a member of some org entitled to its
// feature. No real tool is gated yet, so websearch is gated for the test
// (tools.GateToolForTest swaps process-wide state: not parallel).
func TestListTools_FeatureGatedToolsAreListedOnlyToEntitledCallers(t *testing.T) {
	tools.GateToolForTest(t, tools.ToolWebSearch, featuregate.GTM)

	listed := func(t *testing.T, gate featuregate.Gate) map[string]bool {
		t.Helper()
		svc := catalog.NewTestCatalog(t, catalog.WithCatalogDeps(catalog.Deps{Gate: gate}))
		resp, err := svc.ListTools(catalog.AuthedContext(t), connect.NewRequest(&pb.ListToolsRequest{}))
		require.NoError(t, err)
		names := map[string]bool{}
		for _, tool := range resp.Msg.GetTools() {
			names[tool.GetName()] = true
		}
		assert.Equal(t, len(names), int(resp.Msg.GetTotal()))
		return names
	}
	allowFor := func(answer bool, err error) *featuregate.MockGate {
		return &featuregate.MockGate{AllowedForUserFunc: func(_ context.Context, f featuregate.Feature, userID string) (bool, error) {
			assert.Equal(t, featuregate.GTM, f)
			assert.NotEmpty(t, userID)
			return answer, err
		}}
	}

	t.Run("entitled caller", func(t *testing.T) {
		gate := allowFor(true, nil)
		names := listed(t, gate)
		assert.True(t, names[tools.ToolWebSearch])
		assert.Len(t, names, len(tools.GetToolRegistry()))
		assert.Equal(t, 1, gate.CallCount("AllowedForUser"), "asked once per feature")
	})
	for name, gate := range map[string]featuregate.Gate{
		"not entitled": allowFor(false, nil),
		"gate outage":  allowFor(false, errors.New("membership outage")),
		"no gate":      nil,
	} {
		t.Run(name, func(t *testing.T) {
			names := listed(t, gate)
			assert.False(t, names[tools.ToolWebSearch])
			assert.True(t, names[tools.ToolFetch])
			assert.Len(t, names, len(tools.GetToolRegistry())-1)
		})
	}
}
