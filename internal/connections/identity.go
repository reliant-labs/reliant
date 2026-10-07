// Copyright (c) 2025 Reliant Labs

package connections

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/integrations/tmpl"
	"github.com/reliant-labs/reliant/internal/vault"
)

// probeError carries only a class, never a provider body.
type probeError struct{ class string }

func (e *probeError) Error() string { return "provider probe failed: " + e.class }

// HasProbe reports whether the integration declares an identity probe.
func (p *Provider) HasProbe() bool { return p.spec.GetProbe() != nil }

// probeURL is the probe's absolute URL for a connection's params.
func (p *Provider) probeURL(params map[string]string) (string, error) {
	probe := p.spec.GetProbe()
	if probe.GetUrl() != "" {
		return p.endpoint(probe.GetUrl(), params)
	}
	base, err := p.endpoint(p.spec.GetBaseUrl(), params)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	path := probe.GetPath()
	if tmpl.HasExpr(path) {
		expanded, err := manifest.ExpandURL("https://x.invalid"+path, paramVars(params))
		if err != nil {
			return "", newError(CodeFailedPrecondition, "%s: %s", p.DisplayName, err.Error())
		}
		path = expanded.EscapedPath()
	}
	u.RawPath = strings.TrimRight(u.EscapedPath(), "/") + path
	if u.Path, err = url.PathUnescape(u.RawPath); err != nil {
		return "", err
	}
	return u.String(), nil
}

// identify runs the integration's identity probe with a credential and reads
// the account it acts as. auth writes the credential into the request; the
// probe's own request carries the connection's default headers.
func (p *Provider) identify(ctx context.Context, doer HTTPDoer, params map[string]string, auth func(*http.Request) error) (Identity, error) {
	probe := p.spec.GetProbe()
	if probe == nil {
		return Identity{}, &probeError{class: "no_probe"}
	}
	target, err := p.probeURL(params)
	if err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	method := probe.GetMethod()
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	for k, v := range p.spec.GetDefaultHeaders() {
		req.Header.Set(k, v)
	}
	for k, v := range probe.GetHeaders() {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", "reliant-connections")
	if err := auth(req); err != nil {
		return Identity{}, &probeError{class: "unauthorized"}
	}
	resp, err := doer.Do(req)
	if err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderResponse))
	if err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return Identity{}, &probeError{class: "unauthorized"}
	case resp.StatusCode >= 500:
		return Identity{}, &probeError{class: "unreachable"}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return Identity{}, &probeError{class: "unauthorized"}
	}
	var parsed any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	vars := map[string]any{"response": parsed}
	if expr := probe.GetOk(); expr != "" {
		ok, err := tmpl.EvalExpr(expr, vars)
		if err != nil || ok != true {
			return Identity{}, &probeError{class: "unauthorized"}
		}
	}
	id, err := tmpl.EvalExpr(probe.GetExternalId(), vars)
	if err != nil {
		return Identity{}, &probeError{class: "unreachable"}
	}
	who := Identity{ExternalAccountID: identityString(id)}
	if who.ExternalAccountID == "" {
		return Identity{}, &probeError{class: "unreachable"}
	}
	if expr := probe.GetLabel(); expr != "" {
		if label, err := tmpl.EvalExpr(expr, vars); err == nil {
			who.AccountLabel = identityString(label)
		}
	}
	if expr := probe.GetSenderId(); expr != "" {
		if sender, err := tmpl.EvalExpr(expr, vars); err == nil {
			who.SenderID = identityString(sender)
		}
	}
	return who, nil
}

// exchangeSenderID evaluates the oauth2 method's sender_id over the token
// endpoint's answer, with every token removed first: the expression is
// catalog-fixed, but nothing that is not needed should reach an evaluator.
// "" when the method declares none or the answer lacks it.
func (p *Provider) exchangeSenderID(raw map[string]any) string {
	expr := oauthSpec(p).GetSenderId()
	if expr == "" || raw == nil {
		return ""
	}
	sender, err := tmpl.EvalExpr(expr, map[string]any{"response": withoutTokens(raw)})
	if err != nil {
		return ""
	}
	return identityString(sender)
}

// withoutTokens is a copy of a token response with every field whose name
// mentions a token dropped, at any depth (Slack nests the installing user's
// own access and refresh tokens under authed_user).
func withoutTokens(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if strings.Contains(strings.ToLower(k), "token") {
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			v = withoutTokens(nested)
		}
		out[k] = v
	}
	return out
}

// identityString renders a probe result: ids are often JSON numbers, which
// must not come back as 5.83231e+06.
func identityString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool, nil:
		return ""
	default:
		return ""
	}
}

// revoke calls the integration's revocation endpoint, best effort: a failure
// never blocks a delete, because a user must always be able to remove a
// credential. The token never reaches anything but the request.
func (p *Provider) revoke(ctx context.Context, doer HTTPDoer, params map[string]string, token vault.Secret) {
	m, ok := p.Method(MethodOAuth2)
	if !ok || m.GetOauth2().GetRevoke() == nil || !p.OAuthAvailable() {
		return
	}
	spec := m.GetOauth2().GetRevoke()
	target, err := p.endpoint(spec.GetUrl(), params)
	if err != nil {
		return
	}
	method := spec.GetMethod()
	if method == "" {
		method = http.MethodPost
	}
	param := spec.GetTokenParam()
	if param == "" {
		param = "token"
	}
	_ = token.Use(func(b []byte) error {
		var body io.Reader
		contentType := ""
		u, err := url.Parse(target)
		if err != nil {
			return nil
		}
		switch spec.GetTokenIn() {
		case "", "form":
			body, contentType = strings.NewReader(url.Values{param: {string(b)}}.Encode()), "application/x-www-form-urlencoded"
		case "json":
			raw, _ := json.Marshal(map[string]string{param: string(b)})
			body, contentType = bytes.NewReader(raw), "application/json"
		case "query":
			q := u.Query()
			q.Set(param, string(b))
			u.RawQuery = q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
		if err != nil {
			return nil
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "reliant-connections")
		if spec.GetTokenIn() == "bearer" {
			req.Header.Set("Authorization", "Bearer "+string(b))
		}
		if spec.GetClientAuth() == "basic" {
			_ = p.ClientSecret.Use(func(s []byte) error {
				req.SetBasicAuth(p.ClientID, string(s))
				return nil
			})
		}
		if resp, err := doer.Do(req); err == nil {
			_ = resp.Body.Close()
		}
		return nil
	})
}
