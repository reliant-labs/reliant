// Copyright (c) 2025 Reliant Labs

package connections

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/vault"
)

// Method kinds an integration offers (manifest.Auth*).
const (
	MethodOAuth2    = manifest.AuthOAuth2
	MethodAPIKey    = manifest.AuthAPIKey
	MethodBasic     = manifest.AuthBasic
	MethodDelegated = manifest.AuthDelegated

	maxProviderResponse = 1 << 20
)

// OAuthClientEnv returns the deployment variables an integration's OAuth
// client is read from: RELIANT_OAUTH_<ID>_CLIENT_ID and _CLIENT_SECRET, where
// <ID> is the integration id upper-cased. They are deployment config (forge
// secret refs), never manifest data and never a database row.
func OAuthClientEnv(integrationID string) (idVar, secretVar string) {
	base := "RELIANT_OAUTH_" + strings.ToUpper(integrationID)
	return base + "_CLIENT_ID", base + "_CLIENT_SECRET"
}

// HTTPDoer is the slice of *http.Client the broker, token source and probes use.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewHTTPClient returns the client used for provider calls: bounded, and it
// never follows a redirect, since an Authorization header must not be replayed
// at a host the catalog did not name.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Identity is who a credential acts as at the provider.
type Identity struct {
	ExternalAccountID string
	AccountLabel      string
}

// Provider is one integration a connection can be made to, compiled from its
// catalog manifest. Every URL is catalog-fixed: it comes from the embedded
// manifest, never from a request or a database row, so a user cannot point
// the broker's token exchange at an arbitrary host (§8.4). Connection params
// can pick a tenant label under the manifest's domain and nothing more.
type Provider struct {
	ID          string
	DisplayName string

	spec *reliantv1.ConnectionSpec

	// OAuth client, from deployment config.
	ClientID     string
	ClientSecret vault.Secret
	clientIDVar  string
	secretVar    string

	paramPatterns map[string]*regexp.Regexp
}

// Spec is the integration's connection declaration.
func (p *Provider) Spec() *reliantv1.ConnectionSpec { return p.spec }

// Method returns the provider's method of a kind.
func (p *Provider) Method(kind string) (*reliantv1.AuthMethod, bool) {
	return manifest.Method(p.spec, kind)
}

// OAuthAvailable reports whether the provider declares oauth2 and the
// deployment configured its client.
func (p *Provider) OAuthAvailable() bool {
	_, ok := p.Method(MethodOAuth2)
	return ok && p.ClientID != "" && p.ClientSecret.Len() > 0
}

// MethodStatus is one method as the catalog lists it.
type MethodStatus struct {
	Kind      string
	Available bool
	// Reason explains, for an operator, why Available is false.
	Reason string
	// FieldLabels names the fields a pasted credential takes, keyed by the
	// CreateApiKeyConnectionRequest.fields key.
	FieldLabels map[string]string
}

// DelegatedAvailable reports whether a delegated broker is usable on this
// deployment. The api-server never resolves credentials, so it is told what
// the deployment wires rather than holding brokers itself.
type DelegatedAvailable func(brokerID string) (ok bool, reason string)

// MethodStatus describes one declared method. A delegated method reads as
// unavailable here; Service.ListIntegrations consults the deployment.
func (p *Provider) MethodStatus(kind string) MethodStatus {
	return p.methodStatus(kind, nil)
}

