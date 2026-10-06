// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
)

const slackSecret = "8f742231b10e8888abcd99yyyzzz85a5"

// signSlack is what Slack puts in X-Slack-Signature: v0= and the hex
// HMAC-SHA256 of "v0:<timestamp>:<raw body>" under the signing secret.
func signSlack(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// slackRequest is a signed Slack delivery received at now.
func slackRequest(t *testing.T, body string, now time.Time, mutate ...func(h http.Header)) *Request {
	t.Helper()
	ts := strconv.FormatInt(now.Unix(), 10)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("X-Slack-Request-Timestamp", ts)
	h.Set("X-Slack-Signature", signSlack(slackSecret, ts, []byte(body)))
	for _, m := range mutate {
		m(h)
	}
	return &Request{Method: http.MethodPost, Header: h, Body: []byte(body), ReceivedAt: now}
}

// envelope wraps an inner event the way the Events API delivers it.
func envelope(team, eventID string, event string) string {
	return `{"token":"deprecated-verification-token","team_id":"` + team + `","api_app_id":"A0APP",` +
		`"type":"event_callback","event_id":"` + eventID + `","event_time":1700000000,` +
		`"event":` + event + `,` +
		`"authorizations":[{"team_id":"` + team + `","user_id":"U0BOT","is_bot":true}]}`
}

func TestSlackVerifyAcceptsAValidSignature(t *testing.T) {
	p := NewSlackProvider(slackSecret)
	now := time.Unix(1700000100, 0)
	body := envelope("T0ACME", "Ev1", `{"type":"app_mention","user":"U1","text":"<@U0BOT> hi","ts":"1700000000.000100","channel":"C0GEN","event_ts":"1700000000.000100"}`)
	require.NoError(t, p.Verify(context.Background(), slackRequest(t, body, now)))
}

func TestSlackVerifyRejectsABadSignature(t *testing.T) {
	p := NewSlackProvider(slackSecret)
	now := time.Unix(1700000100, 0)
	body := envelope("T0ACME", "Ev1", `{"type":"app_mention","channel":"C0GEN","ts":"1.2"}`)
	cases := map[string]func(h http.Header){
		"wrong secret": func(h http.Header) {
			h.Set("X-Slack-Signature", signSlack("not-the-secret", h.Get("X-Slack-Request-Timestamp"), []byte(body)))
		},
		"tampered body": func(h http.Header) {
			h.Set("X-Slack-Signature", signSlack(slackSecret, h.Get("X-Slack-Request-Timestamp"), []byte(body+" ")))
		},
		"signed for another timestamp": func(h http.Header) {
			h.Set("X-Slack-Signature", signSlack(slackSecret, "1700000099", []byte(body)))
		},
		"missing signature": func(h http.Header) { h.Del("X-Slack-Signature") },
		"no v0= prefix": func(h http.Header) {
			h.Set("X-Slack-Signature", strings.TrimPrefix(h.Get("X-Slack-Signature"), "v0="))
		},
		"other version": func(h http.Header) {
			h.Set("X-Slack-Signature", "v1="+strings.TrimPrefix(h.Get("X-Slack-Signature"), "v0="))
		},
		"not hex":           func(h http.Header) { h.Set("X-Slack-Signature", "v0=zz") },
		"missing timestamp": func(h http.Header) { h.Del("X-Slack-Request-Timestamp") },
		"garbage timestamp": func(h http.Header) { h.Set("X-Slack-Request-Timestamp", "soon") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			err := p.Verify(context.Background(), slackRequest(t, body, now, mutate))
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnauthorized), "%s must be unauthorized, got %v", name, err)
		})
	}
}

