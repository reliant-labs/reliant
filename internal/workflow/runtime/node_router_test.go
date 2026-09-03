package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildNodeRoutingSystemPrompt(t *testing.T) {
	t.Parallel()
	t.Run("uses default prompt when custom is empty", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "summarize", Description: "Summarize content"},
		}
		result := buildNodeRoutingSystemPrompt(candidates, "")
		assert.Contains(t, result, defaultNodeRoutingSystemPrompt)
		assert.Contains(t, result, "`summarize`")
		assert.Contains(t, result, "Summarize content")
	})

	t.Run("uses custom prompt when provided", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "summarize", Description: "Summarize content"},
		}
		result := buildNodeRoutingSystemPrompt(candidates, "Custom routing instructions")
		assert.Contains(t, result, "Custom routing instructions")
		assert.NotContains(t, result, defaultNodeRoutingSystemPrompt)
	})

	t.Run("lists multiple candidates", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "summarize", Description: "Summarize content"},
			{Id: "translate", Description: "Translate text"},
			{Id: "analyze", Description: "Analyze data"},
		}
		result := buildNodeRoutingSystemPrompt(candidates, "")

		assert.Contains(t, result, "`analyze`")
		assert.Contains(t, result, "`summarize`")
		assert.Contains(t, result, "`translate`")
		assert.Contains(t, result, "Summarize content")
		assert.Contains(t, result, "Translate text")
		assert.Contains(t, result, "Analyze data")
	})

	t.Run("handles candidate without description", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "simple_node"},
		}
		result := buildNodeRoutingSystemPrompt(candidates, "")
		assert.Contains(t, result, "`simple_node`")
		// Should not have a trailing colon with no description
		assert.NotContains(t, result, "simple_node`: ")
	})

	t.Run("candidates are sorted by ID", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "zebra", Description: "Z node"},
			{Id: "alpha", Description: "A node"},
		}
		result := buildNodeRoutingSystemPrompt(candidates, "")

		alphaIdx := len(result) // fallback
		zebraIdx := 0
		for i := range result {
			if i+5 <= len(result) && result[i:i+5] == "alpha" {
				alphaIdx = i
				break
			}
		}
		for i := range result {
			if i+5 <= len(result) && result[i:i+5] == "zebra" {
				zebraIdx = i
				break
			}
		}
		assert.Less(t, alphaIdx, zebraIdx, "alpha should appear before zebra")
	})
}

func TestBuildNodeRoutingResponseSchema(t *testing.T) {
	t.Parallel()
	t.Run("schema has correct structure", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "summarize"},
			{Id: "translate"},
		}
		schema := buildNodeRoutingResponseSchema(candidates)

		assert.Equal(t, "object", schema["type"])

		props, ok := schema["properties"].(map[string]interface{})
		require.True(t, ok)

		// selected_node
		nodeProp, ok := props["selected_node"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "string", nodeProp["type"])
		nodeEnum, ok := nodeProp["enum"].([]interface{})
		require.True(t, ok)
		assert.Contains(t, nodeEnum, "summarize")
		assert.Contains(t, nodeEnum, "translate")

		// reasoning
		reasonProp, ok := props["reasoning"].(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "string", reasonProp["type"])

		// required
		required, ok := schema["required"].([]interface{})
		require.True(t, ok)
		assert.Contains(t, required, "selected_node")
		assert.Contains(t, required, "reasoning")
	})

	t.Run("deduplicates candidate IDs", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "summarize"},
			{Id: "summarize"}, // duplicate
			{Id: "translate"},
		}
		schema := buildNodeRoutingResponseSchema(candidates)

		props := schema["properties"].(map[string]interface{})
		nodeProp := props["selected_node"].(map[string]interface{})
		nodeEnum := nodeProp["enum"].([]interface{})
		assert.Len(t, nodeEnum, 2)
	})

	t.Run("enum is sorted", func(t *testing.T) {
		candidates := []*reliantv1.NodeRouterCandidate{
			{Id: "zebra"},
			{Id: "alpha"},
			{Id: "middle"},
		}
		schema := buildNodeRoutingResponseSchema(candidates)

		props := schema["properties"].(map[string]interface{})
		nodeProp := props["selected_node"].(map[string]interface{})
		nodeEnum := nodeProp["enum"].([]interface{})
		assert.Equal(t, []interface{}{"alpha", "middle", "zebra"}, nodeEnum)
	})
}

