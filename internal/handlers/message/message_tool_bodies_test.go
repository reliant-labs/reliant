package message_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/dbtest"
	message "github.com/reliant-labs/reliant/internal/handlers/message"
	"github.com/reliant-labs/reliant/pkg/config"
)

// GetMessage under ToolBodyMode (dev-docs/design/chat-lazy-tool-bodies.md):
// unset is FULL byte for byte, STUB elides a finished call's bodies, and the
// kill switch serves STUB as FULL.
func TestGetMessageToolBodies(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	defer cleanup()
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	f := dbtest.SeedTranscriptBodies(t, ctx, repo, "test-user")

	get := func(t *testing.T, svc *message.Service, mode *reliantv1.ToolBodyMode) *reliantv1.Message {
		t.Helper()
		resp, err := svc.GetMessage(ctx, connect.NewRequest(&reliantv1.GetMessageRequest{MessageId: f.AssistantMessageID, ToolBodies: mode}))
		require.NoError(t, err)
		return resp.Msg.Message
	}
	marshal := func(t *testing.T, m *reliantv1.Message) []byte {
		t.Helper()
		b, err := proto.MarshalOptions{Deterministic: true}.Marshal(m)
		require.NoError(t, err)
		return b
	}
	block := func(m *reliantv1.Message, id string) *reliantv1.ContentBlock {
		for _, b := range m.ContentBlocks {
			if b.Id == id {
				return b
			}
		}
		return nil
	}
	svc := message.NewTestMessage(t, message.WithMessageDeps(message.Deps{Repo: repo}))
	stub := reliantv1.ToolBodyMode_TOOL_BODY_MODE_STUB.Enum()

	unset := get(t, svc, nil)
	assert.Equal(t, marshal(t, unset), marshal(t, get(t, svc, reliantv1.ToolBodyMode_TOOL_BODY_MODE_FULL.Enum())))
	assert.Equal(t, f.DoneInput, block(unset, f.DoneCallBlockID).GetInput())
	assert.Equal(t, f.Thinking, block(unset, f.ThinkingBlockID).GetContent())
	for _, b := range unset.ContentBlocks {
		assert.Nil(t, b.BodyElided, b.Id)
	}

	stubbed := get(t, svc, stub)
	assert.True(t, block(stubbed, f.DoneCallBlockID).GetBodyElided())
	assert.Equal(t, int64(len(f.DoneInput)), block(stubbed, f.DoneCallBlockID).GetBodySize())
	assert.Equal(t, f.RunningInput, block(stubbed, f.RunningCallBlockID).GetInput(), "an in-flight call is whole")
	assert.True(t, block(stubbed, f.ThinkingBlockID).GetBodyElided())

	off := message.NewTestMessage(t, message.WithMessageDeps(message.Deps{Repo: repo, Config: &config.Config{Transcript: &configv1.TranscriptConfig{ToolBodyStubs: "off"}}}))
	assert.Equal(t, marshal(t, unset), marshal(t, get(t, off, stub)))
}
