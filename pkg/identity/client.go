package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	defaultRequestTimeout = 15 * time.Second
	defaultMaxAttempts    = 4
	retryBaseDelay        = 200 * time.Millisecond
	retryMaxDelay         = 5 * time.Second
	maxResponseBytes      = 8 << 20
)

// client is the HTTP transport to one Zitadel instance. ONE base URL: Zitadel
// serves OIDC, the v2 resource APIs and the v1 management / admin APIs on a
// single origin and authorizes all of them with the same bearer token.
type client struct {
	base        string
	host        string
	issuer      string
	http        *http.Client
	cred        Credential
	orgID       string
	maxAttempts int
	timeout     time.Duration
	log         *slog.Logger
	now         func() time.Time
	sleep       func(context.Context, time.Duration) error

	issuerOnce sync.Mutex
}

// callOpt tunes one request.
type callOpt struct {
	// idempotent allows a retry after a transport error or an ambiguous 5xx.
	// A call that MINTS something (a key, a token) is not: a response lost in
	// flight would leave a live credential nobody holds.
	idempotent bool
	// noAuth sends no Authorization header (token endpoint, JWKS, readiness).
	noAuth bool
	// bearer overrides the credential, to test a token that is not ours.
	bearer string
	// noOrg omits x-zitadel-orgid.
	noOrg bool
}

var (
	idem  = callOpt{idempotent: true}
	mint  = callOpt{}
	anon  = callOpt{idempotent: true, noAuth: true, noOrg: true}
	noOrg = callOpt{idempotent: true, noOrg: true}
)

func (c *client) url(path string) string { return strings.TrimRight(c.base, "/") + path }

// call performs one API request with bounded retries and decodes a JSON
// response into out (when non-nil). Errors are *APIError for non-2xx answers.
func (c *client) call(ctx context.Context, method, path string, body, out any, opt callOpt) error {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
	}
	var last error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		raw, err := c.once(ctx, method, path, payload, opt)
		if err == nil {
			if out == nil || len(bytes.TrimSpace(raw)) == 0 {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("decode %s %s: %w", method, path, err)
			}
			return nil
		}
		last = err
		if ctx.Err() != nil || attempt == c.maxAttempts || !retryable(err, opt) {
			return err
		}
		if serr := c.sleep(ctx, backoff(attempt)); serr != nil {
			return err
		}
	}
	return last
}

// retryable reports whether err is worth another attempt. 429 and 503 mean the
// request was refused before it was processed, so any call may retry; a
// transport error or 502/504 is ambiguous and only an idempotent call retries.
func retryable(err error, opt callOpt) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusTooManyRequests, http.StatusServiceUnavailable:
			return true
		case http.StatusBadGateway, http.StatusGatewayTimeout:
			return opt.idempotent
		}
		return false
	}
	return opt.idempotent
}

func backoff(attempt int) time.Duration {
	d := retryBaseDelay << (attempt - 1)
	if d > retryMaxDelay {
		d = retryMaxDelay
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

func (c *client) once(ctx context.Context, method, path string, payload []byte, opt callOpt) ([]byte, error) {
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), rd)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.host != "" {
		// Zitadel resolves the instance from the Host header; req.Host, not
		// Header.Set("Host"), is what net/http puts on the wire.
		req.Host = c.host
	}
	if !opt.noAuth {
		token := opt.bearer
		if token == "" {
			if token, err = c.cred.bearer(ctx, c); err != nil {
				return nil, err
			}
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if c.orgID != "" && !opt.noOrg {
		req.Header.Set("x-zitadel-orgid", c.orgID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if resp.StatusCode >= 300 {
		return nil, newAPIError(method, path, resp.StatusCode, raw)
	}
	if rerr != nil {
		return nil, fmt.Errorf("read %s %s: %w", method, path, rerr)
	}
	// The gRPC gateway can answer a failure with 200 and a status body.
	if ae := gatewayError(method, path, raw); ae != nil {
		return nil, ae
	}
	return raw, nil
}

// APIError is a non-2xx answer from the instance. Message is Zitadel's own text
// and ID its stable error id (COMMAND-1m88i): branch on the id, never on the
// localized message.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Code    int
	ID      string
	Message string
	Body    string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = e.Body
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.Status, msg)
}

func newAPIError(method, path string, status int, body []byte) *APIError {
	e := &APIError{Method: method, Path: path, Status: status, Body: strings.TrimSpace(string(body))}
	var env struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Details []struct {
			ID      string `json:"id"`
			Message string `json:"message"`
		} `json:"details"`
	}
	if json.Unmarshal(body, &env) == nil {
		e.Code, e.Message = env.Code, env.Message
		if len(env.Details) > 0 {
			e.ID = env.Details[0].ID
		}
	}
	return e
}

func gatewayError(method, path string, body []byte) *APIError {
	var probe struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &probe) != nil || probe.Code == nil || *probe.Code == 0 || probe.Message == "" {
		return nil
	}
	return &APIError{Method: method, Path: path, Status: http.StatusOK, Code: *probe.Code, Message: probe.Message, Body: string(body)}
}

