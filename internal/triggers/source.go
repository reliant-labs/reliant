// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A trigger's source arrives as one arm of a proto oneof and is stored as a
// kind plus a jsonb config. The conversion lives here, beside the schedule's,
// so the API, the CLI and the fire paths agree on what an unset field means.

// Source is a validated source arm: the kind it determines and the config to
// store for it.
type Source struct {
	Kind   core.TriggerKind
	Config json.RawMessage
	// Integration is IntegrationConfig.Integration for an integration source.
	Integration string
	// HMAC reports a webhook source that verifies signatures, and so needs a
	// signing secret.
	HMAC bool
}

// Limits on what a source may declare. They bound what one trigger costs the
// router on every event, not what is meaningful.
const (
	maxSourceEvents   = 50
	maxSourceMatch    = 20
	maxSourceRefs     = 50
	maxSourceValueLen = 512
)

// minPollInterval is the floor on IntegrationSource.poll_interval: a poll is
// an outbound call against the owner's provider quota.
const minPollInterval = time.Minute

// SourceFromDefinition validates the definition's source arm. Errors are
// *ConfigError (InvalidArgument). A workflow_trigger reference is not a
// source and is rejected by the caller before this.
func SourceFromDefinition(def *reliantv1.TriggerDefinition) (*Source, error) {
	switch src := def.GetSource().(type) {
	case *reliantv1.TriggerDefinition_Schedule:
		cfg := ScheduleConfigFromProto(src.Schedule)
		if _, err := ParseScheduleConfig(cfg); err != nil {
			return nil, err
		}
		return marshalSource(core.TriggerKindSchedule, cfg, "", false)
	case *reliantv1.TriggerDefinition_Webhook:
		cfg, err := WebhookConfigFromProto(src.Webhook)
		if err != nil {
			return nil, err
		}
		return marshalSource(core.TriggerKindWebhook, cfg, "", cfg.HMAC != nil)
	case *reliantv1.TriggerDefinition_Integration:
		cfg, err := IntegrationConfigFromProto(src.Integration)
		if err != nil {
			return nil, err
		}
		return marshalSource(core.TriggerKindIntegration, cfg, cfg.Integration, false)
	case *reliantv1.TriggerDefinition_WorkflowEvent:
		cfg, err := WorkflowEventConfigFromProto(src.WorkflowEvent)
		if err != nil {
			return nil, err
		}
		return marshalSource(core.TriggerKindWorkflowEvent, cfg, "", false)
	case nil:
		return nil, &ConfigError{Field: "source", Reason: "a source is required; a trigger with no source can never fire"}
	default:
		return nil, &ConfigError{Field: "source", Reason: fmt.Sprintf("unsupported source %T", src)}
	}
}

func marshalSource(kind core.TriggerKind, cfg any, integration string, hmac bool) (*Source, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode %s config: %w", kind, err)
	}
	return &Source{Kind: kind, Config: raw, Integration: integration, HMAC: hmac}, nil
}

// WebhookConfigFromProto validates a WebhookSource.
func WebhookConfigFromProto(src *reliantv1.WebhookSource) (core.WebhookConfig, error) {
	var cfg core.WebhookConfig
	if src == nil || src.Hmac == nil {
		return cfg, nil
	}
	h := core.WebhookHMACConfig{
		Header:    strings.TrimSpace(src.Hmac.Header),
		Algorithm: strings.ToLower(strings.TrimSpace(src.Hmac.Algorithm)),
		Prefix:    src.Hmac.Prefix,
		Encoding:  strings.ToLower(strings.TrimSpace(src.Hmac.Encoding)),
	}
	switch h.Algorithm {
	case "", "sha256", "sha1", "sha512":
	default:
		return cfg, &ConfigError{Field: "webhook.hmac.algorithm", Reason: `must be "sha256", "sha1" or "sha512", got ` + h.Algorithm}
	}
	switch h.Encoding {
	case "", "hex", "base64":
	default:
		return cfg, &ConfigError{Field: "webhook.hmac.encoding", Reason: `must be "hex" or "base64", got ` + h.Encoding}
	}
	if h.Header != "" && !validHeaderName(h.Header) {
		return cfg, &ConfigError{Field: "webhook.hmac.header", Reason: "not a valid HTTP header name: " + h.Header}
	}
	if strings.EqualFold(h.Header, "Authorization") {
		return cfg, &ConfigError{Field: "webhook.hmac.header", Reason: "Authorization carries the bearer token; name the signature header"}
	}
	if len(h.Prefix) > 64 {
		return cfg, &ConfigError{Field: "webhook.hmac.prefix", Reason: "longer than 64 characters"}
	}
	cfg.HMAC = &h
	return cfg, nil
}

