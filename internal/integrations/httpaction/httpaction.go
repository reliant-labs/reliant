// Package httpaction runs a manifest action declaratively over HTTP: render the
// request from params, enforce the host rules, call out through the SSRF guard,
// and select/shape the response. It never makes a request the manifest's
// connection did not allow, and never one to a non-public address.
package httpaction

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
	"github.com/reliant-labs/reliant/internal/netguard"

	gojsonschema "github.com/google/jsonschema-go/jsonschema"
)

const (
	defaultTimeout  = 30 * time.Second
	defaultMaxBytes = 1 << 20
	defaultMaxPages = 10
	maxContentChars = 30000
	userAgent       = "reliant-integrations/1"
)

// Result is the outcome of one action. A failing request is a Result with
// IsError set, not a Go error: the graph decides what a failure means. Go
// errors are reserved for requests that could not be attempted at all (bad
// params, a refused host, a template that does not render).
type Result struct {
	Content    string
	IsError    bool
	Retryable  bool
	StatusCode int
	Data       map[string]any
	// ConnectionID is the connection the call was authenticated with, for audit.
	ConnectionID string
}

// Runner executes actions through a guarded HTTP client, and dispatches
// `executor: go:<name>` actions to registered Go functions.
type Runner struct {
	client    *http.Client
	executors *ExecutorRegistry
}

// WithRootCAs returns a Runner that trusts pool for TLS, for tests that talk to
// an httptest TLS server. Production runners use the system roots.
func (r *Runner) WithRootCAs(pool *x509.CertPool) *Runner {
	tr := r.client.Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &Runner{client: &http.Client{Transport: tr}, executors: r.executors}
}

// NewRunner builds a Runner whose every connection goes through guard and
// whose go: actions dispatch through the process registry (Executors).
func NewRunner(guard *netguard.Guard) *Runner {
	return &Runner{client: &http.Client{Transport: guard.Transport()}, executors: defaultExecutors}
}

// Run validates params against the action's schema, then performs the request.
func (r *Runner) Run(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any) (*Result, error) {
	return r.run(ctx, m, a, params, nil)
}

