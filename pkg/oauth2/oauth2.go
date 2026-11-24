// Package oauth2 implements the client half of the OAuth 2.0
// authorization-code flow with PKCE (RFC 7636) using only the standard
// library.
//
// # Scope
//
// This package obtains tokens. It does not validate them — that is
// [github.com/reliant-labs/forge/pkg/auth]'s job, and the two never talk to
// each other. A forge server validates bearer tokens statelessly; the flow
// implemented here runs in a browser, a CLI, or a test, against an identity
// provider's endpoints. The forge server is not a participant.
//
// Nothing here is specific to any identity provider. Endpoints are supplied
// by the caller, either literally or via [Discover].
//
// # The flow
//
// A verifier is minted once per login attempt and must survive the redirect
// to the provider and back:
//
//	v, err := oauth2.NewVerifier()
//	state, err := oauth2.NewState()
//	// persist v.Secret() and state server-side (session, signed cookie, keyring)
//
//	u, err := oauth2.AuthRequest{
//		Endpoint:    "https://idp.example.com/oidc/auth",
//		ClientID:    clientID,
//		RedirectURI: "https://app.example.com/callback",
//		Scopes:      []string{"openid", "profile", "offline_access"},
//		State:       state,
//		Challenge:   v.Challenge(),
//	}.URL()
//	// redirect the user agent to u
//
// On the way back, the state is checked before anything else is trusted, and
// the code is exchanged with the verifier that produced the challenge:
//
//	if err := oauth2.CompareState(state, r.URL.Query().Get("state")); err != nil {
//		return err // errors.Is(err, oauth2.ErrStateMismatch) — treat as CSRF
//	}
//	tok, err := (&oauth2.Exchanger{}).Exchange(ctx, oauth2.TokenRequest{
//		Endpoint:    "https://idp.example.com/oidc/token",
//		ClientID:    clientID,
//		RedirectURI: "https://app.example.com/callback",
//		Code:        r.URL.Query().Get("code"),
//		Verifier:    v,
//	})
//
// # Secrets
//
// [Verifier] and [Token] deliberately resist accidental disclosure: they
// redact themselves under fmt, log/slog, and encoding/json rather than
// printing their contents. See [Verifier] for the full list of what is
// blocked and how to get the value out on purpose.
//
// # No global state
//
// Every function takes what it needs. There are no package-level mutable
// variables and no implicit default client to reconfigure.
package oauth2

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// Verifier length bounds from RFC 7636 §4.1: the code verifier is a string of
// 43 to 128 characters drawn from the unreserved set.
const (
	MinVerifierLength = 43
	MaxVerifierLength = 128
)

// unreservedChars is the "unreserved" production of RFC 3986 §2.3, which
// RFC 7636 §4.1 names as the code verifier's alphabet:
//
//	ALPHA / DIGIT / "-" / "." / "_" / "~"
const unreservedChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// Method is a PKCE code challenge method (RFC 7636 §4.2).
type Method string

const (
	// MethodS256 derives the challenge as BASE64URL-ENCODE(SHA256(ASCII(verifier)))
	// with padding omitted. This is the method to use.
	MethodS256 Method = "S256"

	// MethodPlain sends the verifier itself as the challenge.
	//
	// Discouraged. RFC 7636 §7.2 permits plain only for clients that
	// cannot compute a SHA-256 hash, and warns that it offers no
	// protection against an attacker who can observe the authorization
	// request. Any Go program can compute SHA-256, so no Go client has
	// that excuse. It exists here only to interoperate with a provider
	// that rejects S256, and is never selected unless a caller names it
	// explicitly via [Verifier.ChallengeWithMethod].
	MethodPlain Method = "plain"
)

// HTTPDoer is the subset of [*http.Client] this package needs. Taking an
// interface keeps the token exchange testable without a network and lets
// callers supply a client with their own timeout, transport, or proxy.
//
// [*http.Client] satisfies it.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

var (
	// ErrStateMismatch reports that the state returned by the authorization
	// server did not match the state sent with the authorization request.
	// This is a CSRF signal: abandon the flow, do not exchange the code.
	ErrStateMismatch = errors.New("oauth2: state mismatch (possible CSRF)")

	// ErrEmptyState reports that one or both sides of a state comparison
	// were empty. Empty state is never a match — an authorization server
	// that echoes no state, or a session that stored none, must not be
	// allowed to satisfy the CSRF check by comparing "" against "".
	ErrEmptyState = errors.New("oauth2: state is empty")

	// ErrVerifierRedacted is returned by [Verifier]'s marshalling methods.
	// A code verifier must never end up in a log line, an error string, or
	// a JSON document written by accident; use [Verifier.Secret] to obtain
	// the value deliberately.
	ErrVerifierRedacted = errors.New("oauth2: refusing to marshal a code verifier; call Verifier.Secret() if you really mean to")
)

