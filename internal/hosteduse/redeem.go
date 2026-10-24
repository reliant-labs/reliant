package hosteduse

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// Redeem atomically consumes an exact, unexpired ticket and returns only trusted
// metadata. Authentication belongs to the internal transport before this call.
// Denials are deliberately indistinguishable; digest/grant mismatches do not
// consume the ticket. The DB rechecks current active run ownership at consume.
func Redeem(ctx context.Context, store Store, req RedeemRequest) (*Metadata, error) {
	digest, err := hex.DecodeString(req.RequestSHA256)
	if err != nil || len(digest) != 32 || strings.ToLower(req.RequestSHA256) != req.RequestSHA256 || req.TicketID == "" || req.GrantID == "" || len(req.TicketID) > 256 || len(req.GrantID) > 256 {
		return nil, errors.New("hosted HTTP ticket denied")
	}
	if err = ctx.Err(); err != nil {
		return nil, errors.New("hosted HTTP ticket denied")
	}
	t, err := store.RedeemHostedHTTPActionTicket(ctx, req.TicketID, req.GrantID, req.RequestSHA256)
	if err != nil {
		return nil, errors.New("hosted HTTP ticket redemption unavailable")
	}
	if t == nil || !t.ExpiresAt.After(time.Now()) {
		return nil, errors.New("hosted HTTP ticket denied")
	}
	return &Metadata{OwnerUserID: t.OwnerUserID, ProjectID: t.ProjectID, WorkflowID: t.WorkflowID, WorkflowName: t.WorkflowName, NodeID: t.NodeID, RunID: t.WorkflowID, ExpiresAt: t.ExpiresAt}, nil
}
