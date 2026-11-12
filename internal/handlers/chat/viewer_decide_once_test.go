package chat

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// A viewer's answer is shaped by the one access decision that admitted them.
// Membership here answers "member" once and then denies (an expiry) or fails
// (an outage): a read site that asked again before shaping would hand the
// viewer the owner's shape. Every site must ask exactly once and answer in the
// viewer's shape: no tool result, no resolved step value, none of the owner's
// composer, workspace or machine.
func TestViewerShapeIsDecidedOnce(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	step := f.SeedResolvedStep(t)
	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: f.Membership}))

	for _, after := range []struct {
		name string
		err  error
	}{{"membership expires", nil}, {"membership outage", errors.New("control plane down")}} {
		t.Run(after.name, func(t *testing.T) {
			read := func(t *testing.T, call func(m *viewertest.FirstAnswerOnly) (proto.Message, error)) proto.Message {
				t.Helper()
				m := &viewertest.FirstAnswerOnly{First: f.Membership, After: after.err}
				got, err := call(m)
				require.NoError(t, err)
				assert.Equal(t, 1, m.Asked(), "access is decided once")
				wire, err := protojson.Marshal(got)
				require.NoError(t, err)
				assert.NotContains(t, string(wire), step.Secret, "a resolved step value reached a viewer")
				assert.NotContains(t, string(wire), f.Transcript.Result, "a tool result reached a viewer")
				return got
			}

			t.Run("GetChat", func(t *testing.T) {
				got := read(t, func(m *viewertest.FirstAnswerOnly) (proto.Message, error) {
					res, err := svc.GetChat(f.ContextWith(f.ViewerID, m), connect.NewRequest(&reliantv1.GetChatRequest{ChatId: f.ChatID}))
					if err != nil {
						return nil, err
					}
					return res.Msg, nil
				}).(*reliantv1.GetChatResponse)
				assert.True(t, got.GetChat().GetViewerAccess())
				assert.Nil(t, got.GetChat().Selection)
				assert.Nil(t, got.GetChat().Worktree)
				assert.Nil(t, got.GetChat().ActiveDaemonId)
			})
			t.Run("ListMessages", func(t *testing.T) {
				got := read(t, func(m *viewertest.FirstAnswerOnly) (proto.Message, error) {
					res, err := svc.ListMessages(f.ContextWith(f.ViewerID, m), connect.NewRequest(&reliantv1.ListMessagesRequest{
						ChatId: f.ChatID, ToolBodies: reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL.Enum(),
					}))
					if err != nil {
						return nil, err
					}
					return res.Msg, nil
				}).(*reliantv1.ListMessagesResponse)
				result := blocksByID(got.GetMessages())[f.Transcript.ResultBlockID]
				require.NotNil(t, result)
				assert.True(t, result.GetBodyElided())
			})
			t.Run("ListStepExecutions", func(t *testing.T) {
				got := read(t, func(m *viewertest.FirstAnswerOnly) (proto.Message, error) {
					res, err := svc.ListStepExecutions(f.ContextWith(f.ViewerID, m), connect.NewRequest(&reliantv1.ListStepExecutionsRequest{ChatId: f.ChatID}))
					if err != nil {
						return nil, err
					}
					return res.Msg, nil
				}).(*reliantv1.ListStepExecutionsResponse)
				require.NotEmpty(t, got.GetStepExecutions(), "the step is listed")
			})
			t.Run("GetWorkflowExecutions", func(t *testing.T) {
				read(t, func(m *viewertest.FirstAnswerOnly) (proto.Message, error) {
					res, err := svc.GetWorkflowExecutions(f.ContextWith(f.ViewerID, m), connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{ChatId: f.ChatID}))
					if err != nil {
						return nil, err
					}
					return res.Msg, nil
				})
			})
		})
	}
}

// A step's resolved input is a resolved value, like its output: a template
// such as `{{ steps.read.output }}` resolves an earlier tool's result into it.
// A viewer reads which step ran and how it ended, never what it was given or
// produced; the owner reads all of it.
func TestViewerStepExecutionsCarryNoResolvedValue(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	step := f.SeedResolvedStep(t)
	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: f.Membership}))
	req := connect.NewRequest(&reliantv1.ListStepExecutionsRequest{ChatId: f.ChatID})

	byID := func(steps []*reliantv1.StepExecution) *reliantv1.StepExecution {
		for _, s := range steps {
			if s.GetId() == step.StepID {
				return s
			}
		}
		t.Fatalf("step %s is not listed", step.StepID)
		return nil
	}

	viewed, err := svc.ListStepExecutions(f.Viewer, req)
	require.NoError(t, err)
	got := byID(viewed.Msg.GetStepExecutions())
	assert.Empty(t, got.GetInputJson(), "a viewer never reads a step's resolved input")
	assert.Empty(t, got.GetOutputJson())
	assert.Empty(t, got.GetErrorMessage())
	assert.Equal(t, "deploy", got.GetStepId(), "which step ran stays")
	assert.Equal(t, "agent.deploy", got.GetNodePath())
	assert.False(t, got.GetSuccess(), "how it ended stays")

	owned, err := svc.ListStepExecutions(f.Owner, req)
	require.NoError(t, err)
	assert.Contains(t, byID(owned.Msg.GetStepExecutions()).GetInputJson(), step.Secret, "the owner reads the step's input")
}