// A correctly signed delivery older (or newer) than five minutes is a replay.
func TestSlackVerifyRejectsAStaleTimestamp(t *testing.T) {
	p := NewSlackProvider(slackSecret)
	body := envelope("T0ACME", "Ev1", `{"type":"app_mention","channel":"C0GEN","ts":"1.2"}`)
	sent := time.Unix(1700000000, 0)
	for _, tc := range []struct {
		name string
		at   time.Time
		ok   bool
	}{
		{"just now", sent.Add(2 * time.Second), true},
		{"4m59s late", sent.Add(4*time.Minute + 59*time.Second), true},
		{"5m01s late", sent.Add(5*time.Minute + time.Second), false},
		{"an hour late", sent.Add(time.Hour), false},
		{"6m in the future", sent.Add(-6 * time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := slackRequest(t, body, sent)
			req.ReceivedAt = tc.at
			err := p.Verify(context.Background(), req)
			if tc.ok {
				assert.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrUnauthorized)
			}
		})
	}
}

// url_verification is a signed handshake: echo the challenge, record nothing.
func TestSlackURLVerificationEchoesTheChallenge(t *testing.T) {
	p := NewSlackProvider(slackSecret)
	now := time.Unix(1700000100, 0)
	body := `{"token":"Jhj5dZrVaK7ZwHHjRyZWjbDl","challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P","type":"url_verification"}`
	req := slackRequest(t, body, now)
	require.NoError(t, p.Verify(context.Background(), req))
	d, err := p.Parse(context.Background(), req)
	require.NoError(t, err)
	require.NotNil(t, d.Respond)
	assert.Empty(t, d.Events)
	assert.Equal(t, "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P", string(d.Respond.Body))
	assert.Equal(t, "text/plain; charset=utf-8", d.Respond.ContentType)
}

func parseOne(t *testing.T, body string) Event {
	t.Helper()
	p := NewSlackProvider(slackSecret)
	d, err := p.Parse(context.Background(), slackRequest(t, body, time.Unix(1700000100, 0)))
	require.NoError(t, err)
	require.Nil(t, d.Respond)
	require.Len(t, d.Events, 1)
	return d.Events[0]
}

func parseNone(t *testing.T, body string) {
	t.Helper()
	p := NewSlackProvider(slackSecret)
	d, err := p.Parse(context.Background(), slackRequest(t, body, time.Unix(1700000100, 0)))
	require.NoError(t, err)
	require.Nil(t, d.Respond)
	assert.Empty(t, d.Events)
}

func TestSlackParsesAChannelMessage(t *testing.T) {
	ev := parseOne(t, envelope("T0ACME", "Ev0MSG1", `{"type":"message","channel":"C0GEN","user":"U0ADA",
	  "text":"deploy please","ts":"1700000000.000100","channel_type":"channel","event_ts":"1700000000.000100"}`))
	assert.Equal(t, "message.channels", ev.Type)
	assert.Equal(t, "T0ACME", ev.AccountKey, "events route on the team id")
	assert.Equal(t, "Ev0MSG1", ev.DeliveryID, "dedupe is on event_id")
	assert.Equal(t, time.Unix(1700000000, 0).UTC(), ev.OccurredAt.UTC())
	assert.Equal(t, map[string]string{
		"channel": "C0GEN", "channel_type": "channel", "user": "U0ADA", "thread_ts": "", "subtype": "",
	}, ev.Attributes)
	assert.Equal(t, "deploy please", ev.Data["text"])
	assert.Equal(t, "C0GEN", ev.Data["channel"])
	assert.Equal(t, "1700000000.000100", ev.Data["ts"])
	assert.Equal(t, "T0ACME", ev.Data["team_id"], "the envelope's routing facts ride along")
	assert.Equal(t, "Ev0MSG1", ev.Data["event_id"])
	_, hasToken := ev.Data["token"]
	assert.False(t, hasToken, "the deprecated verification token is never recorded")
}

func TestSlackMessageTypeFollowsTheConversation(t *testing.T) {
	for channelType, want := range map[string]string{
		"channel": "message.channels", "group": "message.groups", "im": "message.im", "mpim": "message.mpim",
	} {
		ev := parseOne(t, envelope("T0ACME", "Ev-"+channelType, `{"type":"message","channel":"D0X","user":"U0ADA","text":"x",
		  "ts":"1700000000.000100","channel_type":"`+channelType+`"}`))
		assert.Equal(t, want, ev.Type, channelType)
		assert.Equal(t, channelType, ev.Attributes["channel_type"])
	}
}