func TestParseNodeRoutingDecision(t *testing.T) {
	t.Parallel()
	newExecutor := func() *RouterExecutor {
		return &RouterExecutor{
			logger: nilLogger{},
		}
	}

	t.Run("parses from response_text", func(t *testing.T) {
		executor := newExecutor()
		decision, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: `{"selected_node":"summarize","reasoning":"best fit"}`,
		})
		require.NoError(t, err)
		assert.Equal(t, "summarize", decision.SelectedNode)
		assert.Equal(t, "best fit", decision.Reasoning)
	})

	t.Run("parses from response_data", func(t *testing.T) {
		executor := newExecutor()
		rd, err := structpb.NewStruct(map[string]interface{}{
			"selected_node": "translate",
			"reasoning":     "language task",
		})
		require.NoError(t, err)

		decision, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseData: rd,
		})
		require.NoError(t, err)
		assert.Equal(t, "translate", decision.SelectedNode)
		assert.Equal(t, "language task", decision.Reasoning)
	})

	t.Run("prefers response_data over response_text", func(t *testing.T) {
		executor := newExecutor()
		rd, err := structpb.NewStruct(map[string]interface{}{
			"selected_node": "from_struct",
			"reasoning":     "struct wins",
		})
		require.NoError(t, err)

		decision, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseData: rd,
			ResponseText: `{"selected_node":"from_text","reasoning":"text source"}`,
		})
		require.NoError(t, err)
		assert.Equal(t, "from_struct", decision.SelectedNode)
	})

	t.Run("errors on empty response", func(t *testing.T) {
		executor := newExecutor()
		_, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no response_data or response_text")
	})

	t.Run("errors on empty selected_node", func(t *testing.T) {
		executor := newExecutor()
		_, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: `{"selected_node":"","reasoning":"no selection"}`,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty selected_node")
	})

	t.Run("recovers unambiguous prose candidate mention", func(t *testing.T) {
		executor := newExecutor()
		executor.evalResult = &reliantv1.Node{
			Args: &reliantv1.Node_Router{Router: &reliantv1.RouterArgs{
				Nodes: []*reliantv1.NodeRouterCandidate{
					{Id: "scrape_website"},
					{Id: "write_summary"},
				},
			}},
		}

		decision, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: "The request should go to `scrape_website` because it needs page content first.",
		})
		require.NoError(t, err)
		assert.Equal(t, "scrape_website", decision.SelectedNode)
		assert.Contains(t, decision.Reasoning, "Recovered")
	})

	t.Run("does not recover ambiguous prose candidate mentions", func(t *testing.T) {
		executor := newExecutor()
		executor.evalResult = &reliantv1.Node{
			Args: &reliantv1.Node_Router{Router: &reliantv1.RouterArgs{
				Nodes: []*reliantv1.NodeRouterCandidate{
					{Id: "scrape_website"},
					{Id: "write_summary"},
				},
			}},
		}

		_, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: "Maybe `scrape_website` first, or `write_summary` if content already exists.",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse node routing decision JSON")
	})

	t.Run("does not recover partial candidate name", func(t *testing.T) {
		executor := newExecutor()
		executor.evalResult = &reliantv1.Node{
			Args: &reliantv1.Node_Router{Router: &reliantv1.RouterArgs{
				Nodes: []*reliantv1.NodeRouterCandidate{{Id: "scrape"}},
			}},
		}

		_, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: "The request should go to scrape_website because it needs page content first.",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse node routing decision JSON")
	})

	t.Run("errors on invalid JSON", func(t *testing.T) {
		executor := newExecutor()
		_, err := executor.parseNodeRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: `not json`,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse node routing decision JSON")
	})
}
