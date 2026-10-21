// Copyright (c) 2025 Reliant Labs

// Package github translates GitHub App webhook deliveries into trigger events.
// It is pure: no I/O, no signature checking, no knowledge of triggers. The
// webhook package's GitHubProvider verifies a delivery and hands the body
// here.
//
// There is one GitHub App (control-plane's, slug reliant-labs) and so one
// webhook URL for every installation: <PUBLIC_URL>/integrations/github/events.
//
// Routing is access-gated. An installation is usually an organization that
// many users share, and they do not all see the same repositories, so an
// event about a repository is keyed by its installation (AccountKey) AND its
// repository id (ResourceKey). It reaches only triggers whose owner's own
// GitHub token recently reported that repository as visible
// (internal/integrations/ghaccess). Events that cut access — a repository
// removed from the installation, a member removed from the org, the App
// uninstalled — become revocations, applied before anything routes.
//
// The recorded payload (trigger.payload.data) is a trimmed copy: the fields an
// agent or a filter needs, long text capped, emails and API-only URLs
// dropped. It is untrusted input.
package github

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
)

const (
	// ProviderID is the integration id and the {provider} path segment.
	ProviderID = "github"
	// SecretEnv names the App's webhook secret, a deployment secret shared
	// with the App's webhook configuration.
	SecretEnv = "RELIANT_GITHUB_WEBHOOK_SECRET" //nolint:gosec // G101: the NAME of an env var, not a secret

	// EventHeader names the event type of a delivery.
	EventHeader = "X-GitHub-Event"
	// DeliveryHeader is the delivery's GUID, reused by redeliveries: the
	// dedupe key.
	DeliveryHeader = "X-GitHub-Delivery"
	// SignatureHeader carries "sha256=" + hex HMAC-SHA256 of the raw body.
	SignatureHeader = "X-Hub-Signature-256"
	// SignaturePrefix precedes the hex digest in SignatureHeader.
	SignaturePrefix = "sha256="
)

// Event is one trigger event a delivery carries, before routing.
type Event struct {
	// Type is "<event>.<action>" (or "push").
	Type string
	// AccountKey is the App installation id.
	AccountKey string
	// ResourceKey is the repository id (stable across renames).
	ResourceKey string
	OccurredAt  time.Time
	Attributes  map[string]string
	Data        map[string]any
	// Sender is trigger.sender: the delivery's `sender`, the account that
	// caused the event (see Sender).
	Sender *core.TriggerSender
	// Echo is what the delivery says about itself, computed from the raw body,
	// for attributing it to a write Reliant made. It is not part of the
	// recorded payload.
	Echo *provenance.Echo
}

// Parsed is what one delivery carries.
type Parsed struct {
	// Ping is GitHub's handshake: answer 2xx, record nothing.
	Ping bool
	// Events are the trigger events, at most one per delivery.
	Events []Event
	// Revocations are access the delivery says has ended.
	Revocations []core.IntegrationAccessRevocation
}

// Parse translates one delivery of event type event (the X-GitHub-Event
// header). A delivery nobody can listen to (an unmapped type or action, an
// App's own comment, an event that does not say which installation and
// repository it is about) parses to no events, and is acked.
func Parse(event string, body []byte, receivedAt time.Time) (*Parsed, error) {
	event = strings.TrimSpace(event)
	if event == "" {
		return nil, fmt.Errorf("github: no %s header", EventHeader)
	}
	if event == "ping" {
		return &Parsed{Ping: true}, nil
	}
	var p payload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("github: %s body: %w", event, err)
	}
	return &Parsed{
		Revocations: revocations(event, &p),
		Events:      events(event, &p, receivedAt),
	}, nil
}

// payload is the part of every webhook body the parser reads: the routing
// envelope typed, everything else raw for the per-event trimming.
type payload struct {
	Action       string           `json:"action"`
	Installation *installationRef `json:"installation"`
	Repository   *repositoryRef   `json:"repository"`
	Sender       *userRef         `json:"sender"`

	raw map[string]any
}

