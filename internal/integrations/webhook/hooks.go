// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // G505: HMAC-SHA1 is a sender's opt-in signing scheme; HMAC does not rely on SHA-1 collision resistance
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
)

// HooksStore is what the generic receiver reads. *db.Repo satisfies it.
type HooksStore interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
	GetTriggerWebhookCredentials(ctx context.Context, id string) (*core.TriggerWebhookCredentials, error)
}

// SecretOpener opens a webhook trigger's sealed HMAC secret. Satisfied by
// *vault.Vault.
type SecretOpener interface {
	Open(ctx context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error)
}

// HooksOptions configures the generic receiver.
type HooksOptions struct {
	Store  HooksStore
	Intake Intake
	// Opener opens HMAC secrets; nil disables signature verification (a
	// trigger configured for it then accepts only its token).
	Opener SecretOpener
}

// HooksReceiver serves a webhook trigger's own URL:
//
//	POST /hooks/{trigger_id}/{token}   token in the path (Zapier and friends)
//	POST /hooks/{trigger_id}           Authorization: Bearer <token>, or an
//	                                   HMAC signature when configured
//
// Any one credential is enough. Every failure — unknown trigger, wrong kind,
// wrong token, bad signature — is the same 404, so the endpoint does not
// reveal which trigger ids exist.
type HooksReceiver struct {
	opts HooksOptions
	now  func() time.Time
}

// NewHooksReceiver builds the generic receiver.
func NewHooksReceiver(opts HooksOptions) *HooksReceiver {
	return &HooksReceiver{opts: opts, now: time.Now}
}

// Register mounts the routes on a ServeMux-compatible registrar.
func (h *HooksReceiver) Register(handle func(pattern string, handler http.Handler)) {
	handle("/hooks/{trigger_id}", http.HandlerFunc(h.serve))
	handle("/hooks/{trigger_id}/{token}", http.HandlerFunc(h.serve))
}

// idempotencyHeaders name a sender-supplied event id, in preference order.
var idempotencyHeaders = []string{"Idempotency-Key", "X-Idempotency-Key", "X-Request-Id", "X-Delivery-Id"}

// maxIdempotencyKey bounds a sender's key; a longer one is hashed.
const maxIdempotencyKey = 200

// bodyBucket is the window in which the same body without a key is treated
// as a retry of itself.
const bodyBucket = time.Minute