func (r *Runner) run(ctx context.Context, m *reliantv1.IntegrationManifest, a *reliantv1.ActionSpec, params map[string]any, cred Credential) (res *Result, err error) {
	if cred != nil {
		defer func() {
			scrubResult(cred, res)
			if err != nil {
				err = errors.New(cred.Scrub(err.Error()))
			}
		}()
	}
	if params == nil {
		params = map[string]any{}
	}
	// The connection reference is resolved by RunAuthenticated; it is never a
	// request parameter of the action itself unless the schema declares it.
	if err := validateParams(a, &params); err != nil {
		return nil, err
	}
	if name, ok := manifest.ExecutorName(a); ok {
		return r.runExecutor(ctx, m, a, name, params, cred)
	}
	connVars, connValues := connectionVars(m, cred)
	req := a.GetRequest()
	vars := map[string]any{"params": params, "connection": connVars}
	opts, err := renderOptions(req, vars)
	if err != nil {
		return nil, err
	}
	maxBytes := int64(defaultMaxBytes)
	if b := req.GetMaxResponseBytes(); b > 0 {
		maxBytes = b
	}
	ctx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	method, err := tmpl.RenderString(req.GetMethod(), vars, tmpl.Options{})
	if err != nil {
		return nil, fmt.Errorf("request.method: %w", err)
	}
	method = strings.ToUpper(method)
	if method != "GET" && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
		return nil, fmt.Errorf("request.method %q is not allowed", method)
	}
	base, err := baseURL(m, connValues)
	if err != nil {
		return nil, err
	}
	target, err := r.buildURL(base, req, vars)
	if err != nil {
		return nil, err
	}
	query := target.Query()
	for k, v := range req.GetQuery() {
		rendered, err := tmpl.Render(v, vars, tmpl.Options{})
		if err != nil {
			return nil, fmt.Errorf("request.query.%s: %w", k, err)
		}
		if rendered == nil || rendered == "" {
			continue
		}
		query.Set(k, scalarString(rendered))
	}
	if req.GetQueryExpr() != "" {
		extra, err := evalQueryMap(req.GetQueryExpr(), vars)
		if err != nil {
			return nil, fmt.Errorf("request.query_expr: %w", err)
		}
		for k, vs := range extra {
			query.Del(k)
			for _, v := range vs {
				query.Add(k, v)
			}
		}
	}
	var (
		rendered any
		hasBody  bool
	)
	if req.GetBodyExpr() != "" {
		v, err := tmpl.EvalExpr(req.GetBodyExpr(), vars)
		if err != nil {
			return nil, fmt.Errorf("request.body_expr: %w", err)
		}
		rendered, hasBody = v, v != nil
	} else if req.GetBody() != nil {
		if rendered, err = renderValue(req.GetBody().AsInterface(), vars); err != nil {
			return nil, fmt.Errorf("request.body: %w", err)
		}
		hasBody = true
	}
	var body []byte
	bodyType := "application/json"
	if hasBody {
		switch opts.bodyFormat {
		case manifest.BodyFormatForm:
			if body, err = encodeForm(rendered); err != nil {
				return nil, err
			}
			bodyType = "application/x-www-form-urlencoded"
		case manifest.BodyFormatText:
			if body, err = encodeText(rendered); err != nil {
				return nil, err
			}
			bodyType = "text/plain; charset=utf-8"
		default:
			if body, err = json.Marshal(rendered); err != nil {
				return nil, err
			}
		}
	}
	headers := http.Header{}
	for k, v := range m.GetConnection().GetDefaultHeaders() {
		headers.Set(k, v)
	}
	for k, v := range req.GetHeaders() {
		rendered, err := tmpl.RenderString(v, vars, tmpl.Options{})
		if err != nil {
			return nil, fmt.Errorf("request.headers.%s: %w", k, err)
		}
		headers.Set(k, rendered)
	}
	if req.GetHeadersExpr() != "" {
		extra, err := evalScalarMap(req.GetHeadersExpr(), vars)
		if err != nil {
			return nil, fmt.Errorf("request.headers_expr: %w", err)
		}
		for k, v := range extra {
			if err := checkHeader(k, v); err != nil {
				return nil, err
			}
			headers.Set(k, v)
		}
	}
	if body != nil && headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", bodyType)
	}
	headers.Set("User-Agent", userAgent)

	allowed := allowedHosts(m, base)
	anyHost := m.GetConnection().GetAllowAnyPublicHost()
	// A credential must never reach a host other than the one the call started
	// at: custom auth headers (X-Api-Key) survive a redirect, so a redirect or a
	// pagination link to another host would leak it.
	pinnedHost := ""
	if cred != nil {
		pinnedHost = strings.ToLower(target.Host)
		if target.Scheme != "https" {
			return nil, fmt.Errorf("a connection is only sent over https")
		}
	}
	checkPinned := func(u *url.URL) error {
		if pinnedHost != "" && strings.ToLower(u.Host) != pinnedHost {
			return fmt.Errorf("host %q differs from %q: a credential is never sent to a second host", u.Host, pinnedHost)
		}
		if pinnedHost != "" && u.Scheme != "https" {
			return fmt.Errorf("a connection is only sent over https")
		}
		return nil
	}
	r2 := &http.Client{Transport: r.client.Transport, CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if opts.redirects == manifest.RedirectsReturn {
			return http.ErrUseLastResponse
		}
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if err := checkURL(next.URL, allowed, anyHost); err != nil {
			return fmt.Errorf("redirect refused: %w", err)
		}
		if err := checkPinned(next.URL); err != nil {
			return fmt.Errorf("redirect refused: %w", err)
		}
		return nil
	}}

	pg := req.GetPagination()
	pageNum := int64(1)
	if pg != nil && pg.GetStartPage() > 0 {
		pageNum = int64(pg.GetStartPage())
	}
	maxPages := 1
	if pg != nil {
		maxPages = int(pg.GetMaxPages())
		if maxPages == 0 {
			maxPages = defaultMaxPages
		}
	}
	var items []any
	var last *page
	// followed is a URL the provider handed back (a Link header, a
	// next_url). It is requested verbatim: a next link is opaque, and
	// re-encoding its query could reorder or re-escape what the provider
	// signed or parses positionally.
	var followed *url.URL
	for i := 0; i < maxPages; i++ {
		if pg != nil && pg.GetStyle() == "page" {
			query.Set(pg.GetPageParam(), strconv.FormatInt(pageNum, 10))
		}
		u := *target
		u.RawQuery = query.Encode()
		if followed != nil {
			u = *followed
		}
		pgResp, err := r.do(ctx, r2, method, &u, headers, body, maxBytes, allowed, anyHost, cred, opts.responseFormat)
		if err != nil {
			return nil, err
		}
		last = pgResp
		// A 3xx the caller asked to see (redirects: return) is the answer,
		// not a failure: its status and Location are what they wanted.
		returnedRedirect := opts.redirects == manifest.RedirectsReturn && pgResp.status >= 300 && pgResp.status < 400
		if (pgResp.status < 200 || pgResp.status >= 300) && !returnedRedirect {
			return errorResult(req, pgResp)
		}
		// A provider that reports failure in a 2xx body (Slack's ok:false)
		// declares a guarded rule for it; any other 2xx is a success.
		if rule := matchErrorRule(req, pgResp); rule != nil {
			return ruleResult(rule, pgResp), nil
		}
		if opts.responseFormat == manifest.ResponseFormatJSON && pgResp.jsonErr != nil && !returnedRedirect {
			return notJSONResult(pgResp), nil
		}
		if pg == nil {
			break
		}
		selected, err := selectOutput(a.GetOutput(), pgResp, params)
		if err != nil {
			return nil, err
		}
		list, ok := selected.([]any)
		if !ok {
			return nil, fmt.Errorf("a paginated action's output.select must yield a list, got %T", selected)
		}
		items = append(items, list...)
		next, more, err := advance(pg, pgResp, &u, query, &pageNum)
		if err != nil {
			return nil, err
		}
		if !more {
			break
		}
		if next != nil {
			if err := checkURL(next, allowed, anyHost); err != nil {
				return nil, fmt.Errorf("pagination link refused: %w", err)
			}
			if err := checkPinned(next); err != nil {
				return nil, fmt.Errorf("pagination link refused: %w", err)
			}
			followed = next
		}
	}
	if pg != nil {
		data := map[string]any{"items": items}
		return success(last, data), nil
	}
	selected, err := selectOutput(a.GetOutput(), last, params)
	if err != nil {
		return nil, err
	}
	return success(last, asObject(selected)), nil
}

