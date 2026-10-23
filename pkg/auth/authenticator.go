package auth

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/reliant-labs/forge/pkg/apikey"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Errors [Authenticator.Authenticate] fails with. Their text is what an
// unauthenticated caller reads, so none of it says more than the caller
// already knows.
var (
	// ErrAPIKeysDisabled: an API key was presented and no key validator is
	// installed. It is the scaffold's default until a project installs one.
	ErrAPIKeysDisabled = errors.New("API keys are not enabled on this server")

	// ErrMalformedAPIKey: the credential has the API-key prefix but is too
	// short to carry a lookup prefix, so no key validator is consulted.
	ErrMalformedAPIKey = errors.New("malformed API key")

	// ErrInvalidAPIKey: the key validator rejected the key, or failed. Its
	// own error is kept server-side (see [Authenticator.Authenticate]).
	ErrInvalidAPIKey = errors.New("invalid API key")

	// ErrBearerTokensDisabled: a non-key credential was presented to an
	// Authenticator built with no token validator (a key-only service).
	ErrBearerTokensDisabled = errors.New("bearer tokens are not accepted by this server; present an API key")
)

// KeyValidatorFunc adapts a function to a [KeyValidator], the way
// http.HandlerFunc adapts one to an http.Handler — so a validator that
// closes over a database handle needs no named type.
type KeyValidatorFunc func(ctx context.Context, key string) (*Claims, error)

// ValidateKey calls f(ctx, key).
func (f KeyValidatorFunc) ValidateKey(ctx context.Context, key string) (*Claims, error) {
	return f(ctx, key)
}

// Authenticator checks the credential a request presents and returns its
// [Claims]. It accepts the two credentials a forge service takes, and the
// credential's SHAPE picks which validator checks it:
//
//   - an API key — a credential starting with [apikey.KeyPrefix] ("fk_"),
//     the form [apikey.Generate] issues — goes to the [KeyValidator]
//     installed with [Authenticator.UseKeyValidator], with the request's
//     context, because checking a key is a read of the application's own
//     store;
//   - anything else goes to the token validator the Authenticator was
//     built with — a JWT validator's Validate method.
//
// The channel a credential arrived on never picks the validator. A key is
// checked as a key whether the caller sent "X-API-Key: fk_…" or
// "Authorization: Bearer fk_…" (many API clients can only send the
// latter), and no header can route a credential around the validator for
// its kind. The routing cannot be ambiguous: a JWT is three base64url
// segments, and base64url of a JSON header begins "eyJ", never "fk_".
//
// # Late binding
//
// A key validator reads the application's database, and the database is not
// open when the auth interceptor is built: a forge server builds its
// interceptor first, so that a server with no authenticator refuses to start
// before it touches any dependency. So the key validator is installed AFTER
// construction, once infrastructure is open, with UseKeyValidator; pass the
// Authenticate method value to the interceptor (the ValidateContext field of
// [github.com/reliant-labs/forge/pkg/authn.Policy]) and every later request
// sees whatever is installed.
// UseKeyValidator is safe to call while requests are being authenticated.
//
// # Failing closed
//
// Every path that cannot prove a principal is an error, never an anonymous
// pass, and no failure falls through to the other validator:
//
//   - an API key with no key validator installed: [ErrAPIKeysDisabled];
//   - an API key too short to carry a lookup prefix: [ErrMalformedAPIKey],
//     before the key validator, and so the store behind it, is consulted;
//   - an API key the key validator rejects, or fails to check:
//     [ErrInvalidAPIKey];
//   - a non-key credential on an Authenticator with no token validator:
//     [ErrBearerTokensDisabled];
//   - a validator that returns neither claims nor an error.
//
// A key validator's own error never reaches the caller: the text of a store
// error (a host, a DSN, a table name) is a diagnostic, not a message for an
// unauthenticated caller. The caller reads "invalid API key"; the error
// stays attached as the server-only cause ([svcerr.Cause]), which is what
// forge's logging interceptor writes beside the failure.
//
// The zero value is not usable; construct with [NewAuthenticator].
type Authenticator struct {
	tokens func(token string) (*Claims, error)
	keys   atomic.Pointer[keyValidatorSlot]
}

// keyValidatorSlot boxes the installed KeyValidator: atomic.Pointer needs a
// concrete type, and a nil *keyValidatorSlot is "no key validator".
type keyValidatorSlot struct{ kv KeyValidator }

// NewAuthenticator returns an Authenticator that checks bearer tokens with
// tokens — typically (*Validator).Validate from [NewValidator], [Clerk] or
// [Firebase] — and rejects API keys until a key validator is installed.
//
// tokens may be nil for a service that accepts API keys only: every
// non-key credential is then rejected with [ErrBearerTokensDisabled].
func NewAuthenticator(tokens func(token string) (*Claims, error)) *Authenticator {
	return &Authenticator{tokens: tokens}
}

// UseKeyValidator installs the validator API keys are checked with,
// replacing any installed before. nil uninstalls it: API keys are then
// rejected with [ErrAPIKeysDisabled]. Safe to call concurrently with
// [Authenticator.Authenticate].
func (a *Authenticator) UseKeyValidator(kv KeyValidator) {
	if kv == nil {
		a.keys.Store(nil)
		return
	}
	a.keys.Store(&keyValidatorSlot{kv: kv})
}

// Authenticate checks credential — the raw value, with no "Bearer "
// scheme — and returns the claims of the principal it proves. ctx is the
// request's context; it reaches the key validator. See [Authenticator] for
// how a credential is routed and every way it fails.
func (a *Authenticator) Authenticate(ctx context.Context, credential string) (*Claims, error) {
	if strings.HasPrefix(credential, apikey.KeyPrefix) {
		return a.authenticateKey(ctx, credential)
	}
	if a.tokens == nil {
		return nil, ErrBearerTokensDisabled
	}
	claims, err := a.tokens(credential)
	if err != nil {
		return nil, err
	}
	if claims == nil {
		return nil, errors.New("token validator returned no claims")
	}
	return claims, nil
}

func (a *Authenticator) authenticateKey(ctx context.Context, key string) (*Claims, error) {
	slot := a.keys.Load()
	if slot == nil {
		return nil, ErrAPIKeysDisabled
	}
	if _, err := apikey.LookupPrefix(key); err != nil {
		return nil, ErrMalformedAPIKey
	}
	claims, err := slot.kv.ValidateKey(ctx, key)
	if err != nil {
		return nil, svcerr.WithCause(ErrInvalidAPIKey, err)
	}
	if claims == nil {
		return nil, svcerr.WithCause(ErrInvalidAPIKey, errors.New("key validator returned no claims"))
	}
	return claims, nil
}
