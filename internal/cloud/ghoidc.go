package cloud

// A GitHub Actions job's own identity as a control-plane credential: the job's
// OIDC ID token, exchanged for a short-lived access token that carries one
// federatable scope (accesstoken.FederationOf). It is how a CI upload needs no
// secret. The control plane decides what the token may do: it verifies the ID
// token, maps the repository to an org through its linked GitHub App
// installation, and grants only scopes the catalog marks federatable.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// The environment GitHub Actions gives a step. The two request variables exist
// only in a job granted `permissions: id-token: write`, and never in a pull
// request from a fork.
const (
	envGitHubActions       = "GITHUB_ACTIONS"
	envGitHubRepository    = "GITHUB_REPOSITORY"
	envIDTokenRequestURL   = "ACTIONS_ID_TOKEN_REQUEST_URL"
	envIDTokenRequestToken = "ACTIONS_ID_TOKEN_REQUEST_TOKEN"
)

// CIOIDCEnv turns the CI-identity exchange off when set to "off": forge then
// resolves a federatable scope exactly as it resolves any other.
const CIOIDCEnv = "FORGE_CI_OIDC"

// procExchangeFederatedToken trades a CI provider's ID token for an access
// token. It is called with no bearer: the ID token is the credential.
const procExchangeFederatedToken = "controlplane.v1.AccessTokenService/ExchangeFederatedToken"

// federationProviderGitHubActions is the FederationProvider enum value, as
// Connect's JSON binding spells it.
const federationProviderGitHubActions = "FEDERATION_PROVIDER_GITHUB_ACTIONS"

// MissingIDTokenHint is the remedy for a GitHub Actions job that has no OIDC
// token to exchange.
const MissingIDTokenHint = "this GitHub Actions job has no OIDC token: add `permissions: id-token: write` to this job " +
	"(fork pull requests never get one)"

// idTokenTimeout bounds one request for the job's ID token, and
// idTokenAttempts is how many times a 5xx or a network failure is tried in
// all. idTokenBackoff is the wait before each retry; a package var so a test
// needs no real waits.
const (
	idTokenTimeout  = 10 * time.Second
	idTokenAttempts = 3
	exchangeTimeout = 30 * time.Second
)

var idTokenBackoff = func(attempt int) time.Duration { return time.Duration(attempt) * time.Second }

// ciIdentity resolves the CI-identity credential. It is a value, not package
// state, so a test gives it its own environment (getenv) and mask sink and
// can run in parallel; processCIIdentity is the one ResolveCredentialFor
// uses.
type ciIdentity struct {
	getenv func(string) string
	// mask receives the `::add-mask::` workflow command for each minted token.
	// The runner reads workflow commands from stderr as well as stdout, and
	// stdout is a command's --json document.
	mask io.Writer
	now  func() time.Time
	// backoff, when set, replaces idTokenBackoff.
	backoff func(attempt int) time.Duration

	mu sync.Mutex
	// cache holds the minted token per (normalized endpoint, scope), for the
	// life of the process.
	cache map[string]Credential
}

var processCIIdentity = &ciIdentity{getenv: os.Getenv, mask: os.Stderr, now: time.Now}

// detectGitHubActionsOIDC reports the job's ID-token request URL and bearer
// when this process runs in GitHub Actions with an ID token available and
// FORGE_CI_OIDC is not "off".
func (ci *ciIdentity) detectGitHubActionsOIDC() (reqURL, reqToken string, ok bool) {
	if !ci.inGitHubActions() {
		return "", "", false
	}
	reqURL = strings.TrimSpace(ci.getenv(envIDTokenRequestURL))
	reqToken = strings.TrimSpace(ci.getenv(envIDTokenRequestToken))
	if reqURL == "" || reqToken == "" {
		return "", "", false
	}
	return reqURL, reqToken, true
}

// inGitHubActions reports a GitHub Actions job with the exchange not switched
// off, whether or not it was granted an ID token.
func (ci *ciIdentity) inGitHubActions() bool {
	return ci.getenv(envGitHubActions) == "true" && !strings.EqualFold(strings.TrimSpace(ci.getenv(CIOIDCEnv)), "off")
}

