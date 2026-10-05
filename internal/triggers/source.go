// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
)

// A trigger's source arrives as one arm of a proto oneof and is stored as a
// kind plus a jsonb config. Validating an arm is triggerspec's; reading a
// stored config back is here.

// Source is a validated source arm: the kind it determines and the config to
// store for it. The rules are triggerspec's, shared with workflow validation.
type Source = triggerspec.Source

// SourceFromDefinition validates the definition's source arm. Errors are
// *ConfigError (InvalidArgument). A workflow_trigger reference is not a
// source and is resolved by the caller before this.
func SourceFromDefinition(def *reliantv1.TriggerDefinition) (*Source, error) {
	return triggerspec.FromDefinition(def)
}

// minPollInterval is the floor on IntegrationSource.poll_interval.
const minPollInterval = triggerspec.MinPollInterval

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

// IntegrationEventsPath is the path a provider's app-level webhook delivers
// to; the receiver mounts POST /integrations/{provider}/events.
func IntegrationEventsPath(integration string) string {
	return "/integrations/" + url.PathEscape(integration) + "/events"
}

// IntegrationEventsURL is the absolute URL of a provider's app-level webhook
// (the bare path without a public URL, as WebhookURL).
func IntegrationEventsURL(publicURL, integration string) string {
	return strings.TrimSuffix(strings.TrimSpace(publicURL), "/") + IntegrationEventsPath(integration)
}
