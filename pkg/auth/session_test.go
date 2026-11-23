package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/session"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A session id is resolved through the installed resolver to its access
// token, which the TOKEN VALIDATOR checks: the session channel cannot weaken
// validation, and the claims say a session proved them.
func TestAuthenticatorResolvesSessionsThroughTheTokenValidator(t *testing.T) {
	ctx := context.Background()
	m, err := session.NewManager(session.Config{Store: session.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	good, err := m.Create(ctx, session.Tokens{AccessToken: "good.jwt.token", Expiry: time.Now().Add(time.Hour)}, session.Meta{Subject: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	forged, err := m.Create(ctx, session.Tokens{AccessToken: "forged.jwt.token", Expiry: time.Now().Add(time.Hour)}, session.Meta{Subject: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	jwt := &jwtOnly{}
	a := NewAuthenticator(jwt.validate)

	// No resolver installed: a session-shaped credential reaches the token
	// validator unchanged, exactly as before sessions existed.
	if _, err := a.Authenticate(ctx, good.ID); err == nil {
		t.Fatal("a session id authenticated with no resolver installed")
	}
	if len(jwt.seen) != 1 || jwt.seen[0] != good.ID {
		t.Fatalf("without a resolver the validator saw %v", jwt.seen)
	}

	a.UseSessions(m)
	claims, err := a.Authenticate(ctx, good.ID)
	if err != nil {
		t.Fatalf("Authenticate(session) = %v", err)
	}
	if !claims.IsSession() || claims.Credential.Kind() != CredentialSession || claims.IsAPIKey() {
		t.Errorf("credential = %v, want session", claims.Credential.Kind())
	}
	if jwt.seen[len(jwt.seen)-1] != "good.jwt.token" {
		t.Errorf("the validator was asked about %q, want the session's access token", jwt.seen[len(jwt.seen)-1])
	}
	// The session's token is still validated: a session holding a token
	// the validator refuses does not authenticate.
	if _, err := a.Authenticate(ctx, forged.ID); err == nil {
		t.Error("a session over an invalid token authenticated")
	}
	// A bearer token keeps its own kind beside sessions.
	if claims, err := a.Authenticate(ctx, "good.jwt.token"); err != nil || claims.IsSession() || claims.Credential.Kind() != CredentialToken {
		t.Errorf("bearer token: %v, %v", claims, err)
	}

	// Signed out: ErrInvalidSession, with the resolver's error as the cause.
	if _, _, err := m.Revoke(ctx, good.ID); err != nil {
		t.Fatal(err)
	}
	_, err = a.Authenticate(ctx, good.ID)
	if !errors.Is(err, ErrInvalidSession) || !errors.Is(svcerr.Cause(err), session.ErrNoSession) {
		t.Errorf("revoked session: %v (cause %v), want ErrInvalidSession", err, svcerr.Cause(err))
	}

	a.UseSessions(nil)
	if _, err := a.Authenticate(ctx, forged.ID); errors.Is(err, ErrInvalidSession) {
		t.Error("UseSessions(nil) left the resolver installed")
	}
}

type brokenResolver struct{}

func (brokenResolver) AccessToken(context.Context, string) (string, error) {
	return "", errors.New("dial tcp 10.0.0.5:5432: connection refused")
}

// A store outage is not reported as a signed-out session, and the store's
// error (a host, a DSN) never reaches the caller's message.
func TestAuthenticatorSessionOutageIsNotASignOut(t *testing.T) {
	a := NewAuthenticator((&jwtOnly{}).validate)
	a.UseSessions(brokenResolver{})
	id, err := session.NewID()
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Authenticate(context.Background(), id)
	if !errors.Is(err, ErrSessionUnavailable) || errors.Is(err, ErrInvalidSession) {
		t.Fatalf("outage: %v, want ErrSessionUnavailable", err)
	}
	if got := err.Error(); got != ErrSessionUnavailable.Error() {
		t.Errorf("the caller reads %q; the store's error must stay server-side", got)
	}
}

func TestCredentialKindSessionString(t *testing.T) {
	if got := CredentialSession.String(); got != "session" {
		t.Errorf("CredentialSession.String() = %q", got)
	}
	if !SessionCredential().IsSession() || TokenCredential().IsSession() {
		t.Error("IsSession misreports")
	}
	var nilClaims *Claims
	if nilClaims.IsSession() {
		t.Error("nil claims report a session")
	}
}
