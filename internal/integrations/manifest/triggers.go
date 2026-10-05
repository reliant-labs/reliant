package manifest

import (
	"fmt"
	"regexp"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// A trigger is a catalog statement: "this integration delivers events of
// these types, carrying these routing attributes and this payload". The
// provider that delivers them is Go (internal/integrations/webhook); the
// manifest is what search, the editor and filter validation read. The two
// meet on strings — Event.Type and Event.Attributes keys — which is why the
// loader holds those to the same grammar the trigger layer accepts.

var (
	// eventTypePattern is a provider event type: "issues.opened",
	// "app_mention", "message.received". No wildcard: a trigger names exact
	// types, and IntegrationSource.events adds the ".*" form.
	eventTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*$`)
	// attributePattern is an Event.Attributes / IntegrationSource.match key.
	attributePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

const (
	maxTriggerEvents     = 20
	maxTriggerAttributes = 20
)

// Trigger returns the manifest's trigger with id.
func Trigger(m *reliantv1.IntegrationManifest, id string) (*reliantv1.TriggerSpec, bool) {
	for _, t := range m.GetTriggers() {
		if t.GetId() == id {
			return t, true
		}
	}
	return nil, false
}

// TriggerPayloadSchema is the JSON Schema of trigger.payload for an event of
// t: the envelope every integration event is recorded in, with t's declared
// attributes and data schema filled in. It is what a trigger filter and a
// workflow's `inputs` mapping are checked against, and what the editor
// renders. The envelope's shape is fixed by the receivers that record the
// event (webhook.toInbound for pushed events, triggers.pollEvent for polled
// ones); keep the three in step.
//
// The result is a fresh map the caller may modify.
func TriggerPayloadSchema(m *reliantv1.IntegrationManifest, t *reliantv1.TriggerSpec) map[string]any {
	events := make([]any, 0, len(t.GetEvents()))
	for _, ev := range t.GetEvents() {
		events = append(events, ev)
	}
	attrProps := make(map[string]any, len(t.GetAttributes()))
	attrNames := make([]any, 0, len(t.GetAttributes()))
	for _, a := range t.GetAttributes() {
		prop := map[string]any{"type": "string"}
		if d := strings.TrimSpace(a.GetDescription()); d != "" {
			prop["description"] = d
		}
		if ex := a.GetExample(); ex != "" {
			prop["examples"] = []any{ex}
		}
		attrProps[a.GetName()] = prop
		attrNames = append(attrNames, a.GetName())
	}
	attributes := map[string]any{
		"type":                 "object",
		"description":          "Routing facts the event carries; an integration source's `match` compares them by equality.",
		"properties":           attrProps,
		"additionalProperties": map[string]any{"type": "string"},
	}
	if len(attrNames) > 0 {
		attributes["required"] = attrNames
	}
	data := map[string]any{"type": "object"}
	if t.GetData() != nil {
		// AsMap builds a fresh map, so the caller cannot reach the manifest.
		data = t.GetData().AsMap()
	}
	if _, ok := data["description"]; !ok {
		data["description"] = "The provider's event payload: untrusted data from an outside sender."
	}
	return map[string]any{
		"type":        "object",
		"description": fmt.Sprintf("trigger.payload for a %s %s event.", m.GetDisplayName(), orID(t.GetDisplayName(), t.GetId())),
		"required":    []any{"integration", "event", "account", "delivery_id", "attributes", "data"},
		"properties": map[string]any{
			"integration": map[string]any{"type": "string", "const": m.GetId()},
			"event":       map[string]any{"type": "string", "enum": events},
			"account": map[string]any{
				"type":        "string",
				"description": "The provider account the event belongs to (the connection's external account id).",
			},
			"delivery_id": map[string]any{
				"type":        "string",
				"description": "The provider's id for the event, stable across redeliveries.",
			},
			"resource": map[string]any{
				"type": "string",
				"description": "The resource inside the account the event is about (a GitHub repository id), " +
					"when the provider routes by access to it.",
			},
			"attributes": attributes,
			"data":       data,
		},
	}
}

func orID(name, id string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return id
}

// validateTriggers applies the load-time rules to every trigger. Trigger and
// action ids share one namespace: both become `<integration>/<id>@<major>`
// refs, which search returns and get_integration_schema resolves.
func validateTriggers(m *reliantv1.IntegrationManifest) error {
	actions := make(map[string]bool, len(m.GetActions()))
	for _, a := range m.GetActions() {
		actions[a.GetId()] = true
	}
	ids := map[string]bool{}
	for _, t := range m.GetTriggers() {
		if ids[t.GetId()] {
			return fmt.Errorf("duplicate trigger id %q", t.GetId())
		}
		if actions[t.GetId()] {
			return fmt.Errorf("trigger id %q is also an action id; refs are one namespace", t.GetId())
		}
		ids[t.GetId()] = true
		if err := validateTrigger(t); err != nil {
			return fmt.Errorf("trigger %q: %w", t.GetId(), err)
		}
	}
	return nil
}

func validateTrigger(t *reliantv1.TriggerSpec) error {
	if !actionPattern.MatchString(t.GetId()) {
		return fmt.Errorf("id must match %s", actionPattern)
	}
	if strings.ContainsAny(t.GetSummary(), "\r\n") || len(t.GetSummary()) > 200 {
		return fmt.Errorf("summary must be one line of at most 200 characters")
	}
	if err := validateKeywords("", t.GetKeywords()); err != nil {
		return err
	}
	if len(t.GetEvents()) == 0 {
		return fmt.Errorf("events: name at least one provider event type; a trigger with none can never fire")
	}
	if len(t.GetEvents()) > maxTriggerEvents {
		return fmt.Errorf("events: at most %d", maxTriggerEvents)
	}
	seen := map[string]bool{}
	for i, ev := range t.GetEvents() {
		if !eventTypePattern.MatchString(ev) {
			return fmt.Errorf("events[%d] %q must match %s (exact types; a source adds the \".*\" wildcard)", i, ev, eventTypePattern)
		}
		if seen[ev] {
			return fmt.Errorf("events[%d]: duplicate event %q", i, ev)
		}
		seen[ev] = true
	}
	if len(t.GetAttributes()) > maxTriggerAttributes {
		return fmt.Errorf("attributes: at most %d", maxTriggerAttributes)
	}
	attrs := map[string]bool{}
	for i, a := range t.GetAttributes() {
		if !attributePattern.MatchString(a.GetName()) {
			return fmt.Errorf("attributes[%d].name %q must match %s", i, a.GetName(), attributePattern)
		}
		if attrs[a.GetName()] {
			return fmt.Errorf("duplicate attribute %q", a.GetName())
		}
		attrs[a.GetName()] = true
	}
	if t.GetData() == nil {
		return fmt.Errorf("data is required: the JSON Schema of the event payload, which filters are checked against")
	}
	if typ, _ := t.GetData().AsMap()["type"].(string); typ != "object" {
		return fmt.Errorf("data must be a JSON Schema with type: object")
	}
	return validateSchema("data", t.GetData().AsMap())
}
