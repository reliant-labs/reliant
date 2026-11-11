// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1 is a sender's opt-in signing scheme; HMAC does not rely on SHA-1 collision resistance
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// maxWebhookReply bounds how much of a response is read.
const maxWebhookReply = 64 << 10

// WebhookCredential is how a delivery proves it may fire a webhook trigger. The
// credential is the trigger's own (shown once when the automation is enabled),
// not an API token: a caller that only wants to start a run needs nothing else,
// which makes it the narrowest credential there is.
type WebhookCredential interface {
	apply(req *http.Request, body []byte) error
	// redacted is the credential with its secret removed, for errors and logs.
	redacted() string
}

// WebhookBearer sends the trigger's token as `Authorization: Bearer <token>`.
func WebhookBearer(token string) WebhookCredential { return bearerCred(token) }

// WebhookPathToken puts the token in the URL (`/hooks/<id>/<token>`), for
// receivers that cannot set headers. The whole URL is then a secret: it is
// never included in an error from this package.
func WebhookPathToken(token string) WebhookCredential { return pathCred(token) }

// WebhookHMAC signs the body with a shared secret, for triggers configured with
// an hmac block. Zero fields take the server's defaults: header
// X-Signature-256, sha256, hex, no prefix.
func WebhookHMAC(secret []byte, cfg WebhookHMACConfig) WebhookCredential {
	return hmacCred{secret: secret, cfg: cfg}
}

// WebhookHMACConfig mirrors the trigger's `webhook.hmac` block, which the sender
// and the trigger must agree on.
type WebhookHMACConfig struct {
	Header    string // default "X-Signature-256"
	Algorithm string // "sha256" (default), "sha1" or "sha512"
	Prefix    string // e.g. "sha256="
	Encoding  string // "hex" (default) or "base64"
}

type bearerCred string

func (b bearerCred) apply(req *http.Request, _ []byte) error {
	if b == "" {
		return errors.New("reliant: empty webhook token")
	}
	req.Header.Set("Authorization", "Bearer "+string(b))
	return nil
}
func (bearerCred) redacted() string { return "bearer token" }

type pathCred string

func (p pathCred) apply(req *http.Request, _ []byte) error {
	if p == "" {
		return errors.New("reliant: empty webhook token")
	}
	req.URL = req.URL.JoinPath(string(p))
	return nil
}
func (pathCred) redacted() string { return "path token" }

type hmacCred struct {
	secret []byte
	cfg    WebhookHMACConfig
}

func (h hmacCred) apply(req *http.Request, body []byte) error {
	if len(h.secret) == 0 {
		return errors.New("reliant: empty webhook HMAC secret")
	}
	var newHash func() hash.Hash
	switch h.cfg.Algorithm {
	case "", "sha256":
		newHash = sha256.New
	case "sha1":
		newHash = sha1.New
	case "sha512":
		newHash = sha512.New
	default:
		return fmt.Errorf("reliant: unsupported webhook HMAC algorithm %q", h.cfg.Algorithm)
	}
	mac := hmac.New(newHash, h.secret)
	mac.Write(body)
	sum := mac.Sum(nil)
	var sig string
	switch h.cfg.Encoding {
	case "", "hex":
		sig = hex.EncodeToString(sum)
	case "base64":
		sig = base64.StdEncoding.EncodeToString(sum)
	default:
		return fmt.Errorf("reliant: unsupported webhook HMAC encoding %q", h.cfg.Encoding)
	}
	header := h.cfg.Header
	if header == "" {
		header = "X-Signature-256"
	}
	req.Header.Set(header, h.cfg.Prefix+sig)
	return nil
}
func (hmacCred) redacted() string { return "HMAC signature" }

// Webhook delivers to one webhook trigger's URL, POST /hooks/{trigger_id}.
type Webhook struct {
	endpoint string
	cred     WebhookCredential
	hc       *http.Client
}

// WebhookOption configures [NewWebhook].
type WebhookOption func(*webhookConfig)

type webhookConfig struct {
	hc       *http.Client
	loopback bool
}

// WithWebhookHTTPClient sets the client deliveries are sent with.
func WithWebhookHTTPClient(hc *http.Client) WebhookOption {
	return func(c *webhookConfig) { c.hc = hc }
}

// WithWebhookInsecureLoopback allows an http:// base URL to the local machine,
// like [WithInsecureLoopback].
func WithWebhookInsecureLoopback() WebhookOption {
	return func(c *webhookConfig) { c.loopback = true }
}

