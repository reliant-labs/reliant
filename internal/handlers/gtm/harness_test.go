// Copyright (c) 2026 Reliant Labs

package gtm_test

import (
	"context"
	"testing"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/auth"
	gtmsvc "github.com/reliant-labs/reliant/internal/gtm"
	gtm "github.com/reliant-labs/reliant/internal/handlers/gtm"
)

// testOrg is the org every scaffold row names: tenant zero, the allowlisted
// org of fixtures.
const testOrg = "55949942-3ee9-4cfc-bb7c-817bdba75186"

// mocked is the handler over a domain MockService: the scaffold rows set the
// one method their rpc calls.
func mocked(t *testing.T) (*gtm.Service, *gtmsvc.MockService) {
	t.Helper()
	m := &gtmsvc.MockService{}
	return gtm.NewTestGtm(t, gtm.WithGtmDeps(gtm.Deps{Service: m})), m
}

// sessionCtx is userID signed in to the app: a person-class credential.
func sessionCtx(t *testing.T, userID string) context.Context {
	t.Helper()
	return gtm.AuthedContext(t, testkit.WithUserID(userID))
}

// tokenCtx is userID calling with an rlat_ access token minted in orgID (an
// API token, or the CLI login an agent shell inherits): it binds every
// decision to orgID, as auth.Verified.Bind does.
func tokenCtx(userID, orgID string) context.Context {
	ctx := credentialCtx(userID, auth.CredentialAccessToken)
	return auth.WithMachineToken(ctx, &fat.Principal{TokenID: "tok-" + userID, OrgID: orgID, ActingUserID: userID})
}

// credentialCtx is userID authenticated by a credential of kind c and
// nothing else (no org binding).
func credentialCtx(userID string, c auth.Credential) context.Context {
	return auth.WithCredential(auth.WithUser(context.Background(), userID, "user", ""), c)
}

// asPerson asserts the handler called the domain as the default test
// principal, a Person in testOrg.
func asPerson(t *testing.T, a gtmsvc.Actor) {
	t.Helper()
	assert.Equal(t, gtmsvc.Actor{UserID: "test-user", OrgID: testOrg, Kind: gtmsvc.ActorPerson}, a)
}
