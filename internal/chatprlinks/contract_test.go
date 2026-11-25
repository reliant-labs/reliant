// Contract test rows; canonical pattern in `forge skill load testing/patterns`.
package chatprlinks_test

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/tdd"

	"github.com/reliant-labs/reliant/internal/chatprlinks"
	"github.com/reliant-labs/reliant/internal/db/core"
)

var fixedNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

// TestContract: the write methods turn what each writer knows into the one
// store write, and write nothing for what names no pull request.
func TestContract(t *testing.T) {
	t.Parallel()

	var writes []core.ChatPRLinkWrite
	store := &chatprlinks.MockStore{
		ListWorktreeChatIDsFunc: func(_ context.Context, userID, worktreeID string) ([]string, error) {
			if userID == "alice" && worktreeID == "wt-1" {
				return []string{"chat-1", "chat-2"}, nil
			}
			return nil, nil
		},
		WriteChatPRLinksFunc: func(_ context.Context, w core.ChatPRLinkWrite) (int, error) {
			writes = append(writes, w)
			return len(w.ChatIDs), nil
		},
	}
	svc, err := chatprlinks.New(chatprlinks.Deps{Store: store, Chats: &chatprlinks.MockChats{}, Now: func() time.Time { return fixedNow }})
	if err != nil {
		t.Fatalf("chatprlinks.New: %v", err)
	}
	ctx := context.Background()
	pr7 := core.PRRef{Provider: "github", Repository: "acme/widgets", Number: 7}

	cases := []tdd.ContractCase{
		{
			Name: "LinkWorktreePR links every chat of the worktree",
			Call: func() (any, error) {
				writes = nil
				n, err := svc.LinkWorktreePR(ctx, "alice", "wt-1", "https://github.com/Acme/Widgets/pull/7")
				if err != nil || len(writes) != 1 {
					return writes, err
				}
				return []any{n, writes[0]}, nil
			},
			Want: []any{2, core.ChatPRLinkWrite{PRRef: pr7, ChatIDs: []string{"chat-1", "chat-2"}, Source: core.ChatPRLinkWorktreePR, At: fixedNow}},
		},
		{
			Name: "LinkWorktreePR writes nothing for a URL that names no pull request",
			Call: func() (any, error) {
				writes = nil
				n, err := svc.LinkWorktreePR(ctx, "alice", "wt-1", "https://gitlab.com/acme/widgets/-/merge_requests/7")
				return []any{n, len(writes)}, err
			},
			Want: []any{0, 0},
		},
		{
			Name: "LinkRunPR links the run's chat with the run",
			Call: func() (any, error) {
				writes = nil
				n, err := svc.LinkRunPR(ctx, "chat-1", "run-1", pr7)
				if err != nil || len(writes) != 1 {
					return writes, err
				}
				return []any{n, writes[0]}, nil
			},
			Want: []any{1, core.ChatPRLinkWrite{PRRef: pr7, ChatIDs: []string{"chat-1"}, RunID: "run-1", Source: core.ChatPRLinkIntegration, At: fixedNow}},
		},
		{
			Name: "LinkRunPR without a chat writes nothing",
			Call: func() (any, error) {
				writes = nil
				n, err := svc.LinkRunPR(ctx, "", "run-1", pr7)
				return []any{n, len(writes)}, err
			},
			Want: []any{0, 0},
		},
	}
	tdd.TableContract(t, svc, cases)
}

func TestParsePRURL(t *testing.T) {
	t.Parallel()
	ok := map[string]core.PRRef{
		"https://github.com/acme/widgets/pull/7":                 {Provider: "github", Repository: "acme/widgets", Number: 7},
		"https://github.com/Acme/Widgets/pull/12/files?w=1#diff": {Provider: "github", Repository: "acme/widgets", Number: 12},
		" https://www.github.com/acme/widgets/pull/3\n":          {Provider: "github", Repository: "acme/widgets", Number: 3},
	}
	for in, want := range ok {
		if got, parsed := chatprlinks.ParsePRURL(in); !parsed || got != want {
			t.Errorf("ParsePRURL(%q) = %+v, %v; want %+v", in, got, parsed, want)
		}
	}
	for _, in := range []string{
		"",
		"https://github.com/acme/widgets/issues/7",
		"https://github.com/acme/widgets/pull/0",
		"https://github.com/acme/widgets/pull/x",
		"https://github.com/acme/pull/7",
		"https://evil.example/acme/widgets/pull/7",
		"https://github.com.evil.example/acme/widgets/pull/7",
		"ftp://github.com/acme/widgets/pull/7",
		"github.com/acme/widgets/pull/7",
	} {
		if got, parsed := chatprlinks.ParsePRURL(in); parsed {
			t.Errorf("ParsePRURL(%q) = %+v, want no pull request", in, got)
		}
	}
}
