// Copyright (c) 2025 Reliant Labs

package connections

import (
	"net/http"
	"net/url"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/pkg/secretredact"
)

// allowedAPIKeyHeaders is the closed set of headers an api_key connection may
// pick for itself. It applies only to an integration that leaves placement
// open (the generic HTTP one, which knows no API); every other integration
// declares in/name/prefix in its manifest. Not free-form: an arbitrary header
// name would let a connection write "Host" or "Cookie".
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

// Redactor is shared by native connections and hosted HTTP execution without
// importing the product's protobuf registry into the hosted controller.
type Redactor = secretredact.Redactor

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return secretredact.New() }

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

// queryAuth writes the key into one query parameter, keeping the rest. The
// runtime pins a credential to the host the call started at, so the key
// cannot ride a redirect or a pagination link to another host.
type queryAuth struct{ param, prefix string }

func (a queryAuth) Apply(req *http.Request, secret vault.Secret, redact *Redactor) error {
	return secret.Use(func(b []byte) error {
		redact.Register(string(b))
		redact.Register(url.QueryEscape(string(b)))
		q := req.URL.Query()
		q.Set(a.param, a.prefix+string(b))
		req.URL.RawQuery = q.Encode()
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

// openAPIKeyPlacement reports whether an api_key method leaves placement to
// each connection's allow-listed header choice.
func openAPIKeyPlacement(k *reliantv1.ApiKeyAuth) bool { return k.GetIn() == "" && k.GetName() == "" }

// authenticatorFor builds the authenticator for a connection from its
// integration's declaration. Placement is the manifest's; header is used only
// when the manifest leaves it open.
func authenticatorFor(prov *Provider, conn *core.Connection) (Authenticator, error) {
	switch conn.AuthKind {
	case core.ConnectionAuthOAuth2:
		return bearerAuth{}, nil
	case core.ConnectionAuthAPIKey:
		m, ok := prov.Method(MethodAPIKey)
		if !ok {
			return nil, newError(CodeFailedPrecondition, "%s no longer accepts an API key", prov.DisplayName)
		}
		k := m.GetApiKey()
		if openAPIKeyPlacement(k) {
			header := DefaultAPIKeyHeader
			if conn.AuthHeader != nil && *conn.AuthHeader != "" {
				header = *conn.AuthHeader
			}
			h, ok := allowedAPIKeyHeaders[header]
			if !ok {
				return nil, newError(CodeInvalidArgument, "unsupported api key header %q", header)
			}
			return headerAuth{header: h.header, prefix: h.prefix}, nil
		}
		if k.GetIn() == "query" {
			return queryAuth{param: k.GetName(), prefix: k.GetPrefix()}, nil
		}
		return headerAuth{header: http.CanonicalHeaderKey(k.GetName()), prefix: k.GetPrefix()}, nil
	case core.ConnectionAuthBasic:
		if _, ok := prov.Method(MethodBasic); !ok {
			return nil, newError(CodeFailedPrecondition, "%s no longer accepts a username and password", prov.DisplayName)
		}
		return basicAuth{}, nil
	default:
		return nil, newError(CodeFailedPrecondition, "auth kind %q cannot authenticate a request", conn.AuthKind)
	}
}