type page struct {
	status  int
	headers http.Header
	raw     []byte
	parsed  any
	// jsonErr is why a non-empty body did not parse as JSON (nil when it did,
	// or when the response format said not to try).
	jsonErr error
}

// requestOptions are the per-call knobs a request spec may template, so the
// generic HTTP action can hand them to its caller: how long to wait, how to
// encode the body, how to read the response, and whether to follow a
// redirect. Each resolves to its default when the spec leaves it empty.
type requestOptions struct {
	timeout        time.Duration
	bodyFormat     string
	responseFormat string
	redirects      string
}

func renderOptions(req *reliantv1.HttpRequestSpec, vars map[string]any) (requestOptions, error) {
	opts := requestOptions{timeout: defaultTimeout}
	if s := req.GetTimeoutSeconds(); s > 0 {
		opts.timeout = time.Duration(s) * time.Second
	}
	if expr := req.GetTimeoutExpr(); expr != "" {
		v, err := tmpl.EvalExpr(expr, vars)
		if err != nil {
			return opts, fmt.Errorf("request.timeout_expr: %w", err)
		}
		seconds, ok := v.(float64)
		if !ok || seconds < 1 || seconds > manifest.MaxTimeoutSeconds {
			return opts, fmt.Errorf("request.timeout_expr must yield 1..%d seconds, got %v", manifest.MaxTimeoutSeconds, v)
		}
		opts.timeout = time.Duration(seconds * float64(time.Second))
	}
	var err error
	if opts.bodyFormat, err = renderEnum("request.body_format", req.GetBodyFormat(), vars, manifest.BodyFormats); err != nil {
		return opts, err
	}
	if opts.responseFormat, err = renderEnum("request.response_format", req.GetResponseFormat(), vars, manifest.ResponseFormats); err != nil {
		return opts, err
	}
	if opts.redirects, err = renderEnum("request.redirects", req.GetRedirects(), vars, manifest.RedirectModes); err != nil {
		return opts, err
	}
	return opts, nil
}

// renderEnum renders an enum-like field (a literal or a template) and checks
// the result; empty is allowed[0], the default.
func renderEnum(name, value string, vars map[string]any, allowed []string) (string, error) {
	rendered, err := tmpl.RenderString(value, vars, tmpl.Options{})
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	if err := manifest.CheckEnum(name, rendered, allowed); err != nil {
		return "", err
	}
	if rendered == "" {
		return allowed[0], nil
	}
	return rendered, nil
}