// credential is the CI-identity credential for scope at ep. ok is false, with
// no error, when there is no CI identity to exchange; hint then says why when
// the reason is one the user can fix (a job without `id-token: write`). A
// refused exchange is ErrNoCredential carrying the control plane's remedy.
func (ci *ciIdentity) credential(ep Endpoint, scope accesstoken.Scope) (cred Credential, ok bool, hint string, err error) {
	reqURL, reqToken, ok := ci.detectGitHubActionsOIDC()
	if !ok {
		if ci.inGitHubActions() {
			hint = MissingIDTokenHint
		}
		return Credential{}, false, hint, nil
	}
	audience, err := credentials.Normalize(ep.URL)
	if err != nil {
		return Credential{}, false, "", err
	}
	key := audience + "\x00" + string(scope)

	ci.mu.Lock()
	defer ci.mu.Unlock()
	if cached, hit := ci.cache[key]; hit {
		if cached.ExpiresAt != nil && cached.ExpiresAt.Sub(ci.now()) > helperReuseMargin {
			return cached, true, "", nil
		}
		delete(ci.cache, key)
	}

	ctx, cancel := context.WithTimeout(context.Background(), idTokenTimeout*idTokenAttempts+exchangeTimeout)
	defer cancel()
	// A fresh ID token for every exchange: the control plane refuses one it
	// has seen (its jti), and a job's ID token is good for minutes only.
	idToken, err := ci.fetchGitHubIDToken(ctx, reqURL, reqToken, audience)
	if err != nil {
		return Credential{}, false, "", fmt.Errorf("could not get this GitHub Actions job's OIDC token: %w", err)
	}
	cred, err = ci.exchangeFederated(ctx, ep, idToken, scope)
	if err != nil {
		return Credential{}, false, "", err
	}
	if ci.cache == nil {
		ci.cache = map[string]Credential{}
	}
	ci.cache[key] = cred
	// Before first use, so the token is masked in every line the job prints
	// after this one, whoever prints it.
	if ci.mask != nil {
		_, _ = fmt.Fprintf(ci.mask, "::add-mask::%s\n", cred.Token)
	}
	return cred, true, "", nil
}

