// Copyright (c) 2025 Reliant Labs
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// Slack's Events API: one app-level Request URL,
// POST <PUBLIC_URL>/integrations/slack/events, for every workspace the app is
// installed in.
//
//   - Verify: X-Slack-Signature is "v0=" + hex HMAC-SHA256 under the app's
//     signing secret of "v0:" + X-Slack-Request-Timestamp + ":" + raw body,
//     and a timestamp more than five minutes from now is a replay.
//   - url_verification (sent when the Request URL is saved) is a signed
//     handshake: the challenge is echoed and nothing is recorded.
//   - event_callback carries one event. It routes on team_id, which is what a
//     Slack connection records as its external account (auth.test's
//     team_id), and dedupes on event_id, which Slack keeps across its retries
//     (X-Slack-Retry-Num), so a retry is acked and never fires twice.
//
// The event types are the trigger catalog's (catalog/slack/manifest.yaml):
// a message is "message.<conversation kind>" — Slack's own subscription
// names (message.channels, message.groups, message.im, message.mpim) — so a
// source can take one kind or all with "message.*"; mentions and reactions
// keep Slack's names. Messages from bots (the Reliant bot's own posts among
// them) and edits/deletions never fire: a trigger that posts must not
// trigger itself.

// SlackProviderID is Slack's integration id.
const SlackProviderID = "slack"

// SlackSigningSecretEnv names the deployment secret that enables the Slack
// provider: the Slack app's Signing Secret (Basic Information page).
const SlackSigningSecretEnv = "RELIANT_SLACK_SIGNING_SECRET"

// SlackMaxEventData caps the event recorded as trigger.payload.data. A Slack
// event is usually a few KiB; one past the cap keeps only its routing fields
// (the run can fetch the message with conversations.history).
const SlackMaxEventData = 64 << 10

// slackReplayWindow is how far X-Slack-Request-Timestamp may be from now.
const slackReplayWindow = 5 * time.Minute

// SlackProvider verifies and parses Slack Events API deliveries.
type SlackProvider struct {
	secret []byte
}

// NewSlackProvider builds the provider with the app's signing secret.
func NewSlackProvider(signingSecret string) *SlackProvider {
	return &SlackProvider{secret: []byte(signingSecret)}
}

// ID implements Provider.
func (p *SlackProvider) ID() string { return SlackProviderID }

// Verify implements Provider.
func (p *SlackProvider) Verify(_ context.Context, req *Request) error {
	if len(p.secret) == 0 {
		return errors.New("slack: no signing secret configured")
	}
	tsHeader := strings.TrimSpace(req.Header.Get("X-Slack-Request-Timestamp"))
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: missing or malformed X-Slack-Request-Timestamp", ErrUnauthorized)
	}
	now := req.ReceivedAt
	if now.IsZero() {
		now = time.Now()
	}
	if skew := now.Sub(time.Unix(ts, 0)); math.Abs(float64(skew)) > float64(slackReplayWindow) {
		return fmt.Errorf("%w: request timestamp is %s from now, outside the replay window", ErrUnauthorized, skew.Round(time.Second))
	}
	sig, ok := strings.CutPrefix(req.Header.Get("X-Slack-Signature"), "v0=")
	if !ok {
		return fmt.Errorf("%w: missing or unversioned X-Slack-Signature", ErrUnauthorized)
	}
	got, err := hex.DecodeString(sig)
	if err != nil || len(got) != sha256.Size {
		return fmt.Errorf("%w: malformed X-Slack-Signature", ErrUnauthorized)
	}
	mac := hmac.New(sha256.New, p.secret)
	mac.Write([]byte("v0:" + tsHeader + ":"))
	mac.Write(req.Body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return fmt.Errorf("%w: signature mismatch", ErrUnauthorized)
	}
	return nil
}

// slackEnvelope is the outer body of every Events API delivery.
type slackEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	TeamID    string          `json:"team_id"`
	APIAppID  string          `json:"api_app_id"`
	EventID   string          `json:"event_id"`
	EventTime int64           `json:"event_time"`
	Event     json.RawMessage `json:"event"`
}

