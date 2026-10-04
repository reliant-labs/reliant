// Copyright (c) 2025 Reliant Labs

package connections

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/vault"
)

// allowedAPIKeyHeaders is the closed set of headers an api_key connection may
// use. Not free-form: an arbitrary header name would let a connection write
// "Host" or "Cookie".
var allowedAPIKeyHeaders = map[string]struct{ header, prefix string }{
	"bearer":       {"Authorization", "Bearer "},
	"x-api-key":    {"X-API-Key", ""},
	"api-key":      {"Api-Key", ""},
	"x-auth-token": {"X-Auth-Token", ""},
}

// DefaultAPIKeyHeader is used when a connection names none.
const DefaultAPIKeyHeader = "bearer"

// ValidAPIKeyHeader reports whether name is an allow-listed header choice.
func ValidAPIKeyHeader(name string) bool { _, ok := allowedAPIKeyHeaders[name]; return ok }

// Redactor scrubs credentials out of text that is about to enter workflow
// history, an error message, or a log line. A call registers each applied
// secret; Scrub then replaces every occurrence. It is a belt-and-braces layer
// under the vault.Secret type, for providers that echo a request back.
type Redactor struct {
	mu      sync.Mutex
	secrets []string
}

// A Redactor holds plaintext by design, so it must not print it.
func (*Redactor) String() string             { return "connections.Redactor{}" }
func (*Redactor) GoString() string           { return "connections.Redactor{}" }
func (*Redactor) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "connections.Redactor{}") }
func (*Redactor) LogValue() slog.Value       { return slog.StringValue("connections.Redactor{}") }

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{} }

// Register adds plaintext to scrub. Very short values are ignored: replacing
// them would shred unrelated text.
func (r *Redactor) Register(plaintext string) {
	if len(plaintext) < 4 {
		return
	}
	r.mu.Lock()
	r.secrets = append(r.secrets, plaintext)
	r.mu.Unlock()
}

// Scrub replaces every registered secret in s with vault.Redacted. It also
// covers the base64 form, which is how a basic credential travels.
func (r *Redactor) Scrub(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, vault.Redacted)
		s = strings.ReplaceAll(s, base64.StdEncoding.EncodeToString([]byte(secret)), vault.Redacted)
	}
	return s
}

// ScrubError returns err with every registered secret removed from its text. A
// nil error stays nil; a changed message loses the wrapped chain, since the
// chain is what would carry the secret.
func (r *Redactor) ScrubError(err error) error {
	if err == nil {
		return nil
	}
	if msg := r.Scrub(err.Error()); msg != err.Error() {
		return fmt.Errorf("%s", msg)
	}
	return err
}

// Authenticator applies a connection's credential to an outbound request.
type Authenticator interface {
	// Apply writes the credential into req and registers its plaintext with
	// redact. The plaintext is visible only inside Secret.Use.
	Apply(req *http.Request, secret vault.Secret, redact *Redactor) error
}

type bearerAuth struct{}

func (bearerAuth) Apply(req *http.Request, secret vault.Secret, redact *Redactor) error {
	return secret.Use(func(b []byte) error {
		redact.Register(string(b))
		req.Header.Set("Authorization", "Bearer "+string(b))
		return nil
	})
}

type headerAuth struct{ header, prefix string }

func (a headerAuth) Apply(req *http.Request, secret vault.Secret, redact *Redactor) error {
	return secret.Use(func(b []byte) error {
		redact.Register(string(b))
		req.Header.Set(a.header, a.prefix+string(b))
		return nil
	})
}

// basicAuth takes the stored "username\x00password" pair.
type basicAuth struct{}

func (basicAuth) Apply(req *http.Request, secret vault.Secret, redact *Redactor) error {
	return secret.Use(func(b []byte) error {
		user, pass, ok := strings.Cut(string(b), "\x00")
		if !ok {
			return newError(CodeInternal, "stored basic credential is malformed")
		}
		redact.Register(user + ":" + pass)
		redact.Register(pass)
		req.SetBasicAuth(user, pass)
		return nil
	})
}

// AuthenticatorFor picks the authenticator for a connection. header is the
// connection's allow-listed api_key header choice (empty means bearer).
func AuthenticatorFor(authKind, header string) (Authenticator, error) {
	switch authKind {
	case "oauth2", "github_app_user":
		return bearerAuth{}, nil
	case "api_key":
		if header == "" {
			header = DefaultAPIKeyHeader
		}
		h, ok := allowedAPIKeyHeaders[header]
		if !ok {
			return nil, newError(CodeInvalidArgument, "unsupported api key header %q", header)
		}
		return headerAuth{header: h.header, prefix: h.prefix}, nil
	case "basic":
		return basicAuth{}, nil
	default:
		return nil, newError(CodeFailedPrecondition, "auth kind %q cannot authenticate a request", authKind)
	}
}
