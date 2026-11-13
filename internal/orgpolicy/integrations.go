// Copyright (c) 2025 Reliant Labs

package orgpolicy

import (
	"context"
	"slices"
	"strings"

	controlplanev1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
)

// ReasonIntegrationNotAllowed is the reason a use of an integration the org's
// policy does not admit is refused with (§4.3): a FailedPrecondition, the same
// class as a connection that needs reconnecting. It is part of the API and
// never changes.
const ReasonIntegrationNotAllowed = "integration_not_allowed"

// IntegrationAdmission is what doc's integrations section says about
// integrationID (a catalog id: "github", "slack"). No section governs
// nothing; allow_all admits every integration; otherwise only the listed ids
// are admitted, so an empty list admits none.
func IntegrationAdmission(doc *controlplanev1.OrgPolicy, integrationID string) Admission {
	sec := doc.GetIntegrations()
	if sec == nil {
		return Admission{}
	}
	id := strings.TrimSpace(integrationID)
	return Admission{
		Governed: true,
		Admitted: sec.GetAllowAll() || slices.Contains(sec.GetAllowed(), id),
		Mode:     sec.GetMode(),
	}
}

// JudgeIntegration decides a use of integrationID in orgID's runs (Judge, at
// PointIntegrations).
func JudgeIntegration(ctx context.Context, orgID, integrationID string, quiet bool) (Decision, error) {
	return Judge(ctx, PointIntegrations, orgID, integrationID, ReasonIntegrationNotAllowed, quiet,
		func(doc *controlplanev1.OrgPolicy) Admission { return IntegrationAdmission(doc, integrationID) })
}
