// Copyright (c) 2025 Reliant Labs
//
// Package triggertools lets an agent activate and list the triggers of the
// user it works for. It is the implementation behind the activate_trigger and
// list_triggers tools, which declare the narrow interface this package
// satisfies (internal/llm/tools cannot import the trigger service: it depends
// on that package).
//
// It adds no trigger logic of its own. Every call goes through TriggerService
// as the user, so ownership, declaration resolution, connection and daemon
// checks are exactly the API's — the agent cannot do anything the user could
// not do from the app.
//
//forge:exclude-contract: adapter that satisfies the consumer-declared TriggerActivator interface in internal/llm/tools; wiring, not a service
package triggertools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// TriggerAPI is the slice of the trigger service the tools use. It is
// satisfied by *services.TriggerService, whose handlers authorize the caller
// from the request context.
type TriggerAPI interface {
	CreateTrigger(context.Context, *connect.Request[reliantv1.CreateTriggerRequest]) (*connect.Response[reliantv1.CreateTriggerResponse], error)
	ListTriggers(context.Context, *connect.Request[reliantv1.ListTriggersRequest]) (*connect.Response[reliantv1.ListTriggersResponse], error)
}

// Triggers implements tools.TriggerActivator.
type Triggers struct{ api TriggerAPI }

var _ tools.TriggerActivator = (*Triggers)(nil)

// New builds the adapter.
func New(api TriggerAPI) *Triggers { return &Triggers{api: api} }

func asOwner(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, auth.UserIDContextKey, userID)
}

// ActivateTrigger creates the activation through CreateTrigger.
func (t *Triggers) ActivateTrigger(ctx context.Context, req tools.ActivateTriggerRequest) (tools.ActivatedTrigger, error) {
	if t.api == nil {
		return tools.ActivatedTrigger{}, errors.New("no trigger service is configured")
	}
	if req.OwnerUserID == "" {
		return tools.ActivatedTrigger{}, errors.New("no owner")
	}
	params, err := toParams(req.Params)
	if err != nil {
		return tools.ActivatedTrigger{}, fmt.Errorf("params: %w", err)
	}
	enabled := !req.Disabled
	def := &reliantv1.TriggerDefinition{
		Name:             req.Name,
		ProjectId:        req.ProjectID,
		Workflow:         req.Workflow,
		Presets:          req.Presets,
		Params:           params,
		Message:          req.Message,
		DaemonId:         req.DaemonID,
		NotifyOnComplete: req.NotifyOnSuccess,
		Enabled:          &enabled,
		Source:           &reliantv1.TriggerDefinition_WorkflowTrigger{WorkflowTrigger: req.WorkflowTrigger},
	}
	if req.WorktreeID != "" {
		def.WorktreeId = &req.WorktreeID
	}
	if req.ConnectionID != "" {
		def.ConnectionId = &req.ConnectionID
	}
	resp, err := t.api.CreateTrigger(asOwner(ctx, req.OwnerUserID), connect.NewRequest(&reliantv1.CreateTriggerRequest{Trigger: def}))
	if err != nil {
		return tools.ActivatedTrigger{}, unwrapConnect(err)
	}
	out := tools.ActivatedTrigger{Summary: summarize(resp.Msg.GetTrigger())}
	if hook := resp.Msg.GetWebhook(); hook != nil {
		out.WebhookToken = hook.GetToken()
		out.WebhookURLWithToken = hook.GetUrl()
	}
	return out, nil
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

func toParams(values map[string]any) (map[string]*structpb.Value, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]*structpb.Value, len(values))
	for name, value := range values {
		v, err := structpb.NewValue(value)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", name, err)
		}
		out[name] = v
	}
	return out, nil
}
