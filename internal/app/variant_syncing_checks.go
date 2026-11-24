// yours: scaffolded once by `forge scaffold variant`, never touched again
package app

import (
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/connauth"
	"github.com/reliant-labs/reliant/internal/integrations/ghdelegated"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/workflowcheck"
)

// SyncingChecks is the configuration of workflowcheck.Service that writes
// GitHub: the store, the GitHub check_run actions run as the App through the
// credential source's GitHub broker, and the hosted run URL. The worker's
// WorkflowCheckSyncer drives it; RecordOnlyChecks is the record-only one.
type SyncingChecks struct{ workflowcheck.Service }

// SyncingChecksDeps is where checks are recorded and the credential source
// whose GitHub broker writes them as the App.
type SyncingChecksDeps struct {
	DB          *db.Repo
	Credentials *WorkerIntegrationCredentials
}

// NewSyncingChecks builds the GitHub-writing workflowcheck.Service. Without
// the GitHub broker (a deployment with no control plane) every write fails
// with that reason and is retried, so nothing is written as anyone but the
// App.
//
// Root-only: no other root may reach it, and it never fills an interface
// field it merely satisfies.
//
//forge:root-only worker
func NewSyncingChecks(d SyncingChecksDeps) (*SyncingChecks, error) {
	var broker workflowcheck.AppBroker
	if d.Credentials != nil {
		if cs, ok := d.Credentials.Source.(connauth.Service); ok {
			if b, ok := cs.Broker(ghdelegated.BrokerID); ok {
				broker, _ = b.(workflowcheck.AppBroker)
			}
		}
	}
	return &SyncingChecks{Service: workflowcheck.NewInstrumented(workflowcheck.Deps{
		Store:  store.NewWorkflowCheckRunExtStore(d.DB.ORM()),
		GitHub: workflowcheck.NewGitHub(workflowcheck.GitHubDeps{Runner: httpaction.NewRunner(netguard.New()), Broker: broker}),
		RunURL: workflowcheck.HostedRunURL,
	})}, nil
}
