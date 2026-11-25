package auth

// CredentialKind names the kind of credential that authenticated a request.
type CredentialKind uint8

const (
	// CredentialUnknown is the zero value: claims built by hand, or returned
	// by a validator that is not one of forge's (a custom Validate func).
	CredentialUnknown CredentialKind = iota
	// CredentialToken: a bearer token a token validator checked — a JWT.
	CredentialToken
	// CredentialAPIKey: an API key the installed [KeyValidator] checked.
	CredentialAPIKey
	// CredentialSession: a server-side browser session id (forge/pkg/session)
	// the installed [SessionResolver] resolved to an access token, which the
	// token validator then checked. Only a browser holding the session
	// cookie presents one: no script can read the cookie, and the id is not
	// a token any other client is ever issued.
	CredentialSession
)

// String returns "unknown", "token", "api_key" or "session".
func (k CredentialKind) String() string {
	switch k {
	case CredentialToken:
		return "token"
	case CredentialAPIKey:
		return "api_key"
	case CredentialSession:
		return "session"
	}
	return "unknown"
}

// Credential records which kind of credential proved a set of [Claims] —
// the field a handler or an enricher reads to tell a key-authenticated
// call apart from a person's token, for example to refuse key management
// made with a key, or to trust a key's own tenant and role instead of
// mapping a subject.
//
// It is unforgeable by a token. Its fields are unexported and
// [Claims.Credential] is `json:"-"`, so no decoder — the JWT payload's
// JSON, a [UserResolver] that unmarshals it into Claims — can set it, and
// forge's authenticators stamp it themselves rather than trust a
// validator: [Authenticator.Authenticate] marks every key it checks as
// [CredentialAPIKey] and every token as [CredentialToken], whatever claims
// the validator returned, and the [Validator]'s own JWT and key paths do the
// same. The authn interceptor then restores it after its Enrich hook, so an
// enricher reads it but cannot change it. Claims.Raw, by contrast, also
// holds a token's decoded claims, so a marker there can be forged by
// anyone who can get a token issued with that claim.
type Credential struct {
	kind      CredentialKind
	keyPrefix string
}

// TokenCredential is the credential forge's token paths stamp. It is
// exported for tests that build a request's claims by hand.
func TokenCredential() Credential { return Credential{kind: CredentialToken} }

// APIKeyCredential is the credential [Authenticator] stamps on a checked
// key: keyPrefix is the key's lookup prefix ([apikey.LookupPrefix]), its
// public handle and the indexed column the auth/api-keys skill's table
// keys on. It is exported for tests that build a key caller's claims by
// hand, and for a custom authenticator that checks keys itself; nothing a
// token carries reaches it.
//
// [apikey.LookupPrefix]: https://pkg.go.dev/github.com/reliant-labs/forge/pkg/apikey#LookupPrefix
func APIKeyCredential(keyPrefix string) Credential {
	return Credential{kind: CredentialAPIKey, keyPrefix: keyPrefix}
}

// SessionCredential is the credential [Authenticator] stamps on a request
// whose credential was a server-side session id. It is exported for tests that
// build a session caller's claims by hand.
func SessionCredential() Credential { return Credential{kind: CredentialSession} }

// Kind returns the kind of credential.
func (c Credential) Kind() CredentialKind { return c.kind }

// IsAPIKey reports whether an API key proved the claims.
func (c Credential) IsAPIKey() bool { return c.kind == CredentialAPIKey }

// IsSession reports whether a server-side browser session proved the claims.
func (c Credential) IsSession() bool { return c.kind == CredentialSession }

// KeyPrefix is the API key's lookup prefix, or "" for any other kind.
func (c Credential) KeyPrefix() string { return c.keyPrefix }

// IsAPIKey reports whether an API key, not a person's token, proved c. It
// is false for nil claims.
func (c *Claims) IsAPIKey() bool { return c != nil && c.Credential.IsAPIKey() }

// IsSession reports whether a server-side browser session, not a bearer
// token or an API key, proved c. It is false for nil claims.
func (c *Claims) IsSession() bool { return c != nil && c.Credential.IsSession() }

// withCredential returns a copy of c carrying cred, so stamping never writes
// to claims a validator may hold on to (a cache, a test fixture).
func withCredential(c *Claims, cred Credential) *Claims {
	out := *c
	out.Credential = cred
	return &out
}
