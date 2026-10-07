// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/vault"
)

const (
	twAccountA = "ACaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	twAccountB = "ACbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	twTokenA   = "auth-token-account-a"
	twTokenB   = "auth-token-account-b"
	twPublic   = "https://reliant.example.com"
	twEvents   = twPublic + "/integrations/twilio/events"
)

// twilioSign is Twilio's signature, computed independently of the provider:
// base64 HMAC-SHA1 under the Auth Token of the URL followed by every POST
// parameter, sorted by name, as name+value with no delimiters
// (twilio.com/docs/usage/webhooks/webhooks-security).
func twilioSign(token, rawURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(rawURL)
	for _, k := range keys {
		for _, v := range form[k] {
			b.WriteString(k + v)
		}
	}
	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Twilio's documented example: AuthToken 12345, a URL with a query string,
// five POST params. The provider's signature must equal the one in the docs.
func TestTwilioSignatureMatchesTwilioDocumentedExample(t *testing.T) {
	form := url.Values{
		"Digits": {"1234"}, "To": {"+18005551212"}, "From": {"+14158675310"},
		"Caller": {"+14158675310"}, "CallSid": {"CA1234567890ABCDE"},
	}
	u, _ := url.Parse("https://example.com/myapp.php?foo=1&bar=2")
	assert.Equal(t, "L/OH5YylLD5NRKLltdqwSvS0BnU=", twilioSignature([]byte("12345"), u.String(), form))
	assert.Equal(t, "L/OH5YylLD5NRKLltdqwSvS0BnU=", twilioSign("12345", u.String(), form))
}

func inboundSMS(account, sid, from, to, body string) url.Values {
	return url.Values{
		"ToCountry": {"US"}, "SmsMessageSid": {sid}, "NumMedia": {"0"}, "SmsSid": {sid}, "SmsStatus": {"received"},
		"Body": {body}, "To": {to}, "NumSegments": {"1"}, "MessageSid": {sid}, "AccountSid": {account},
		"From": {from}, "ApiVersion": {"2010-04-01"}, "FromCity": {"SAN FRANCISCO"}, "FromCountry": {"US"},
	}
}

// fakeSecrets is the connection-secret lookup: each connection's Auth Token,
// readable only for that connection's owner.
type fakeSecrets struct {
	tokens map[string]string // connection id -> token
	owners map[string]string // connection id -> user id
	asked  []string
}

func (f *fakeSecrets) Token(_ context.Context, userID, connectionID string) (vault.Secret, error) {
	f.asked = append(f.asked, userID+"/"+connectionID)
	if f.owners[connectionID] != userID {
		return vault.Secret{}, errors.New("not found")
	}
	tok, ok := f.tokens[connectionID]
	if !ok {
		return vault.Secret{}, errors.New("no secret")
	}
	// A basic credential opens as "username\x00password".
	return vault.NewSecret([]byte("AC-user\x00" + tok)), nil
}

type twilioEnv struct {
	store   *fakeStore
	intake  *fakeIntake
	secrets *fakeSecrets
	handler http.Handler
}

func newTwilioEnv(t *testing.T) *twilioEnv {
	t.Helper()
	store := newFakeStore()
	intake := &fakeIntake{}
	secrets := &fakeSecrets{tokens: map[string]string{}, owners: map[string]string{}}
	registry := NewRegistry()
	require.NoError(t, registry.Register(NewTwilioProvider()))
	receiver := NewEventsReceiver(EventsOptions{
		Store: store, Intake: intake, Registry: registry, PublicURL: twPublic, ConnectionSecrets: secrets,
	})
	mux := http.NewServeMux()
	receiver.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })
	return &twilioEnv{store: store, intake: intake, secrets: secrets, handler: mux}
}

// connect gives userID a Twilio connection to account with token, and a
// message.received trigger on it (optionally narrowed by match).
func (e *twilioEnv) connect(t *testing.T, triggerID, userID, connID, account, token string, match map[string]string) {
	t.Helper()
	cfg := core.IntegrationConfig{Integration: TwilioProviderID, Events: []string{"message.received"}, Match: match}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	conn := connID
	e.store.routes[TwilioProviderID] = append(e.store.routes[TwilioProviderID], &core.IntegrationTriggerRoute{
		Trigger:           &core.Trigger{ID: triggerID, UserID: userID, Kind: core.TriggerKindIntegration, Enabled: true, Config: raw, ConnectionID: &conn},
		ConnectionAccount: account, ConnectionStatus: core.ConnectionStatusActive,
	})
	e.secrets.tokens[connID] = token
	e.secrets.owners[connID] = userID
}

