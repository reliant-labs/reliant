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

// OrgAutomationNoOrgAccountNotice is one pause of an automation for want of an
// org account, for its org's owners and admins.
type OrgAutomationNoOrgAccountNotice struct {
	OrgID string
	// IncidentKey identifies this pause: a repeat is deduplicated, a later
	// pause (after it was resumed) is a new email.
	IncidentKey string
	// TriggerID and Automation are the automation's id and name.
	TriggerID, Automation string
	// Model and OwnerUserID are optional; the control plane names the owner
	// only when they are a live member of the org.
	Model, OwnerUserID string
}

// NotifyOrgAutomationNoOrgAccount is Service.NotifyOrgAutomationNoOrgAccount.
func (c *client) NotifyOrgAutomationNoOrgAccount(ctx context.Context, n OrgAutomationNoOrgAccountNotice) (bool, error) {
	in := map[string]any{
		"orgId":       n.OrgID,
		"incidentKey": n.IncidentKey,
		"orgAutomationNoOrgAccount": map[string]any{
			"triggerId": n.TriggerID, "automation": n.Automation, "model": n.Model, "ownerUserId": n.OwnerUserID,
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
