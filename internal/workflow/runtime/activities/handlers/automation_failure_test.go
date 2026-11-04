package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/stretchr/testify/require"
)

func TestAutomationFailureFallbackAndDetail(t *testing.T) {
	ctx := context.Background()
	end := time.Now()
	for _, kind := range []string{"schedule", "chat.start"} {
		t.Run(kind, func(t *testing.T) {
			tid := "trigger"
			saved := 0
			var key string
			detail := ""
			repo := &MockWorkflowStatusStore{
				GetChatFunc: func(context.Context, string) (*db.Chat, error) {
					return &db.Chat{ID: "chat", LaunchKind: kind, TriggerID: &tid}, nil
				},
				ListRunNodeExecutionsFunc: func(context.Context, string, int) ([]*db.StepExecution, error) {
					return []*db.StepExecution{{ID: "attempt", StepID: "gate", CreatedAt: end, ExitCode: sql.NullInt64{Int64: 2, Valid: true}, ErrorMessage: sql.NullString{String: "client_secret: secret", Valid: true}}}, nil
				},
				SetLaunchEventRunFailureDetailFunc: func(_ context.Context, _, s string) error { detail = s; return nil },
				GetLatestMessageInThreadFunc: func(context.Context, string) (*db.Message, error) {
					return &db.Message{Role: pb.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: end.Add(-time.Second)}, nil
				},
				SaveMessageToThreadOnceFunc: func(_ context.Context, chat, thread string, role int32, content string, wf *string, attachments []string, style *int32, id string) (*db.Message, bool, error) {
					require.Equal(t, "chat", chat)
					require.Equal(t, "root", thread)
					require.Equal(t, int32(pb.MessageRole_MESSAGE_ROLE_ASSISTANT), role)
					require.Equal(t, "Node gate failed: [REDACTED: credential-bearing diagnostic omitted]", content)
					if key == "" {
						key = id
						saved++
					} else {
						require.Equal(t, key, id)
					}
					return nil, true, nil
				},
			}
			a := NewWorkflowStatusActivity(repo)
			a.ensureAutomationFailureMessage(ctx, WorkflowStatusInput{ChatID: "chat", WorkflowID: "root"})
			a.ensureAutomationFailureMessage(ctx, WorkflowStatusInput{ChatID: "chat", WorkflowID: "root"})
			require.Equal(t, "Node gate failed: [REDACTED: credential-bearing diagnostic omitted]", detail)
			if kind == "schedule" {
				require.Equal(t, 1, saved)
			} else {
				require.Zero(t, saved)
			}
		})
	}
}

func TestAutomationFailureSuppressesAmbiguousSingleLineAssignments(t *testing.T) {
	for _, text := range []string{
		`password="prefix\"opaque-secret-value"`,
		`password='prefix'\''opaque-secret-value'`,
		`password="prefix\\opaque-secret-value"`,
		`password="prefix\\nopaque-secret-value"`,
		`password="prefix;opaque-secret-value"`,
		`password=[REDACTED]opaque-secret-value`,
		`password=opaque-secret-value`,
	} {
		for _, tail := range []bool{false, true} {
			if tail {
				text = strings.Repeat("before\n", 45) + text + strings.Repeat("\nafter", 49)
			}
			raw, _ := json.Marshal(map[string]string{"stderr": text})
			tid := "trigger"
			saved := false
			repo := &MockWorkflowStatusStore{
				GetChatFunc: func(context.Context, string) (*db.Chat, error) {
					return &db.Chat{ID: "chat", LaunchKind: "schedule", TriggerID: &tid}, nil
				},
				ListRunNodeExecutionsFunc: func(context.Context, string, int) ([]*db.StepExecution, error) {
					return []*db.StepExecution{{StepID: "gate", ActivityName: "ExecuteRunStep", ExitCode: sql.NullInt64{Int64: 2, Valid: true}, OutputJSON: sql.NullString{String: string(raw), Valid: true}}}, nil
				},
				SetLaunchEventRunFailureDetailFunc: func(_ context.Context, _, detail string) error {
					require.NotContains(t, detail, "opaque-secret-value")
					return nil
				},
				GetLatestMessageInThreadFunc: func(context.Context, string) (*db.Message, error) { return nil, nil },
				SaveMessageToThreadOnceFunc: func(_ context.Context, _, _ string, _ int32, content string, _ *string, _ []string, _ *int32, _ string) (*db.Message, bool, error) {
					require.NotContains(t, content, "opaque-secret-value")
					require.Contains(t, content, "credential-bearing diagnostic omitted")
					saved = true
					return nil, true, nil
				},
			}
			NewWorkflowStatusActivity(repo).ensureAutomationFailureMessage(context.Background(), WorkflowStatusInput{ChatID: "chat", WorkflowID: "root"})
			require.True(t, saved)
		}
	}
}

