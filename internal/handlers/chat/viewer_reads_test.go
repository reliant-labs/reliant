package chat

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
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
	svc := NewTestChat(t, WithChatDeps(Deps{Repo: repo, Orgs: f.Membership}))
	viewertest.RunService(t, f, svc, "reliant.v1.ChatService", nil)
}
