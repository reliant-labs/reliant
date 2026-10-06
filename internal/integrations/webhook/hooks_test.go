// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
	"github.com/reliant-labs/reliant/internal/vault"
)

func (fakeOpener) Open(_ context.Context, tenant vault.Tenant, ciphertext, aad []byte) ([]byte, error) {
	parts := strings.SplitN(string(ciphertext), "|", 3)
	if len(parts) != 3 || parts[0] != tenant.ID || parts[1] != string(aad) {
		return nil, assert.AnError
	}
	return []byte(parts[2]), nil
}

const hookToken = "whk_correct-horse-battery-staple-0123456789abcdef"

type hooksEnv struct {
	store   *fakeStore
	intake  *fakeIntake
	handler http.Handler
	trigger *core.Trigger
	now     time.Time
}

func newHooksEnv(t *testing.T, cfg core.WebhookConfig, secret string) *hooksEnv {
	t.Helper()
	store := newFakeStore()
	intake := &fakeIntake{}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	trigger := &core.Trigger{ID: "trig-1", UserID: "user-1", Kind: core.TriggerKindWebhook, Enabled: true, Config: raw}
	store.triggers[trigger.ID] = trigger
	creds := &core.TriggerWebhookCredentials{TokenHash: triggers.HashWebhookToken(hookToken)}
	if secret != "" {
		creds.SecretSealed = fakeSeal(trigger.UserID, trigger.ID, secret)
	}
	store.creds[trigger.ID] = creds

	env := &hooksEnv{store: store, intake: intake, trigger: trigger, now: time.Date(2026, 5, 6, 7, 8, 30, 0, time.UTC)}
	receiver := NewHooksReceiver(HooksOptions{Store: store, Intake: intake, Opener: fakeOpener{}})
	receiver.now = func() time.Time { return env.now }
	mux := http.NewServeMux()
	receiver.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })
	env.handler = mux
	return env
}

func (e *hooksEnv) post(path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func hexHMAC(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func TestHooksPathTokenIsAccepted(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	rec := env.post("/hooks/trig-1/"+hookToken, `{"action":"deploy"}`, nil)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	got := env.intake.all()
	require.Len(t, got, 1)
	assert.Equal(t, "trig-1", got[0].TriggerID)
	assert.Equal(t, core.TriggerEventKindWebhook, got[0].Event.Kind)
	body, _ := got[0].Event.Payload["body"].(map[string]any)
	assert.Equal(t, "deploy", body["action"], "the JSON body becomes trigger.payload.body")
}

// A webhook names no person, so its trigger.sender is the trigger itself:
// verified means the caller held its credential, whichever one it used. A
// "sender" in the body is the caller's own claim and changes nothing.
func TestHooksSenderIsTheTriggerWhoseCredentialWasHeld(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{HMAC: &core.WebhookHMACConfig{}}, "s3cret")
	env.trigger.Name = "deploys"
	body := `{"sender":{"id":"U123","verified":true}}`
	for _, rec := range []*httptest.ResponseRecorder{
		env.post("/hooks/trig-1/"+hookToken, body, map[string]string{"Idempotency-Key": "path"}),
		env.post("/hooks/trig-1", body, map[string]string{"Idempotency-Key": "bearer", "Authorization": "Bearer " + hookToken}),
		env.post("/hooks/trig-1", body, map[string]string{"Idempotency-Key": "hmac", "X-Signature-256": hexHMAC("s3cret", body)}),
	} {
		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	}
	got := env.intake.all()
	require.Len(t, got, 3)
	for _, call := range got {
		assert.Equal(t, &core.TriggerSender{Kind: core.TriggerSenderKindWebhook, ID: "trig-1", DisplayName: "deploys", Verified: true}, call.Event.Sender)
	}
}

func TestHooksBearerTokenIsAccepted(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	rec := env.post("/hooks/trig-1", `{}`, map[string]string{"Authorization": "Bearer " + hookToken})
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Len(t, env.intake.all(), 1)
}

func TestHooksRejectsWrongOrMissingCredentials(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	cases := map[string]struct {
		path    string
		headers map[string]string
	}{
		"wrong path token":    {"/hooks/trig-1/whk_wrong", nil},
		"no credential":       {"/hooks/trig-1", nil},
		"wrong bearer":        {"/hooks/trig-1", map[string]string{"Authorization": "Bearer whk_wrong"}},
		"basic is not bearer": {"/hooks/trig-1", map[string]string{"Authorization": "Basic " + hookToken}},
		"unknown trigger":     {"/hooks/nope/" + hookToken, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := env.post(c.path, `{}`, c.headers)
			assert.Equal(t, http.StatusNotFound, rec.Code,
				"every failure looks the same, so the endpoint does not reveal which trigger ids exist")
		})
	}
	assert.Empty(t, env.intake.all())
}

