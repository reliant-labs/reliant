package catalog_test

import (
	"context"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
)

// noProvidersUser has no provider credentials in any repository the
// drivers package may have been pointed at, so the registry contributes
// nothing to the catalog it is shown.
const noProvidersUser = "catalog-no-providers"

// localDaemons is a local model directory holding these daemons for every
// caller.
type localDaemons []local.DaemonInventory

func (d localDaemons) LocalDaemons(context.Context, string) ([]local.DaemonInventory, error) {
	return d, nil
}

// ollamaDaemon is an online daemon serving one chat model through ollama.
func ollamaDaemon(daemonID, model string) local.DaemonInventory {
	return local.DaemonInventory{
		DaemonID: daemonID,
		Machine:  "GPU box",
		Online:   true,
		Inventory: &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{{
			Id:     "ollama",
			Kind:   "ollama",
			Models: []*reliantv1.LocalModelInfo{{Name: model, ContextWindow: 40960, SupportsTools: true, SupportsChat: true}},
		}}},
	}
}
