// Copyright (c) 2025 Reliant Labs
package services

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

func TestInterruptThread_TranslatesServiceResult(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupSendAgentMessageFixture(t, repo, "test-user")
	svc := &ChatService{database: repo, threads: threads.NewService(repo)}

	resp, err := svc.InterruptThread(ctx, connect.NewRequest(&reliantv1.InterruptThreadRequest{
		ChatId:   fx.chatID,
		ThreadId: fx.rootThreadID,
	}))
	require.NoError(t, err)
	assert.Equal(t, int32(0), resp.Msg.CancelledToolCalls)
	assert.Empty(t, resp.Msg.UndeliverableToolCalls)
}

func TestInterruptThread_MapsDomainErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code connect.Code
	}{
		{name: "invalid argument", err: threads.ErrInvalidArgument, code: connect.CodeInvalidArgument},
		{name: "not found", err: threads.ErrNotFound, code: connect.CodeNotFound},
		{name: "interrupt undeliverable", err: threads.ErrInterruptUndeliverable, code: connect.CodeUnavailable},
		{name: "unknown", err: errors.New("database unavailable"), code: connect.CodeInternal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := interruptThreadError(tt.err)
			require.Error(t, err)
			assert.Equal(t, tt.code, connect.CodeOf(err))
		})
	}
}