// redacted is what secret-bearing types render as under fmt and log/slog.
const redacted = "[REDACTED]"

// Verifier is a PKCE code verifier: the high-entropy secret whose hash is
// published in the authorization request and whose plaintext is revealed only
// to the token endpoint.
//
// The value is held in an unexported field, and the type actively refuses to
// disclose it:
//
//   - fmt verbs (%v, %s, %d, %q) print [REDACTED] via [Verifier.String].
//   - %#v prints Verifier{[REDACTED]} via [Verifier.GoString]. Without this,
//     fmt would reflect straight through the unexported field and print the
//     secret.
//   - log/slog logs verifier=[REDACTED] via [Verifier.LogValue].
//   - encoding/json and encoding/text based encoders fail with
//     [ErrVerifierRedacted] rather than emitting the secret. Failing is
//     deliberate: a silent [REDACTED] in a session cookie would be a login
//     that mysteriously stops working, while an error names the problem at
//     the call site.
//
// A Verifier must outlive the redirect to the authorization server. Persist
// [Verifier.Secret] somewhere the user agent cannot read — a server-side
// session, an encrypted cookie, an OS keyring — and rebuild it with
// [ParseVerifier].
//
// The zero Verifier is invalid; [Verifier.Challenge] on it panics rather than
// producing the hash of the empty string, which would be a challenge no
// verifier can ever satisfy.
type Verifier struct {
	// value is unexported so that json.Marshal cannot reach it even if the
	// Marshaler methods below were ever removed.
	value string
}

// NewVerifier generates a code verifier from 32 bytes of cryptographically
// secure randomness, base64url-encoded without padding into the 43 characters
// RFC 7636 §4.1 recommends.
func NewVerifier() (Verifier, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return Verifier{}, fmt.Errorf("oauth2: read randomness for code verifier: %w", err)
	}
	return Verifier{value: base64.RawURLEncoding.EncodeToString(b)}, nil
}

// NewVerifierLength generates a code verifier of exactly n characters drawn
// uniformly from the unreserved alphabet, for the rare provider that demands a
// specific length. n must be within [MinVerifierLength, MaxVerifierLength].
//
// Prefer [NewVerifier].
func NewVerifierLength(n int) (Verifier, error) {
	if n < MinVerifierLength || n > MaxVerifierLength {
		return Verifier{}, fmt.Errorf("oauth2: code verifier length %d out of range [%d, %d] (RFC 7636 §4.1)", n, MinVerifierLength, MaxVerifierLength)
	}
	out := make([]byte, n)
	// Rejection sampling: accept a random byte only if it falls in the
	// largest whole multiple of len(unreservedChars) below 256, so every
	// character is equally likely. Plain modulo would bias the first
	// 256 % 66 = 58 characters of the alphabet.
	const alphabet = unreservedChars
	limit := byte(256 - (256 % len(alphabet)))
	buf := make([]byte, n)
	for filled := 0; filled < n; {
		if _, err := rand.Read(buf); err != nil {
			return Verifier{}, fmt.Errorf("oauth2: read randomness for code verifier: %w", err)
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out[filled] = alphabet[int(b)%len(alphabet)]
			if filled++; filled == n {
				break
			}
		}
	}
	return Verifier{value: string(out)}, nil
}

// ParseVerifier rebuilds a Verifier from a previously generated string,
// rejecting anything RFC 7636 §4.1 would not accept. Use it to restore a
// verifier persisted across the authorization redirect.
func ParseVerifier(s string) (Verifier, error) {
	if err := validateVerifier(s); err != nil {
		return Verifier{}, err
	}
	return Verifier{value: s}, nil
}

// validateVerifier reports why s is not a legal code verifier. Its messages
// never include s itself, since callers pass secrets in.
func validateVerifier(s string) error {
	if n := len(s); n < MinVerifierLength || n > MaxVerifierLength {
		return fmt.Errorf("oauth2: code verifier length %d out of range [%d, %d] (RFC 7636 §4.1)", n, MinVerifierLength, MaxVerifierLength)
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(unreservedChars, rune(s[i])) {
			return fmt.Errorf("oauth2: code verifier contains a character outside the unreserved set at index %d (RFC 7636 §4.1)", i)
		}
	}
	return nil
}

