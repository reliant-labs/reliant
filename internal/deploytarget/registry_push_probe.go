package deploytarget

// THE PUSH-ACCESS PROBE: can the credential docker holds for a registry push
// to the repositories a build is about to push to? Asked BEFORE the build, so
// a refusal costs seconds instead of a full Go, Docker and frontend build.
//
// It asks the registry's own token realm, the way a push does, and pushes
// nothing:
//
//  1. the credential is the one oras will present (dockercred, the docker
//     CLI's reading of the store), not a second guess at it;
//  2. GET /v2/ for the registry's Bearer challenge (realm and service), so
//     the realm's address is the registry's answer rather than a path forge
//     assumes — and the credential is presented only to a realm on the
//     registry's own host, over https (or plain http to a local registry);
//     see checkRealm;
//  3. ask the realm for `repository:<name>:pull,push` on every repository,
//     and read the token's `access` claim: a realm narrows a grant to what
//     the caller may do rather than refusing it, so the claim is the answer.
//
// THREE VERDICTS, AND ONLY ONE REFUSES. Denied is said only on an
// authoritative answer: the realm refused the credential (401/403), or the
// token it issued grants no push on a repository. Everything that is not an
// answer — the registry unreachable, a challenge that is not Bearer, an
// opaque token, a 5xx, a rate limit — is Unknown, and the caller proceeds
// with a note: the push itself is still the last word, and refusing on
// missing evidence would block deploys over a probe's own trouble.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/reliant-labs/forge/internal/dockercred"
)

// PushAccessVerdict is the probe's answer.
type PushAccessVerdict int

const (
	// PushAccessUnknown means the probe could not get an authoritative answer.
	PushAccessUnknown PushAccessVerdict = iota
	// PushAccessGranted means the realm issued a token granting push on every
	// repository asked about.
	PushAccessGranted
	// PushAccessDenied means the realm refused the credential, or granted no
	// push on at least one repository (Missing).
	PushAccessDenied
)

// PushAccess is the probe's verdict and why.
type PushAccess struct {
	Verdict PushAccessVerdict
	// Missing are the repositories (as asked, host included) the issued
	// token grants no push on. Empty when the realm refused the credential
	// outright, which Reason says.
	Missing []string
	// Reason is the human-readable why, for Denied and Unknown.
	Reason string
}

// pushProbeTimeout bounds the whole probe: it runs in front of a build and
// must never be what a deploy waits on.
const pushProbeTimeout = 10 * time.Second

// ProbePushAccess asks host's token realm whether the credential docker holds
// for host may push to each repository (`<host>/<path>`). Repositories on
// another host are not asked about.
func ProbePushAccess(ctx context.Context, host string, repositories []string) PushAccess {
	credential, err := dockercred.Credential()
	if err != nil {
		return PushAccess{Reason: fmt.Sprintf("read docker credentials: %v", err)}
	}
	return probePushAccess(ctx, &http.Client{Timeout: pushProbeTimeout}, credential, host, repositories)
}

