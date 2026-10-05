// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// A trigger's source arrives as one arm of a proto oneof — on a stored
// trigger's TriggerDefinition or on a workflow's declared WorkflowTrigger —
// and is stored as a kind plus a jsonb config. Both shapes convert here, so
// the API, the CLI, the fire paths and workflow validation agree on what an
// unset field means.

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

// MinPollInterval is the floor on IntegrationSource.poll_interval: a poll is
// an outbound call against the owner's provider quota.
const MinPollInterval = time.Minute

// FromDefinition validates a stored trigger's source arm. Errors are
// *ConfigError (InvalidArgument). A workflow_trigger reference is not a source
// and is resolved by the caller before this.
func FromDefinition(def *reliantv1.TriggerDefinition) (*Source, error) {
	switch src := def.GetSource().(type) {
	case *reliantv1.TriggerDefinition_Schedule:
		return fromSchedule(src.Schedule)
	case *reliantv1.TriggerDefinition_Webhook:
		return fromWebhook(src.Webhook)
	case *reliantv1.TriggerDefinition_Integration:
		return fromIntegration(src.Integration)
	case *reliantv1.TriggerDefinition_WorkflowEvent:
		return fromWorkflowEvent(src.WorkflowEvent)
	case *reliantv1.TriggerDefinition_WorkflowTrigger:
		return nil, &ConfigError{Field: "source", Reason: "workflow_trigger names a declaration; resolve it against the workflow first"}
	case nil:
		return nil, &ConfigError{Field: "source", Reason: "a source is required; a trigger with no source can never fire"}
	default:
		return nil, &ConfigError{Field: "source", Reason: fmt.Sprintf("unsupported source %T", src)}
	}
}

// FromWorkflowTrigger validates the source arm of a trigger a workflow
// declares. It is the same validation as FromDefinition: a declaration is
// accepted by workflow validation exactly when its activation would be
// accepted by the API.
func FromWorkflowTrigger(wt *reliantv1.WorkflowTrigger) (*Source, error) {
	switch src := wt.GetSource().(type) {
	case *reliantv1.WorkflowTrigger_Schedule:
		return fromSchedule(src.Schedule)
	case *reliantv1.WorkflowTrigger_Webhook:
		return fromWebhook(src.Webhook)
	case *reliantv1.WorkflowTrigger_Integration:
		return fromIntegration(src.Integration)
	case *reliantv1.WorkflowTrigger_WorkflowEvent:
		return fromWorkflowEvent(src.WorkflowEvent)
	case nil:
		return nil, &ConfigError{Field: "source", Reason: "a source is required (schedule, webhook, integration or workflow_event); a trigger with no source can never fire"}
	default:
		return nil, &ConfigError{Field: "source", Reason: fmt.Sprintf("unsupported source %T", src)}
	}
}

func fromSchedule(src *reliantv1.ScheduleSource) (*Source, error) {
	cfg := ScheduleConfigFromProto(src)
	if _, err := ValidateSchedule(cfg); err != nil {
		return nil, err
	}
	return marshalSource(core.TriggerKindSchedule, cfg, "", false)
}

func fromWebhook(src *reliantv1.WebhookSource) (*Source, error) {
	cfg, err := WebhookConfigFromProto(src)
	if err != nil {
		return nil, err
	}
	return marshalSource(core.TriggerKindWebhook, cfg, "", cfg.HMAC != nil)
}

func fromIntegration(src *reliantv1.IntegrationSource) (*Source, error) {
	cfg, err := IntegrationConfigFromProto(src)
	if err != nil {
		return nil, err
	}
	return marshalSource(core.TriggerKindIntegration, cfg, cfg.Integration, false)
}

func fromWorkflowEvent(src *reliantv1.WorkflowEventSource) (*Source, error) {
	cfg, err := WorkflowEventConfigFromProto(src)
	if err != nil {
		return nil, err
	}
	return marshalSource(core.TriggerKindWorkflowEvent, cfg, "", false)
}

func marshalSource(kind core.TriggerKind, cfg any, integration string, hmac bool) (*Source, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("encode %s config: %w", kind, err)
	}
	return &Source{Kind: kind, Config: raw, Integration: integration, HMAC: hmac}, nil
}

// ScheduleConfigFromProto reads a ScheduleSource into the stored config shape.
func ScheduleConfigFromProto(src *reliantv1.ScheduleSource) core.ScheduleConfig {
	if src == nil {
		return core.ScheduleConfig{}
	}
	cfg := core.ScheduleConfig{
		Cron:     src.Cron,
		Timezone: src.Timezone,
		Overlap:  OverlapFromProto(src.Overlap),
	}
	if src.Interval != nil {
		cfg.Interval = *src.Interval
	}
	if src.CatchupWindow != nil {
		cfg.CatchupWindow = *src.CatchupWindow
	}
	return cfg
}

// OverlapFromProto maps the wire overlap policy to the stored one.
func OverlapFromProto(p reliantv1.TriggerOverlapPolicy) string {
	switch p {
	case reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW:
		return core.ScheduleOverlapAllow
	default:
		// UNSPECIFIED is SKIP. A client that omits the field gets the safe
		// behavior rather than concurrent unattended runs.
		return core.ScheduleOverlapSkip
	}
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
		if every < MinPollInterval {
			return cfg, &ConfigError{Field: "integration.poll_interval", Reason: fmt.Sprintf("must be at least %s", MinPollInterval)}
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

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return http.CanonicalHeaderKey(name) != ""
}
