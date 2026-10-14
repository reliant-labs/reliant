// Copyright (c) 2025 Reliant Labs
//
// Package triggertools lets an agent list the triggers of the user it works
// for. It is the implementation behind the list_triggers tool, which declares
// the narrow interface this package satisfies (internal/llm/tools cannot
// import the trigger service: it depends on that package).
//
// It is read-only on purpose. Enabling a workflow's triggers is the user's act,
// done in the app, so TriggerAPI has no write method for an agent to reach.
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

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// TriggerAPI is the slice of the trigger service the tool reads. It is
// satisfied by *services.TriggerService, whose handlers authorize the caller
// from the request context.
type TriggerAPI interface {
	ListTriggers(context.Context, *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error)
}

// Triggers implements tools.TriggerLister.
type Triggers struct{ api TriggerAPI }

var _ tools.TriggerLister = (*Triggers)(nil)

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
		ID:              tr.GetId(),
		Name:            tr.GetName(),
		Workflow:        tr.GetWorkflow(),
		WorkflowTrigger: tr.GetWorkflowTrigger(),
		Enabled:         tr.GetEnabled(),
		ProjectID:       tr.GetProjectId(),
		DaemonID:        tr.GetDaemonId(),
		ConnectionID:    tr.GetConnectionId(),
		WebhookURL:      tr.GetWebhookUrl(),
		NextFireAt:      tr.GetNextFireAt(),
		Filter:          tr.GetFilter(),
		Health:          healthName(tr.GetHealth().GetStatus()),
		HealthDetail:    tr.GetHealth().GetLastFailureDetail(),
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

// unwrapConnect turns a handler's connect error into the message the agent
// reads; the code adds nothing it can act on.
func unwrapConnect(err error) error {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return errors.New(ce.Message())
	}
	return err
}