// IntegrationConfigFromProto validates an IntegrationSource's shape. Whether
// the integration exists and has an inbound source is the caller's check: it
// depends on what this deployment registered.
func IntegrationConfigFromProto(src *reliantv1.IntegrationSource) (core.IntegrationConfig, error) {
	var cfg core.IntegrationConfig
	if src == nil {
		return cfg, &ConfigError{Field: "integration", Reason: "required"}
	}
	cfg.Integration = strings.TrimSpace(src.Integration)
	if cfg.Integration == "" {
		return cfg, &ConfigError{Field: "integration.integration", Reason: "required"}
	}
	if len(src.Events) == 0 {
		return cfg, &ConfigError{Field: "integration.events", Reason: "name at least one event type; a trigger with none can never fire"}
	}
	if len(src.Events) > maxSourceEvents {
		return cfg, &ConfigError{Field: "integration.events", Reason: fmt.Sprintf("at most %d event types", maxSourceEvents)}
	}
	for _, ev := range src.Events {
		ev = strings.TrimSpace(ev)
		if ev == "" || len(ev) > maxSourceValueLen || strings.ContainsAny(ev, " \t\n") {
			return cfg, &ConfigError{Field: "integration.events", Reason: fmt.Sprintf("invalid event type %q", ev)}
		}
		if strings.Contains(strings.TrimSuffix(ev, "*"), "*") {
			return cfg, &ConfigError{Field: "integration.events", Reason: fmt.Sprintf("%q: only a trailing \".*\" (or \"*\" alone) is a wildcard", ev)}
		}
		cfg.Events = append(cfg.Events, ev)
	}
	if len(src.Match) > maxSourceMatch {
		return cfg, &ConfigError{Field: "integration.match", Reason: fmt.Sprintf("at most %d entries", maxSourceMatch)}
	}
	for key, value := range src.Match {
		if strings.TrimSpace(key) == "" || len(key) > maxSourceValueLen || len(value) > maxSourceValueLen {
			return cfg, &ConfigError{Field: "integration.match", Reason: fmt.Sprintf("invalid entry %q", key)}
		}
		if cfg.Match == nil {
			cfg.Match = make(map[string]string, len(src.Match))
		}
		cfg.Match[key] = value
	}
	if src.PollInterval != "" {
		every, err := time.ParseDuration(src.PollInterval)
		if err != nil {
			return cfg, &ConfigError{Field: "integration.poll_interval", Reason: "not a Go duration: " + src.PollInterval}
		}
		if every < minPollInterval {
			return cfg, &ConfigError{Field: "integration.poll_interval", Reason: fmt.Sprintf("must be at least %s", minPollInterval)}
		}
		cfg.PollInterval = src.PollInterval
	}
	return cfg, nil
}

// WorkflowEventConfigFromProto validates a WorkflowEventSource.
func WorkflowEventConfigFromProto(src *reliantv1.WorkflowEventSource) (core.WorkflowEventConfig, error) {
	var cfg core.WorkflowEventConfig
	if src == nil {
		return cfg, nil
	}
	if len(src.Workflows) > maxSourceRefs {
		return cfg, &ConfigError{Field: "workflow_event.workflows", Reason: fmt.Sprintf("at most %d workflows", maxSourceRefs)}
	}
	for _, ref := range src.Workflows {
		ref = strings.TrimSpace(ref)
		if ref == "" || len(ref) > maxSourceValueLen {
			return cfg, &ConfigError{Field: "workflow_event.workflows", Reason: fmt.Sprintf("invalid workflow ref %q", ref)}
		}
		cfg.Workflows = append(cfg.Workflows, ref)
	}
	for _, outcome := range src.Outcomes {
		switch outcome {
		case core.WorkflowEventFinished, core.WorkflowEventFailed, core.WorkflowEventBlocked:
			cfg.Outcomes = append(cfg.Outcomes, outcome)
		default:
			return cfg, &ConfigError{Field: "workflow_event.outcomes", Reason: fmt.Sprintf(`%q: must be "finished", "failed" or "blocked"`, outcome)}
		}
	}
	return cfg, nil
}

