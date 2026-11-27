// forge:deps — consumer-declared interfaces for this package's Deps.
// mock_gen.go mocks them. store.TriggerExtStore is the TriggerReads and
// *trigger.Service the TriggerAdopter; the composition root hands them to New.
package automation_takeover

import (
	"context"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// TriggerReads reads one trigger by id. store.TriggerExtStore satisfies it.
type TriggerReads interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
}

// Offers is the org-wide read of what a takeover lists and the offers it
// makes and settles. internal/triggerowner's Service satisfies it: its writes
// run under the trigger's row lock, so an offer is made only for the owner it
// was decided on.
type Offers interface {
	ListOrgTriggers(ctx context.Context, orgID string) ([]*core.Trigger, error)
	ListOrgWorkflowDrafts(ctx context.Context, orgID string) ([]*core.WorkflowDraft, error)
	CreateTriggerOffer(ctx context.Context, o *core.TriggerOffer) error
	GetTriggerOffer(ctx context.Context, id string) (*core.TriggerOffer, error)
	ListPendingTriggerOffers(ctx context.Context, f core.TriggerOfferFilters) ([]*core.TriggerOffer, error)
	SettleTriggerOffer(ctx context.Context, id, status, reason string) (bool, error)
}

// TriggerAdopter is the trigger service's half of a takeover.
type TriggerAdopter interface {
	// AdoptAs re-owns t to adopterID with rb's references (accepting
	// offerID when set), disabled.
	AdoptAs(ctx context.Context, t *core.Trigger, adopterID string, rb *pb.TriggerRebinding, offerID string) (*pb.AcceptTriggerOfferResponse, error)
	// RenderDefinition renders t for someone who does not own it: param
	// values, connection and event payloads withheld; the param names.
	RenderDefinition(ctx context.Context, t *core.Trigger) (*pb.Trigger, []string)
}