// fetchGitHubIDToken asks the Actions runtime for the job's ID token with
// audience, the normalized control-plane URL: a token minted for us is useless
// to any other relying party, and theirs to us. A 5xx or a network failure is
// tried idTokenAttempts times in all; anything else is final.
func (ci *ciIdentity) fetchGitHubIDToken(ctx context.Context, reqURL, reqToken, audience string) (string, error) {
	sep := "?"
	if strings.Contains(reqURL, "?") {
		sep = "&"
	}
	target := reqURL + sep + "audience=" + url.QueryEscape(audience)
	var lastErr error
	for attempt := 1; attempt <= idTokenAttempts; attempt++ {
		if attempt > 1 {
			wait := idTokenBackoff
			if ci.backoff != nil {
				wait = ci.backoff
			}
			t := time.NewTimer(wait(attempt - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				return "", lastErr
			case <-t.C:
			}
		}
		token, retry, err := ci.fetchIDTokenOnce(ctx, target, reqToken)
		if err == nil {
			return token, nil
		}
		lastErr = err
		if !retry || ctx.Err() != nil {
			break
		}
	}
	return "", lastErr
}

// fetchIDTokenOnce is one ID-token request. retry reports a failure worth
// trying again (a 5xx or a request that got no answer). Errors name the status,
// never a body or a header: neither token may reach a log.
func (ci *ciIdentity) fetchIDTokenOnce(ctx context.Context, target, reqToken string) (token string, retry bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, idTokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		// The URL is the runner's; do not echo it with the audience appended.
		return "", false, fmt.Errorf("build the ID-token request from $%s: %w", envIDTokenRequestURL, withoutURL(err))
	}
	req.Header.Set("Authorization", "Bearer "+reqToken)
	req.Header.Set("Accept", "application/json")
	resp, err := noRedirectClient(idTokenTimeout).Do(req)
	if err != nil {
		return "", true, fmt.Errorf("request to $%s failed: %w", envIDTokenRequestURL, withoutURL(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", true, fmt.Errorf("read the ID-token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode >= 500, fmt.Errorf("$%s answered HTTP %d", envIDTokenRequestURL, resp.StatusCode)
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", false, errors.New("the ID-token response is not JSON")
	}
	if strings.TrimSpace(body.Value) == "" {
		return "", false, errors.New("the ID-token response carries no token")
	}
	return body.Value, false, nil
}

// exchangeFederated trades idToken for an access token carrying scope.
//
// A refusal the job cannot get past by asking again (an older control plane,
// a repository whose GitHub App is not installed or linked, an event it does
// not federate, a throttle, an outage) is ErrNoCredential carrying the server's
// message, which names the remedy: an upload then skips and stays green, as it
// does with no credential at all.
func (ci *ciIdentity) exchangeFederated(ctx context.Context, ep Endpoint, idToken string, scope accesstoken.Scope) (Credential, error) {
	c := &Client{Endpoint: ep, HTTP: noRedirectClient(exchangeTimeout)}
	var resp struct {
		Token     string   `json:"token"`
		ExpiresAt string   `json:"expiresAt"`
		Scopes    []string `json:"scopes"`
		OrgID     string   `json:"orgId"`
		Subject   string   `json:"subject"`
	}
	req := map[string]any{
		"provider": federationProviderGitHubActions,
		"idToken":  idToken,
		"scopes":   []string{string(scope)},
	}
	// No bearer: the ID token in the body is the credential.
	if err := c.call(ctx, procExchangeFederatedToken, req, &resp, ""); err != nil {
		if exchangeDegrades(err) {
			return Credential{}, exchangeRefusal(ep, err)
		}
		return Credential{}, err
	}
	if strings.TrimSpace(resp.Token) == "" {
		return Credential{}, errors.New("the control plane answered the OIDC exchange with no token")
	}
	expires := ci.now().Add(2 * helperReuseMargin)
	if at, err := time.Parse(time.RFC3339Nano, resp.ExpiresAt); err == nil {
		expires = at
	}
	from := "GitHub Actions OIDC"
	switch {
	case resp.Subject != "":
		from += " (" + resp.Subject + ")"
	case ci.getenv(envGitHubRepository) != "":
		from += " (" + ci.getenv(envGitHubRepository) + ")"
	}
	return Credential{Token: resp.Token, Source: SourceCIIdentity, From: from, ExpiresAt: &expires, Scopes: resp.Scopes}, nil
}

// withoutURL drops the request URL net/http puts in its errors.
func withoutURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// noRedirectClient is the client for both requests, and it NEVER follows a
// redirect.
// Each carries a secret: the ID-token request the runner's bearer
// ($ACTIONS_ID_TOKEN_REQUEST_TOKEN) in its header, the exchange the job's ID
// token in its body. net/http replays a 307/308's body to wherever the
// Location names, any origin, so an endpoint that redirects (misconfigured,
// or a repository's KCL naming a hostile one) would hand the ID token to a
// third party. The redirect is answered as the response itself instead, and
// fails as a non-200.
func noRedirectClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// exchangeDegrades reports an exchange answer that means "no CI credential
// here", as opposed to a defect worth surfacing as an error.
func exchangeDegrades(err error) bool {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.HTTPStatus == http.StatusNotFound && apiErr.Code == "" {
		// A control plane that predates the rpc, behind a router that does
		// not speak Connect.
		return true
	}
	switch apiErr.Code {
	case CodeUnimplemented, CodeFailedPrecondition, CodePermissionDenied,
		CodeUnauthenticated, CodeResourceExhausted, CodeUnavailable:
		return true
	}
	return false
}

// exchangeRefusal words a degraded exchange as ErrNoCredential. Its first line
// carries the server's remedy, since a caller showing one line shows that.
func exchangeRefusal(ep Endpoint, err error) error {
	var apiErr *Error
	errors.As(err, &apiErr)
	why := strings.TrimSpace(apiErr.Message)
	if apiErr.HasCode(CodeUnimplemented) || apiErr.HTTPStatus == http.StatusNotFound {
		why = "this control plane does not exchange GitHub Actions OIDC tokens yet"
	}
	if why == "" {
		why = fmt.Sprintf("HTTP %d%s", apiErr.HTTPStatus, codeSuffix(apiErr.Code))
	}
	msg := fmt.Sprintf("%v for %s: the control plane refused this job's GitHub Actions OIDC token: %s", ErrNoCredential, ep.URL, why)
	return &ciIdentityError{msg: msg, why: why, cause: err}
}

// CIIdentityRefusal reports why the control plane refused to exchange this CI
// job's identity, when err is that refusal: the server's own remedy, whole, for
// a caller that shows a bounded one-line summary of other errors.
func CIIdentityRefusal(err error) (why string, ok bool) {
	var refusal *ciIdentityError
	if errors.As(err, &refusal) {
		return refusal.why, true
	}
	return "", false
}

// ciIdentityError is ErrNoCredential (errors.Is) whose message is the control
// plane's refusal, and which still unwraps to the *Error for its code.
type ciIdentityError struct {
	msg   string
	why   string
	cause error
}

func (e *ciIdentityError) Error() string   { return e.msg }
func (e *ciIdentityError) Unwrap() []error { return []error{ErrNoCredential, e.cause} }
