package auth

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
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
	exp := time.Now().Add(time.Hour).Unix()

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