func TestSlackThreadReplyCarriesThreadTS(t *testing.T) {
	ev := parseOne(t, envelope("T0ACME", "Ev2", `{"type":"message","channel":"C0GEN","user":"U0ADA","text":"in thread",
	  "ts":"1700000005.000200","thread_ts":"1700000000.000100","parent_user_id":"U0BOB","channel_type":"channel"}`))
	assert.Equal(t, "1700000000.000100", ev.Attributes["thread_ts"])
	assert.Equal(t, "1700000000.000100", ev.Data["thread_ts"])
}

// Bot messages — including the Reliant bot's own posts, which come straight
// back as message events — never fire, so a trigger that posts cannot loop.
func TestSlackIgnoresBotMessagesAndEdits(t *testing.T) {
	for name, event := range map[string]string{
		"bot_id":          `{"type":"message","channel":"C0GEN","bot_id":"B0BOT","user":"U0BOT","text":"done","ts":"1.1","channel_type":"channel"}`,
		"bot_message":     `{"type":"message","subtype":"bot_message","channel":"C0GEN","bot_id":"B0X","text":"hook","ts":"1.1","channel_type":"channel"}`,
		"bot_profile":     `{"type":"message","channel":"C0GEN","user":"U0APP","bot_profile":{"id":"B0Z"},"text":"x","ts":"1.1","channel_type":"channel"}`,
		"message_changed": `{"type":"message","subtype":"message_changed","channel":"C0GEN","hidden":true,"message":{"text":"edit","user":"U0ADA"},"ts":"1.2","channel_type":"channel"}`,
		"message_deleted": `{"type":"message","subtype":"message_deleted","channel":"C0GEN","hidden":true,"deleted_ts":"1.1","ts":"1.3","channel_type":"channel"}`,
		"message_replied": `{"type":"message","subtype":"message_replied","channel":"C0GEN","hidden":true,"message":{},"ts":"1.4","channel_type":"channel"}`,
	} {
		t.Run(name, func(t *testing.T) {
			parseNone(t, envelope("T0ACME", "Ev-"+name, event))
		})
	}
}

// A human message with a subtype (a file share, a /me) is still a post; the
// subtype is an attribute a source can match.
func TestSlackKeepsHumanSubtypes(t *testing.T) {
	ev := parseOne(t, envelope("T0ACME", "Ev3", `{"type":"message","subtype":"file_share","channel":"C0GEN","user":"U0ADA",
	  "text":"the report","ts":"1700000000.000100","channel_type":"channel","files":[{"id":"F1","name":"r.pdf"}]}`))
	assert.Equal(t, "file_share", ev.Attributes["subtype"])
	assert.Equal(t, "message.channels", ev.Type)
}

func TestSlackParsesAnAppMention(t *testing.T) {
	ev := parseOne(t, envelope("T0ACME", "Ev0MENTION", `{"type":"app_mention","user":"U0ADA","text":"<@U0BOT> summarize this",
	  "ts":"1700000000.000100","channel":"C0GEN","event_ts":"1700000000.000100"}`))
	assert.Equal(t, "app_mention", ev.Type)
	assert.Equal(t, "T0ACME", ev.AccountKey)
	assert.Equal(t, "Ev0MENTION", ev.DeliveryID)
	assert.Equal(t, map[string]string{"channel": "C0GEN", "user": "U0ADA", "thread_ts": ""}, ev.Attributes)
	assert.Equal(t, "<@U0BOT> summarize this", ev.Data["text"])
}

