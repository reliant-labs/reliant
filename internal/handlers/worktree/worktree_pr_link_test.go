// Copyright (c) 2025 Reliant Labs
package worktree_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	worktree "github.com/reliant-labs/reliant/internal/handlers/worktree"
)

// TestCreateWorktreePRLinksTheWorktreesChats: the pull request CreateWorktreePR
// opens is linked to the worktree's chats (chat_pr_links) by the URL the
// daemon reported, and a link that fails still returns the pull request.
func TestCreateWorktreePRLinksTheWorktreesChats(t *testing.T) {
	t.Parallel()
	for name, linkErr := range map[string]error{"linked": nil, "the link fails": errors.New("database is down")} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := ownedWorktreeRepo(t)
			store.ListReposByProjectFunc = func(context.Context, string) ([]*core.Repo, error) { return nil, nil }
			send := func(_ context.Context, _, commandType string, _ []byte, _ int32) ([]byte, error) {
				switch commandType {
				case "worktree.validate_path":
					return json.Marshal(map[string]any{"exists": true})
				case "worktree.create_pr":
					return json.Marshal(map[string]any{"success": true, "pr_url": "https://github.com/acme/widgets/pull/7"})
				}
				return nil, fmt.Errorf("unexpected command %s", commandType)
			}
			router := &worktree.StubRouter{
				SendDaemonCommandFunc: send,
				SendDaemonCommandToDaemonFunc: func(ctx context.Context, userID, _, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
					return send(ctx, userID, commandType, payload, timeoutMs)
				},
			}
			type call struct{ userID, worktreeID, url string }
			var calls []call
			links := &worktree.MockPRLinker{LinkWorktreePRFunc: func(_ context.Context, userID, worktreeID, url string) (int, error) {
				calls = append(calls, call{userID, worktreeID, url})
				return 2, linkErr
			}}
			svc := worktree.NewTestWorktree(t, worktree.WithWorktreeDeps(worktree.WithReadRoles(worktree.Deps{Repo: store, Router: router, PRLinks: links})))
			resp, err := svc.CreateWorktreePR(worktree.AuthedContext(t), connect.NewRequest(&reliantv1.CreateWorktreePRRequest{
				WorktreeId: ownWorktreeID, Title: "Add it",
			}))
			require.NoError(t, err)
			assert.Equal(t, "https://github.com/acme/widgets/pull/7", resp.Msg.PrUrl)
			assert.Equal(t, []call{{"test-user", ownWorktreeID, "https://github.com/acme/widgets/pull/7"}}, calls)
		})
	}
}