// deliver POSTs form to the receiver as Twilio would, signed with token over
// signedURL; the request itself arrives at an internal address.
func (e *twilioEnv) deliver(t *testing.T, form url.Values, token, signedURL string) *httptest.ResponseRecorder {
	t.Helper()
	target := "http://10.0.0.7:8080/integrations/twilio/events"
	if u, _ := url.Parse(signedURL); u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", twilioSign(token, signedURL, form))
	req.Header.Set("I-Twilio-Idempotency-Token", "idem-1")
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestTwilioVerifiesAgainstThePublicURL(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)

	form := inboundSMS(twAccountA, "SM0001", "+15551230000", "+15559870000", "hello there")
	rec := env.deliver(t, form, twTokenA, twEvents)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "text/xml", rec.Header().Get("Content-Type"))
	assert.Equal(t, `<?xml version="1.0" encoding="UTF-8"?><Response></Response>`, rec.Body.String(),
		"empty TwiML: Twilio sends no auto-reply and logs no error")

	all := env.intake.all()
	require.Len(t, all, 1)
	ev := all[0]
	assert.Equal(t, "alice-sms", ev.TriggerID)
	assert.Equal(t, "alice-sms:SM0001", ev.Event.DedupeKey)
	assert.Equal(t, "message.received", ev.Event.Payload["event"])
	assert.Equal(t, twAccountA, ev.Event.Payload["account"])
	assert.Equal(t, map[string]any{"from": "+15551230000", "to": "+15559870000", "channel": "sms", "num_media": "0"},
		ev.Event.Payload["attributes"])
	data := ev.Event.Payload["data"].(map[string]any)
	assert.Equal(t, "hello there", data["body"])
	assert.Equal(t, "SM0001", data["message_sid"])
	assert.Equal(t, "SAN FRANCISCO", data["from_city"])
	assert.Equal(t, []any{}, data["media"])
}

// Twilio's validators accept the URL with and without the default port,
// because Twilio signs inconsistently; so must we.
func TestTwilioAcceptsTheDefaultPortVariant(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	rec := env.deliver(t, inboundSMS(twAccountA, "SM0443", "+15551230000", "+15559870000", "x"),
		twTokenA, "https://reliant.example.com:443/integrations/twilio/events")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, env.intake.all(), 1)
}

// A query string on the configured URL is part of what Twilio signs.
func TestTwilioSignsTheQueryString(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	rec := env.deliver(t, inboundSMS(twAccountA, "SMq", "+15551230000", "+15559870000", "x"),
		twTokenA, twEvents+"?number=main&v=1")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, env.intake.all(), 1)
}

func TestTwilioRejectsABadSignature(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	form := inboundSMS(twAccountA, "SMbad", "+15551230000", "+15559870000", "x")

	rec := env.deliver(t, form, "not-the-token", twEvents)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// A valid signature over different params (the body was altered).
	req := httptest.NewRequest(http.MethodPost, "http://10.0.0.7:8080/integrations/twilio/events", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	altered := inboundSMS(twAccountA, "SMbad", "+15551230000", "+15559870000", "different body")
	req.Header.Set("X-Twilio-Signature", twilioSign(twTokenA, twEvents, altered))
	rec2 := httptest.NewRecorder()
	env.handler.ServeHTTP(rec2, req)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)

	// No signature at all.
	req3 := httptest.NewRequest(http.MethodPost, "http://10.0.0.7:8080/integrations/twilio/events", strings.NewReader(form.Encode()))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec3 := httptest.NewRecorder()
	env.handler.ServeHTTP(rec3, req3)
	assert.Equal(t, http.StatusUnauthorized, rec3.Code)

	assert.Empty(t, env.intake.all())
}