func TestHooksRejectsAnotherTriggersToken(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	other := &core.Trigger{ID: "trig-2", UserID: "user-2", Kind: core.TriggerKindWebhook, Enabled: true, Config: []byte(`{}`)}
	env.store.triggers[other.ID] = other
	env.store.creds[other.ID] = &core.TriggerWebhookCredentials{TokenHash: triggers.HashWebhookToken("whk_other")}

	rec := env.post("/hooks/trig-2/"+hookToken, `{}`, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Empty(t, env.intake.all())
}

func TestHooksRejectsNonWebhookAndDisabledTriggers(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	env.store.triggers["trig-1"].Kind = core.TriggerKindSchedule
	assert.Equal(t, http.StatusNotFound, env.post("/hooks/trig-1/"+hookToken, `{}`, nil).Code)

	env.store.triggers["trig-1"].Kind = core.TriggerKindWebhook
	env.store.triggers["trig-1"].Enabled = false
	rec := env.post("/hooks/trig-1/"+hookToken, `{}`, nil)
	assert.Equal(t, http.StatusConflict, rec.Code, "a disabled trigger is a real answer to an authenticated sender")
	assert.Empty(t, env.intake.all())
}

func TestHooksHMACIsAccepted(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{HMAC: &core.WebhookHMACConfig{
		Header: "X-Hub-Signature-256", Prefix: "sha256=",
	}}, "topsecret")
	body := `{"ref":"refs/heads/main"}`

	rec := env.post("/hooks/trig-1", body, map[string]string{"X-Hub-Signature-256": "sha256=" + hexHMAC("topsecret", body)})
	assert.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	require.Len(t, env.intake.all(), 1)
	headers, _ := env.intake.all()[0].Event.Payload["headers"].(map[string]any)
	assert.NotContains(t, headers, "X-Hub-Signature-256", "the signature is stripped from the stored payload")
}

func TestHooksHMACRejectsABadOrMissingSignature(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{HMAC: &core.WebhookHMACConfig{Header: "X-Signature-256"}}, "topsecret")
	body := `{"a":1}`
	cases := map[string]string{
		"wrong secret":   hexHMAC("other", body),
		"tampered body":  hexHMAC("topsecret", `{"a":2}`),
		"not hex":        "zz-not-hex",
		"empty":          "",
		"wrong length":   hexHMAC("topsecret", body)[:20],
		"prefix not set": "sha256=" + hexHMAC("topsecret", body),
	}
	for name, sig := range cases {
		t.Run(name, func(t *testing.T) {
			rec := env.post("/hooks/trig-1", body, map[string]string{"X-Signature-256": sig})
			assert.Equal(t, http.StatusNotFound, rec.Code)
		})
	}
	assert.Empty(t, env.intake.all())
}

func TestHooksHMACVariants(t *testing.T) {
	body := `{"x":true}`
	sha1Sig := func() string {
		m := hmac.New(sha1.New, []byte("k"))
		m.Write([]byte(body))
		return base64.StdEncoding.EncodeToString(m.Sum(nil))
	}()
	env := newHooksEnv(t, core.WebhookConfig{HMAC: &core.WebhookHMACConfig{
		Header: "X-Sig", Algorithm: "sha1", Encoding: "base64",
	}}, "k")
	assert.Equal(t, http.StatusAccepted, env.post("/hooks/trig-1", body, map[string]string{"X-Sig": sha1Sig}).Code)
}