// Secret returns the code verifier in the clear. Every call site is a place
// the secret can escape: send it to the token endpoint, or persist it for the
// duration of one login. Do not log it.
func (v Verifier) Secret() string { return v.value }

// String implements [fmt.Stringer] and returns a redacted placeholder.
func (v Verifier) String() string { return redacted }

// GoString implements [fmt.GoStringer] so that %#v does not reflect through
// the unexported field.
func (v Verifier) GoString() string { return "oauth2.Verifier{" + redacted + "}" }

// LogValue implements [slog.LogValuer] and returns a redacted placeholder.
func (v Verifier) LogValue() slog.Value { return slog.StringValue(redacted) }

// MarshalJSON always fails with [ErrVerifierRedacted]. See [Verifier].
func (v Verifier) MarshalJSON() ([]byte, error) { return nil, ErrVerifierRedacted }

// MarshalText always fails with [ErrVerifierRedacted]. See [Verifier].
func (v Verifier) MarshalText() ([]byte, error) { return nil, ErrVerifierRedacted }

// Challenge derives the S256 code challenge for v:
// BASE64URL(SHA256(ASCII(verifier))) with padding omitted (RFC 7636 §4.2).
//
// It panics on the zero Verifier. Hashing the empty string would yield a
// well-formed challenge that no verifier can redeem, turning a programming
// mistake into an authorization failure at the token endpoint, far from its
// cause.
func (v Verifier) Challenge() Challenge {
	if v.value == "" {
		panic("oauth2: Challenge called on the zero Verifier; obtain one from NewVerifier or ParseVerifier")
	}
	sum := sha256.Sum256([]byte(v.value))
	return Challenge{
		Value:  base64.RawURLEncoding.EncodeToString(sum[:]),
		Method: MethodS256,
	}
}

// ChallengeWithMethod derives the code challenge for v using an explicitly
// named method.
//
// Pass [MethodS256] unless a provider genuinely rejects it; see [MethodPlain]
// for why plain is a downgrade. There is no way to reach plain without naming
// it here.
func (v Verifier) ChallengeWithMethod(m Method) (Challenge, error) {
	if v.value == "" {
		return Challenge{}, errors.New("oauth2: ChallengeWithMethod called on the zero Verifier; obtain one from NewVerifier or ParseVerifier")
	}
	switch m {
	case MethodS256:
		return v.Challenge(), nil
	case MethodPlain:
		return Challenge{Value: v.value, Method: MethodPlain}, nil
	default:
		return Challenge{}, fmt.Errorf("oauth2: unknown code challenge method %q (RFC 7636 §4.2 defines %q and %q)", string(m), MethodS256, MethodPlain)
	}
}

// Challenge is the public half of a PKCE pair: the value and method sent as
// code_challenge and code_challenge_method on the authorization request.
//
// Unlike [Verifier] this carries no secret under S256 and is safe to log.
// Under [MethodPlain] it is the verifier verbatim, which is exactly why plain
// is discouraged.
type Challenge struct {
	Value  string
	Method Method
}

// NewState generates an opaque CSRF token for the state parameter, 32 bytes of
// cryptographically secure randomness base64url-encoded without padding.
//
// State travels in a URL, so it is returned as a plain string rather than a
// secret-bearing type. Bind it to the user agent (session or cookie) and check
// it with [CompareState] before trusting anything in the callback.
func NewState() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth2: read randomness for state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// CompareState checks the state echoed by the authorization server against the
// state that was sent, in constant time, and returns nil only if they match.
//
// A mismatch wraps [ErrStateMismatch]; an empty value on either side wraps
// [ErrEmptyState]. The empty case is rejected before the comparison on
// purpose: an authorization server that echoes no state at all, paired with a
// session that recorded none, would otherwise compare "" against "" and pass
// the CSRF check without either side having proved anything.
func CompareState(sent, received string) error {
	if sent == "" || received == "" {
		return fmt.Errorf("%w: sent=%d bytes received=%d bytes", ErrEmptyState, len(sent), len(received))
	}
	// ConstantTimeCompare returns 0 for unequal lengths without inspecting
	// the contents; state length is not secret, so that is fine.
	if subtle.ConstantTimeCompare([]byte(sent), []byte(received)) != 1 {
		return ErrStateMismatch
	}
	return nil
}
