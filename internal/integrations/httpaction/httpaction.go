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

// Runner executes actions through a guarded HTTP client.
type Runner struct {
	client *http.Client
}

// WithRootCAs returns a Runner that trusts pool for TLS, for tests that talk to
// an httptest TLS server. Production runners use the system roots.
func (r *Runner) WithRootCAs(pool *x509.CertPool) *Runner {
	tr := r.client.Transport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &Runner{client: &http.Client{Transport: tr}}
}

// NewRunner builds a Runner whose every connection goes through guard.
func NewRunner(guard *netguard.Guard) *Runner {
	return &Runner{client: &http.Client{Transport: guard.Transport()}}
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
	if err := validateParams(a, &params); err != nil {
		return nil, err
	}
	req := a.GetRequest()
	timeout := defaultTimeout
	if s := req.GetTimeoutSeconds(); s > 0 {
		timeout = time.Duration(s) * time.Second
	}
	maxBytes := int64(defaultMaxBytes)
	if b := req.GetMaxResponseBytes(); b > 0 {
		maxBytes = b
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	vars := map[string]any{"params": params}
	method, err := tmpl.RenderString(req.GetMethod(), vars, tmpl.Options{})
	if err != nil {
		return nil, fmt.Errorf("request.method: %w", err)
	}
	method = strings.ToUpper(method)
	if method != "GET" && method != "POST" && method != "PUT" && method != "PATCH" && method != "DELETE" {
		return nil, fmt.Errorf("request.method %q is not allowed", method)
	}
	target, err := r.buildURL(m, req, params)
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
		extra, err := evalScalarMap(req.GetQueryExpr(), vars)
		if err != nil {
			return nil, fmt.Errorf("request.query_expr: %w", err)
		}
		for k, v := range extra {
			query.Set(k, v)
		}
	}
	var body []byte
	if req.GetBodyExpr() != "" {
		v, err := tmpl.EvalExpr(req.GetBodyExpr(), vars)
		if err != nil {
			return nil, fmt.Errorf("request.body_expr: %w", err)
		}
		if v != nil {
			if body, err = json.Marshal(v); err != nil {
				return nil, err
			}
		}
	} else if req.GetBody() != nil {
		if body, err = renderBody(req.GetBody(), vars); err != nil {
			return nil, err
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
		headers.Set("Content-Type", "application/json")
	}
	headers.Set("User-Agent", userAgent)

	allowed := allowedHosts(m)
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
	for i := 0; i < maxPages; i++ {
		if pg != nil && pg.GetStyle() == "page" {
			query.Set(pg.GetPageParam(), strconv.FormatInt(pageNum, 10))
		}
		u := *target
		u.RawQuery = query.Encode()
		pgResp, err := r.do(ctx, r2, method, &u, headers, body, maxBytes, allowed, anyHost, cred)
		if err != nil {
			return nil, err
		}
		last = pgResp
		if pgResp.status < 200 || pgResp.status >= 300 {
			return errorResult(req, pgResp)
		}
		if pg == nil {
			break
		}
		selected, err := selectOutput(a.GetOutput(), pgResp)
		if err != nil {
			return nil, err
		}
		list, ok := selected.([]any)
		if !ok {
			return nil, fmt.Errorf("a paginated action's output.select must yield a list, got %T", selected)
		}
		items = append(items, list...)
		next, more, err := advance(pg, pgResp, query, &pageNum)
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
			target = next
			query = target.Query()
		}
	}
	if pg != nil {
		data := map[string]any{"items": items}
		return success(last, data), nil
	}
	selected, err := selectOutput(a.GetOutput(), last)
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
}

func (r *Runner) do(ctx context.Context, client *http.Client, method string, u *url.URL, headers http.Header, body []byte, maxBytes int64, allowed map[string]bool, anyHost bool, cred Credential) (*page, error) {
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
	if len(bytes.TrimSpace(raw)) > 0 {
		var parsed any
		if json.Unmarshal(raw, &parsed) == nil {
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

func allowedHosts(m *reliantv1.IntegrationManifest) map[string]bool {
	allowed := map[string]bool{}
	if b := m.GetConnection().GetBaseUrl(); b != "" {
		if u, err := url.Parse(b); err == nil {
			allowed[strings.ToLower(u.Host)] = true
		}
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
	if u.Scheme != "https" && !(u.Scheme == "http" && anyHost) {
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

func (r *Runner) buildURL(m *reliantv1.IntegrationManifest, req *reliantv1.HttpRequestSpec, params map[string]any) (*url.URL, error) {
	vars := map[string]any{"params": params}
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
	base, err := url.Parse(m.GetConnection().GetBaseUrl())
	if err != nil {
		return nil, err
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

func renderBody(body interface{ AsInterface() any }, vars map[string]any) ([]byte, error) {
	rendered, err := renderValue(body.AsInterface(), vars)
	if err != nil {
		return nil, fmt.Errorf("request.body: %w", err)
	}
	return json.Marshal(rendered)
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

func selectOutput(o *reliantv1.OutputSpec, p *page) (any, error) {
	switch sel := o.GetSelect(); sel {
	case "", "$":
		if p.parsed == nil {
			return map[string]any{"body": string(p.raw)}, nil
		}
		return p.parsed, nil
	case "$raw":
		return map[string]any{"body": string(p.raw)}, nil
	default:
		v, err := tmpl.EvalExpr(sel, responseVars(p))
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
	text := string(content)
	if len(text) > maxContentChars {
		text = text[:maxContentChars] + "…(truncated)"
	}
	return &Result{Content: text, StatusCode: p.status, Data: data}
}

func errorResult(req *reliantv1.HttpRequestSpec, p *page) (*Result, error) {
	retryable := p.status == http.StatusTooManyRequests || p.status >= 500
	message := fmt.Sprintf("HTTP %d", p.status)
	if snippet := strings.TrimSpace(string(p.raw)); snippet != "" {
		if len(snippet) > 300 {
			snippet = snippet[:300] + "…"
		}
		message += ": " + snippet
	}
	for _, rule := range req.GetErrors() {
		match := rule.GetStatus() != 0 && int(rule.GetStatus()) == p.status
		if rule.GetStatusMin() != 0 {
			max := int(rule.GetStatusMax())
			if max == 0 {
				max = int(rule.GetStatusMin())
			}
			match = p.status >= int(rule.GetStatusMin()) && p.status <= max
		}
		if !match {
			continue
		}
		retryable = rule.GetRetryable()
		if rule.GetMessage() != "" {
			rendered, err := tmpl.RenderString(rule.GetMessage(), responseVars(p), tmpl.Options{})
			if err == nil {
				message = rendered
			}
		}
		break
	}
	return &Result{Content: message, IsError: true, Retryable: retryable, StatusCode: p.status}, nil
}

var linkNext = regexp.MustCompile(`<([^>]+)>\s*;[^,]*rel="?next"?`)

// advance moves to the next page. It returns the next URL (link_header), or
// mutates query/pageNum in place and returns nil.
func advance(pg *reliantv1.PaginationSpec, p *page, query url.Values, pageNum *int64) (*url.URL, bool, error) {
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
	}
	return nil, false, nil
}
