// Copyright (c) 2025 Reliant Labs

package gmail

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

	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

// The Go half of Gmail (the go: executors and the poller) calls the API
// through api: the guarded client the declarative runner uses — SSRF-checked
// dialing, no redirect off the starting host — with the connection's
// credential, which itself refuses any host but the manifest's
// (connections.Resolved.Apply). Both pins hold even if a URL here were wrong.

const (
	// apiTimeout bounds one Gmail call.
	apiTimeout = 30 * time.Second
	// maxResponse bounds one Gmail response body. A full message with a long
	// HTML body is the largest thing read; attachments are never fetched.
	maxResponse = 10 << 20
	userAgent   = "reliant-integrations/1 (gmail)"
)

// api is one mailbox, authenticated as one connection.
type api struct {
	base   *url.URL
	client *http.Client
	cred   httpaction.Credential
}

func newAPI(baseURL string, client *http.Client, cred httpaction.Credential) (*api, error) {
	if cred == nil {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "Gmail needs a connection"}
	}
	if client == nil {
		return nil, fmt.Errorf("gmail: no HTTP client")
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("gmail: base_url %q is not an https URL", baseURL)
	}
	// The client must not follow a redirect at all: a credential on the
	// redirected request is what the host pin exists to prevent, and Gmail's
	// API never redirects.
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &api{base: u, client: &noRedirect, cred: cred}, nil
}

// apiError is a non-2xx Gmail answer, classified. Text is the user-facing
// message, worded exactly as the manifest's declarative error rules word the
// same status, so an action reads the same whichever half ran it.
type apiError struct {
	Status    int
	Reason    string // errors[0].reason, e.g. rateLimitExceeded
	Text      string
	Retryable bool
}

func (e *apiError) Error() string { return e.Text }

// googleError is Google's JSON error model.
type googleError struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

// do sends one request and decodes a 2xx JSON answer into out.
//
// A 401 means Google refused a token that had not expired (revoked, or
// invalidated early). When the credential can be replaced
// (httpaction.RejectableCredential), the token is refreshed and the request
// sent once more with it; the replacement is kept for the rest of this api's
// calls. A 401 that survives that — or a credential that cannot be replaced —
// is a CredentialError with CodeNeedsReauth, so a poll is recorded as
// waiting on a reconnect instead of being retried forever.
func (c *api) do(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	err := c.once(ctx, method, path, query, body, out)
	var ce *httpaction.CredentialError
	if !errors.As(err, &ce) || ce.Code != httpaction.CodeNeedsReauth {
		return err
	}
	rc, ok := c.cred.(httpaction.RejectableCredential)
	if !ok {
		return err
	}
	next, rerr := rc.Rejected(ctx)
	if rerr != nil {
		var nce *httpaction.CredentialError
		if errors.As(rerr, &nce) && nce.Code == httpaction.CodeNeedsReauth {
			// The grant is dead; keep Google's 401 as the status.
			nce.Err = errors.Join(nce.Err, ce.Err)
			return nce
		}
		return rerr
	}
	c.cred = next
	return c.once(ctx, method, path, query, body, out)
}

func (c *api) once(ctx context.Context, method, path string, query url.Values, body any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	u := *c.base
	u.Path = c.base.Path + path
	u.RawPath = ""
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err := c.cred.Apply(req); err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("gmail: %s", c.cred.Scrub(err.Error()))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("gmail: reading the response: %w", err)
	}
	if len(raw) > maxResponse {
		return fmt.Errorf("gmail: the response exceeds %d bytes", maxResponse)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return c.classify(resp.StatusCode, raw)
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("gmail: the response is not the expected JSON: %w", err)
	}
	return nil
}

// classify maps a Gmail error onto the vocabulary in the manifest's
// gmail_errors rules (keep the two in step).
func (c *api) classify(status int, raw []byte) error {
	var ge googleError
	_ = json.Unmarshal(raw, &ge)
	msg := func(fallback string) string {
		if m := strings.TrimSpace(ge.Error.Message); m != "" {
			return truncate(c.cred.Scrub(m), 300)
		}
		return fallback
	}
	e := &apiError{Status: status}
	if len(ge.Error.Errors) > 0 {
		e.Reason = ge.Error.Errors[0].Reason
	}
	reasons := map[string]bool{}
	for _, r := range ge.Error.Errors {
		reasons[r.Reason] = true
	}
	for _, d := range ge.Error.Details {
		reasons[d.Reason] = true
	}
	switch {
	case status == http.StatusUnauthorized:
		e.Text = fmt.Sprintf("Google rejected the Gmail credential (%s): reconnect Gmail in Settings.", msg("invalid credentials"))
		return &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth, Message: e.Text, Err: e}
	case status == http.StatusForbidden && (reasons["rateLimitExceeded"] || reasons["userRateLimitExceeded"]),
		status == http.StatusTooManyRequests:
		e.Text = fmt.Sprintf("Gmail rate limit exceeded (%s). Retry after at least 60 seconds.", msg("rate limited"))
		e.Retryable = true
	case status == http.StatusForbidden && (reasons["insufficientPermissions"] || reasons["ACCESS_TOKEN_SCOPE_INSUFFICIENT"]):
		e.Text = fmt.Sprintf("The Gmail connection was not granted the access this needs (%s): reconnect Gmail and allow every permission it asks for.", msg("insufficient permission"))
	case status == http.StatusForbidden:
		e.Text = fmt.Sprintf("Gmail refused the request: %s.", msg("forbidden"))
	case status == http.StatusBadRequest:
		e.Text = fmt.Sprintf("Gmail rejected the request as invalid: %s.", msg("bad request"))
	case status == http.StatusNotFound:
		e.Text = fmt.Sprintf("Gmail returned not found: %s.", msg("the message or label does not exist in this mailbox"))
	default:
		e.Text = fmt.Sprintf("Gmail returned HTTP %d: %s", status, msg("no detail"))
		e.Retryable = status >= 500
	}
	return e
}

// asResult turns an error from do into an action result: a failed call the
// graph can branch on, not a Go error, exactly as the declarative runner
// reports a non-2xx. Anything that is not a Gmail answer stays an error.
//
// An apiError — including the one a 401's CredentialError wraps — carries the
// status and the manifest-worded text.
func asResult(err error) (*httpaction.Result, error) {
	var ae *apiError
	if errors.As(err, &ae) {
		return &httpaction.Result{Content: ae.Text, IsError: true, Retryable: ae.Retryable, StatusCode: ae.Status}, nil
	}
	return nil, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