// Parse implements Provider.
func (p *SlackProvider) Parse(_ context.Context, req *Request) (*Delivery, error) {
	var env slackEnvelope
	if err := json.Unmarshal(req.Body, &env); err != nil {
		return nil, fmt.Errorf("slack delivery: %w", err)
	}
	switch env.Type {
	case "url_verification":
		if env.Challenge == "" {
			return nil, errors.New("slack url_verification without a challenge")
		}
		return &Delivery{Respond: &Response{ContentType: "text/plain; charset=utf-8", Body: []byte(env.Challenge)}}, nil
	case "event_callback":
	default:
		// app_rate_limited and anything newer: acknowledged, nothing to fire.
		return &Delivery{}, nil
	}
	if env.TeamID == "" || env.EventID == "" {
		return nil, errors.New("slack event_callback without team_id or event_id")
	}
	var event map[string]any
	if err := json.Unmarshal(env.Event, &event); err != nil || event == nil {
		return nil, errors.New("slack event_callback without an event object")
	}
	eventType, attrs, ok := slackClassify(event)
	if !ok {
		return &Delivery{}, nil
	}
	occurred := time.Time{}
	if env.EventTime > 0 {
		occurred = time.Unix(env.EventTime, 0).UTC()
	}
	return &Delivery{Events: []Event{{
		Type:       eventType,
		AccountKey: env.TeamID,
		DeliveryID: env.EventID,
		OccurredAt: occurred,
		Attributes: attrs,
		Data:       slackData(event, env),
		Sender:     slackSender(event),
	}}}, nil
}

// slackSender is a Slack event's trigger.sender: the event's `user`, the
// Slack user id of whoever posted, mentioned or reacted. Slack wrote it into
// a body Verify checked the signature of, so it is verified. The id is
// scoped to the team the trigger's connection already routes on.
//
// The display name is the user_profile Slack attaches to some message
// events; most carry none, and it is never a basis for authorizing.
func slackSender(event map[string]any) *core.TriggerSender {
	id, _ := event["user"].(string)
	sender := &core.TriggerSender{Kind: core.TriggerSenderKindSlack, ID: id, Verified: id != ""}
	if profile, ok := event["user_profile"].(map[string]any); ok {
		for _, key := range []string{"display_name", "real_name", "name"} {
			if name, _ := profile[key].(string); name != "" {
				sender.DisplayName = name
				break
			}
		}
	}
	return sender
}

// slackClassify names an inner event's trigger type and routing attributes,
// or reports that no trigger listens to it. Every attribute a trigger
// declares is always set (empty when Slack omits it), so a source's match
// never depends on whether Slack happened to include a field.
func slackClassify(event map[string]any) (string, map[string]string, bool) {
	str := func(k string) string { s, _ := event[k].(string); return s }
	switch str("type") {
	case "message":
		if slackFromBot(event) {
			return "", nil, false
		}
		switch str("subtype") {
		case "message_changed", "message_deleted", "message_replied", "bot_message":
			// An edit, a deletion or a thread-summary update is not a new
			// post.
			return "", nil, false
		}
		kind := map[string]string{"channel": "channels", "group": "groups", "im": "im", "mpim": "mpim"}[str("channel_type")]
		if kind == "" {
			return "", nil, false
		}
		return "message." + kind, map[string]string{
			"channel": str("channel"), "channel_type": str("channel_type"), "user": str("user"),
			"thread_ts": str("thread_ts"), "subtype": str("subtype"),
		}, true
	case "app_mention":
		if slackFromBot(event) {
			return "", nil, false
		}
		return "app_mention", map[string]string{
			"channel": str("channel"), "user": str("user"), "thread_ts": str("thread_ts"),
		}, true
	case "reaction_added":
		item, _ := event["item"].(map[string]any)
		channel, _ := item["channel"].(string)
		return "reaction_added", map[string]string{
			"channel": channel, "user": str("user"), "reaction": str("reaction"), "item_user": str("item_user"),
		}, true
	}
	return "", nil, false
}

// slackFromBot reports a message a bot posted: Slack marks those with bot_id
// (and bot_profile), whatever their subtype.
func slackFromBot(event map[string]any) bool {
	if id, _ := event["bot_id"].(string); id != "" {
		return true
	}
	_, profiled := event["bot_profile"].(map[string]any)
	return profiled
}

// slackData is trigger.payload.data: the inner event, plus the envelope's
// team_id, api_app_id and event_id, capped at SlackMaxEventData. The
// envelope's deprecated verification token is never copied.
func slackData(event map[string]any, env slackEnvelope) map[string]any {
	data := make(map[string]any, len(event)+3)
	for k, v := range event {
		data[k] = v
	}
	data["team_id"] = env.TeamID
	data["api_app_id"] = env.APIAppID
	data["event_id"] = env.EventID
	if raw, err := json.Marshal(data); err == nil && len(raw) <= SlackMaxEventData {
		return data
	}
	capped := map[string]any{"truncated": true}
	for _, k := range []string{"type", "subtype", "channel", "channel_type", "user", "ts", "thread_ts", "event_ts",
		"reaction", "item", "item_user", "team_id", "api_app_id", "event_id"} {
		if v, ok := data[k]; ok {
			capped[k] = v
		}
	}
	return capped
}