func probePushAccess(ctx context.Context, client *http.Client, credential auth.CredentialFunc, host string, repositories []string) PushAccess {
	ctx, cancel := context.WithTimeout(ctx, pushProbeTimeout)
	defer cancel()

	paths := map[string]string{} // repository path → repository as asked
	var scopes []string
	for _, r := range repositories {
		h, path, ok := strings.Cut(r, "/")
		if !ok || h != host || path == "" {
			continue
		}
		if _, seen := paths[path]; !seen {
			paths[path] = r
			scopes = append(scopes, "repository:"+path+":pull,push")
		}
	}
	if len(scopes) == 0 {
		return PushAccess{Reason: "no repository on " + host + " to check"}
	}

	cred, err := credential(ctx, host)
	if err != nil {
		return PushAccess{Reason: fmt.Sprintf("read the docker credential for %s: %v", host, err)}
	}
	if cred == auth.EmptyCredential {
		return PushAccess{Verdict: PushAccessDenied, Reason: "docker holds no credential for " + host + ", so every push to it would be refused"}
	}
	if cred.AccessToken != "" && cred.Password == "" && cred.RefreshToken == "" {
		return PushAccess{Reason: "the credential for " + host + " is a registry token, which the realm cannot be asked about"}
	}

	scheme := "https"
	if plainHTTPRegistry(host) {
		scheme = "http"
	}
	realm, service, err := bearerChallenge(ctx, client, scheme+"://"+host+"/v2/")
	if err != nil {
		return PushAccess{Reason: err.Error()}
	}
	// The realm is the registry's say-so, and the credential is about to be
	// presented to it: a realm the registry does not itself serve gets no
	// credential, only a note.
	if err := checkRealm(realm, host); err != nil {
		return PushAccess{Reason: err.Error()}
	}

	req, err := tokenRequest(ctx, realm, service, scopes, cred)
	if err != nil {
		return PushAccess{Reason: err.Error()}
	}
	// No redirect is followed with the credential on: a redirect is the realm
	// naming another address, which checkRealm would have had to judge.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noRedirect.Do(req)
	if err != nil {
		return PushAccess{Reason: fmt.Sprintf("could not reach the token realm %s: %v", realm, err)}
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return PushAccess{Verdict: PushAccessDenied, Reason: fmt.Sprintf("%s refused the credential docker holds for it (%d: %s)",
			host, resp.StatusCode, registryErrorMessage(body))}
	case resp.StatusCode != http.StatusOK:
		return PushAccess{Reason: fmt.Sprintf("the token realm %s answered %d: %s", realm, resp.StatusCode, registryErrorMessage(body))}
	}

	access, err := tokenAccess(body)
	if err != nil {
		return PushAccess{Reason: fmt.Sprintf("the token %s issued could not be read: %v", host, err)}
	}
	var missing []string
	for _, s := range scopes {
		path := strings.TrimSuffix(strings.TrimPrefix(s, "repository:"), ":pull,push")
		if !grantsPush(access, path) {
			missing = append(missing, paths[path])
		}
	}
	if len(missing) > 0 {
		return PushAccess{Verdict: PushAccessDenied, Missing: missing,
			Reason: fmt.Sprintf("the token %s issues for this credential grants no push on %s", host, strings.Join(missing, ", "))}
	}
	return PushAccess{Verdict: PushAccessGranted}
}

// bearerChallenge reads the registry's WWW-Authenticate challenge on /v2/.
func bearerChallenge(ctx context.Context, client *http.Client, base string) (realm, service string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("could not reach %s: %v", base, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return "", "", fmt.Errorf("%s answered %d rather than a token challenge", base, resp.StatusCode)
	}
	scheme, params := parseChallenge(resp.Header.Get("WWW-Authenticate"))
	if !strings.EqualFold(scheme, "bearer") || params["realm"] == "" {
		return "", "", fmt.Errorf("%s does not issue registry tokens (challenge %q)", base, resp.Header.Get("WWW-Authenticate"))
	}
	return params["realm"], params["service"], nil
}

// checkRealm refuses a token realm the probe must not present a credential
// to. The registry names its realm in its 401, so a hostile or compromised
// registry could otherwise collect the password, in cleartext or at a host
// of its choosing. Allowed, and nothing else:
//
//   - the registry's own host (hostname and effective port): the platform's
//     prod realm is https://registry.reliantapi.com/auth/token beside its
//     registry, and dev's (built from the request Host) is
//     http://localhost:<port>/auth/token;
//   - https, or http only where the registry itself is spoken to over plain
//     HTTP (plainHTTPRegistry: loopback, and k3d's host gateway), where the
//     push already presents the same credential over the same transport;
//   - no userinfo in the URL.
//
// forge holds no declared realm host for a platform that serves its realm
// elsewhere, so there is no allow-list to consult; such a realm is a note.
func checkRealm(realm, registryHost string) error {
	u, err := url.Parse(realm)
	if err != nil || u.Host == "" {
		return fmt.Errorf("could not verify push access (registry's token realm %q is not an absolute URL)", realm)
	}
	notOurs := func(why string) error {
		return fmt.Errorf("could not verify push access (registry's token realm %s %s); no credential was sent to it", u.Host, why)
	}
	if u.User != nil {
		return notOurs("carries userinfo")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !plainHTTPRegistry(registryHost) {
			return notOurs("is plain http, and the registry is not a local one")
		}
	default:
		return notOurs("uses scheme " + u.Scheme)
	}
	regScheme := "https"
	if plainHTTPRegistry(registryHost) {
		regScheme = "http"
	}
	reg, err := url.Parse(regScheme + "://" + registryHost)
	if err != nil {
		return notOurs("cannot be compared with the registry's host")
	}
	if !strings.EqualFold(u.Hostname(), reg.Hostname()) || effectivePort(u) != effectivePort(reg) {
		return notOurs("is not the registry's host " + registryHost)
	}
	return nil
}