func asAPI(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}

// isNotFound reports a missing object (gRPC NotFound / HTTP 404).
func isNotFound(err error) bool {
	ae := asAPI(err)
	return ae != nil && (ae.Status == http.StatusNotFound || ae.Code == 5)
}

// isAlreadyExists reports a uniqueness conflict (gRPC AlreadyExists / HTTP 409).
func isAlreadyExists(err error) bool {
	ae := asAPI(err)
	return ae != nil && (ae.Status == http.StatusConflict || ae.Code == 6 ||
		strings.Contains(strings.ToLower(ae.Message), "already exists"))
}

// isNoChange reports Zitadel's refusal to write what is already there. It
// answers an idempotent update with HTTP 400 (FailedPrecondition) rather than
// 200, so a converge step that treated every 4xx as failure would break on its
// own second run. Matched on the stable ids, then on the family of messages.
func isNoChange(err error) bool {
	ae := asAPI(err)
	if ae == nil {
		return false
	}
	switch ae.ID {
	case "COMMAND-1m88i", // project / application
		"COMMAND-2n8vs",      // machine user
		"COMMAND-Rs8fy",      // user grant
		"INSTANCE-9jlsf",     // password complexity
		"INSTANCE-0psjF",     // lockout
		"COMMAND-vUHBSmBzaw": // SMTP already active
		return true
	}
	m := strings.ToLower(ae.Message)
	return strings.Contains(m, "not been changed") || strings.Contains(m, "notchanged") ||
		strings.Contains(m, "no changes") || strings.Contains(m, "alreadyactive")
}

// ObjectError names the object a failure concerns. Every error leaving the
// package is wrapped in one, so "the pass failed" always says WHICH thing.
type ObjectError struct {
	Kind string
	Name string
	Err  error
}

func (e *ObjectError) Error() string { return fmt.Sprintf("%s %q: %v", e.Kind, e.Name, e.Err) }
func (e *ObjectError) Unwrap() error { return e.Err }

func objErr(kind, name string, err error) error {
	if err == nil {
		return nil
	}
	return &ObjectError{Kind: kind, Name: name, Err: err}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// resolveIssuer returns the issuer the instance mints tokens under. Zitadel
// derives it from the external domain it is registered with, which is not
// necessarily the address this process dials, so it is read from the instance's
// own discovery document unless the caller pinned it.
func (c *client) resolveIssuer(ctx context.Context) (string, error) {
	c.issuerOnce.Lock()
	defer c.issuerOnce.Unlock()
	if c.issuer != "" {
		return c.issuer, nil
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := c.call(ctx, http.MethodGet, "/.well-known/openid-configuration", nil, &doc, anon); err != nil {
		return "", objErr("issuer", c.base, err)
	}
	if doc.Issuer == "" {
		return "", objErr("issuer", c.base, errors.New("the discovery document carries no issuer"))
	}
	c.issuer = strings.TrimRight(doc.Issuer, "/")
	return c.issuer, nil
}

// waitReady blocks until the instance can actually serve API calls.
//
// Two probes, both needed. /debug/ready reports READY rather than merely
// listening, which matters on a first boot where the instance is still running
// its setup. But it answers before the gRPC gateway can dial the server behind
// it, so the first management call after it can still fail with a 503
// "connection refused". The management healthz route goes THROUGH the gateway:
// any answer that is not a 5xx or a transport error (even 401 or 403) proves
// the gateway is wired up.
func (c *client) waitReady(ctx context.Context) error {
	for {
		err := c.call(ctx, http.MethodGet, "/debug/ready", nil, nil, callOpt{noAuth: true, noOrg: true})
		if err == nil {
			err = c.call(ctx, http.MethodGet, "/management/v1/healthz", nil, nil, callOpt{noAuth: true, noOrg: true})
			if ae := asAPI(err); ae != nil && ae.Status < 500 {
				err = nil
			}
		}
		if err == nil {
			return nil
		}
		if serr := c.sleep(ctx, time.Second); serr != nil {
			return objErr("instance", c.base, fmt.Errorf("not ready: %w (last probe: %v)", serr, err))
		}
	}
}