func TestSlackParsesAReaction(t *testing.T) {
	ev := parseOne(t, envelope("T0ACME", "Ev0REACT", `{"type":"reaction_added","user":"U0ADA","reaction":"white_check_mark",
	  "item_user":"U0BOB","item":{"type":"message","channel":"C0GEN","ts":"1700000000.000100"},"event_ts":"1700000010.000300"}`))
	assert.Equal(t, "reaction_added", ev.Type)
	assert.Equal(t, map[string]string{"channel": "C0GEN", "user": "U0ADA", "reaction": "white_check_mark", "item_user": "U0BOB"}, ev.Attributes)
	assert.Equal(t, "1700000000.000100", ev.Data["item"].(map[string]any)["ts"])
}

// trigger.sender is the event's own `user` — the poster, the mentioner, the
// reactor — from a body whose signature Verify checked, so it is verified.
// It is never the envelope's authorizations (the bot) or an item_user.
func TestSlackSenderIsTheEventsUser(t *testing.T) {
	for name, tc := range map[string]struct {
		event string
		want  core.TriggerSender
	}{
		"message": {
			event: `{"type":"message","channel":"C0GEN","user":"U0ADA","text":"x","ts":"1.1","channel_type":"channel",
			  "user_profile":{"display_name":"ada","real_name":"Ada Lovelace"}}`,
			want: core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: "U0ADA", DisplayName: "ada", Verified: true},
		},
		"app_mention": {
			event: `{"type":"app_mention","user":"U0ADA","text":"<@U0BOT> hi","ts":"1.1","channel":"C0GEN"}`,
			want:  core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: "U0ADA", Verified: true},
		},
		"reaction_added": {
			event: `{"type":"reaction_added","user":"U0ADA","reaction":"eyes","item_user":"U0BOB","item":{"type":"message","channel":"C0GEN","ts":"1.1"}}`,
			want:  core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: "U0ADA", Verified: true},
		},
		"no user": {
			event: `{"type":"message","channel":"C0GEN","text":"x","ts":"1.1","channel_type":"channel"}`,
			want:  core.TriggerSender{Kind: core.TriggerSenderKindSlack, Verified: false},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ev := parseOne(t, envelope("T0ACME", "Ev-sender-"+name, tc.event))
			require.NotNil(t, ev.Sender)
			assert.Equal(t, tc.want, *ev.Sender)
		})
	}
}

// Event types no Slack trigger listens to (and envelopes that are not
// events) are acked without recording anything.
func TestSlackIgnoresOtherEvents(t *testing.T) {
	parseNone(t, envelope("T0ACME", "Ev4", `{"type":"reaction_removed","user":"U0ADA","reaction":"x","item":{"type":"message","channel":"C0GEN","ts":"1.1"}}`))
	parseNone(t, envelope("T0ACME", "Ev5", `{"type":"channel_created","channel":{"id":"C0NEW","name":"new"}}`))
	parseNone(t, `{"type":"app_rate_limited","team_id":"T0ACME","minute_rate_limited":1518467820,"api_app_id":"A0APP"}`)
}

func TestSlackRejectsAMalformedEnvelope(t *testing.T) {
	p := NewSlackProvider(slackSecret)
	now := time.Unix(1700000100, 0)
	for name, body := range map[string]string{
		"not json":      `not json`,
		"no team":       envelope("", "Ev1", `{"type":"app_mention","channel":"C0GEN","ts":"1.1"}`),
		"no event id":   envelope("T0ACME", "", `{"type":"app_mention","channel":"C0GEN","ts":"1.1"}`),
		"empty handshk": `{"type":"url_verification","challenge":""}`,
	} {
		_, err := p.Parse(context.Background(), slackRequest(t, body, now))
		assert.Error(t, err, name)
	}
}