// notJSONResult is the failure for response_format json: the caller said the
// body is JSON, so a body that is not is a permanent error with a snippet of
// what came back, rather than a silent null downstream.
func notJSONResult(p *page) *Result {
	message := "the response is not JSON"
	if ct := p.headers.Get("Content-Type"); ct != "" {
		message += " (Content-Type " + ct + ")"
	}
	if snippet := strings.TrimSpace(string(p.raw)); snippet != "" {
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		message += ": " + snippet
	}
	return &Result{Content: message, IsError: true, StatusCode: p.status}
}

func (r *Runner) do(ctx context.Context, client *http.Client, method string, u *url.URL, headers http.Header, body []byte, maxBytes int64, allowed map[string]bool, anyHost bool, cred Credential, responseFormat string) (*page, error) {
	if err := checkURL(u, allowed, anyHost); err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return nil, err
	}
	httpReq.Header = headers.Clone()
	if cred != nil {
		if err := cred.Apply(httpReq); err != nil {
			return nil, fmt.Errorf("applying credential: %w", err)
		}
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds the %d byte limit", maxBytes)
	}
	p := &page{status: resp.StatusCode, headers: resp.Header, raw: raw}
	if responseFormat != manifest.ResponseFormatText && len(bytes.TrimSpace(raw)) > 0 {
		var parsed any
		if p.jsonErr = json.Unmarshal(raw, &parsed); p.jsonErr == nil {
			p.parsed = parsed
		}
	}
	return p, nil
}

func validateParams(a *reliantv1.ActionSpec, params *map[string]any) error {
	if a.GetParams() == nil {
		return nil
	}
	raw, err := json.Marshal(a.GetParams().AsMap())
	if err != nil {
		return err
	}
	var schema gojsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		return fmt.Errorf("param schema: %w", err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("param schema: %w", err)
	}
	if err := resolved.ApplyDefaults(params); err != nil {
		return fmt.Errorf("param defaults: %w", err)
	}
	if err := resolved.Validate(*params); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

// baseURL is the connection's base_url with its connection params expanded.
// A templated host can only ever expand to one DNS label under the catalog's
// domain (manifest.ExpandURL), so the params pick a tenant, never a host.
func baseURL(m *reliantv1.IntegrationManifest, connValues map[string]string) (*url.URL, error) {
	raw := m.GetConnection().GetBaseUrl()
	if raw == "" {
		return nil, nil
	}
	u, err := manifest.ExpandURL(raw, connValues)
	if err != nil {
		return nil, fmt.Errorf("connection.base_url: %w", err)
	}
	return u, nil
}

func allowedHosts(m *reliantv1.IntegrationManifest, base *url.URL) map[string]bool {
	allowed := map[string]bool{}
	if base != nil {
		allowed[strings.ToLower(base.Host)] = true
	}
	for _, h := range m.GetConnection().GetAllowedHosts() {
		allowed[strings.ToLower(h)] = true
	}
	return allowed
}

// checkURL is the runtime half of the host rule: https only, no userinfo, and
// the host must be allowed (unless the connection takes any public host, in
// which case the dialer is the backstop against private addresses).
func checkURL(u *url.URL, allowed map[string]bool, anyHost bool) error {
	if u.Scheme != "https" && (u.Scheme != "http" || !anyHost) {
		return fmt.Errorf("scheme %q is not allowed", u.Scheme)
	}
	if u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("url %q must have a host and no userinfo", u.Redacted())
	}
	if anyHost {
		return nil
	}
	host := strings.ToLower(u.Host)
	if !allowed[host] && !allowed[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("host %q is outside the integration's base_url and allowed_hosts", u.Host)
	}
	return nil
}

func (r *Runner) buildURL(base *url.URL, req *reliantv1.HttpRequestSpec, vars map[string]any) (*url.URL, error) {
	if req.GetUrl() != "" {
		s, err := tmpl.RenderString(req.GetUrl(), vars, tmpl.Options{})
		if err != nil {
			return nil, fmt.Errorf("request.url: %w", err)
		}
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("request.url: %w", err)
		}
		return u, nil
	}
	if base == nil {
		return nil, fmt.Errorf("request.path needs connection.base_url")
	}
	p, err := tmpl.RenderString(req.GetPath(), vars, tmpl.Options{EscapePath: true})
	if err != nil {
		return nil, fmt.Errorf("request.path: %w", err)
	}
	u := *base
	u.RawPath = strings.TrimRight(base.EscapedPath(), "/") + p
	decoded, err := url.PathUnescape(u.RawPath)
	if err != nil {
		return nil, fmt.Errorf("request.path: %w", err)
	}
	u.Path = decoded
	return &u, nil
}

