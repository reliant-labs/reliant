package chat

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for ChatService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project, asked as the owner, a viewer
// of the shared chat and an outsider, answers per viewertest.Matrix: the
// read-only chat to a viewer, and for everything else exactly what an
// outsider gets.
func TestChatServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestChat(t, WithChatDeps(Deps{TriggerReads: &MockTriggerReads{}, Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Orgs: f.Membership, Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}))
	viewertest.RunService(t, f, svc, "reliant.v1.ChatService", nil)
}
