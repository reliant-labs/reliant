package cloud

import (
	"context"
	"fmt"
	"strings"
)

// ProcGetOrganization is the one control-plane call that answers "which
// organization does this credential act for". The server derives the answer
// from the authenticated token row — never from anything the client sends. The
// method's name is the control plane's identifier and cannot be renamed here.
const ProcGetOrganization = "controlplane.v1.DeployService/GetTenant"

// Caller is the one method ResolveOrganization needs.
type Caller interface {
	Call(ctx context.Context, procedure string, req, out any) error
}

// ResolveOrganization asks the control plane which organization the credential
// behind c acts for.
//
// forge does not DECLARE the organization: it is a fact about the credential,
// and a declared copy could only ever agree with it or be wrong. Reading it
// from the authority that enforces it (the registry realm scopes pushes to this
// same org) leaves nothing to drift.
//
// The id is a lowercase UUID, because that is the path segment the platform
// registry's grammar uses (`<org>/<project>/<image>`).
func ResolveOrganization(ctx context.Context, c Caller) (string, error) {
	var resp struct {
		Org struct {
			OrgID string `json:"orgId"`
		} `json:"tenant"`
	}
	if err := c.Call(ctx, ProcGetOrganization, map[string]any{}, &resp); err != nil {
		return "", fmt.Errorf("resolve the credential's organization: %w", err)
	}
	org := strings.TrimSpace(resp.Org.OrgID)
	if org == "" {
		return "", fmt.Errorf("resolve the credential's organization: the control plane answered with no organization id")
	}
	return org, nil
}

// OrganizationReply is the body a control plane answers ProcGetOrganization
// with, for a fake control plane in a test of code that resolves the org. It
// lives beside the reader so the wire spelling exists in one file.
func OrganizationReply(orgID string) map[string]any {
	return map[string]any{"tenant": map[string]any{"orgId": orgID}}
}