// evalQueryMap is evalScalarMap for query parameters, where a list value is a
// repeated parameter (labelIds=INBOX&labelIds=UNREAD) and an empty list sends
// none. List elements must be scalars.
func evalQueryMap(expr string, vars map[string]any) (map[string][]string, error) {
	v, err := tmpl.EvalExpr(expr, vars)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must yield a map, got %T", v)
	}
	out := make(map[string][]string, len(obj))
	for k, val := range obj {
		list, isList := val.([]any)
		if !isList {
			out[k] = []string{scalarString(val)}
			continue
		}
		for i, e := range list {
			switch e.(type) {
			case string, float64, bool, int64:
			default:
				return nil, fmt.Errorf("%s[%d]: a repeated query parameter takes scalars, got %T", k, i, e)
			}
			out[k] = append(out[k], scalarString(e))
		}
		if len(list) == 0 {
			out[k] = nil
		}
	}
	return out, nil
}

func evalScalarMap(expr string, vars map[string]any) (map[string]string, error) {
	v, err := tmpl.EvalExpr(expr, vars)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must yield a map, got %T", v)
	}
	out := make(map[string]string, len(obj))
	for k, val := range obj {
		out[k] = scalarString(val)
	}
	return out, nil
}

// checkHeader rejects header names/values that could split a request.
func checkHeader(name, value string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n:") || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("invalid header %q", name)
	}
	return nil
}

// encodeForm renders a body as application/x-www-form-urlencoded. The body
// must be an object; a scalar value is one key=value, a list repeats its key
// in order (Twilio's MediaUrl), and a null leaves the key out. Anything
// nested has no encoding every form API reads the same way, so it is refused
// rather than guessed at.
func encodeForm(v any) ([]byte, error) {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a form body must render to an object, got %T", v)
	}
	form := url.Values{}
	for k, val := range obj {
		switch t := val.(type) {
		case nil:
		case []any:
			for i, e := range t {
				s, err := formScalar(e)
				if err != nil {
					return nil, fmt.Errorf("form field %s[%d]: %w", k, i, err)
				}
				form.Add(k, s)
			}
		default:
			s, err := formScalar(t)
			if err != nil {
				return nil, fmt.Errorf("form field %s: %w", k, err)
			}
			form.Set(k, s)
		}
	}
	return []byte(form.Encode()), nil
}

// encodeText sends a body verbatim: a string as-is, a number or boolean in
// its JSON spelling. An object or list has no single text form, so it is
// refused rather than JSON-encoded behind the caller's back.
func encodeText(v any) ([]byte, error) {
	switch t := v.(type) {
	case string:
		return []byte(t), nil
	case float64, bool:
		return []byte(scalarString(t)), nil
	}
	return nil, fmt.Errorf("a text body must render to a string, got %T (use body_format json for objects and lists)", v)
}

func formScalar(v any) (string, error) {
	switch v.(type) {
	case string, float64, bool:
		return scalarString(v), nil
	}
	return "", fmt.Errorf("a form value must be a string, number or boolean, got %T", v)
}

