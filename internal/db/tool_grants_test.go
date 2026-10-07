// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// ListToolGrants is the durable record a coarse fresh restart rebuilds each
// thread's load_tool grants from. It returns what each result granted, keyed
// by the call's thread and confined to the chat; a result rewritten without a
// grant (an error replacing it) no longer grants anything.
func TestListToolGrants(t *testing.T) {
	repo, cleanup := SetupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	chatID := "chat-tool-grants"
	createActivityTestChat(t, repo, chatID)
	child := "thread-tool-grants-child"
	if _, err := repo.CreateThread(ctx, &Thread{ID: child, ChatID: chatID, CreatedAt: time.Now()}); err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	other := "chat-tool-grants-other"
	createActivityTestChat(t, repo, other)

	recordTestResult(t, repo, chatID, chatID, "tc-a", "load_tool", []string{"sourcegraph"})
	recordTestResult(t, repo, chatID, chatID, "tc-b", "load_tool", []string{"fetch", "generate_image"})
	recordTestResult(t, repo, chatID, child, "tc-c", "load_tool", []string{"fetch"})
	recordTestResult(t, repo, chatID, chatID, "tc-d", "view", nil)
	recordTestResult(t, repo, other, other, "tc-e", "load_tool", []string{"sourcegraph"})

	// A grant whose result was later replaced by one that grants nothing.
	recordTestResult(t, repo, chatID, chatID, "tc-f", "load_tool", []string{"web_search"})
	recordTestResult(t, repo, chatID, chatID, "tc-f", "load_tool", nil)

	got, err := repo.ListToolGrants(ctx, chatID)
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	want := []*ToolGrant{
		{ThreadID: chatID, Tools: []string{"sourcegraph"}},
		{ThreadID: chatID, Tools: []string{"fetch", "generate_image"}},
		{ThreadID: child, Tools: []string{"fetch"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListToolGrants:\n got %+v\nwant %+v", describeGrants(got), describeGrants(want))
	}
}

func recordTestResult(t *testing.T, repo *Repo, chatID, thread, id, toolName string, granted []string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := repo.UpsertToolCall(ctx, &ToolCall{
		ID: id, ChatID: chatID, ThreadID: &thread, ToolName: toolName, Status: core.ToolCallStatusCompleted,
		RequestedAt: now, CompletedAt: &now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertToolCall(%s): %v", id, err)
	}
	if err := repo.UpsertToolCallResult(ctx, chatID, &ToolCallResult{
		ToolCallID: id, Content: "ok", GrantedTools: granted, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertToolCallResult(%s): %v", id, err)
	}
}

func describeGrants(grants []*ToolGrant) []ToolGrant {
	out := make([]ToolGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, *g)
	}
	return out
}
