// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
)

// A tool call runs on whichever worker picks up the execute_tools activity,
// not necessarily the one whose call_llm hydrated the user's session token.
// The batch's own token must reach this process, or tools that act as the
// user (list_changes, manage_change) alternate between working and "no live
// sign-in" within one turn.
func TestOfferRunUserJWTHydratesThisWorker(t *testing.T) {
	chat := &db.Chat{UserID: "user-execute-tools-jwt"}
	_, ok := auth.GetUserJWT(chat.UserID)
	assert.False(t, ok, "precondition: this process has no token for the user")

	offerRunUserJWT(context.Background(), chat)
	_, ok = auth.GetUserJWT(chat.UserID)
	assert.False(t, ok, "no run token, nothing offered")

	ctx := context.WithValue(context.Background(), runUserJWTKey{}, "run-token")
	offerRunUserJWT(ctx, nil)
	offerRunUserJWT(ctx, chat)
	got, ok := auth.GetUserJWT(chat.UserID)
	assert.True(t, ok)
	assert.Equal(t, "run-token", got)
}