// Behind the ingress the request's own URL is internal. A signature over
// that URL is not what Twilio signs, so it must not verify — this is what
// proves the receiver checks against PUBLIC_URL.
func TestTwilioRejectsASignatureOverTheInternalURL(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	rec := env.deliver(t, inboundSMS(twAccountA, "SMint", "+15551230000", "+15559870000", "x"),
		twTokenA, "http://10.0.0.7:8080/integrations/twilio/events")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, env.intake.all())
}

// An account no connection records cannot be verified by anything, and is
// refused the same way as a bad signature (nothing distinguishes "unknown
// account" from "wrong key" to a caller).
func TestTwilioRejectsAnAccountNoConnectionRecords(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	rec := env.deliver(t, inboundSMS(twAccountB, "SMx", "+15551230000", "+15559870000", "x"), twTokenB, twEvents)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, env.intake.all())
	assert.Empty(t, env.secrets.asked, "no secret is opened for an account nobody recorded")
}

// Twilio retries a delivery it saw fail; MessageSid is stable across those,
// so the second delivery is recorded once (the fake intake dedupes like the
// trigger_events constraint does).
func TestTwilioDedupesOnMessageSid(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	form := inboundSMS(twAccountA, "SMdup", "+15551230000", "+15559870000", "x")
	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusOK, env.deliver(t, form, twTokenA, twEvents).Code)
	}
	assert.Len(t, env.intake.all(), 1)
}

// Two users on DIFFERENT Twilio accounts each get only their own messages,
// even when both point their numbers at the same URL.
func TestTwilioRoutesEachAccountOnlyToItsOwnConnections(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	env.connect(t, "bob-sms", "bob", "conn-b", twAccountB, twTokenB, nil)

	require.Equal(t, http.StatusOK, env.deliver(t, inboundSMS(twAccountA, "SMa", "+15551230000", "+15550000001", "for alice"), twTokenA, twEvents).Code)
	require.Equal(t, http.StatusOK, env.deliver(t, inboundSMS(twAccountB, "SMb", "+15551230000", "+15550000002", "for bob"), twTokenB, twEvents).Code)

	got := map[string]string{}
	for _, a := range env.intake.all() {
		got[a.TriggerID] = a.Event.Payload["data"].(map[string]any)["body"].(string)
	}
	assert.Equal(t, map[string]string{"alice-sms": "for alice", "bob-sms": "for bob"}, got)

	// A message for B signed with A's token (Alice's token cannot speak for
	// Bob's account): refused, and nobody hears it.
	before := len(env.intake.all())
	rec := env.deliver(t, inboundSMS(twAccountB, "SMforged", "+15551230000", "+15550000002", "forged"), twTokenA, twEvents)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Len(t, env.intake.all(), before)
}

// Two users on the SAME Twilio account both receive a message, but each
// only through a trigger that matches it.
func TestTwilioSharedAccountReachesEveryMatchingTrigger(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-main", "alice", "conn-a", twAccountA, twTokenA, map[string]string{"to": "+15559870000"})
	env.connect(t, "bob-main", "bob", "conn-b", twAccountA, twTokenA, map[string]string{"to": "+15559870000"})
	env.connect(t, "bob-support", "bob", "conn-b2", twAccountA, twTokenA, map[string]string{"to": "+15550000099"})

	require.Equal(t, http.StatusOK, env.deliver(t, inboundSMS(twAccountA, "SMshared", "+15551230000", "+15559870000", "hi"), twTokenA, twEvents).Code)
	assert.Equal(t, []string{"alice-main", "bob-main"}, env.intake.triggerIDs(),
		"both users' matching triggers fire; the support number's does not")
}

// A connection whose stored token is stale (the account rotated its Auth
// Token, or the user pasted a wrong one) cannot verify the delivery, so it
// receives nothing — even though another user's current token on the same
// account verified it.
func TestTwilioRoutesOnlyThroughConnectionsWhoseTokenVerified(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	env.connect(t, "carol-sms", "carol", "conn-c", twAccountA, "rotated-away-token", nil)

	require.Equal(t, http.StatusOK, env.deliver(t, inboundSMS(twAccountA, "SMrot", "+15551230000", "+15559870000", "x"), twTokenA, twEvents).Code)
	assert.Equal(t, []string{"alice-sms"}, env.intake.triggerIDs())
}

