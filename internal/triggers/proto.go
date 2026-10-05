// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// Conversion between the wire shape and the stored row lives here rather than
// in the handler so the fire path, the CLI and the handler all agree on one
// mapping — in particular on what an unset overlap or timezone means.

// ScheduleConfigFromProto reads a ScheduleSource into the stored config shape.
func ScheduleConfigFromProto(src *reliantv1.ScheduleSource) core.ScheduleConfig {
	if src == nil {
		return core.ScheduleConfig{}
	}
	cfg := core.ScheduleConfig{
		Cron:     src.Cron,
		Timezone: src.Timezone,
		Overlap:  overlapFromProto(src.Overlap),
	}
	if src.Interval != nil {
		cfg.Interval = *src.Interval
	}
	if src.CatchupWindow != nil {
		cfg.CatchupWindow = *src.CatchupWindow
	}
	return cfg
}

// scheduleConfigToProto is the reverse. It renders the DEFAULTS explicitly
// rather than echoing the empty strings that were stored: a client showing a
// trigger should see "UTC" and "skip", which is what the trigger actually
// does, not blanks it has to know how to interpret.
func scheduleConfigToProto(cfg core.ScheduleConfig) *reliantv1.ScheduleSource {
	src := &reliantv1.ScheduleSource{
		Cron:     cfg.Cron,
		Timezone: cfg.Timezone,
		Overlap:  overlapToProto(cfg.Overlap),
	}
	if src.Timezone == "" {
		src.Timezone = "UTC"
	}
	if cfg.Interval != "" {
		interval := cfg.Interval
		src.Interval = &interval
	}
	catchup := cfg.CatchupWindow
	if catchup == "" {
		catchup = DefaultCatchupWindow.String()
	}
	src.CatchupWindow = &catchup
	return src
}

func overlapFromProto(p reliantv1.TriggerOverlapPolicy) string {
	switch p {
	case reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW:
		return core.ScheduleOverlapAllow
	default:
		// UNSPECIFIED is SKIP. A client that omits the field gets the safe
		// behavior rather than concurrent unattended runs.
		return core.ScheduleOverlapSkip
	}
}

func overlapToProto(overlap string) reliantv1.TriggerOverlapPolicy {
	if overlap == core.ScheduleOverlapAllow {
		return reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_ALLOW
	}
	return reliantv1.TriggerOverlapPolicy_TRIGGER_OVERLAP_POLICY_SKIP
}

// ToProto renders a stored trigger. nextFireAt and firings are the read-only
// projections the caller resolved; either may be empty.
//
// firings is the trigger's recent window, newest first; the newest becomes
// last_event and the whole window yields health.
func ToProto(t *core.Trigger, nextFireAt *time.Time, firings []*core.TriggerEventWithRun) (*reliantv1.Trigger, error) {
	params, err := paramsToProto(t.Params)
	if err != nil {
		return nil, fmt.Errorf("trigger %s params: %w", t.ID, err)
	}

	out := &reliantv1.Trigger{
		Id:               t.ID,
		Name:             t.Name,
		ProjectId:        t.ProjectID,
		WorktreeId:       t.WorktreeID,
		Enabled:          t.Enabled,
		Workflow:         t.Workflow,
		Presets:          t.Presets,
		Params:           params,
		Message:          t.Message,
		DaemonId:         t.DaemonID,
		NotifyOnComplete: t.NotifyOnComplete,
		ProjectName:      t.ProjectName,
		DaemonName:       t.DaemonName,
		Health:           ComputeHealth(firings),
		Filter:           t.Filter,
		ConnectionId:     t.ConnectionID,
		CreatedAt:        t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        t.UpdatedAt.UTC().Format(time.RFC3339),
	}

	if err := sourceToProto(t, out); err != nil {
		return nil, err
	}

	if nextFireAt != nil {
		next := nextFireAt.UTC().Format(time.RFC3339)
		out.NextFireAt = &next
	}
	if len(firings) > 0 {
		ev, err := EventWithRunToProto(firings[0])
		if err != nil {
			return nil, err
		}
		out.LastEvent = ev
	}
	return out, nil
}

// EventToProto renders one firing.
func EventToProto(ev *core.TriggerEvent) (*reliantv1.TriggerEvent, error) {
	payload, err := structpb.NewStruct(ev.Payload)
	if err != nil {
		// The payload is recorded verbatim at fire time and is informational,
		// so a value that will not round-trip must not make the whole event
		// unreadable — that would hide the outcome, which is the part that
		// matters.
		payload = nil
	}
	return &reliantv1.TriggerEvent{
		Id:            ev.ID,
		TriggerId:     ev.TriggerID,
		Kind:          eventKindToProto(ev.Kind),
		OccurredAt:    ev.OccurredAt.UTC().Format(time.RFC3339),
		Outcome:       outcomeToProto(ev.Outcome),
		OutcomeDetail: ev.OutcomeDetail,
		ChatId:        ev.ChatID,
		Payload:       payload,
	}, nil
}

// EventWithRunToProto renders one firing together with the run it launched.
func EventWithRunToProto(ev *core.TriggerEventWithRun) (*reliantv1.TriggerEvent, error) {
	out, err := EventToProto(ev.Event)
	if err != nil {
		return nil, err
	}
	if ev.Run != nil {
		out.Run = &reliantv1.TriggerEventRun{
			DisplayState: reliantv1.RunDisplayState(ev.Run.DisplayState),
			Title:        ev.Run.Title,
			State:        reliantv1.WorkflowState(ev.Run.RootStatus.State),
			StopReason:   reliantv1.WorkflowStopReason(ev.Run.RootStatus.StopReason),
		}
	}
	return out, nil
}

func eventKindToProto(kind core.TriggerEventKind) reliantv1.TriggerEventKind {
	switch kind {
	case core.TriggerEventKindChatStart:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_CHAT_START
	case core.TriggerEventKindSchedule:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_SCHEDULE
	case core.TriggerEventKindAgentStartRun:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_AGENT_START_RUN
	case core.TriggerEventKindBuilderTest:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_BUILDER_TEST
	case core.TriggerEventKindWebhook:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_WEBHOOK
	case core.TriggerEventKindIntegration:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_INTEGRATION
	case core.TriggerEventKindWorkflowEvent:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_WORKFLOW_EVENT
	default:
		return reliantv1.TriggerEventKind_TRIGGER_EVENT_KIND_UNSPECIFIED
	}
}

func outcomeToProto(outcome core.TriggerEventOutcome) reliantv1.TriggerEventOutcome {
	switch outcome {
	case core.TriggerEventLaunched:
		return reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_LAUNCHED
	case core.TriggerEventSkipped:
		return reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_SKIPPED
	case core.TriggerEventFailed:
		return reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_FAILED
	case core.TriggerEventPending:
		return reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_PENDING
	default:
		return reliantv1.TriggerEventOutcome_TRIGGER_EVENT_OUTCOME_UNSPECIFIED
	}
}

// ParamsFromProto converts wire params to the stored shape.
func ParamsFromProto(params map[string]*structpb.Value) map[string]any {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]any, len(params))
	for k, v := range params {
		out[k] = v.AsInterface()
	}
	return out
}
