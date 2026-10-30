// Copyright (c) 2025 Reliant Labs
//
// Package triggertools lets an agent inspect and manually fire existing triggers
// of the user it works for. The tools declare the narrow interfaces this adapter
// satisfies (internal/llm/tools cannot import the trigger service).
//
// Enabling a workflow's triggers remains the user's act, done in the app.
// TriggerAPI cannot create, enable, edit or delete an activation.
// Every call goes through TriggerService as the user, so ownership is exactly
// the API's.
//
//forge:exclude-contract: adapter that satisfies the consumer-declared TriggerLister interface in internal/llm/tools; wiring, not a service
package triggertools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// TriggerAPI is the slice of the trigger service the tools can reach. It is
// satisfied by *trigger.Service (internal/handlers/trigger), whose handlers
// authorize the caller from the request context.
type TriggerAPI interface {
	ListTriggers(context.Context, *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error)
	FireTrigger(context.Context, *connect.Request[reliantv1.FireTriggerRequest]) (*connect.Response[reliantv1.FireTriggerResponse], error)
	ListTriggerEvents(context.Context, *connect.Request[reliantv1.ListTriggerEventsRequest]) (*connect.Response[reliantv1.ListTriggerEventsResponse], error)
}

// Triggers implements tools.TriggerLister.
type Triggers struct{ api TriggerAPI }

var _ tools.TriggerLister = (*Triggers)(nil)
var _ tools.TriggerFirer = (*Triggers)(nil)
var _ tools.TriggerEventLister = (*Triggers)(nil)

// New builds the adapter.
func New(api TriggerAPI) *Triggers { return &Triggers{api: api} }

func asOwner(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, auth.UserIDContextKey, userID)
}

// ListTriggers lists the user's triggers, narrowed by project (server-side)
// and workflow.
func (t *Triggers) ListTriggers(ctx context.Context, userID string, f tools.TriggerListFilter) ([]tools.TriggerSummary, error) {
	if t.api == nil {
		return nil, errors.New("no trigger service is configured")
	}
	req := &reliantv1.ListTriggersRequest{}
	if f.ProjectID != "" {
		req.ProjectId = &f.ProjectID
	}
	resp, err := t.api.ListTriggers(asOwner(ctx, userID), connect.NewRequest(req))
	if err != nil {
		return nil, unwrapConnect(err)
	}
	out := make([]tools.TriggerSummary, 0, len(resp.Msg.GetTriggers()))
	for _, tr := range resp.Msg.GetTriggers() {
		if f.Workflow != "" && !sameWorkflow(tr.GetWorkflow(), f.Workflow) {
			continue
		}
		out = append(out, summarize(tr))
	}
	return out, nil
}

// FireTrigger requests one asynchronous manual fire using only saved settings.
func (t *Triggers) FireTrigger(ctx context.Context, userID, triggerID string) (string, error) {
	if t.api == nil {
		return "", errors.New("no trigger service is configured")
	}
	resp, err := t.api.FireTrigger(asOwner(ctx, userID), connect.NewRequest(&reliantv1.FireTriggerRequest{Id: triggerID}))
	if err != nil {
		return "", unwrapConnect(err)
	}
	return resp.Msg.GetFireWorkflowId(), nil
}

// ListTriggerEvents projects only operational summaries; source payloads and
// sender data are deliberately not exposed to the agent.
func (t *Triggers) ListTriggerEvents(ctx context.Context, userID, triggerID string, limit int, pageToken string) (tools.TriggerEventPage, error) {
	if t.api == nil {
		return tools.TriggerEventPage{}, errors.New("no trigger service is configured")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	req := &reliantv1.ListTriggerEventsRequest{TriggerId: triggerID, Limit: int32(limit)}
	if pageToken != "" {
		req.PageToken = &pageToken
	}
	resp, err := t.api.ListTriggerEvents(asOwner(ctx, userID), connect.NewRequest(req))
	if err != nil {
		return tools.TriggerEventPage{}, unwrapConnect(err)
	}
	page := tools.TriggerEventPage{Events: make([]tools.TriggerEventSummary, 0, len(resp.Msg.GetEvents())), NextPageToken: resp.Msg.GetNextPageToken()}
	for _, ev := range resp.Msg.GetEvents() {
		s := tools.TriggerEventSummary{FireWorkflowID: ev.GetFireWorkflowId(), ID: ev.GetId(), OccurredAt: ev.GetOccurredAt(), Outcome: strings.TrimPrefix(ev.GetOutcome().String(), "TRIGGER_EVENT_OUTCOME_"), Detail: ev.GetOutcomeDetail(), ChatID: ev.GetChatId()}
		if ev.GetRun() != nil {
			s.RunState = strings.TrimPrefix(ev.GetRun().GetDisplayState().String(), "RUN_DISPLAY_STATE_")
		}
		page.Events = append(page.Events, s)
	}
	return page, nil
}

// sameWorkflow compares workflow refs as slugs: "Code Review" names the same
// workflow as "code-review".
func sameWorkflow(a, b string) bool {
	norm := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "-"), "_", "-")
	}
	return norm(a) == norm(b)
}

