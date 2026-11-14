package controlplane

import (
	"context"
	"errors"
)

// OrgNotifyInternalService (control-plane), withheld from the exported
// contract like ProjectOrgMoveInternalService: Connect unary over plain
// HTTP+JSON, signed with this process's internal-service identity.
const orgNotifyAdminsPath = "/controlplane.v1.OrgNotifyInternalService/NotifyOrgAdmins"

// ErrOrgNotifyUnsupported is a control plane without OrgNotifyInternalService,
// or an adapter with no internal-service identity to call it with.
var ErrOrgNotifyUnsupported = errors.New("controlplane: emailing an organization's admins is not available")

// OrgKeyRejectedNotice is one rejection of an org's own API key, for its
// owners and admins.
type OrgKeyRejectedNotice struct {
	OrgID string
	// IncidentKey identifies this rejection: a repeat with the same key is
	// deduplicated, a later rejection (after a rotation) is a new email.
	IncidentKey string
	// AccountID is the org key's provider_accounts id.
	AccountID string
	// Provider is the provider's display name.
	Provider string
	// Label, KeyHint and Reason are optional.
	Label, KeyHint, Reason string
}

// NotifyOrgKeyRejected is Service.NotifyOrgKeyRejected.
func (c *client) NotifyOrgKeyRejected(ctx context.Context, n OrgKeyRejectedNotice) (bool, error) {
	in := map[string]any{
		"orgId":       n.OrgID,
		"incidentKey": n.IncidentKey,
		"orgProviderKeyRejected": map[string]any{
			"accountId": n.AccountID, "provider": n.Provider, "label": n.Label, "keyHint": n.KeyHint, "reason": n.Reason,
		},
	}
	var out struct {
		Deduplicated bool `json:"deduplicated"`
	}
	err := c.projectOrgMoveCall(ctx, orgNotifyAdminsPath, in, &out)
	if errors.Is(err, ErrProjectOrgMoveUnsupported) {
		return false, ErrOrgNotifyUnsupported
	}
	if err != nil {
		return false, err
	}
	return out.Deduplicated, nil
}
