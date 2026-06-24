package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/preset"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
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

func TestNodeRoutingCallLLMUsesExecutionContextThread(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var capturedThread string
	env.RegisterActivityWithOptions(
		func(_ context.Context, input json.RawMessage) (*reliantv1.CallLLMOutput, error) {
			var envelope struct {
				Runtime struct {
					Thread string `json:"thread"`
				} `json:"runtime"`
			}
			require.NoError(t, json.Unmarshal(input, &envelope))
			capturedThread = envelope.Runtime.Thread

			responseData, err := structpb.NewStruct(map[string]interface{}{
				"selected_node": "summarize",
				"reasoning":     "best match",
			})
			require.NoError(t, err)
			return &reliantv1.CallLLMOutput{ResponseData: responseData}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)

	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		node := buildTestNodeRoutingRouter()
		executor := NewRouterExecutor(
			ctx,
			"wf-123",
			"chat-456",
			"test-router",
			map[string]interface{}{"mode": "manual"},
			map[string]interface{}{},
			&ChildWorkflowTracker{children: make(map[string]bool)},
			node,
			node,
		).WithExecContext(&ExecutionContext{
			WorkflowID:   "wf-123",
			ChatID:       "chat-456",
			Thread:       "thread-parent-abc",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "test-router",
		})

		output, err := executor.executeNodeRouting(node.GetRouter())
		if err != nil {
			return err
		}
		if output["selected_node"] != "summarize" {
			return fmt.Errorf("selected_node = %v", output["selected_node"])
		}
		return nil
	})

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, "thread-parent-abc", capturedThread)
}

func TestDynamicWorkflowNodeRoutingPassesThreadToCallLLM(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	workflowBytes, err := protojson.Marshal(&reliantv1.Workflow{
		Name:  "node-router-workflow",
		Entry: []string{"classify"},
		Nodes: []*reliantv1.Node{
			buildTestNodeRoutingRouter(),
			{
				Id:   "done",
				Type: model.NodeTypeJoin,
				Args: &reliantv1.Node_Join{Join: &reliantv1.JoinArgs{}},
			},
		},
		Edges: []*reliantv1.Edge{
			{From: "classify", Default: []string{"done"}},
		},
	})
	require.NoError(t, err)

	var capturedCallLLMThread string
	env.RegisterActivityWithOptions(
		func(_ context.Context, input json.RawMessage) (*reliantv1.CallLLMOutput, error) {
			var envelope struct {
				Runtime struct {
					Thread string `json:"thread"`
				} `json:"runtime"`
			}
			require.NoError(t, json.Unmarshal(input, &envelope))
			capturedCallLLMThread = envelope.Runtime.Thread
			responseData, err := structpb.NewStruct(map[string]interface{}{
				"selected_node": "summarize",
				"reasoning":     "best match",
			})
			require.NoError(t, err)
			return &reliantv1.CallLLMOutput{ResponseData: responseData}, nil
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: workflowBytes}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
			return nil, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
			return map[string]interface{}{}, nil
		},
		activity.RegisterOptions{Name: "Cleanup"},
	)

	env.ExecuteWorkflow(DynamicWorkflow, WorkflowInput{
		ChatID:       "chat-456",
		WorkflowName: "node-router-workflow",
		Inputs:       map[string]interface{}{},
		ExecContext: &ExecutionContext{
			WorkflowID:   "wf-ignored",
			ChatID:       "chat-456",
			Thread:       "thread-root-xyz",
			ThreadMode:   model.ThreadModeNew,
			WorkflowName: "node-router-workflow",
		},
	})

	require.NoError(t, env.GetWorkflowError())
	assert.Equal(t, "thread-root-xyz", capturedCallLLMThread)
}