func summarize(tr *reliantv1.Trigger) tools.TriggerSummary {
	s := tools.TriggerSummary{
		ID:                 tr.GetId(),
		Name:               tr.GetName(),
		Workflow:           tr.GetWorkflow(),
		WorkflowTrigger:    tr.GetWorkflowTrigger(),
		Enabled:            tr.GetEnabled(),
		ProjectID:          tr.GetProjectId(),
		DaemonID:           tr.GetDaemonId(),
		DaemonName:         tr.GetDaemonName(),
		MachineState:       tr.GetMachineState(),
		NoMachine:          tr.GetNoMachine(),
		ConnectionID:       tr.GetConnectionId(),
		WebhookURL:         tr.GetWebhookUrl(),
		NextFireAt:         tr.GetNextFireAt(),
		Filter:             tr.GetFilter(),
		Health:             healthName(tr.GetHealth().GetStatus()),
		HealthDetail:       tr.GetHealth().GetLastFailureDetail(),
		Definition:         tr.GetDefinitionSource().GetSummary(),
		DefinitionWarnings: tr.GetDefinitionSource().GetWarnings(),
	}
	s.Kind, s.Source = describeSource(tr)
	return s
}

func healthName(status reliantv1.TriggerHealthStatus) string {
	name := strings.TrimPrefix(status.String(), "TRIGGER_HEALTH_STATUS_")
	if name == "UNSPECIFIED" || name == "" {
		return "UNKNOWN"
	}
	return name
}

// describeSource is the kind and a one-line description of what fires the
// trigger.
func describeSource(tr *reliantv1.Trigger) (string, string) {
	switch src := tr.GetSource().(type) {
	case *reliantv1.Trigger_Schedule:
		parts := []string{}
		if len(src.Schedule.GetCron()) > 0 {
			parts = append(parts, "cron "+strings.Join(src.Schedule.GetCron(), " | "))
		}
		if src.Schedule.Interval != nil {
			parts = append(parts, "every "+src.Schedule.GetInterval())
		}
		return "schedule", strings.Join(parts, ", ") + " (" + src.Schedule.GetTimezone() + ")"
	case *reliantv1.Trigger_Webhook:
		desc := "a POST to its webhook URL"
		if src.Webhook.GetHmac() != nil {
			desc += " (token or HMAC signature)"
		}
		return "webhook", desc
	case *reliantv1.Trigger_Integration:
		desc := fmt.Sprintf("%s events %s", src.Integration.GetIntegration(), strings.Join(src.Integration.GetEvents(), ", "))
		if m := src.Integration.GetMatch(); len(m) > 0 {
			pairs := make([]string, 0, len(m))
			for k, v := range m {
				pairs = append(pairs, k+"="+v)
			}
			desc += " where " + strings.Join(pairs, ", ")
		}
		return "integration", desc
	case *reliantv1.Trigger_WorkflowEvent:
		workflows := "any workflow"
		if w := src.WorkflowEvent.GetWorkflows(); len(w) > 0 {
			workflows = strings.Join(w, ", ")
		}
		outcomes := "finished, failed or blocked"
		if o := src.WorkflowEvent.GetOutcomes(); len(o) > 0 {
			outcomes = strings.Join(o, " or ")
		}
		return "workflow_event", fmt.Sprintf("a run of %s that %s", workflows, outcomes)
	default:
		return "unknown", "unknown source"
	}
}

// unwrapConnect turns a handler's error into the message the agent reads —
// what the svcerr interceptor would send a client, so an unclassified
// failure's own text stays server-side; the code adds nothing it can act on.
func unwrapConnect(err error) error {
	return errors.New(svcerr.ToConnect(err).Message())
}