func (p *payload) UnmarshalJSON(b []byte) error {
	type envelope payload
	var e envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return err
	}
	var raw map[string]any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	*p = payload(e)
	p.raw = raw
	return nil
}

type installationRef struct {
	ID int64 `json:"id"`
}

type repositoryRef struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type userRef struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Type  string `json:"type"`
}

// mapping says how one (X-GitHub-Event, action) becomes an event. The table is
// generated from projection/*.yaml (zz_projection_gen.go), which is also where
// the event's data schema in the catalog comes from.
type mapping struct {
	// attrs adds the per-event routing attributes. Every attribute a
	// trigger declares is set on every event of it: a match on an absent
	// attribute is a miss.
	attrs func(raw any, attrs map[string]string)
	// data builds the trimmed payload, the routing envelope's data included.
	data func(raw any) map[string]any
	// shrink cuts an oversized payload down; nil for an event that cannot be.
	shrink *shrinkRule
}

// EventTypes lists every event type the parser emits, sorted.
func EventTypes() []string {
	out := make([]string, 0, len(mappings))
	for k := range mappings {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func eventType(event, action string) string {
	if event == "push" {
		return event
	}
	return event + "." + action
}

func events(event string, body *payload, receivedAt time.Time) []Event {
	typ := eventType(event, body.Action)
	m, ok := mappings[typ]
	if !ok {
		return nil
	}
	// Without both, the event cannot be routed by access, and guessing
	// would be routing it to someone who may not see it.
	if body.Installation == nil || body.Installation.ID == 0 || body.Repository == nil || body.Repository.ID == 0 {
		return nil
	}
	if isOurComment(event, body) {
		return nil
	}
	installation := strconv.FormatInt(body.Installation.ID, 10)
	attrs := map[string]string{
		"repository":      body.Repository.FullName,
		"installation_id": installation,
		"sender":          senderLogin(body),
		"sender_id":       senderID(body),
	}
	if event != "push" {
		attrs["action"] = body.Action
	}
	if m.attrs != nil {
		m.attrs(body.raw, attrs)
	}
	data := m.data(body.raw)
	return []Event{{
		Type:        typ,
		AccountKey:  installation,
		ResourceKey: strconv.FormatInt(body.Repository.ID, 10),
		OccurredAt:  occurredAt(body.raw, receivedAt),
		Attributes:  attrs,
		Data:        capData(data, m.shrink),
		Sender:      sender(body),
		Echo:        echoOf(typ, body.raw, body),
	}}
}

// sender is a delivery's trigger.sender: GitHub's `sender`, the account that
// opened, pushed, commented or reviewed. GitHub wrote it into a body whose
// X-Hub-Signature-256 the provider verified before parsing, so it is
// verified.
//
// The id is GitHub's numeric user id, as a string, and the display name is
// the login. Never the other way round: a login can be renamed, and the old
// one registered by someone else, so an allowlist of logins would admit
// whoever claims a name after its owner gave it up. The numeric id is
// assigned once and never reused. "Only from" shows the login and stores the
// id (web/src/lib/onlyFromFilter.ts).
func sender(body *payload) *core.TriggerSender {
	login := senderLogin(body)
	return &core.TriggerSender{
		Kind:        core.TriggerSenderKindGitHub,
		ID:          senderID(body),
		DisplayName: login,
		Verified:    login != "",
	}
}

// isOurComment is a comment written by OUR App's bot. A trigger whose run
// comments as the bot would otherwise fire on its own comment, forever. It
// covers inline diff comments too: a responder's reply in a review thread is a
// pull_request_review_comment of its own.
//
// Only our bot is dropped. Every other bot (Dependabot, a CI app, a review
// tool) is a legitimate event source: a trigger that reviews what a bot opens
// or answers what a bot says needs to see it, and the earlier "any Bot sender"
// rule hid them all from every filter. Our own bot is told apart by the
// numeric id GitHub assigned it, never by its login, which a rename could hand
// to someone else (the same rule the sender allowlist keeps).
func isOurComment(event string, body *payload) bool {
	if event != "issue_comment" && event != "pull_request_review_comment" {
		return false
	}
	return body.Sender != nil && IsOurBot(body.Sender.Type, body.Sender.ID, body.Sender.Login)
}

// senderID is the sender's numeric GitHub id as a string; empty when the
// delivery names none (never "0": an id an allowlist could be written to match).
func senderID(body *payload) string {
	if body.Sender == nil || body.Sender.ID <= 0 {
		return ""
	}
	return strconv.FormatInt(body.Sender.ID, 10)
}

func senderLogin(body *payload) string {
	if body.Sender == nil {
		return ""
	}
	return body.Sender.Login
}

// occurredAt is when the event says it happened, from the field each event
// type carries; receivedAt when it carries none.
func occurredAt(raw map[string]any, receivedAt time.Time) time.Time {
	for _, path := range [][]string{
		{"comment", "created_at"}, {"review", "submitted_at"}, {"workflow_run", "updated_at"},
		{"check_run", "completed_at"}, {"check_suite", "updated_at"},
		{"head_commit", "timestamp"}, {"pull_request", "updated_at"}, {"issue", "updated_at"},
	} {
		if s, ok := str(dig(raw, path...)); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.UTC()
			}
		}
	}
	return receivedAt.UTC()
}