// effectivePort is u's port, or its scheme's default.
func effectivePort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "http" {
		return "80"
	}
	return "443"
}

// tokenRequest is the realm request a push's client makes: GET with basic
// auth for a username and password, the OAuth2 refresh-token form for an
// identity token (distribution's token and oauth specs).
func tokenRequest(ctx context.Context, realm, service string, scopes []string, cred auth.Credential) (*http.Request, error) {
	if cred.RefreshToken != "" {
		form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {cred.RefreshToken}, "client_id": {"forge"}}
		if service != "" {
			form.Set("service", service)
		}
		form.Set("scope", strings.Join(scopes, " "))
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, realm, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, fmt.Errorf("token realm %q: %w", realm, err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return req, nil
	}
	u, err := url.Parse(realm)
	if err != nil {
		return nil, fmt.Errorf("token realm %q: %w", realm, err)
	}
	q := u.Query()
	if service != "" {
		q.Set("service", service)
	}
	for _, s := range scopes {
		q.Add("scope", s)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(cred.Username, cred.Password)
	return req, nil
}

// tokenAccess is the `access` claim of the JWT a realm issued.
type tokenGrant struct {
	Type    string   `json:"type"`
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

func tokenAccess(body []byte) ([]tokenGrant, error) {
	var resp struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("not a token response: %w", err)
	}
	token := resp.Token
	if token == "" {
		token = resp.AccessToken
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("the token is opaque, not a JWT, so its grant cannot be read")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, fmt.Errorf("decode the token's claims: %w", err)
	}
	var claims struct {
		Access []tokenGrant `json:"access"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("decode the token's claims: %w", err)
	}
	return claims.Access, nil
}

func grantsPush(access []tokenGrant, path string) bool {
	for _, g := range access {
		if g.Type == "repository" && g.Name == path && (slices.Contains(g.Actions, "push") || slices.Contains(g.Actions, "*")) {
			return true
		}
	}
	return false
}

// parseChallenge splits `Bearer realm="…",service="…"` into its scheme and
// parameters. Quoted values may hold commas.
func parseChallenge(header string) (scheme string, params map[string]string) {
	params = map[string]string{}
	header = strings.TrimSpace(header)
	scheme, rest, _ := strings.Cut(header, " ")
	for rest = strings.TrimSpace(rest); rest != ""; rest = strings.TrimSpace(rest) {
		key, after, ok := strings.Cut(rest, "=")
		if !ok {
			break
		}
		key = strings.ToLower(strings.TrimSpace(key))
		var value string
		if strings.HasPrefix(after, `"`) {
			end := strings.Index(after[1:], `"`)
			if end < 0 {
				value, rest = after[1:], ""
			} else {
				value, rest = after[1:end+1], after[end+2:]
			}
		} else {
			value, rest, _ = strings.Cut(after, ",")
		}
		params[key] = strings.TrimSpace(value)
		rest = strings.TrimPrefix(strings.TrimSpace(rest), ",")
	}
	return scheme, params
}

// registryErrorMessage is a registry error body's first message, or the body
// itself, trimmed.
func registryErrorMessage(body []byte) string {
	var e struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
		Details string `json:"details"`
		Error   string `json:"error_description"`
	}
	if json.Unmarshal(body, &e) == nil {
		switch {
		case len(e.Errors) > 0 && e.Errors[0].Message != "":
			return e.Errors[0].Message
		case e.Error != "":
			return e.Error
		case e.Details != "":
			return e.Details
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "no detail"
	}
	return s
}