func (h *HooksReceiver) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeStatus(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := r.Context()
	triggerID := r.PathValue("trigger_id")

	body, err := readBody(r)
	if err != nil {
		if errors.Is(err, errBodyTooLarge) {
			writeStatus(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", MaxBodyBytes))
			return
		}
		writeStatus(w, http.StatusBadRequest, "could not read body")
		return
	}

	trigger, cfg, ok := h.authenticate(ctx, triggerID, r, body)
	if !ok {
		writeStatus(w, http.StatusNotFound, "not found")
		return
	}
	if !trigger.Enabled {
		// Authenticated, so the sender may know: it is a real answer, and a
		// retrying sender should stop rather than hammer.
		writeStatus(w, http.StatusConflict, "trigger is disabled")
		return
	}

	now := h.now().UTC()
	sigHeader := ""
	if cfg.HMAC != nil {
		sigHeader = hmacHeader(cfg.HMAC)
	}
	payload := map[string]any{
		"body":         parseBody(body, r.Header),
		"headers":      recordedHeaders(r.Header, sigHeader),
		"query":        recordedQuery(r.URL.Query()),
		"content_type": r.Header.Get("Content-Type"),
		"received_at":  now.Format(time.RFC3339),
	}
	res, err := h.opts.Intake.Accept(ctx, trigger, triggers.InboundEvent{
		Kind:       core.TriggerEventKindWebhook,
		DedupeKey:  trigger.ID + ":" + deliveryKey(r.Header, body, now),
		OccurredAt: now,
		Payload:    payload,
	}, triggers.AcceptOptions{})
	if err != nil {
		logging.Error("webhook delivery could not be recorded", "trigger_id", trigger.ID, "error", err)
		writeStatus(w, http.StatusServiceUnavailable, "could not record the delivery; retry")
		return
	}
	reply, err := json.Marshal(map[string]any{"event_id": res.EventID, "outcome": res.Outcome, "duplicate": res.Duplicate})
	if err != nil {
		writeStatus(w, http.StatusAccepted, "accepted")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write(append(reply, '\n'))
}

// authenticate resolves the trigger and checks the request carries one of
// its credentials.
func (h *HooksReceiver) authenticate(ctx context.Context, triggerID string, r *http.Request, body []byte) (*core.Trigger, core.WebhookConfig, bool) {
	var cfg core.WebhookConfig
	if triggerID == "" {
		return nil, cfg, false
	}
	trigger, err := h.opts.Store.GetTrigger(ctx, triggerID)
	if err != nil || trigger == nil || trigger.Kind != core.TriggerKindWebhook {
		return nil, cfg, false
	}
	cfg, err = triggers.WebhookConfigFor(trigger)
	if err != nil {
		logging.Warn("webhook trigger config does not parse", "trigger_id", triggerID, "error", err)
		return nil, cfg, false
	}
	creds, err := h.opts.Store.GetTriggerWebhookCredentials(ctx, triggerID)
	if err != nil || creds == nil {
		return nil, cfg, false
	}

	if token := r.PathValue("token"); token != "" {
		return trigger, cfg, triggers.WebhookTokenMatches(creds.TokenHash, token)
	}
	if token, ok := bearerToken(r.Header); ok {
		return trigger, cfg, triggers.WebhookTokenMatches(creds.TokenHash, token)
	}
	if cfg.HMAC != nil && len(creds.SecretSealed) > 0 && h.opts.Opener != nil {
		secret, err := h.opts.Opener.Open(ctx, vault.UserTenant(trigger.UserID), creds.SecretSealed, triggers.WebhookSecretAAD(trigger.ID))
		if err != nil {
			logging.Error("webhook trigger secret does not open", "trigger_id", triggerID, "error", err)
			return nil, cfg, false
		}
		defer clear(secret)
		return trigger, cfg, VerifyHMAC(cfg.HMAC, secret, body, r.Header.Get(hmacHeader(cfg.HMAC)))
	}
	return nil, cfg, false
}

func bearerToken(header http.Header) (string, bool) {
	auth := header.Get("Authorization")
	scheme, token, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

func hmacHeader(cfg *core.WebhookHMACConfig) string {
	if cfg.Header == "" {
		return "X-Signature-256"
	}
	return cfg.Header
}

// VerifyHMAC checks a signature header value against an HMAC of body under
// secret, in constant time. Exported for providers whose signature is a plain
// body HMAC.
func VerifyHMAC(cfg *core.WebhookHMACConfig, secret, body []byte, signature string) bool {
	if len(secret) == 0 || signature == "" {
		return false
	}
	if cfg.Prefix != "" {
		if !strings.HasPrefix(signature, cfg.Prefix) {
			return false
		}
		signature = strings.TrimPrefix(signature, cfg.Prefix)
	}
	var newHash func() hash.Hash
	switch cfg.Algorithm {
	case "", "sha256":
		newHash = sha256.New
	case "sha1":
		newHash = sha1.New
	case "sha512":
		newHash = sha512.New
	default:
		return false
	}
	var got []byte
	var err error
	if cfg.Encoding == "base64" {
		got, err = base64.StdEncoding.DecodeString(signature)
	} else {
		got, err = hex.DecodeString(strings.ToLower(signature))
	}
	if err != nil {
		return false
	}
	mac := hmac.New(newHash, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// deliveryKey is the delivery's dedupe key within its trigger: the sender's
// idempotency key when it gives one, else the body's hash within a
// one-minute bucket — so a sender retrying the same body is one event, and
// the same body sent again later is another.
func deliveryKey(header http.Header, body []byte, now time.Time) string {
	for _, name := range idempotencyHeaders {
		if key := strings.TrimSpace(header.Get(name)); key != "" {
			if len(key) > maxIdempotencyKey {
				sum := sha256.Sum256([]byte(key))
				key = hex.EncodeToString(sum[:])
			}
			return "key:" + key
		}
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("body:%s:%d", hex.EncodeToString(sum[:16]), now.Truncate(bodyBucket).Unix())
}