// revocations is the access a delivery says has ended. Each is applied for
// every user before anything routes; the access refresher restores whatever a
// broad revocation cut that a user still has.
func revocations(event string, body *payload) []core.IntegrationAccessRevocation {
	if body.Installation == nil || body.Installation.ID == 0 {
		return nil
	}
	installation := strconv.FormatInt(body.Installation.ID, 10)
	repo := func() (string, bool) {
		if body.Repository == nil || body.Repository.ID == 0 {
			return "", false
		}
		return strconv.FormatInt(body.Repository.ID, 10), true
	}
	switch event {
	case "installation":
		switch body.Action {
		case "deleted", "suspend":
			return []core.IntegrationAccessRevocation{{AccountKey: installation}}
		}
	case "installation_repositories":
		if body.Action != "removed" {
			return nil
		}
		var out []core.IntegrationAccessRevocation
		for _, r := range list(body.raw["repositories_removed"]) {
			if id, ok := intString(dig(r, "id")); ok {
				out = append(out, core.IntegrationAccessRevocation{AccountKey: installation, ResourceKey: id})
			}
		}
		return out
	case "organization":
		if body.Action == "member_removed" {
			if id, ok := intString(dig(body.raw, "membership", "user", "id")); ok {
				return []core.IntegrationAccessRevocation{{AccountKey: installation, SubjectID: id}}
			}
		}
	case "membership":
		// Removed from a team: the repositories the team granted are gone,
		// and which ones is not in the event. Cut all of the member's access
		// through the installation; the next refresh restores what remains.
		if body.Action == "removed" {
			if id, ok := intString(dig(body.raw, "member", "id")); ok {
				return []core.IntegrationAccessRevocation{{AccountKey: installation, SubjectID: id}}
			}
		}
	case "member":
		if body.Action == "removed" {
			id, okID := intString(dig(body.raw, "member", "id"))
			r, okRepo := repo()
			if okID && okRepo {
				return []core.IntegrationAccessRevocation{{AccountKey: installation, ResourceKey: r, SubjectID: id}}
			}
		}
	case "repository":
		// A repository made private may have been visible to people who
		// cannot see it now: cut it for everyone until refreshes re-confirm.
		if body.Action == "privatized" {
			if r, ok := repo(); ok {
				return []core.IntegrationAccessRevocation{{AccountKey: installation, ResourceKey: r}}
			}
		}
	}
	return nil
}
