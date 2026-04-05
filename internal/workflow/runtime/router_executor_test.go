package runtime

import (
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/internal/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nilLogger struct{}

func (nilLogger) Debug(string, ...interface{}) {}
func (nilLogger) Info(string, ...interface{})  {}
func (nilLogger) Warn(string, ...interface{})  {}
func (nilLogger) Error(string, ...interface{}) {}

func TestRouterThreadMode(t *testing.T) {
	t.Run("returns new when no thread config", func(t *testing.T) {
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
			Args: &reliantv1.Node_Router{
				Router: &reliantv1.RouterArgs{
					Workflows: []*reliantv1.RouterWorkflowCandidate{
						{Ref: "builtin://agent"},
					},
				},
			},
		}
		assert.Equal(t, "new", routerThreadMode(node))
	})

	t.Run("returns configured mode when thread config is set", func(t *testing.T) {
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
			Args: &reliantv1.Node_Router{
				Router: &reliantv1.RouterArgs{
					Workflows: []*reliantv1.RouterWorkflowCandidate{
						{Ref: "builtin://agent"},
					},
					Thread: &reliantv1.ThreadConfig{Mode: "fork"},
				},
			},
		}
		assert.Equal(t, "fork", routerThreadMode(node))
	})

	t.Run("returns new for nil evalResult router args", func(t *testing.T) {
		// Node with no router args
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
		}
		assert.Equal(t, "new", routerThreadMode(node))
	})

	t.Run("returns new when thread mode is empty string", func(t *testing.T) {
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
			Args: &reliantv1.Node_Router{
				Router: &reliantv1.RouterArgs{
					Thread: &reliantv1.ThreadConfig{Mode: ""},
				},
			},
		}
		assert.Equal(t, "new", routerThreadMode(node))
	})
}

func TestRouterWorkflowIdentity(t *testing.T) {
	t.Run("returns router with candidate refs", func(t *testing.T) {
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
			Args: &reliantv1.Node_Router{
				Router: &reliantv1.RouterArgs{
					Workflows: []*reliantv1.RouterWorkflowCandidate{
						{Ref: "builtin://agent"},
						{Ref: "builtin://code-review"},
					},
				},
			},
		}
		assert.Equal(t, "router[builtin://agent,builtin://code-review]", routerWorkflowIdentity(node))
	})

	t.Run("returns router with no candidates", func(t *testing.T) {
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
			Args: &reliantv1.Node_Router{
				Router: &reliantv1.RouterArgs{
					Workflows: []*reliantv1.RouterWorkflowCandidate{},
				},
			},
		}
		assert.Equal(t, "router", routerWorkflowIdentity(node))
	})

	t.Run("returns router for nil args", func(t *testing.T) {
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
		}
		assert.Equal(t, "router", routerWorkflowIdentity(node))
	})

	t.Run("returns router with single candidate", func(t *testing.T) {
		node := &reliantv1.Node{
			Id:   "route",
			Type: "router",
			Args: &reliantv1.Node_Router{
				Router: &reliantv1.RouterArgs{
					Workflows: []*reliantv1.RouterWorkflowCandidate{
						{Ref: "builtin://agent"},
					},
				},
			},
		}
		assert.Equal(t, "router[builtin://agent]", routerWorkflowIdentity(node))
	})
}

func TestParseRoutingDecision(t *testing.T) {
	newExecutor := func(fallback string) *RouterExecutor {
		evalResult := &reliantv1.Node{
			Id:   "route",
			Type: model.NodeTypeRouter,
			Args: &reliantv1.Node_Router{Router: &reliantv1.RouterArgs{Fallback: fallback}},
		}
		return &RouterExecutor{
			evalResult: evalResult,
			logger:     nilLogger{},
			candidates: []routerWorkflowInfo{{
				Ref:     "builtin://agent",
				Presets: []*preset.Preset{{Name: "general"}, {Name: "researcher"}},
			}},
		}
	}

	t.Run("accepts valid workflow and preset", func(t *testing.T) {
		executor := newExecutor("")
		err := executor.parseRoutingDecision(map[string]interface{}{
			"response_text": `{"workflow":"builtin://agent","preset":"general","prompt":"rewrite","reasoning":"best fit"}`,
		})
		require.NoError(t, err)
		require.NotNil(t, executor.decision)
		assert.Equal(t, "general", executor.decision.Preset)
		assert.Equal(t, "rewrite", executor.decision.Prompt)
	})

	t.Run("uses valid fallback preset", func(t *testing.T) {
		executor := newExecutor("researcher")
		err := executor.parseRoutingDecision(map[string]interface{}{
			"response_text": `{"workflow":"builtin://agent","preset":"invalid","prompt":"rewrite","reasoning":"best fit"}`,
		})
		require.NoError(t, err)
		require.NotNil(t, executor.decision)
		assert.Equal(t, "researcher", executor.decision.Preset)
	})

	t.Run("rejects invalid fallback preset", func(t *testing.T) {
		executor := newExecutor("missing")
		err := executor.parseRoutingDecision(map[string]interface{}{
			"response_text": `{"workflow":"builtin://agent","preset":"invalid","prompt":"rewrite","reasoning":"best fit"}`,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `fallback preset "missing" is invalid`)
	})
}