// The stored payload is size-capped: an oversized event keeps its routing
// facts and says it was truncated rather than recording the whole body.
func TestSlackCapsTheRecordedPayload(t *testing.T) {
	huge := strings.Repeat("x", SlackMaxEventData+1)
	ev := parseOne(t, envelope("T0ACME", "Ev6", `{"type":"message","channel":"C0GEN","user":"U0ADA","text":"`+huge+`",
	  "ts":"1700000000.000100","channel_type":"channel"}`))
	raw, err := json.Marshal(ev.Data)
	require.NoError(t, err)
	assert.Less(t, len(raw), SlackMaxEventData)
	assert.Equal(t, true, ev.Data["truncated"])
	assert.Equal(t, "C0GEN", ev.Data["channel"])
	assert.Equal(t, "1700000000.000100", ev.Data["ts"])
	assert.Equal(t, "message", ev.Data["type"])
	assert.Equal(t, "C0GEN", ev.Attributes["channel"], "routing never depends on the capped data")
}

// ---------------------------------------------------------------------------
// Through the app-level receiver.
// ---------------------------------------------------------------------------

type slackEnv struct {
	store   *fakeStore
	intake  *fakeIntake
	handler http.Handler
	now     time.Time
}

func newSlackEnv(t *testing.T) *slackEnv {
	t.Helper()
	store, intake := newFakeStore(), &fakeIntake{}
	registry := NewRegistry()
	require.NoError(t, registry.Register(NewSlackProvider(slackSecret)))
	receiver := NewEventsReceiver(EventsOptions{Store: store, Intake: intake, Registry: registry, PublicURL: "https://api.example.com"})
	env := &slackEnv{store: store, intake: intake, now: time.Now()}
	receiver.now = func() time.Time { return env.now }
	mux := http.NewServeMux()
	receiver.Register(func(pattern string, h http.Handler) { mux.Handle(pattern, h) })
	env.handler = mux
	return env
}

func (e *slackEnv) route(id, userID, team string, cfg core.IntegrationConfig) {
	cfg.Integration = SlackProviderID
	raw, _ := json.Marshal(cfg)
	tr := &core.Trigger{ID: id, UserID: userID, Kind: core.TriggerKindIntegration, Enabled: true, Config: raw}
	e.store.routes[SlackProviderID] = append(e.store.routes[SlackProviderID], &core.IntegrationTriggerRoute{
		Trigger: tr, ConnectionAccount: team, ConnectionStatus: core.ConnectionStatusActive,
	})
}

