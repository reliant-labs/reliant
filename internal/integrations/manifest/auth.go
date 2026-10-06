package manifest

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
)

// Auth kinds a ConnectionSpec can declare. They are also the auth_kind a
// connection row records (delegated excepted: it never has a row).
const (
	AuthOAuth2    = "oauth2"
	AuthAPIKey    = "api_key"
	AuthBasic     = "basic"
	AuthDelegated = "delegated"
)

// ExecutorPrefix marks an action implemented by a registered Go function.
const ExecutorPrefix = "go:"

// ParamVar is how a URL template names a connection param:
// {{ connection.params.<name> }}.
const ParamVar = "connection.params."

var (
	paramNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	brokerPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	executorPattern  = regexp.MustCompile(`^go:([a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*)$`)
	keywordPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9 _.+-]*$`)
	headerPattern    = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	queryNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	urlParamPattern  = regexp.MustCompile(`^\{\{\s*connection\.params\.([a-z][a-z0-9_]*)\s*\}\}$`)
	// hostLabel is one DNS label: what a param may expand to inside a host.
	hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

	// forbiddenCredentialHeaders are headers a declared credential may never be
	// written into: they route, frame or identify the request, not authorize it.
	forbiddenCredentialHeaders = map[string]bool{
		"host": true, "cookie": true, "content-length": true, "content-type": true, "transfer-encoding": true,
		"connection": true, "upgrade": true, "te": true, "trailer": true, "proxy-authorization": true,
		"x-forwarded-for": true, "x-forwarded-host": true, "x-forwarded-proto": true, "forwarded": true,
	}
	// reservedAuthorizeParams are set by the flow itself.
	reservedAuthorizeParams = map[string]bool{
		"response_type": true, "client_id": true, "redirect_uri": true, "scope": true, "state": true,
		"code_challenge": true, "code_challenge_method": true,
	}
)

// AuthKind names the arm an AuthMethod sets ("" for none).
func AuthKind(a *reliantv1.AuthMethod) string {
	switch a.GetMethod().(type) {
	case *reliantv1.AuthMethod_Oauth2:
		return AuthOAuth2
	case *reliantv1.AuthMethod_ApiKey:
		return AuthAPIKey
	case *reliantv1.AuthMethod_Basic:
		return AuthBasic
	case *reliantv1.AuthMethod_Delegated:
		return AuthDelegated
	}
	return ""
}

// Method returns the connection's method of the given kind.
func Method(conn *reliantv1.ConnectionSpec, kind string) (*reliantv1.AuthMethod, bool) {
	for _, a := range conn.GetAuth() {
		if AuthKind(a) == kind {
			return a, true
		}
	}
	return nil, false
}

// ExecutorName returns <name> for an action whose executor is "go:<name>".
func ExecutorName(a *reliantv1.ActionSpec) (string, bool) {
	m := executorPattern.FindStringSubmatch(a.GetExecutor())
	if m == nil {
		return "", false
	}
	return m[1], true
}

// ActionSchemas returns an action's params and output as JSON Schema
// documents: what the builder renders forms from and what a catalog entry
// serves. An action that declares neither gets an empty object schema, so a
// caller never has to special-case nil.
func ActionSchemas(a *reliantv1.ActionSpec) (params, output map[string]any) {
	params = map[string]any{"type": "object"}
	if a.GetParams() != nil {
		params = a.GetParams().AsMap()
	}
	output = map[string]any{"type": "object"}
	if a.GetOutput().GetSchema() != nil {
		output = a.GetOutput().GetSchema().AsMap()
	}
	return params, output
}

// ExpandURL fills a catalog URL template's connection params. values maps a
// variable ("connection.params.shop") to its value. A host label must expand
// to exactly one lower-cased DNS label, so a value can pick a tenant but never
// add a label, a port, a path or a userinfo; path segments are
// percent-escaped. Anything but a connection param is refused: the template
// was validated at load, so a CEL expression here is a defect.
func ExpandURL(raw string, values map[string]string) (*url.URL, error) {
	if !tmpl.HasExpr(raw) {
		return url.Parse(raw)
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return nil, fmt.Errorf("url template %q is not absolute", raw)
	}
	hostEnd := strings.IndexAny(rest, "/?#")
	if hostEnd < 0 {
		hostEnd = len(rest)
	}
	labels := hostLabels(rest[:hostEnd])
	for i, label := range labels {
		if !tmpl.HasExpr(label) {
			continue
		}
		name, ok := urlParamName(label)
		if !ok {
			return nil, fmt.Errorf("url template %q: a host label must be exactly {{ connection.params.<name> }}", raw)
		}
		v, ok := values[ParamVar+name]
		v = strings.ToLower(v)
		if !ok || !hostLabel.MatchString(v) {
			return nil, fmt.Errorf("connection param %q must be a single DNS label (letters, digits and inner hyphens)", name)
		}
		labels[i] = v
	}
	tail, err := expandPath(rest[hostEnd:], values)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(scheme + "://" + strings.Join(labels, ".") + tail)
	if err != nil {
		return nil, err
	}
	if u.User != nil || u.Hostname() == "" {
		return nil, fmt.Errorf("url template %q expanded to an invalid host", raw)
	}
	return u, nil
}

func expandPath(s string, values map[string]string) (string, error) {
	var b strings.Builder
	for {
		open := strings.Index(s, "{{")
		if open < 0 {
			b.WriteString(s)
			return b.String(), nil
		}
		closeIdx := strings.Index(s[open:], "}}")
		if closeIdx < 0 {
			return "", fmt.Errorf("unterminated {{ in %q", s)
		}
		name, ok := urlParamName(s[open : open+closeIdx+2])
		if !ok {
			return "", fmt.Errorf("only {{ connection.params.<name> }} may appear in a catalog URL")
		}
		v, ok := values[ParamVar+name]
		if !ok || v == "" {
			return "", fmt.Errorf("connection param %q has no value", name)
		}
		b.WriteString(s[:open])
		b.WriteString(url.PathEscape(v))
		s = s[open+closeIdx+2:]
	}
}

func urlParamName(segment string) (string, bool) {
	m := urlParamPattern.FindStringSubmatch(segment)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// checkCatalogURL validates a catalog-fixed URL template (base_url, OAuth
// endpoints, probe and revoke URLs): https, no userinfo, and only declared
// connection params interpolated. A templated host label is the leftmost one
// and is followed by at least two literal labels, so the domain is always
// the catalog's. It returns the literal host for an untemplated URL, or "".
func checkCatalogURL(raw string, params map[string]bool, extraVars ...string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("is required")
	}
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme != "https" {
		return "", fmt.Errorf("%q must be an https URL", raw)
	}
	hostEnd := strings.IndexAny(rest, "/?#")
	if hostEnd < 0 {
		hostEnd = len(rest)
	}
	host := rest[:hostEnd]
	if strings.ContainsAny(host, "@") {
		return "", fmt.Errorf("%q must have no userinfo", raw)
	}
	labels := hostLabels(host)
	templated := false
	for i, label := range labels {
		if !tmpl.HasExpr(label) {
			continue
		}
		name, ok := urlParamName(label)
		if !ok {
			return "", fmt.Errorf("a templated host label must be exactly {{ connection.params.<name> }}, not %q", label)
		}
		if !params[name] {
			return "", fmt.Errorf("unknown connection param %q", name)
		}
		if i != 0 || len(labels) < 3 {
			return "", fmt.Errorf("a templated host label must be the leftmost, followed by at least two literal labels, so the domain stays fixed")
		}
		templated = true
	}
	for _, seg := range templateSegments(rest[hostEnd:]) {
		if name, ok := urlParamName(seg); ok {
			if !params[name] {
				return "", fmt.Errorf("unknown connection param %q", name)
			}
			continue
		}
		allowed := false
		for _, v := range extraVars {
			if regexp.MustCompile(`^\{\{\s*` + regexp.QuoteMeta(v) + `\s*\}\}$`).MatchString(seg) {
				allowed = true
			}
		}
		if !allowed {
			return "", fmt.Errorf("only {{ connection.params.<name> }} may be interpolated, not %s", seg)
		}
	}
	if templated {
		return "", nil
	}
	u, err := url.Parse(strings.NewReplacer("{{", "x", "}}", "x").Replace(raw))
	if err != nil {
		return "", fmt.Errorf("%q: %w", raw, err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("%q must have a host", raw)
	}
	return strings.ToLower(u.Host), nil
}

// hostLabels splits a host template on dots that are outside {{ }}, so
// "{{ connection.params.shop }}.myshopify.com" is three labels, not five.
func hostLabels(host string) []string {
	var labels []string
	depth, start := 0, 0
	for i := 0; i < len(host); i++ {
		switch {
		case strings.HasPrefix(host[i:], "{{"):
			depth++
			i++
		case strings.HasPrefix(host[i:], "}}") && depth > 0:
			depth--
			i++
		case host[i] == '.' && depth == 0:
			labels = append(labels, host[start:i])
			start = i + 1
		}
	}
	return append(labels, host[start:])
}

// templateSegments returns every {{ ... }} in s, braces included.
func templateSegments(s string) []string {
	var out []string
	for {
		open := strings.Index(s, "{{")
		if open < 0 {
			return out
		}
		closeIdx := strings.Index(s[open:], "}}")
		if closeIdx < 0 {
			return append(out, s[open:])
		}
		out = append(out, s[open:open+closeIdx+2])
		s = s[open+closeIdx+2:]
	}
}

// validateConnection checks the auth declaration. It returns the set of hosts
// the connection's requests may reach (the literal base_url host, if any, plus
// allowed_hosts) and whether base_url is templated.
func validateConnection(conn *reliantv1.ConnectionSpec, trust Trust) (allowed map[string]bool, templatedBase bool, err error) {
	params := map[string]bool{}
	for i, p := range conn.GetConnectionParams() {
		where := fmt.Sprintf("connection.connection_params[%d]", i)
		if !paramNamePattern.MatchString(p.GetName()) {
			return nil, false, fmt.Errorf("%s.name %q must match %s", where, p.GetName(), paramNamePattern)
		}
		if params[p.GetName()] {
			return nil, false, fmt.Errorf("%s: %q is declared twice", where, p.GetName())
		}
		for _, bad := range []string{"secret", "password", "token", "key", "credential"} {
			if strings.Contains(p.GetName(), bad) {
				return nil, false, fmt.Errorf("%s: %q reads like a credential; connection params are stored and returned in the clear, so a secret belongs to an auth method", where, p.GetName())
			}
		}
		params[p.GetName()] = true
		if p.GetPattern() != "" {
			re, err := regexp.Compile("^(?:" + p.GetPattern() + ")$")
			if err != nil {
				return nil, false, fmt.Errorf("%s.pattern: %w", where, err)
			}
			if p.GetDefaultValue() != "" && !re.MatchString(p.GetDefaultValue()) {
				return nil, false, fmt.Errorf("%s.default_value does not match its pattern", where)
			}
		}
	}

	allowed = map[string]bool{}
	if conn.GetBaseUrl() != "" {
		host, err := checkCatalogURL(conn.GetBaseUrl(), params)
		if err != nil {
			return nil, false, fmt.Errorf("connection.base_url: %w", err)
		}
		if host != "" {
			allowed[host] = true
		} else {
			templatedBase = true
		}
	}
	for _, h := range conn.GetAllowedHosts() {
		if h == "" || strings.ContainsAny(h, "/:@ {}") {
			return nil, false, fmt.Errorf("connection.allowed_hosts: %q must be a bare hostname", h)
		}
		allowed[strings.ToLower(h)] = true
	}

	if conn.GetAllowAnyPublicHost() {
		if trust != TrustCurated {
			return nil, false, fmt.Errorf("allow_any_public_host is only valid in curated manifests")
		}
		if len(conn.GetAuth()) > 0 && !conn.GetAuthOptional() {
			return nil, false, fmt.Errorf("allow_any_public_host requires auth_optional: a credential is only ever attached when the caller names one")
		}
		if conn.GetBaseUrl() != "" || len(conn.GetConnectionParams()) > 0 || conn.GetProbe() != nil {
			return nil, false, fmt.Errorf("allow_any_public_host takes no base_url, connection_params or probe")
		}
	}
	if conn.GetAuthOptional() && len(conn.GetAuth()) == 0 {
		return nil, false, fmt.Errorf("connection.auth_optional needs at least one connection.auth method")
	}

	seen := map[string]bool{}
	hasOAuth := false
	for i, a := range conn.GetAuth() {
		where := fmt.Sprintf("connection.auth[%d]", i)
		kind := AuthKind(a)
		if kind == "" {
			return nil, false, fmt.Errorf("%s: exactly one of oauth2, api_key, basic or delegated is required", where)
		}
		if seen[kind] {
			return nil, false, fmt.Errorf("%s: %s is declared twice", where, kind)
		}
		seen[kind] = true
		switch kind {
		case AuthOAuth2:
			hasOAuth = true
			if conn.GetAllowAnyPublicHost() {
				return nil, false, fmt.Errorf("%s: oauth2 cannot be combined with allow_any_public_host", where)
			}
			if err := validateOAuth2(a.GetOauth2(), params); err != nil {
				return nil, false, fmt.Errorf("%s.oauth2.%w", where, err)
			}
		case AuthAPIKey:
			if err := validateAPIKey(a.GetApiKey(), conn); err != nil {
				return nil, false, fmt.Errorf("%s.api_key%w", where, err)
			}
		case AuthBasic:
			if up := a.GetBasic().GetUsernameParam(); up != "" && !params[up] {
				return nil, false, fmt.Errorf("%s.basic.username_param %q is not a connection param", where, up)
			}
		case AuthDelegated:
			if conn.GetAllowAnyPublicHost() {
				return nil, false, fmt.Errorf("%s: delegated cannot be combined with allow_any_public_host", where)
			}
			if !brokerPattern.MatchString(a.GetDelegated().GetBroker()) {
				return nil, false, fmt.Errorf("%s.delegated.broker %q must match %s", where, a.GetDelegated().GetBroker(), brokerPattern)
			}
		}
	}
	if conn.GetProbe() != nil {
		if err := validateProbe(conn.GetProbe(), conn, params, allowed); err != nil {
			return nil, false, err
		}
	} else if hasOAuth {
		return nil, false, fmt.Errorf("connection.probe is required with oauth2: it names the account a new connection acts as")
	}
	return allowed, templatedBase, nil
}

func validateOAuth2(o *reliantv1.OAuth2Auth, params map[string]bool) error {
	if _, err := checkCatalogURL(o.GetAuthorizeUrl(), params); err != nil {
		return fmt.Errorf("authorize_url: %w", err)
	}
	if _, err := checkCatalogURL(o.GetTokenUrl(), params); err != nil {
		return fmt.Errorf("token_url: %w", err)
	}
	for i, s := range o.GetScopes() {
		if s == "" || strings.ContainsAny(s, " ,\t\r\n") {
			return fmt.Errorf("scopes[%d] %q must be one non-empty scope", i, s)
		}
	}
	switch o.GetScopeSeparator() {
	case "", " ", ",":
	default:
		return fmt.Errorf("scope_separator %q must be a space or a comma", o.GetScopeSeparator())
	}
	switch o.GetPkce() {
	case "", "S256", "plain":
	default:
		return fmt.Errorf("pkce %q must be S256 or plain", o.GetPkce())
	}
	if expr := o.GetSenderId(); expr != "" {
		if err := tmpl.ValidateExpr(expr); err != nil {
			return fmt.Errorf("sender_id: %w", err)
		}
	}
	for k, v := range o.GetAuthorizeParams() {
		if reservedAuthorizeParams[k] {
			return fmt.Errorf("authorize_params: %q is set by the flow itself", k)
		}
		if k == "" || strings.ContainsAny(k+v, "\r\n") {
			return fmt.Errorf("authorize_params: %q is not a valid parameter", k)
		}
	}
	if r := o.GetRevoke(); r != nil {
		if _, err := checkCatalogURL(r.GetUrl(), params, "client_id"); err != nil {
			return fmt.Errorf("revoke.url: %w", err)
		}
		switch r.GetMethod() {
		case "", "POST", "DELETE", "GET":
		default:
			return fmt.Errorf("revoke.method %q must be POST, DELETE or GET", r.GetMethod())
		}
		switch r.GetClientAuth() {
		case "", "none", "basic":
		default:
			return fmt.Errorf("revoke.client_auth %q must be none or basic", r.GetClientAuth())
		}
		switch r.GetTokenIn() {
		case "", "form", "json", "query", "bearer":
		default:
			return fmt.Errorf("revoke.token_in %q must be form, json, query or bearer", r.GetTokenIn())
		}
		if r.GetTokenParam() != "" && !queryNamePattern.MatchString(r.GetTokenParam()) {
			return fmt.Errorf("revoke.token_param %q is not a valid parameter name", r.GetTokenParam())
		}
	}
	return nil
}

// validateAPIKey returns errors that begin with "." or ":" so the caller can
// prefix the field path.
func validateAPIKey(k *reliantv1.ApiKeyAuth, conn *reliantv1.ConnectionSpec) error {
	if strings.ContainsAny(k.GetPrefix(), "\r\n") {
		return fmt.Errorf(".prefix must be one line")
	}
	if k.GetIn() == "" && k.GetName() == "" {
		if !conn.GetAllowAnyPublicHost() {
			return fmt.Errorf(": in and name are required; only an allow_any_public_host integration may leave the header to each connection")
		}
		if k.GetPrefix() != "" {
			return fmt.Errorf(": prefix needs in and name")
		}
		return nil
	}
	if k.GetIn() == "" || k.GetName() == "" {
		return fmt.Errorf(": in and name are set together")
	}
	switch k.GetIn() {
	case "header":
		if !headerPattern.MatchString(k.GetName()) {
			return fmt.Errorf(".name %q is not a valid header name", k.GetName())
		}
		if forbiddenCredentialHeaders[strings.ToLower(k.GetName())] {
			return fmt.Errorf(".name: header %q cannot carry a credential", k.GetName())
		}
	case "query":
		if !queryNamePattern.MatchString(k.GetName()) {
			return fmt.Errorf(".name %q is not a valid query parameter name", k.GetName())
		}
	default:
		return fmt.Errorf(".in %q must be header or query", k.GetIn())
	}
	return nil
}

func validateProbe(p *reliantv1.IdentityProbe, conn *reliantv1.ConnectionSpec, params, allowed map[string]bool) error {
	switch p.GetMethod() {
	case "", "GET", "POST":
	default:
		return fmt.Errorf("connection.probe.method %q must be GET or POST", p.GetMethod())
	}
	if (p.GetUrl() == "") == (p.GetPath() == "") {
		return fmt.Errorf("connection.probe needs exactly one of url or path")
	}
	if p.GetPath() != "" {
		if conn.GetBaseUrl() == "" {
			return fmt.Errorf("connection.probe.path needs connection.base_url")
		}
		if err := checkPath(p.GetPath()); err != nil {
			return fmt.Errorf("connection.probe.path: %w", err)
		}
		for _, seg := range templateSegments(p.GetPath()) {
			if name, ok := urlParamName(seg); !ok || !params[name] {
				return fmt.Errorf("connection.probe.path: only declared {{ connection.params.<name> }} may be interpolated, not %s", seg)
			}
		}
	} else {
		host, err := checkCatalogURL(p.GetUrl(), params)
		if err != nil {
			return fmt.Errorf("connection.probe.url: %w", err)
		}
		if host != "" && !allowed[host] {
			return fmt.Errorf("connection.probe.url host %q is outside base_url and allowed_hosts", host)
		}
	}
	for k, v := range p.GetHeaders() {
		if !headerPattern.MatchString(k) || strings.ContainsAny(v, "\r\n") || tmpl.HasExpr(v) {
			return fmt.Errorf("connection.probe.headers: %q must be a literal header", k)
		}
	}
	if p.GetExternalId() == "" {
		return fmt.Errorf("connection.probe.external_id is required")
	}
	for name, expr := range map[string]string{"ok": p.GetOk(), "external_id": p.GetExternalId(), "label": p.GetLabel(), "sender_id": p.GetSenderId()} {
		if expr == "" {
			continue
		}
		if err := tmpl.ValidateExpr(expr); err != nil {
			return fmt.Errorf("connection.probe.%s: %w", name, err)
		}
	}
	return nil
}

func checkPath(p string) error {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, "://") || strings.Contains(p, `\`) {
		return fmt.Errorf("%q must be a path starting with a single /", p)
	}
	return nil
}

// validateKeywords checks what the catalog search index reads.
func validateKeywords(where string, kws []string) error {
	for i, k := range kws {
		if !keywordPattern.MatchString(k) || len(k) > 40 {
			return fmt.Errorf("%skeywords[%d] %q must be lower-case words (%s), at most 40 characters", where, i, k, keywordPattern)
		}
	}
	return nil
}

// validateSchema checks that a params or output schema is a JSON Schema the
// runtime and the form generator can both read.
func validateSchema(name string, s map[string]any) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var schema gojsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if _, err := schema.Resolve(nil); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
