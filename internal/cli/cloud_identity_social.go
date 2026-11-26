package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

// `forge cloud identity status --check-social`: start a social sign-in for
// each declared provider and check the authorize URL the issuer answers with,
// so a wrong registration shows up before a user hits it.
//
// The issuer builds that URL: the provider's authorize endpoint, the app's
// client id and scopes, and its OWN <issuer>/idps/callback as the redirect.
// The provider refuses a redirect_uri the OAuth app does not list, so the one
// thing the operator registers by hand — the callback status prints — must
// equal what the issuer actually sends. A custom issuer domain, a proxy that
// rewrites the host, or a provider the reconciler has not applied all break
// that silently; this check names each.
//
// Starting a sign-in needs a credential on the issuer, which the CLI never
// holds. So the intent is started either by the control plane
// (procIdentityCheckSocial) or, with --app, by the app's own login broker
// through the public route every sign-in button uses (POST /auth/social/start).
// The second is the exact path a user takes, and works with any control plane.

// procIdentityCheckSocial starts one social sign-in per declared provider on
// the environment's instance and returns each issuer-built authorize URL. The
// intents are never completed; they expire on the issuer.
const procIdentityCheckSocial = "controlplane.v1.IdentityService/CheckSocialSignIn"

// socialAuthEndpoint is where each provider's authorize URL must point:
// Zitadel's GitHub provider uses github.com's fixed endpoint, and its Google
// provider the authorization_endpoint of accounts.google.com's discovery
// document.
var socialAuthEndpoint = map[string]struct{ host, path string }{
	"github": {"github.com", "/login/oauth/authorize"},
	"google": {"accounts.google.com", "/o/oauth2/v2/auth"},
}

// identitySocialStartWire is one entry of the procIdentityCheckSocial
// response: the authorize URL the issuer answered, or why none was started.
type identitySocialStartWire struct {
	Type    string `json:"type"`
	AuthURL string `json:"authUrl,omitempty"`
	Error   string `json:"error,omitempty"`
}