// A WhatsApp message carries whatsapp:-prefixed addresses; channel says so,
// and media and the profile name reach the data.
func TestTwilioWhatsAppWithMedia(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-wa", "alice", "conn-a", twAccountA, twTokenA, map[string]string{"channel": "whatsapp"})
	form := url.Values{
		"MessageSid": {"MM0002"}, "AccountSid": {twAccountA}, "From": {"whatsapp:+15551230000"},
		"To": {"whatsapp:+14155238886"}, "Body": {"see attached"}, "NumMedia": {"2"}, "NumSegments": {"1"},
		"MediaUrl0":         {"https://api.twilio.com/2010-04-01/Accounts/" + twAccountA + "/Messages/MM0002/Media/ME1"},
		"MediaContentType0": {"image/jpeg"},
		"MediaUrl1":         {"https://api.twilio.com/2010-04-01/Accounts/" + twAccountA + "/Messages/MM0002/Media/ME2"},
		"MediaContentType1": {"application/pdf"},
		"ProfileName":       {"Ada"}, "WaId": {"15551230000"}, "ApiVersion": {"2010-04-01"},
	}
	require.Equal(t, http.StatusOK, env.deliver(t, form, twTokenA, twEvents).Code)
	all := env.intake.all()
	require.Len(t, all, 1)
	assert.Equal(t, map[string]any{"from": "whatsapp:+15551230000", "to": "whatsapp:+14155238886", "channel": "whatsapp", "num_media": "2"},
		all[0].Event.Payload["attributes"])
	data := all[0].Event.Payload["data"].(map[string]any)
	assert.Equal(t, "Ada", data["profile_name"])
	assert.Equal(t, "15551230000", data["wa_id"])
	assert.Equal(t, 2, data["num_media"])
	assert.Equal(t, []any{
		map[string]any{"url": form.Get("MediaUrl0"), "content_type": "image/jpeg"},
		map[string]any{"url": form.Get("MediaUrl1"), "content_type": "application/pdf"},
	}, data["media"])
}

// trigger.sender is the From number, and it is NOT verified even though the
// delivery's Twilio signature was: the signature proves Twilio delivered the
// message, not who sent it, and SMS caller id can be spoofed upstream.
func TestTwilioSenderIsTheFromNumberAndNeverVerified(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-main", "alice", "conn-a", twAccountA, twTokenA, nil)
	for sid, from := range map[string]string{"SM-sender-1": "+15551230000", "SM-sender-2": "whatsapp:+15551230001"} {
		form := url.Values{"MessageSid": {sid}, "AccountSid": {twAccountA}, "From": {from}, "To": {"+15559870000"}, "Body": {"hi"}, "ProfileName": {"Ada"}}
		require.Equal(t, http.StatusOK, env.deliver(t, form, twTokenA, twEvents).Code)
	}
	all := env.intake.all()
	require.Len(t, all, 2)
	got := map[string]core.TriggerSender{}
	for _, call := range all {
		require.NotNil(t, call.Event.Sender)
		got[call.Event.Sender.ID] = *call.Event.Sender
	}
	assert.Equal(t, map[string]core.TriggerSender{
		"+15551230000":          {Kind: core.TriggerSenderKindSMS, ID: "+15551230000", DisplayName: "Ada", Verified: false},
		"whatsapp:+15551230001": {Kind: core.TriggerSenderKindSMS, ID: "whatsapp:+15551230001", DisplayName: "Ada", Verified: false},
	}, got)
}

// A delivery that verified but routes to no trigger (a number nobody
// listens to) is still acked with empty TwiML, so Twilio logs no error.
func TestTwilioAcksAVerifiedDeliveryNoTriggerMatches(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-main", "alice", "conn-a", twAccountA, twTokenA, map[string]string{"to": "+15559870000"})
	rec := env.deliver(t, inboundSMS(twAccountA, "SMother", "+15551230000", "+15550000000", "x"), twTokenA, twEvents)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/xml", rec.Header().Get("Content-Type"))
	assert.Empty(t, env.intake.all())
}

