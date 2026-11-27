package inbox

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for InboxService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project answers a viewer of a shared
// one per viewertest.Matrix.
func TestInboxServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestInbox(t, withDeps(Deps{Repo: repo, Registrations: store.NewTriggerRegistrationExtStore(repo.ORM()), Orgs: f.Membership}))
	viewertest.RunService(t, f, svc, "reliant.v1.InboxService", nil)
}