func buildTestNodeRoutingRouter() *reliantv1.Node {
	return &reliantv1.Node{
		Id:   "classify",
		Type: model.NodeTypeRouter,
		Args: &reliantv1.Node_Router{Router: &reliantv1.RouterArgs{
			Model: &reliantv1.CelModelSelector{Value: &reliantv1.CelModelSelector_Literal{Literal: &reliantv1.ModelSelector{Id: "test-model"}}},
			Nodes: []*reliantv1.NodeRouterCandidate{
				{Id: "summarize", Description: "Summarize the request"},
			},
		}},
	}
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
		err := executor.parseRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: `{"workflow":"builtin://agent","preset":"general","prompt":"rewrite","reasoning":"best fit"}`,
		})
		require.NoError(t, err)
		require.NotNil(t, executor.decision)
		assert.Equal(t, "general", executor.decision.Preset)
		assert.Equal(t, "rewrite", executor.decision.Prompt)
	})

	t.Run("uses valid fallback preset", func(t *testing.T) {
		executor := newExecutor("researcher")
		err := executor.parseRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: `{"workflow":"builtin://agent","preset":"invalid","prompt":"rewrite","reasoning":"best fit"}`,
		})
		require.NoError(t, err)
		require.NotNil(t, executor.decision)
		assert.Equal(t, "researcher", executor.decision.Preset)
	})

	t.Run("rejects invalid fallback preset", func(t *testing.T) {
		executor := newExecutor("missing")
		err := executor.parseRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: `{"workflow":"builtin://agent","preset":"invalid","prompt":"rewrite","reasoning":"best fit"}`,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), `fallback preset "missing" is invalid`)
	})

	t.Run("parses from response_data struct", func(t *testing.T) {
		executor := newExecutor("")
		rd, err := structpb.NewStruct(map[string]interface{}{
			"workflow":  "builtin://agent",
			"preset":    "general",
			"prompt":    "test",
			"reasoning": "ok",
		})
		require.NoError(t, err)

		err = executor.parseRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseData: rd,
		})
		require.NoError(t, err)
		require.NotNil(t, executor.decision)
		assert.Equal(t, "builtin://agent", executor.decision.Workflow)
		assert.Equal(t, "general", executor.decision.Preset)
		assert.Equal(t, "test", executor.decision.Prompt)
		assert.Equal(t, "ok", executor.decision.Reasoning)
	})

	t.Run("prefers response_data over response_text", func(t *testing.T) {
		executor := newExecutor("")
		rd, err := structpb.NewStruct(map[string]interface{}{
			"workflow":  "builtin://agent",
			"preset":    "general",
			"prompt":    "from struct",
			"reasoning": "struct wins",
		})
		require.NoError(t, err)

		err = executor.parseRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseData: rd,
			ResponseText: `{"workflow":"builtin://agent","preset":"researcher","prompt":"from text","reasoning":"text source"}`,
		})
		require.NoError(t, err)
		require.NotNil(t, executor.decision)
		assert.Equal(t, "general", executor.decision.Preset, "should use response_data preset, not response_text")
		assert.Equal(t, "from struct", executor.decision.Prompt, "should use response_data prompt, not response_text")
	})

	t.Run("falls back to response_text when no response_data", func(t *testing.T) {
		executor := newExecutor("")
		err := executor.parseRoutingDecision(&reliantv1.CallLLMOutput{
			ResponseText: `{"workflow":"builtin://agent","preset":"general","prompt":"fallback","reasoning":"no struct"}`,
		})
		require.NoError(t, err)
		require.NotNil(t, executor.decision)
		assert.Equal(t, "general", executor.decision.Preset)
		assert.Equal(t, "fallback", executor.decision.Prompt)
	})

	t.Run("errors when both empty", func(t *testing.T) {
		executor := newExecutor("")
		err := executor.parseRoutingDecision(&reliantv1.CallLLMOutput{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no response_data or response_text")
	})
}

// saveMessageCall captures the arguments passed to the SaveMessage activity stub.
type saveMessageCall struct {
	ChatID     string
	Thread     string
	Role       string
	Content    string
	WorkflowID string
}

func TestExecuteSelectedWorkflow_SavesInjectMessage(t *testing.T) {
	// emptyWorkflowJSON is a minimal valid workflow with no nodes.
	emptyWorkflowJSON, err := json.Marshal(map[string]interface{}{
		"name":  "test-workflow",
		"nodes": []interface{}{},
	})
	require.NoError(t, err)

	// buildRouterNode creates the evalResult node expected by both the
	// RouterExecutor and the downstream InlineWorkflowExecutor.
	buildRouterNode := func() *reliantv1.Node {
		return &reliantv1.Node{
			Id:   "route",
			Type: model.NodeTypeRouter,
			Args: &reliantv1.Node_Router{Router: &reliantv1.RouterArgs{
				Workflows: []*reliantv1.RouterWorkflowCandidate{
					{Ref: "builtin://agent"},
				},
			}},
		}
	}

	t.Run("saves inject message when prompt is non-empty", func(t *testing.T) {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()

		// Track SaveMessage calls.
		var (
			mu    sync.Mutex
			calls []saveMessageCall
		)

		// Register a capturing SaveMessage stub that extracts the flat fields
		// from the ActivityInput envelope.
		env.RegisterActivityWithOptions(
			func(_ context.Context, input json.RawMessage) (interface{}, error) {
				// The runtime sends an ActivityInput{Runtime, Node} but Temporal
				// serialises it as JSON. We decode the parts we care about.
				var envelope struct {
					Runtime struct {
						ChatID     string `json:"chat_id"`
						Thread     string `json:"thread"`
						WorkflowID string `json:"workflow_id"`
					} `json:"runtime"`
					Node struct {
						SaveMessageNode struct {
							ResolvedRole    string `json:"resolved_role"`
							ResolvedContent string `json:"resolved_content"`
						} `json:"save_message_node"`
					} `json:"node"`
				}
				if err := json.Unmarshal(input, &envelope); err != nil {
					return nil, err
				}
				mu.Lock()
				calls = append(calls, saveMessageCall{
					ChatID:     envelope.Runtime.ChatID,
					Thread:     envelope.Runtime.Thread,
					Role:       envelope.Node.SaveMessageNode.ResolvedRole,
					Content:    envelope.Node.SaveMessageNode.ResolvedContent,
					WorkflowID: envelope.Runtime.WorkflowID,
				})
				mu.Unlock()
				return map[string]interface{}{"message_id": "msg-inject"}, nil
			},
			activity.RegisterOptions{Name: "SaveMessage"},
		)

		// Stubs for activities called by InlineWorkflowExecutor.
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
				return LoadedWorkflow{WorkflowJSON: emptyWorkflowJSON}, nil
			},
			activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
		)
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{}, nil
			},
			activity.RegisterOptions{Name: "LoadPresetParams"},
		)
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
				return nil, nil
			},
			activity.RegisterOptions{Name: "WorkflowStatus"},
		)
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
				return nil, nil
			},
			activity.RegisterOptions{Name: "EmitThreadEvent"},
		)

		env.ExecuteWorkflow(func(ctx workflow.Context) error {
			node := buildRouterNode()
			executor := NewRouterExecutor(
				ctx,
				"wf-123",      // workflowID
				"chat-456",    // chatID
				"test-router", // workflowName
				map[string]interface{}{"mode": "manual"},
				map[string]interface{}{},
				&ChildWorkflowTracker{children: make(map[string]bool)},
				node,
				node,
			)
			executor.decision = &routerDecision{
				Workflow: "builtin://agent",
				Preset:   "general",
				Prompt:   "please rewrite the code",
			}
			executor.candidates = []routerWorkflowInfo{{
				Ref:     "builtin://agent",
				Presets: []*preset.Preset{{Name: "general"}},
			}}
			executor = executor.WithExecContext(&ExecutionContext{
				WorkflowID:   "wf-123",
				ChatID:       "chat-456",
				Thread:       "thread-child-abc",
				ThreadMode:   model.ThreadModeNew,
				WorkflowName: "test-router",
			})

			_, err := executor.executeSelectedWorkflow()
			return err
		})

		require.NoError(t, env.GetWorkflowError())

		mu.Lock()
		defer mu.Unlock()
		require.GreaterOrEqual(t, len(calls), 1, "SaveMessage should have been called at least once")

		// Find the inject call (role=user, content=prompt).
		var found bool
		for _, c := range calls {
			if c.Role == "user" && c.Content == "please rewrite the code" {
				assert.Equal(t, "chat-456", c.ChatID)
				assert.Equal(t, "thread-child-abc", c.Thread)
				assert.Equal(t, "wf-123", c.WorkflowID)
				found = true
				break
			}
		}
		assert.True(t, found, "expected SaveMessage call with role=user and content=prompt; got calls: %+v", calls)
	})

	t.Run("skips inject message when prompt is empty", func(t *testing.T) {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()

		var (
			mu           sync.Mutex
			saveMsgCalls int
		)

		env.RegisterActivityWithOptions(
			func(_ context.Context, _ json.RawMessage) (interface{}, error) {
				mu.Lock()
				saveMsgCalls++
				mu.Unlock()
				return map[string]interface{}{"message_id": "msg-nope"}, nil
			},
			activity.RegisterOptions{Name: "SaveMessage"},
		)
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
				return LoadedWorkflow{WorkflowJSON: emptyWorkflowJSON}, nil
			},
			activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
		)
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (map[string]interface{}, error) {
				return map[string]interface{}{}, nil
			},
			activity.RegisterOptions{Name: "LoadPresetParams"},
		)
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
				return nil, nil
			},
			activity.RegisterOptions{Name: "WorkflowStatus"},
		)
		env.RegisterActivityWithOptions(
			func(_ context.Context, _ map[string]interface{}) (interface{}, error) {
				return nil, nil
			},
			activity.RegisterOptions{Name: "EmitThreadEvent"},
		)

		env.ExecuteWorkflow(func(ctx workflow.Context) error {
			node := buildRouterNode()
			executor := NewRouterExecutor(
				ctx,
				"wf-123",
				"chat-456",
				"test-router",
				map[string]interface{}{"mode": "manual"},
				map[string]interface{}{},
				&ChildWorkflowTracker{children: make(map[string]bool)},
				node,
				node,
			)
			executor.decision = &routerDecision{
				Workflow: "builtin://agent",
				Preset:   "general",
				Prompt:   "", // Empty prompt
			}
			executor.candidates = []routerWorkflowInfo{{
				Ref:     "builtin://agent",
				Presets: []*preset.Preset{{Name: "general"}},
			}}
			executor = executor.WithExecContext(&ExecutionContext{
				WorkflowID:   "wf-123",
				ChatID:       "chat-456",
				Thread:       "thread-child-abc",
				ThreadMode:   model.ThreadModeNew,
				WorkflowName: "test-router",
			})

			_, err := executor.executeSelectedWorkflow()
			return err
		})

		require.NoError(t, env.GetWorkflowError())

		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, 0, saveMsgCalls, "SaveMessage should NOT be called when prompt is empty")
	})
}
