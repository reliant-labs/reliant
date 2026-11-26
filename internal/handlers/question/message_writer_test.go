package question

import (
	"context"
	"testing"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func TestSaveUserReplyMessageUsesInjectedWriter(t *testing.T) {
	t.Parallel()
	writer := &MockMessageWriter{SaveMessageToThreadFunc: func(_ context.Context, chatID, thread string, role int32, content string, workflowID *string, attachments []string, displayStyle *int32) (*db.Message, error) {
		require.Equal(t, "chat", chatID)
		require.Equal(t, "thread", thread)
		require.Equal(t, int32(pb.MessageRole_MESSAGE_ROLE_USER), role)
		require.Equal(t, "answer", content)
		require.Equal(t, "workflow", *workflowID)
		require.Nil(t, attachments)
		require.Nil(t, displayStyle)
		return &db.Message{ID: "saved"}, nil
	}}
	svc := &Service{deps: Deps{Writer: writer}}
	q := &db.Question{ChatID: "chat", ThreadID: "thread", WorkflowID: "workflow"}
	require.NoError(t, svc.saveUserReplyMessage(context.Background(), q, `{"reply":"answer"}`))
	require.Equal(t, 1, writer.CallCount("SaveMessageToThread"))
}