// A run that ends at an `outcome: failure` save_message: once its node is
// marked, the failure detail is that node and the text it wrote, and the
// node's own message is not repeated by a fallback (bug 938cc4e2: every
// surface said "declared failure without a recorded node error").
func TestAutomationFailureNamesTheDeclaringSaveMessage(t *testing.T) {
	end := time.Now()
	trigger := "t"
	text := "Review did not finish: the review was incomplete"
	detail := ""
	repo := &MockWorkflowStatusStore{
		GetChatFunc: func(context.Context, string) (*db.Chat, error) {
			return &db.Chat{ID: "c", LaunchKind: string(core.TriggerEventKindSchedule), TriggerID: &trigger}, nil
		},
		ListRunNodeExecutionsFunc: func(context.Context, string, int) ([]*db.StepExecution, error) {
			return []*db.StepExecution{{ID: "row", StepID: "stopped", ActivityName: "SaveMessage", CreatedAt: end,
				Success:      sql.NullBool{Bool: false, Valid: true},
				ErrorMessage: sql.NullString{String: "declared the run's outcome: failure", Valid: true},
				OutputJSON:   sql.NullString{String: `{"message":"` + text + `"}`, Valid: true}}}, nil
		},
		SetLaunchEventRunFailureDetailFunc: func(_ context.Context, _, s string) error { detail = s; return nil },
		GetLatestMessageInThreadFunc: func(context.Context, string) (*db.Message, error) {
			return &db.Message{ID: "m", Role: pb.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: end.Add(-time.Millisecond)}, nil
		},
		ListContentBlocksFunc: func(context.Context, string) ([]*db.MessageContentBlock, error) {
			return []*db.MessageContentBlock{{BlockType: pb.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: &text}}, nil
		},
	}
	NewWorkflowStatusActivity(repo).ensureAutomationFailureMessage(context.Background(),
		WorkflowStatusInput{ChatID: "c", WorkflowID: "root", Status: "completed", Outcome: "failure", OutcomeNode: "stopped"})
	require.Equal(t, "Node stopped failed: declared the run's outcome: failure: "+text, detail)
	require.Zero(t, repo.CallCount("SaveMessageToThreadOnce"))
}

// A declaring node with no attempt (a compute node) is still named.
func TestAutomationFailureNamesADeclaringNodeWithNoAttempt(t *testing.T) {
	detail := ""
	repo := &MockWorkflowStatusStore{
		GetChatFunc:                        func(context.Context, string) (*db.Chat, error) { return &db.Chat{ID: "c"}, nil },
		ListRunNodeExecutionsFunc:          func(context.Context, string, int) ([]*db.StepExecution, error) { return nil, nil },
		SetLaunchEventRunFailureDetailFunc: func(_ context.Context, _, s string) error { detail = s; return nil },
	}
	NewWorkflowStatusActivity(repo).ensureAutomationFailureMessage(context.Background(),
		WorkflowStatusInput{ChatID: "c", WorkflowID: "root", Status: "completed", Outcome: "failure", OutcomeNode: "verdict"})
	require.Equal(t, "Node verdict declared the run's outcome: failure.", detail)
}

func TestAutomationFailurePreservesAuthoredFailureMessage(t *testing.T) {
	end := time.Now()
	trigger := "t"
	text := "The gate failed; please repair compilation."
	repo := &MockWorkflowStatusStore{
		GetChatFunc: func(context.Context, string) (*db.Chat, error) {
			return &db.Chat{ID: "c", LaunchKind: string(core.TriggerEventKindSchedule), TriggerID: &trigger}, nil
		},
		ListRunNodeExecutionsFunc: func(context.Context, string, int) ([]*db.StepExecution, error) {
			return []*db.StepExecution{{StepID: "gate", ErrorMessage: sql.NullString{String: "bad", Valid: true}, CreatedAt: end}}, nil
		},
		SetLaunchEventRunFailureDetailFunc: func(context.Context, string, string) error { return nil },
		GetLatestMessageInThreadFunc: func(context.Context, string) (*db.Message, error) {
			return &db.Message{ID: "message", Role: pb.MessageRole_MESSAGE_ROLE_ASSISTANT, CreatedAt: end.Add(time.Second)}, nil
		},
	}
	// Use the existing content-block seam directly: the fallback must not need
	// to load a whole transcript just to inspect the latest saved message.
	repo.ListContentBlocksFunc = func(context.Context, string) ([]*db.MessageContentBlock, error) {
		return []*db.MessageContentBlock{{BlockType: pb.ContentBlockType_CONTENT_BLOCK_TYPE_TEXT, Content: &text}}, nil
	}
	NewWorkflowStatusActivity(repo).ensureAutomationFailureMessage(context.Background(), WorkflowStatusInput{ChatID: "c", WorkflowID: "root"})
	require.Zero(t, repo.CallCount("SaveMessageToThreadOnce"))
}