func (e *slackEnv) post(t *testing.T, body string, headers ...map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	ts := strconv.FormatInt(e.now.Unix(), 10)
	req := httptest.NewRequest(http.MethodPost, "http://10.0.0.7:8080/integrations/slack/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Slack-Request-Timestamp", ts)
	req.Header.Set("X-Slack-Signature", signSlack(slackSecret, ts, []byte(body)))
	for _, h := range headers {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func TestSlackReceiverHandshake(t *testing.T) {
	env := newSlackEnv(t)
	env.route("t1", "alice", "T0ACME", core.IntegrationConfig{Events: []string{"*"}})
	rec := env.post(t, `{"token":"x","challenge":"ch-42","type":"url_verification"}`)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ch-42", rec.Body.String())
	assert.Empty(t, env.intake.all(), "a handshake writes no event")
}

// Two users connected to DIFFERENT workspaces each get only their own
// workspace's events; a third whose connection is in the same workspace as
// the first gets that one too. Slack retries (X-Slack-Retry-Num) of the same
// event_id are acked and never recorded twice.
func TestSlackReceiverRoutesByTeamAndDedupesRetries(t *testing.T) {
	env := newSlackEnv(t)
	env.route("alice-mentions", "alice", "T0ACME", core.IntegrationConfig{Events: []string{"app_mention"}})
	env.route("bob-mentions", "bob", "T0OTHER", core.IntegrationConfig{Events: []string{"app_mention"}})
	env.route("carol-mentions", "carol", "T0ACME", core.IntegrationConfig{Events: []string{"app_mention"},
		Match: map[string]string{"channel": "C0OPS"}})
	env.route("alice-messages", "alice", "T0ACME", core.IntegrationConfig{Events: []string{"message.*"}})

	mention := func(team, id, channel string) string {
		return envelope(team, id, `{"type":"app_mention","user":"U0ADA","text":"<@U0BOT> hi","ts":"1700000000.000100","channel":"`+channel+`"}`)
	}
	require.Equal(t, http.StatusOK, env.post(t, mention("T0ACME", "EvA1", "C0GEN")).Code)
	for i := 1; i <= 3; i++ {
		rec := env.post(t, mention("T0ACME", "EvA1", "C0GEN"), map[string]string{"X-Slack-Retry-Num": strconv.Itoa(i), "X-Slack-Retry-Reason": "http_timeout"})
		require.Equal(t, http.StatusOK, rec.Code)
	}
	require.Equal(t, http.StatusOK, env.post(t, mention("T0OTHER", "EvB1", "C0GEN")).Code)
	require.Equal(t, http.StatusOK, env.post(t, mention("T0ACME", "EvA2", "C0OPS")).Code)

	got := map[string][]string{}
	for _, a := range env.intake.all() {
		got[a.TriggerID] = append(got[a.TriggerID], a.Event.Payload["delivery_id"].(string))
		assert.Equal(t, "slack", a.Event.Payload["integration"])
		assert.Equal(t, "app_mention", a.Event.Payload["event"])
	}
	assert.Equal(t, map[string][]string{
		"alice-mentions": {"EvA1", "EvA2"},
		"bob-mentions":   {"EvB1"},
		"carol-mentions": {"EvA2"},
	}, got, "each workspace's events reach only its own connections' triggers; retries record once")

	// The bot's own reply in that workspace never fires alice's message trigger.
	require.Equal(t, http.StatusOK, env.post(t, envelope("T0ACME", "EvBOT", `{"type":"message","channel":"C0GEN","bot_id":"B0BOT",
	  "text":"on it","ts":"1700000001.000100","channel_type":"channel"}`)).Code)
	for _, a := range env.intake.all() {
		assert.NotEqual(t, "alice-messages", a.TriggerID)
	}
}

func TestSlackReceiverRejectsReplaysAndForgeries(t *testing.T) {
	env := newSlackEnv(t)
	env.route("t1", "alice", "T0ACME", core.IntegrationConfig{Events: []string{"app_mention"}})
	body := envelope("T0ACME", "EvX", `{"type":"app_mention","user":"U0ADA","text":"hi","ts":"1.1","channel":"C0GEN"}`)

	stale := strconv.FormatInt(env.now.Add(-10*time.Minute).Unix(), 10)
	rec := env.post(t, body, map[string]string{
		"X-Slack-Request-Timestamp": stale,
		"X-Slack-Signature":         signSlack(slackSecret, stale, []byte(body)),
	})
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "a correctly signed replay older than 5 minutes")
	rec = env.post(t, body, map[string]string{"X-Slack-Signature": "v0=" + strings.Repeat("0", 64)})
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, env.intake.all())
}