// HMAC is an ADDITIONAL way in: a trigger with a signing secret still
// accepts its token, because a sender like Zapier cannot sign.
func TestHooksTokenStillWorksWhenHMACIsConfigured(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{HMAC: &core.WebhookHMACConfig{}}, "topsecret")
	assert.Equal(t, http.StatusAccepted, env.post("/hooks/trig-1/"+hookToken, `{}`, nil).Code)
}

func TestHooksDedupeOnIdempotencyKey(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	for i := 0; i < 2; i++ {
		rec := env.post("/hooks/trig-1/"+hookToken, `{"n":1}`, map[string]string{"Idempotency-Key": "abc"})
		assert.Equal(t, http.StatusAccepted, rec.Code)
	}
	got := env.intake.all()
	require.Len(t, got, 1, "a redelivery with the same Idempotency-Key is one event")
	assert.Equal(t, "trig-1:key:abc", got[0].Event.DedupeKey)

	env.post("/hooks/trig-1/"+hookToken, `{"n":1}`, map[string]string{"X-Request-Id": "xyz"})
	assert.Len(t, env.intake.all(), 2, "X-Request-Id is a key too")
}

func TestHooksDedupeWithoutAKeyIsBodyHashPerMinute(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	env.post("/hooks/trig-1/"+hookToken, `{"n":1}`, nil)
	env.now = env.now.Add(10 * time.Second)
	env.post("/hooks/trig-1/"+hookToken, `{"n":1}`, nil)
	assert.Len(t, env.intake.all(), 1, "the same body within the minute is a retry")

	env.post("/hooks/trig-1/"+hookToken, `{"n":2}`, nil)
	assert.Len(t, env.intake.all(), 2, "a different body is a different event")

	env.now = env.now.Add(time.Minute)
	env.post("/hooks/trig-1/"+hookToken, `{"n":1}`, nil)
	assert.Len(t, env.intake.all(), 3, "the same body a minute later is a new event")
}

func TestHooksRejectsAnOversizedBody(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	big := `{"x":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	rec := env.post("/hooks/trig-1/"+hookToken, big, nil)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Empty(t, env.intake.all())
}

func TestHooksStoredPayloadNeverCarriesSecrets(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	req := httptest.NewRequest(http.MethodPost, "/hooks/trig-1?token=leak&page=2", strings.NewReader("plain text body"))
	req.Header.Set("Authorization", "Bearer "+hookToken)
	req.Header.Set("Cookie", "session=1")
	req.Header.Set("X-Api-Key", "k")
	req.Header.Set("X-Custom", "kept")
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())

	payload := env.intake.all()[0].Event.Payload
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), hookToken)
	assert.NotContains(t, string(encoded), "session=1")
	assert.NotContains(t, string(encoded), "leak")
	headers := payload["headers"].(map[string]any)
	assert.Equal(t, "kept", headers["X-Custom"])
	assert.NotContains(t, headers, "X-Api-Key")
	assert.Equal(t, "plain text body", payload["body"], "a non-JSON body is kept as text")
	assert.Equal(t, "2", payload["query"].(map[string]any)["page"])
}

func TestHooksPathTokenIsNotInTheStoredPayloadOrResponse(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	rec := env.post("/hooks/trig-1/"+hookToken, `{}`, nil)
	assert.NotContains(t, rec.Body.String(), hookToken)
	encoded, _ := json.Marshal(env.intake.all()[0].Event.Payload)
	assert.NotContains(t, string(encoded), hookToken)
}

func TestHooksOnlyAcceptsPOST(t *testing.T) {
	env := newHooksEnv(t, core.WebhookConfig{}, "")
	req := httptest.NewRequest(http.MethodGet, "/hooks/trig-1/"+hookToken, nil)
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}