// sourceToProto renders a stored trigger's source arm.
func sourceToProto(t *core.Trigger, out *reliantv1.Trigger) error {
	switch t.Kind {
	case core.TriggerKindSchedule:
		var cfg core.ScheduleConfig
		if err := unmarshalConfig(t, &cfg); err != nil {
			return err
		}
		out.Source = &reliantv1.Trigger_Schedule{Schedule: scheduleConfigToProto(cfg)}
	case core.TriggerKindWebhook:
		var cfg core.WebhookConfig
		if err := unmarshalConfig(t, &cfg); err != nil {
			return err
		}
		src := &reliantv1.WebhookSource{}
		if cfg.HMAC != nil {
			src.Hmac = &reliantv1.WebhookHmac{
				Header:    cfg.HMAC.Header,
				Algorithm: cfg.HMAC.Algorithm,
				Prefix:    cfg.HMAC.Prefix,
				Encoding:  cfg.HMAC.Encoding,
			}
		}
		out.Source = &reliantv1.Trigger_Webhook{Webhook: src}
	case core.TriggerKindIntegration:
		var cfg core.IntegrationConfig
		if err := unmarshalConfig(t, &cfg); err != nil {
			return err
		}
		out.Source = &reliantv1.Trigger_Integration{Integration: &reliantv1.IntegrationSource{
			Integration:  cfg.Integration,
			Events:       cfg.Events,
			Match:        cfg.Match,
			PollInterval: cfg.PollInterval,
		}}
	case core.TriggerKindWorkflowEvent:
		var cfg core.WorkflowEventConfig
		if err := unmarshalConfig(t, &cfg); err != nil {
			return err
		}
		out.Source = &reliantv1.Trigger_WorkflowEvent{WorkflowEvent: &reliantv1.WorkflowEventSource{
			Workflows: cfg.Workflows,
			Outcomes:  cfg.Outcomes,
		}}
	}
	return nil
}

func unmarshalConfig(t *core.Trigger, out any) error {
	if len(t.Config) == 0 {
		return nil
	}
	if err := json.Unmarshal(t.Config, out); err != nil {
		return fmt.Errorf("trigger %s config: %w", t.ID, err)
	}
	return nil
}

// IntegrationConfigFor reads a stored integration trigger's config.
func IntegrationConfigFor(t *core.Trigger) (core.IntegrationConfig, error) {
	var cfg core.IntegrationConfig
	if t.Kind != core.TriggerKindIntegration {
		return cfg, &ConfigError{Reason: fmt.Sprintf("trigger %s is a %s trigger, not an integration trigger", t.ID, t.Kind)}
	}
	if err := unmarshalConfig(t, &cfg); err != nil {
		return cfg, &ConfigError{Reason: err.Error()}
	}
	return cfg, nil
}

// WebhookConfigFor reads a stored webhook trigger's config.
func WebhookConfigFor(t *core.Trigger) (core.WebhookConfig, error) {
	var cfg core.WebhookConfig
	if t.Kind != core.TriggerKindWebhook {
		return cfg, &ConfigError{Reason: fmt.Sprintf("trigger %s is a %s trigger, not a webhook trigger", t.ID, t.Kind)}
	}
	if err := unmarshalConfig(t, &cfg); err != nil {
		return cfg, &ConfigError{Reason: err.Error()}
	}
	return cfg, nil
}

// HooksPath is the path a webhook trigger receives deliveries on. Append
// "/<token>" for the path-token form.
func HooksPath(triggerID string) string { return "/hooks/" + url.PathEscape(triggerID) }

// WebhookURL is the absolute URL of a webhook trigger, or the bare path when
// the deployment has no public URL (a guessed host would be worse: a wrong
// URL pasted into a sender fails in a way that is hard to diagnose).
func WebhookURL(publicURL, triggerID string) string {
	return strings.TrimSuffix(strings.TrimSpace(publicURL), "/") + HooksPath(triggerID)
}

func validHeaderName(name string) bool {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return name != ""
}
