package trigger

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/projectaccess/projectaccesstest"
	"github.com/reliant-labs/reliant/internal/store"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// The viewer matrix for TriggerService (control-plane docs/design/org-sharing.md
// §4): every rpc that takes a chat or a project answers a viewer of a shared
// one per viewertest.Matrix.
func TestTriggerServiceViewerMatrix(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestTrigger(t, WithTriggerDeps(realStores(repo, Deps{Repo: repo, Triggers: store.NewTriggerExtStore(repo.ORM()), FiringReads: store.NewTriggerEventExtStore(repo.ORM()), Registrations: store.NewTriggerRegistrationExtStore(repo.ORM()), Projects: projectaccesstest.For(repo), Orgs: f.Membership})))
	viewertest.RunService(t, f, svc, "reliant.v1.TriggerService", nil)
}