// NewWebhook targets a webhook trigger. baseURL is the Reliant server
// ("https://api.reliantapi.com"; "" means that), and the credential is the
// trigger's own. It needs no API token and no [Client].
func NewWebhook(baseURL, triggerID string, cred WebhookCredential, opts ...WebhookOption) (*Webhook, error) {
	if triggerID == "" || strings.ContainsAny(triggerID, "/?#") {
		return nil, fmt.Errorf("reliant: invalid trigger id %q", triggerID)
	}
	if cred == nil {
		return nil, errors.New("reliant: a webhook needs a credential: WebhookBearer, WebhookPathToken or WebhookHMAC")
	}
	var cfg webhookConfig
	for _, o := range opts {
		o(&cfg)
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	base, err := normalizeBaseURL(baseURL, cfg.loopback, false)
	if err != nil {
		return nil, err
	}
	// A copy, so the caller's client is left as it was: redirects are refused
	// on deliveries only.
	var hc http.Client
	if cfg.hc != nil {
		hc = *cfg.hc
	}
	hc.CheckRedirect = refuseRedirect
	return &Webhook{endpoint: base + "/hooks/" + url.PathEscape(triggerID), cred: cred, hc: &hc}, nil
}

// refuseRedirect keeps a delivery at the URL it was built for. Go's client
// follows redirects by default, re-sending the Authorization header to the same
// host even over plain http, a signature header (X-Signature-256, or a custom
// one) to any host, and, on 307/308, the signed body too: so a redirect would
// carry the trigger's credential past the https origin NewWebhook validated.
// Returning ErrUseLastResponse sends no second request; the 3xx is the answer.
func refuseRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// WebhookDelivery is the server's receipt for an accepted delivery.
type WebhookDelivery struct {
	// EventID is the trigger event the delivery was recorded as.
	EventID string `json:"event_id"`
	// Outcome is where the event stands: typically "pending" (accepted, run not
	// launched yet); follow it through [TriggersClient.Events].
	Outcome string `json:"outcome"`
	// Duplicate is true when the delivery was recognised as a retry of one
	// already received, and started nothing new.
	Duplicate bool `json:"duplicate"`
}

// SendOption configures one delivery.
type SendOption func(*sendConfig)

type sendConfig struct {
	contentType string
	idempotency string
	header      http.Header
}

// WithIdempotencyKey names the event, so a retry of this delivery is recognised
// as one event and starts one run. Without it the server treats an identical
// body within the same minute as a retry; a key makes that exact.
func WithIdempotencyKey(key string) SendOption {
	return func(c *sendConfig) { c.idempotency = key }
}

// WithContentType overrides the body's content type (application/json for
// [Webhook.SendJSON], application/octet-stream otherwise).
func WithContentType(ct string) SendOption {
	return func(c *sendConfig) { c.contentType = ct }
}

// WithHeader adds a header to the delivery; a workflow filter can read it from
// trigger.payload.headers.
func WithHeader(key, value string) SendOption {
	return func(c *sendConfig) {
		if c.header == nil {
			c.header = http.Header{}
		}
		c.header.Add(key, value)
	}
}

// SendJSON delivers v, marshalled as JSON.
func (w *Webhook) SendJSON(ctx context.Context, v any, opts ...SendOption) (*WebhookDelivery, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("reliant: webhook body: %w", err)
	}
	return w.send(ctx, body, "application/json", opts)
}

// Send delivers body as is. The body is limited to 1 MiB by the server.
func (w *Webhook) Send(ctx context.Context, body []byte, opts ...SendOption) (*WebhookDelivery, error) {
	return w.send(ctx, body, "application/octet-stream", opts)
}

func (w *Webhook) send(ctx context.Context, body []byte, defaultCT string, opts []SendOption) (*WebhookDelivery, error) {
	cfg := sendConfig{contentType: defaultCT}
	for _, o := range opts {
		o(&cfg)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("reliant: webhook request: %w", err)
	}
	req.Header.Set("Content-Type", cfg.contentType)
	req.Header.Set("User-Agent", "reliant-go-sdk/"+Version)
	for k, vs := range cfg.header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if cfg.idempotency != "" {
		req.Header.Set("Idempotency-Key", cfg.idempotency)
	}
	if err := w.cred.apply(req, body); err != nil {
		return nil, err
	}

	resp, err := w.hc.Do(req)
	if err != nil {
		// A *url.Error carries the request URL, which for a path token IS the
		// credential. Report the cause only.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &Error{Op: "webhook.send", Server: w.server(), Message: err.Error(), kind: ErrUnavailable, cause: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebhookReply))

	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
		var d WebhookDelivery
		if err := json.Unmarshal(raw, &d); err != nil {
			// Accepted is accepted: a receipt we cannot read is not a failure.
			return &WebhookDelivery{}, nil
		}
		return &d, nil
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		// Not the Location: for a path token the redirect may echo the URL,
		// which IS the credential.
		return nil, &Error{Op: "webhook.send", Server: w.server(), kind: ErrFailedPrecondition,
			Message: fmt.Sprintf("the server answered with a redirect (HTTP %d); a webhook delivery never follows one, so its credential stays at the origin", resp.StatusCode)}
	}
	return nil, w.statusError(resp.StatusCode, strings.TrimSpace(string(raw)))
}

// server is the origin, for errors.
func (w *Webhook) server() string {
	u, err := url.Parse(w.endpoint)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// statusError maps the receiver's documented answers. Every authentication
// failure is the same 404, deliberately, so a caller cannot probe which
// trigger ids exist.
func (w *Webhook) statusError(status int, body string) error {
	e := &Error{Op: "webhook.send", Server: w.server(), Message: body}
	switch status {
	case http.StatusNotFound:
		e.kind, e.Message = ErrNotFound, "not found: the trigger id, the credential or the signature is wrong (the server does not say which)"
	case http.StatusConflict:
		e.kind, e.Message = ErrFailedPrecondition, "the trigger is disabled"
	case http.StatusRequestEntityTooLarge:
		e.kind, e.Message = ErrInvalidArgument, "body exceeds the 1 MiB limit"
	case http.StatusBadRequest:
		e.kind = ErrInvalidArgument
	case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusTooManyRequests:
		e.kind = ErrUnavailable
	default:
		e.kind = fmt.Errorf("reliant: webhook answered HTTP %d", status)
	}
	return e
}
