package devidp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/reliant-labs/forge/pkg/observe"
)

// The standard auth events, from the library every forge login broker calls.
// Emitting here rather than in the scaffolded broker (a file the project owns
// and forge never rewrites) is what gives every existing project the events
// with a pin bump and no edit:
//
//   - finish, the convergence point of SignIn and CompleteSignIn, records
//     auth_signed_in {kind: credential} once the code is redeemed;
//   - Register, and CreateUser for a self sign-up, record auth_signed_up;
//   - SignInRoutes records auth_signed_up {method: invite} when an
//     invitation is accepted (an admin's invite is not a sign-up).
//
// They are forge's automatic product events, so the env's
// signals.product_events switch (FORGE_PRODUCT_EVENTS=off) turns them off,
// like the CRUD events.

// trackAuth is observe.TrackAuth; tests replace it to see what was recorded.
var trackAuth = observe.TrackAuth

type authMethodKey struct{}

type authMethodValue struct {
	method observe.AuthMethod
	social string
}

// WithAuthMethod names how the person is authenticating for the sign-in or
// sign-up the calls under ctx complete, when the credential alone does not
// say it. A reset handler that signs in with the new password marks it
// observe.AuthMethodReset; without it the sign-in is recorded as a password
// sign-in. Unset, the method is read from the credentials (SignIn) or from
// the factors the issuer recorded on the session (CompleteSignIn).
func WithAuthMethod(ctx context.Context, method observe.AuthMethod) context.Context {
	return context.WithValue(ctx, authMethodKey{}, authMethodValue{method: method})
}

// withSocialMethod marks a social sign-in or sign-up through provider
// ("google", "github", …).
func withSocialMethod(ctx context.Context, provider string) context.Context {
	return context.WithValue(ctx, authMethodKey{}, authMethodValue{method: observe.AuthMethodSocial, social: provider})
}

// methodFrom is the method ctx names, else fallback.
func methodFrom(ctx context.Context, fallback observe.AuthMethod) authMethodValue {
	if v, ok := ctx.Value(authMethodKey{}).(authMethodValue); ok && v.method != "" {
		return v
	}
	return authMethodValue{method: fallback}
}

func (m authMethodValue) options() []observe.AuthOption {
	opts := []observe.AuthOption{observe.WithProvider(observe.AuthProviderForge), observe.WithMethod(m.method)}
	if m.social != "" {
		opts = append(opts, observe.WithSocialProvider(m.social))
	}
	return opts
}

// methodFromCredentials is the method a Credentials proves.
func methodFromCredentials(c Credentials) observe.AuthMethod {
	switch {
	case c.IDPIntent != nil:
		return observe.AuthMethodSocial
	case c.Password != "":
		return observe.AuthMethodPassword
	}
	return observe.AuthMethodOther
}

// methodFromFactors is the method a session's verified factors prove, the
// strongest first: a passkey, a social intent, an emailed code, a password.
func methodFromFactors(verified []string) observe.AuthMethod {
	has := map[string]bool{}
	for _, f := range verified {
		has[f] = true
	}
	switch {
	case has["webAuthN"]:
		return observe.AuthMethodPasskey
	case has["intent"]:
		return observe.AuthMethodSocial
	case has["otpEmail"]:
		return observe.AuthMethodMagicLink
	case has["password"]:
		return observe.AuthMethodPassword
	}
	return observe.AuthMethodOther
}

// reportSignIn records auth_signed_in {kind: credential} for the subject the
// tokens were issued to.
func reportSignIn(ctx context.Context, tokens Tokens, method observe.AuthMethod) {
	if !observe.AutoProductEvents() {
		return
	}
	sub := tokenSubject(tokens)
	if sub == "" {
		return
	}
	opts := append(methodFrom(ctx, method).options(), observe.WithKind(observe.AuthKindCredential))
	trackAuth(ctx, observe.AuthSignedIn, sub, opts...)
}

// reportSignUp records auth_signed_up for a just-created account.
func reportSignUp(ctx context.Context, userID string, method observe.AuthMethod, verified bool) {
	if !observe.AutoProductEvents() || userID == "" {
		return
	}
	opts := append(methodFrom(ctx, method).options(), observe.WithVerified(verified))
	trackAuth(ctx, observe.AuthSignedUp, userID, opts...)
}

// reportInviteAccepted records the sign-up an accepted invitation is. The
// invitation code proved the address, so the account is verified.
func reportInviteAccepted(ctx context.Context, userID string) {
	if !observe.AutoProductEvents() || userID == "" {
		return
	}
	trackAuth(ctx, observe.AuthSignedUp, userID, observe.WithProvider(observe.AuthProviderForge),
		observe.WithMethod(observe.AuthMethodInvite), observe.WithVerified(true))
}

// tokenSubject is the `sub` of the ID token (else of a JWT access token),
// decoded WITHOUT verifying the signature: the tokens were just received from
// the issuer over TLS by this process, not supplied by a caller, and the
// value only names whom the event is about.
func tokenSubject(t Tokens) string {
	for _, tok := range []string{t.IDToken, t.AccessToken} {
		parts := strings.Split(tok, ".")
		if len(parts) < 2 {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
		if err != nil {
			continue
		}
		var claims struct {
			Sub string `json:"sub"`
		}
		if json.Unmarshal(raw, &claims) == nil && claims.Sub != "" {
			return claims.Sub
		}
	}
	return ""
}