func (p *Provider) methodStatus(kind string, delegated DelegatedAvailable) MethodStatus {
	m, ok := p.Method(kind)
	if !ok {
		return MethodStatus{Kind: kind, Reason: fmt.Sprintf("%s does not offer %s", p.DisplayName, kind)}
	}
	st := MethodStatus{Kind: kind, Available: true}
	switch kind {
	case MethodOAuth2:
		if !p.OAuthAvailable() {
			st.Available, st.Reason = false, p.clientIDVar+" and "+p.secretVar+" are not set"
		}
	case MethodAPIKey:
		st.FieldLabels = map[string]string{"api_key": orDefault(m.GetApiKey().GetLabel(), "API key")}
		if openAPIKeyPlacement(m.GetApiKey()) {
			st.FieldLabels["header"] = "Header"
		}
	case MethodBasic:
		b := m.GetBasic()
		st.FieldLabels = map[string]string{"password": orDefault(b.GetPasswordLabel(), "Password")}
		if b.GetUsernameParam() == "" {
			st.FieldLabels["username"] = orDefault(b.GetUsernameLabel(), "Username")
		}
	case MethodDelegated:
		broker := m.GetDelegated().GetBroker()
		if delegated == nil {
			st.Available, st.Reason = false, fmt.Sprintf("delegated broker %q is not configured on this deployment", broker)
		} else if ok, why := delegated(broker); !ok {
			st.Available, st.Reason = false, why
		}
	}
	return st
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// checkParams validates a connection's params against the declaration: every
// key declared, every value matching its pattern, every param without a
// default supplied, and every catalog URL still expanding to a host under the
// catalog's domain. It returns the params with defaults filled in.
func (p *Provider) checkParams(in map[string]string) (map[string]string, error) {
	declared := map[string]bool{}
	for _, cp := range p.spec.GetConnectionParams() {
		declared[cp.GetName()] = true
	}
	for k := range in {
		if !declared[k] {
			return nil, newError(CodeInvalidArgument, "%s has no connection param %q", p.DisplayName, k)
		}
	}
	out := map[string]string{}
	for _, cp := range p.spec.GetConnectionParams() {
		v := strings.TrimSpace(in[cp.GetName()])
		if v == "" {
			v = cp.GetDefaultValue()
		}
		if v == "" {
			return nil, newError(CodeInvalidArgument, "%s needs %s", p.DisplayName, orDefault(cp.GetDisplayName(), cp.GetName()))
		}
		if len(v) > 200 || strings.ContainsAny(v, "\x00\r\n") {
			return nil, newError(CodeInvalidArgument, "connection param %q is not a valid value", cp.GetName())
		}
		if re := p.paramPatterns[cp.GetName()]; re != nil && !re.MatchString(v) {
			return nil, newError(CodeInvalidArgument, "connection param %q does not match %s", cp.GetName(), cp.GetPattern())
		}
		out[cp.GetName()] = v
	}
	for _, raw := range p.catalogURLs() {
		if _, err := manifest.ExpandURL(raw, paramVars(out)); err != nil {
			return nil, newError(CodeInvalidArgument, "%s", err.Error())
		}
	}
	return out, nil
}

func (p *Provider) catalogURLs() []string {
	var out []string
	add := func(u string) {
		if u != "" {
			out = append(out, u)
		}
	}
	add(p.spec.GetBaseUrl())
	if m, ok := p.Method(MethodOAuth2); ok {
		add(m.GetOauth2().GetAuthorizeUrl())
		add(m.GetOauth2().GetTokenUrl())
	}
	add(p.spec.GetProbe().GetUrl())
	return out
}

func paramVars(params map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for k, v := range params {
		out[manifest.ParamVar+k] = v
	}
	return out
}

// templateVar matches {{ name }} with any inner spacing.
func templateVar(name string) *regexp.Regexp {
	return regexp.MustCompile(`\{\{\s*` + regexp.QuoteMeta(name) + `\s*\}\}`)
}

// endpoint expands one catalog URL for a connection's params (and, for a
// revoke URL, {{ client_id }}). The loader proved the template https with a
// fixed domain; this re-checks the expansion.
func (p *Provider) endpoint(raw string, params map[string]string) (string, error) {
	if p.ClientID != "" {
		raw = templateVar("client_id").ReplaceAllLiteralString(raw, url.PathEscape(p.ClientID))
	}
	u, err := manifest.ExpandURL(raw, paramVars(params))
	if err != nil {
		return "", newError(CodeFailedPrecondition, "%s: %s", p.DisplayName, err.Error())
	}
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", newError(CodeInternal, "%s: catalog URL is not https", p.DisplayName)
	}
	return u.String(), nil
}

// Registry is the catalog of integrations a connection can be made to.
type Registry struct {
	order     []string
	providers map[string]*Provider
}

// ProvidersFromCatalog compiles every manifest that declares auth into a
// provider, reading each OAuth client from deployment config
// (OAuthClientEnv). A provider whose client is unset is still registered,
// listed with its oauth2 method unavailable; it never fails boot. A manifest
// that fails validation does: the catalog is embedded, so that is a build
// defect, and it is re-validated here because a caller may hand in a manifest
// that never went through the loader.
func ProvidersFromCatalog(ms []*reliantv1.IntegrationManifest, getenv func(string) string) (*Registry, error) {
	r := &Registry{providers: map[string]*Provider{}}
	for _, m := range ms {
		if len(m.GetConnection().GetAuth()) == 0 {
			continue
		}
		if err := manifest.Validate(m, manifest.TrustCurated); err != nil {
			return nil, fmt.Errorf("integration %q: %w", m.GetId(), err)
		}
		if _, dup := r.providers[m.GetId()]; dup {
			// Versions of one integration share its connections; the lowest
			// version that declares auth is the declaration.
			continue
		}
		idVar, secretVar := OAuthClientEnv(m.GetId())
		p := &Provider{
			ID: m.GetId(), DisplayName: orDefault(m.GetDisplayName(), m.GetId()), spec: m.GetConnection(),
			ClientID:      strings.TrimSpace(getenv(idVar)),
			ClientSecret:  vault.NewSecret([]byte(strings.TrimSpace(getenv(secretVar)))),
			clientIDVar:   idVar,
			secretVar:     secretVar,
			paramPatterns: map[string]*regexp.Regexp{},
		}
		for _, cp := range m.GetConnection().GetConnectionParams() {
			if cp.GetPattern() != "" {
				p.paramPatterns[cp.GetName()] = regexp.MustCompile("^(?:" + cp.GetPattern() + ")$")
			}
		}
		r.providers[p.ID] = p
		r.order = append(r.order, p.ID)
	}
	return r, nil
}

// Get returns the provider for an integration id.
func (r *Registry) Get(id string) (*Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.providers[id]
	return p, ok
}

// List returns every provider in catalog order.
func (r *Registry) List() []*Provider {
	if r == nil {
		return nil
	}
	out := make([]*Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}
