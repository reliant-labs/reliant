package auth

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// The validator that verified a bearer classifies it, and only a session from
// signing in to the app (or the self-host shared key) is a person's: an
// identity-provider token issued to an OAuth client carries client_id and is
// software's.
func TestValidatorsClassifyTheirCredential(t *testing.T) {
	key, pemKey := newTestKeyAndPEM(t)
	v, err := NewJWTValidator(pemKey)
	if err != nil {
		t.Fatal(err)
	}
	// A fixed expiry far in the future: nothing here depends on the time of day.
	exp := time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC).Unix()

	session, err := v.ValidateToken(signES256(t, key, map[string]any{"sub": "u1", "exp": exp, "session_id": "s1"}))
	if err != nil {
		t.Fatal(err)
	}
	if session.Credential != CredentialSession {
		t.Errorf("app session JWT = %v, want %v", session.Credential, CredentialSession)
	}

	oauth, err := v.ValidateToken(signES256(t, key, map[string]any{"sub": "u1", "exp": exp, "client_id": "mcp-client"}))
	if err != nil {
		t.Fatal(err)
	}
	if oauth.Credential != CredentialOAuthClient {
		t.Errorf("OAuth client JWT = %v, want %v", oauth.Credential, CredentialOAuthClient)
	}

	// A client cannot classify its own token: the field is not read from JSON.
	forged, err := v.ValidateToken(signES256(t, key, map[string]any{"sub": "u1", "exp": exp, "client_id": "x", "Credential": 1}))
	if err != nil {
		t.Fatal(err)
	}
	if forged.Credential != CredentialOAuthClient {
		t.Errorf("a token naming its own credential = %v, want %v", forged.Credential, CredentialOAuthClient)
	}

	apiKey, err := NewAPIKeyValidator("k")
	if err != nil {
		t.Fatal(err)
	}
	shared, err := apiKey.ValidateToken("k")
	if err != nil {
		t.Fatal(err)
	}
	if shared.Credential != CredentialSharedKey {
		t.Errorf("shared key = %v, want %v", shared.Credential, CredentialSharedKey)
	}
}

func TestRequirePerson(t *testing.T) {
	for _, tc := range []struct {
		cred Credential
		ok   bool
	}{
		{CredentialSession, true},
		{CredentialSharedKey, true},
		{CredentialAccessToken, false},
		{CredentialOAuthClient, false},
		{CredentialUnknown, false},
	} {
		t.Run(tc.cred.String(), func(t *testing.T) {
			ctx := context.Background()
			if tc.cred != CredentialUnknown {
				ctx = WithCredential(ctx, tc.cred)
			}
			err := RequirePerson(ctx, "decide an approval")
			if tc.ok {
				if err != nil {
					t.Fatalf("RequirePerson = %v, want nil", err)
				}
				return
			}
			if svcerr.Code(err) != connect.CodePermissionDenied {
				t.Fatalf("RequirePerson = %v, want PermissionDenied", err)
			}
			if got := svcerr.ToConnect(err).Meta().Get(svcerr.ReasonHeader); got != ReasonPersonRequired {
				t.Errorf("reason = %q, want %q", got, ReasonPersonRequired)
			}
		})
	}
}

func TestMachineBound(t *testing.T) {
	daemon := &fat.Resource{Kind: fat.ResourceDaemon, ID: "d"}
	for name, tc := range map[string]struct {
		p    *fat.Principal
		want bool
	}{
		"nil":               {nil, false},
		"reliant:api":       {&fat.Principal{Scopes: fat.SetOf(fat.ScopeReliantAPI)}, false},
		"approvals:decide":  {&fat.Principal{Scopes: fat.SetOf(fat.ScopeApprovalsDecide)}, false},
		"daemon-bound":      {&fat.Principal{Scopes: fat.SetOf(fat.ScopeReliantAPI), Resource: daemon}, true},
		"environment-bound": {&fat.Principal{Scopes: fat.SetOf(fat.ScopeReliantAPI), Resource: &fat.Resource{Kind: fat.ResourceEnvironment, ID: "e"}}, true},
		"wildcard":          {&fat.Principal{Scopes: fat.SetOf(fat.ScopeReliantAPI), AllScopes: true}, true},
		"daemon:connect":    {&fat.Principal{Scopes: fat.SetOf(fat.ScopeDaemonConnect)}, true},
		"daemon:resume":     {&fat.Principal{Scopes: fat.SetOf(fat.ScopeDaemonResume)}, true},
	} {
		if got := MachineBound(tc.p); got != tc.want {
			t.Errorf("%s: MachineBound = %v, want %v", name, got, tc.want)
		}
	}
}

// WithCredential tells authz whether a person holds the credential, by the
// same class RequirePerson admits: only a person may read what another org
// member shared (org-sharing S2). Measured through a shared chat a live member
// may read only as a person.
func TestWithCredentialInstallsThePersonKind(t *testing.T) {
	acme := "acme"
	shared := &core.Chat{UserID: "owner", OrgID: &acme, Visibility: core.VisibilityOrg}
	base := authz.WithMembership(context.Background(), everyoneInAcme{})
	for c, person := range map[Credential]bool{
		CredentialSession:     true,
		CredentialSharedKey:   true,
		CredentialAccessToken: false,
		CredentialOAuthClient: false,
		CredentialUnknown:     false,
	} {
		ctx := WithCredential(base, c)
		if got := authz.CanReadChat(authz.WithViewerReads(ctx), "viewer", shared); got != person {
			t.Errorf("%v: a shared read = %v, want %v", c, got, person)
		}
		if (RequirePerson(ctx, "x") == nil) != person {
			t.Errorf("%v: RequirePerson disagrees with the installed kind", c)
		}
	}
}

type everyoneInAcme struct{}

func (everyoneInAcme) IsMember(_ context.Context, _, orgID string) (bool, bool, error) {
	return orgID == "acme", true, nil
}