func renderValue(v any, vars map[string]any) (any, error) {
	switch t := v.(type) {
	case string:
		return tmpl.Render(t, vars, tmpl.Options{})
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			r, err := renderValue(e, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			r, err := renderValue(e, vars)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func responseVars(p *page) map[string]any {
	hdrs := map[string]any{}
	for k, v := range p.headers {
		hdrs[strings.ToLower(k)] = strings.Join(v, ",")
	}
	return map[string]any{"response": p.parsed, "raw": string(p.raw), "status": int64(p.status), "headers": hdrs}
}

// selectOutput shapes a response into the action's data. A select expression
// sees the validated params as well as the response, so the output can be
// shaped by what was asked (keep only the entries under the directory the
// caller named).
func selectOutput(o *reliantv1.OutputSpec, p *page, params map[string]any) (any, error) {
	switch sel := o.GetSelect(); sel {
	case "", "$":
		if p.parsed == nil {
			return map[string]any{"body": string(p.raw)}, nil
		}
		return p.parsed, nil
	case "$raw":
		return map[string]any{"body": string(p.raw)}, nil
	default:
		vars := responseVars(p)
		vars["params"] = params
		v, err := tmpl.EvalExpr(sel, vars)
		if err != nil {
			return nil, fmt.Errorf("output.select: %w", err)
		}
		return v, nil
	}
}

func asObject(v any) map[string]any {
	if obj, ok := v.(map[string]any); ok {
		return obj
	}
	return map[string]any{"value": v}
}

func success(p *page, data map[string]any) *Result {
	content, _ := json.Marshal(data)
	return &Result{Content: truncate(string(content)), StatusCode: p.status, Data: data}
}

func truncate(text string) string {
	if len(text) > maxContentChars {
		return text[:maxContentChars] + "…(truncated)"
	}
	return text
}

// errorResult classifies a non-2xx response: the first matching rule, else
// the defaults (429 and 5xx retryable, everything else permanent).
func errorResult(req *reliantv1.HttpRequestSpec, p *page) (*Result, error) {
	if rule := matchErrorRule(req, p); rule != nil {
		return ruleResult(rule, p), nil
	}
	return &Result{Content: defaultErrorMessage(p), IsError: true,
		Retryable: p.status == http.StatusTooManyRequests || p.status >= 500, StatusCode: p.status}, nil
}

// matchErrorRule returns the first rule whose status (or range) and `when`
// both hold for the response, or nil.
func matchErrorRule(req *reliantv1.HttpRequestSpec, p *page) *reliantv1.ErrorRule {
	for _, rule := range req.GetErrors() {
		match := rule.GetStatus() != 0 && int(rule.GetStatus()) == p.status
		if rule.GetStatusMin() != 0 {
			max := int(rule.GetStatusMax())
			if max == 0 {
				max = int(rule.GetStatusMin())
			}
			match = p.status >= int(rule.GetStatusMin()) && p.status <= max
		}
		if match && rule.GetWhen() != "" {
			// A guard that errors (a null where a string was expected) is a
			// non-match, so the next rule or the defaults still classify it.
			v, err := tmpl.EvalExpr(rule.GetWhen(), responseVars(p))
			holds, _ := v.(bool)
			match = err == nil && holds
		}
		if match {
			return rule
		}
	}
	return nil
}

func ruleResult(rule *reliantv1.ErrorRule, p *page) *Result {
	message := defaultErrorMessage(p)
	if rule.GetMessage() != "" {
		if rendered, err := tmpl.RenderString(rule.GetMessage(), responseVars(p), tmpl.Options{}); err == nil {
			message = rendered
		}
	}
	return &Result{Content: message, IsError: true, Retryable: rule.GetRetryable(), StatusCode: p.status}
}

func defaultErrorMessage(p *page) string {
	message := fmt.Sprintf("HTTP %d", p.status)
	if snippet := strings.TrimSpace(string(p.raw)); snippet != "" {
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		message += ": " + snippet
	}
	return message
}

var linkNext = regexp.MustCompile(`<([^>]+)>\s*;[^,]*rel="?next"?`)

// advance moves to the next page. It returns the next URL (link_header,
// next_url), or mutates query/pageNum in place and returns nil. current is
// the URL of the page just read, which a relative next_url resolves against.
func advance(pg *reliantv1.PaginationSpec, p *page, current *url.URL, query url.Values, pageNum *int64) (*url.URL, bool, error) {
	switch pg.GetStyle() {
	case "link_header":
		m := linkNext.FindStringSubmatch(strings.Join(p.headers.Values("Link"), ","))
		if m == nil {
			return nil, false, nil
		}
		next, err := url.Parse(m[1])
		if err != nil {
			return nil, false, fmt.Errorf("bad Link header: %w", err)
		}
		return next, true, nil
	case "cursor":
		v, err := tmpl.EvalExpr(pg.GetNextCursor(), responseVars(p))
		if err != nil {
			return nil, false, fmt.Errorf("pagination.next_cursor: %w", err)
		}
		cursor := ""
		if v != nil {
			cursor = scalarString(v)
		}
		if cursor == "" {
			return nil, false, nil
		}
		query.Set(pg.GetCursorParam(), cursor)
		return nil, true, nil
	case "page":
		*pageNum++
		return nil, true, nil
	case "next_url":
		v, err := tmpl.EvalExpr(pg.GetNextUrl(), responseVars(p))
		if err != nil {
			return nil, false, fmt.Errorf("pagination.next_url: %w", err)
		}
		raw := ""
		if v != nil {
			raw = strings.TrimSpace(scalarString(v))
		}
		if raw == "" {
			return nil, false, nil
		}
		ref, err := url.Parse(raw)
		if err != nil {
			return nil, false, fmt.Errorf("pagination.next_url %q: %w", raw, err)
		}
		// The caller checks the resolved URL against the allowed hosts and
		// the credential's pinned host before following it.
		return current.ResolveReference(ref), true, nil
	}
	return nil, false, nil
}