func TestTwilioRejectsANonFormDelivery(t *testing.T) {
	env := newTwilioEnv(t)
	env.connect(t, "alice-sms", "alice", "conn-a", twAccountA, twTokenA, nil)
	req := httptest.NewRequest(http.MethodPost, "http://10.0.0.7:8080/integrations/twilio/events", strings.NewReader(`{"AccountSid":"`+twAccountA+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Twilio-Signature", "x")
	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, env.intake.all())
}

// Without the per-connection secret lookup the receiver cannot verify a
// Twilio delivery at all, and says so rather than accepting it.
func TestTwilioWithoutConnectionSecretsIsUnavailable(t *testing.T) {
	store := newFakeStore()
	registry := NewRegistry()
	require.NoError(t, registry.Register(NewTwilioProvider()))
	receiver := NewEventsReceiver(EventsOptions{Store: store, Intake: &fakeIntake{}, Registry: registry, PublicURL: twPublic})
	mux := http.NewServeMux()
	receiver.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })
	form := inboundSMS(twAccountA, "SM1", "+15551230000", "+15559870000", "x")
	req := httptest.NewRequest(http.MethodPost, "http://10.0.0.7:8080/integrations/twilio/events", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", twilioSign(twTokenA, twEvents, form))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestTwilioRegisteredFromEnvWhenThereIsAPublicURL(t *testing.T) {
	r, err := RegistryFromEnv(func(string) string { return "" })
	require.NoError(t, err)
	assert.False(t, r.HasInboundSource(TwilioProviderID), "no PUBLIC_URL: Twilio's signatures cannot be checked")

	r, err = RegistryFromEnv(func(k string) string {
		if k == "PUBLIC_URL" {
			return "https://reliant.example.com"
		}
		return ""
	})
	require.NoError(t, err)
	assert.True(t, r.HasInboundSource(TwilioProviderID))
	assert.True(t, r.UserConfiguredURL(TwilioProviderID), "each number's webhook is the user's to set")
	assert.False(t, r.UserConfiguredURL(SlackProviderID))
}

// Every event the provider emits is declared by a trigger of the shipped
// twilio manifest, carries every attribute that trigger declares, and has a
// payload that validates against the trigger's payload schema.
func TestTwilioEventsMatchTheShippedManifest(t *testing.T) {
	resolved, err := catalog.MustBuiltin().Resolve("twilio/message.send@1")
	require.NoError(t, err)
	m := resolved.Manifest
	spec, ok := manifest.Trigger(m, "message.received")
	require.True(t, ok)

	p := NewTwilioProvider()
	for _, form := range []url.Values{
		inboundSMS(twAccountA, "SM1", "+15551230000", "+15559870000", "hi"),
		{"MessageSid": {"MM2"}, "AccountSid": {twAccountA}, "From": {"whatsapp:+1555"}, "To": {"whatsapp:+1415"},
			"Body": {""}, "NumMedia": {"1"}, "MediaUrl0": {"https://api.twilio.com/m"}, "MediaContentType0": {"image/png"},
			"ProfileName": {"Ada"}, "WaId": {"1555"}, "Latitude": {"51.5"}, "Longitude": {"-0.1"}, "ButtonText": {"Yes"}},
	} {
		d, err := p.Parse(context.Background(), &Request{Form: form, Header: http.Header{}})
		require.NoError(t, err)
		require.Len(t, d.Events, 1)
		ev := d.Events[0]
		assert.Contains(t, spec.GetEvents(), ev.Type)
		var want, got []string
		for _, a := range spec.GetAttributes() {
			want = append(want, a.GetName())
		}
		for k := range ev.Attributes {
			got = append(got, k)
		}
		assert.ElementsMatch(t, want, got, "the event carries exactly the declared attributes")

		// The recorded payload validates against the trigger's schema.
		payload := toInbound(TwilioProviderID, &core.Trigger{ID: "t"}, ev).Payload
		rawSchema, err := json.Marshal(manifest.TriggerPayloadSchema(m, spec))
		require.NoError(t, err)
		var schema jsonschema.Schema
		require.NoError(t, json.Unmarshal(rawSchema, &schema))
		resolvedSchema, err := schema.Resolve(nil)
		require.NoError(t, err)
		raw, _ := json.Marshal(payload)
		var asJSON any
		require.NoError(t, json.Unmarshal(raw, &asJSON))
		assert.NoError(t, resolvedSchema.Validate(asJSON))
	}
}