// The provider and the catalog meet on strings: every Event.Type the
// provider emits is declared by exactly one trigger in the shipped Slack
// manifest, and every event carries exactly the attributes that trigger
// declares (a source's `match` and the filter schema rely on both).
func TestSlackEventsMatchTheManifestTriggers(t *testing.T) {
	var m *reliantv1.IntegrationManifest
	for _, cand := range catalog.MustBuiltin().Manifests() {
		if cand.GetId() == SlackProviderID {
			m = cand
		}
	}
	require.NotNil(t, m, "the catalog ships the slack manifest")

	declared := map[string]*reliantv1.TriggerSpec{}
	for _, tr := range m.GetTriggers() {
		for _, ev := range tr.GetEvents() {
			require.Nil(t, declared[ev], "event %s is declared by two triggers", ev)
			declared[ev] = tr
		}
	}
	samples := []string{
		`{"type":"message","channel":"C0GEN","user":"U0ADA","text":"x","ts":"1.1","channel_type":"channel"}`,
		`{"type":"message","channel":"G0SEC","user":"U0ADA","text":"x","ts":"1.1","channel_type":"group"}`,
		`{"type":"message","channel":"D0DM","user":"U0ADA","text":"x","ts":"1.1","channel_type":"im"}`,
		`{"type":"message","channel":"G0MP","user":"U0ADA","text":"x","ts":"1.1","channel_type":"mpim"}`,
		`{"type":"app_mention","channel":"C0GEN","user":"U0ADA","text":"<@U0BOT>","ts":"1.1"}`,
		`{"type":"reaction_added","user":"U0ADA","reaction":"eyes","item":{"type":"message","channel":"C0GEN","ts":"1.1"}}`,
	}
	emitted := map[string]bool{}
	for i, inner := range samples {
		ev := parseOne(t, envelope("T0ACME", "Ev"+strconv.Itoa(i), inner))
		tr := declared[ev.Type]
		require.NotNil(t, tr, "the provider emits %q, which no trigger declares", ev.Type)
		emitted[ev.Type] = true
		var want []string
		for _, a := range tr.GetAttributes() {
			want = append(want, a.GetName())
		}
		var got []string
		for k := range ev.Attributes {
			got = append(got, k)
		}
		assert.ElementsMatch(t, want, got, "attributes of %s", ev.Type)

		// The recorded payload validates against the trigger's schema.
		payload := toInbound(SlackProviderID, &core.Trigger{ID: "t"}, ev).Payload
		rawSchema, err := json.Marshal(manifest.TriggerPayloadSchema(m, tr))
		require.NoError(t, err)
		var schema jsonschema.Schema
		require.NoError(t, json.Unmarshal(rawSchema, &schema))
		resolved, err := schema.Resolve(nil)
		require.NoError(t, err)
		raw, _ := json.Marshal(payload)
		var asJSON any
		require.NoError(t, json.Unmarshal(raw, &asJSON))
		assert.NoError(t, resolved.Validate(asJSON), "payload of %s against %s's schema", ev.Type, tr.GetId())
	}
	for ev := range declared {
		assert.True(t, emitted[ev], "trigger declares %q, which the provider never emits", ev)
	}
}

func TestSlackRegisteredFromEnvOnlyWithASigningSecret(t *testing.T) {
	r, err := RegistryFromEnv(func(string) string { return "" })
	require.NoError(t, err)
	assert.False(t, r.HasInboundSource(SlackProviderID))

	r, err = RegistryFromEnv(func(k string) string {
		if k == SlackSigningSecretEnv {
			return "  " + slackSecret + "\n"
		}
		return ""
	})
	require.NoError(t, err)
	p, ok := r.Provider(SlackProviderID)
	require.True(t, ok)
	body := envelope("T0ACME", "Ev1", `{"type":"app_mention","channel":"C0GEN","ts":"1.2"}`)
	assert.NoError(t, p.Verify(context.Background(), slackRequest(t, body, time.Now())), "the secret is trimmed")
}

// Gmail is polled. Its poller is registered when the deployment configured
// the Google OAuth client — without one nobody can hold a Gmail connection —
// and never otherwise, so a trigger for it is refused at write time.
func TestGmailPollerRegisteredFromEnvOnlyWithAnOAuthClient(t *testing.T) {
	r, err := RegistryFromEnv(func(string) string { return "" })
	require.NoError(t, err)
	assert.False(t, r.HasInboundSource("gmail"))
	assert.False(t, r.IsPolled("gmail"))

	r, err = RegistryFromEnv(func(k string) string {
		if k == "RELIANT_OAUTH_GMAIL_CLIENT_ID" {
			return "1234-gmail.apps.googleusercontent.com"
		}
		return ""
	})
	require.NoError(t, err)
	assert.True(t, r.IsPolled("gmail"))
	assert.True(t, r.HasInboundSource("gmail"))
	_, pushed := r.Provider("gmail")
	assert.False(t, pushed, "polled, not a webhook")
}