// socialCheck is the verdict on one provider.
type socialCheck struct {
	Type string `json:"type"`
	OK   bool   `json:"ok"`
	// Skipped says why no sign-in was started (the provider is not applied).
	// A skipped provider is not OK.
	Skipped string `json:"skipped,omitempty"`
	// What the authorize URL carried.
	AuthEndpoint string   `json:"authorizeEndpoint,omitempty"`
	ClientID     string   `json:"clientId,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	RedirectURI  string   `json:"redirectUri,omitempty"`
	// Problems are each thing wrong, as a sentence that says what to do.
	Problems []string `json:"problems,omitempty"`
}

// verifySocialAuthURL checks the authorize URL the issuer answered for a
// provider of type typ against callback, the redirect URI registered at the
// provider (status's socialCallbackUrl).
func verifySocialAuthURL(typ, authURL, callback string) socialCheck {
	c := socialCheck{Type: typ}
	bad := func(format string, args ...any) { c.Problems = append(c.Problems, fmt.Sprintf(format, args...)) }
	u, err := url.Parse(authURL)
	if err != nil || u.Host == "" {
		bad("the issuer answered no usable authorize URL (%q)", authURL)
		return c
	}
	c.AuthEndpoint = u.Scheme + "://" + u.Host + u.Path
	q := u.Query()
	c.ClientID, c.RedirectURI = q.Get("client_id"), q.Get("redirect_uri")
	c.Scopes = strings.Fields(q.Get("scope"))
	if want, ok := socialAuthEndpoint[typ]; !ok {
		bad("unknown provider type %q", typ)
	} else if u.Scheme != "https" || u.Host != want.host || u.Path != want.path {
		bad("the authorize endpoint is %s, want https://%s%s", c.AuthEndpoint, want.host, want.path)
	}
	if c.ClientID == "" {
		bad("the authorize URL carries no client_id: the provider's client id did not reach the issuer")
	}
	if len(c.Scopes) == 0 {
		bad("the authorize URL requests no scope")
	}
	if q.Get("state") == "" {
		bad("the authorize URL carries no state (the issuer's intent id)")
	}
	switch {
	case callback == "":
		bad("no callback to compare with: the control plane reported neither socialCallbackUrl nor an issuer")
	case c.RedirectURI != callback:
		bad("the issuer sends redirect_uri %q, but the callback registered at %s is %q: %s refuses the sign-in. "+
			"Register %q as the OAuth app's callback, or fix the issuer's public URL", c.RedirectURI, typ, callback, typ, c.RedirectURI)
	}
	c.OK = len(c.Problems) == 0
	return c
}

// socialCallbackFor is the redirect URI to compare with: the one status
// reports, else the issuer's own (<issuer>/idps/callback), for a control
// plane that predates socialCallbackUrl.
func socialCallbackFor(doc identityStatusDocument) string {
	if doc.SocialCallbackURL != "" {
		return doc.SocialCallbackURL
	}
	if doc.Issuer != "" {
		return strings.TrimRight(doc.Issuer, "/") + "/idps/callback"
	}
	return ""
}

// errSocialCheckUnsupported is a control plane with no procIdentityCheckSocial.
var errSocialCheckUnsupported = errors.New("this control plane cannot start a social sign-in check (" + procIdentityCheckSocial +
	" is not implemented): pass --app <the URL your app's login broker answers on> to start each sign-in through the app instead")

// socialStarter starts one sign-in per provider type and returns each
// authorize URL, or the reason it could not.
type socialStarter func(ctx context.Context, types []string) (map[string]identitySocialStartWire, error)

// controlPlaneSocialStarter starts the sign-ins on the control plane.
func controlPlaneSocialStarter(client cloudCaller, req map[string]any) socialStarter {
	return func(ctx context.Context, _ []string) (map[string]identitySocialStartWire, error) {
		var out struct {
			Providers []identitySocialStartWire `json:"providers"`
		}
		if err := client.Call(ctx, procIdentityCheckSocial, req, &out); err != nil {
			var ce *cloud.Error
			if errors.As(err, &ce) && (ce.Code == cloud.CodeUnimplemented || ce.HTTPStatus == http.StatusNotFound) {
				return nil, errSocialCheckUnsupported
			}
			return nil, fmt.Errorf("start the social sign-in check: %w", err)
		}
		got := map[string]identitySocialStartWire{}
		for _, p := range out.Providers {
			got[p.Type] = p
		}
		return got, nil
	}
}

// brokerSocialStarter starts the sign-ins through the app's login broker:
// POST <app>/auth/social/start {"provider": <type>}, answered with
// {"redirect": <authorize URL>}. It sends no Origin or Sec-Fetch-Site, which
// the broker's cross-origin guard admits (it refuses only a browser request
// from another site).
func brokerSocialStarter(hc *http.Client, app string) socialStarter {
	app = strings.TrimRight(app, "/")
	return func(ctx context.Context, types []string) (map[string]identitySocialStartWire, error) {
		got := map[string]identitySocialStartWire{}
		for _, typ := range types {
			got[typ] = startSocialViaBroker(ctx, hc, app, typ)
		}
		return got, nil
	}
}

func startSocialViaBroker(ctx context.Context, hc *http.Client, app, typ string) identitySocialStartWire {
	w := identitySocialStartWire{Type: typ}
	body, _ := json.Marshal(map[string]string{"provider": typ})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app+"/auth/social/start", bytes.NewReader(body))
	if err != nil {
		w.Error = err.Error()
		return w
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		w.Error = fmt.Sprintf("POST %s/auth/social/start: %v", app, err)
		return w
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var out struct {
		Redirect string `json:"redirect"`
		Error    string `json:"error"`
		Code     string `json:"code"`
	}
	_ = json.Unmarshal(raw, &out)
	switch {
	case resp.StatusCode == http.StatusOK && out.Redirect != "":
		w.AuthURL = out.Redirect
	case resp.StatusCode == http.StatusNotFound && out.Code != "":
		w.Error = fmt.Sprintf("the app's broker does not offer %s sign-in (%s): its LOGIN_POLICY lists no %s provider; is the release that carries it deployed?", typ, out.Code, typ)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		w.Error = fmt.Sprintf("%s/auth/social/start is not served (HTTP %d): this is not the login broker's URL, or its LOGIN_POLICY offers no social sign-in", app, resp.StatusCode)
	default:
		w.Error = fmt.Sprintf("POST %s/auth/social/start: HTTP %d %s", app, resp.StatusCode, firstNonEmptyStr(out.Error, strings.TrimSpace(string(raw))))
	}
	return w
}

// checkSocial runs the check for every provider status reports. A provider
// that is not applied is skipped (no sign-in can start with it) and fails.
func checkSocial(ctx context.Context, doc identityStatusDocument, start socialStarter) ([]socialCheck, error) {
	callback := socialCallbackFor(doc)
	var types []string
	checks := make([]socialCheck, 0, len(doc.Social))
	for _, s := range doc.Social {
		if s.State != "" && !strings.EqualFold(s.State, "applied") {
			checks = append(checks, socialCheck{Type: s.Type, Skipped: fmt.Sprintf("the provider is %s, not applied: no sign-in can start with it until the reconciler applies it", s.State)})
			continue
		}
		types = append(types, s.Type)
	}
	if len(types) == 0 {
		return checks, nil
	}
	started, err := start(ctx, types)
	if err != nil {
		return nil, err
	}
	for _, typ := range types {
		s, ok := started[typ]
		switch {
		case !ok:
			checks = append(checks, socialCheck{Type: typ, Problems: []string{"no sign-in was started for it"}})
		case s.Error != "":
			checks = append(checks, socialCheck{Type: typ, Problems: []string{s.Error}})
		default:
			checks = append(checks, verifySocialAuthURL(typ, s.AuthURL, callback))
		}
	}
	return checks, nil
}

// socialCheckErr is the command's error when a check did not pass.
func socialCheckErr(checks []socialCheck) error {
	var failed []string
	for _, c := range checks {
		if !c.OK {
			failed = append(failed, c.Type)
		}
	}
	if len(failed) == 0 {
		return nil
	}
	return fmt.Errorf("social sign-in check failed for %s", strings.Join(failed, ", "))
}

// writeSocialChecks prints one block per provider.
func writeSocialChecks(w io.Writer, callback string, checks []socialCheck) {
	if len(checks) == 0 {
		fmt.Fprintln(w, "Social check: no social provider is declared.")
		return
	}
	for _, c := range checks {
		switch {
		case c.Skipped != "":
			fmt.Fprintf(w, "Social check: %s SKIPPED — %s\n", c.Type, c.Skipped)
			continue
		case c.OK:
			fmt.Fprintf(w, "Social check: %s ok — redirect_uri is the callback %s\n", c.Type, callback)
		default:
			fmt.Fprintf(w, "Social check: %s FAILED\n", c.Type)
		}
		if c.AuthEndpoint != "" {
			fmt.Fprintf(w, "  authorize: %s (client_id %s, scope %s)\n", c.AuthEndpoint, c.ClientID, strings.Join(c.Scopes, " "))
		}
		for _, p := range c.Problems {
			fmt.Fprintf(w, "  - %s\n", p)
		}
	}
}

// socialCheckHTTPClient is the --app client: no redirects followed (the
// broker answers JSON), and a bound on a broker that never answers.
func socialCheckHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
