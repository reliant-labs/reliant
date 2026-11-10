// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// triggerReads are the TriggerService rpcs that change nothing. Every other
// rpc must have a case in TestEveryTriggerMutationActsInItsOrg.
var triggerReads = map[string]bool{
	"GetTrigger": true, "ListTriggers": true, "ListTriggerEvents": true, "GetLaunchEvent": true,
	"ResolveTriggerSenders": true, "ListReplayEvents": true, "PreviewReplay": true,
	"DraftAutomation": true, // drafts a definition for review; stores nothing
}

const scheduledWorkflowYAML = `name: nightly
triggers:
  - name: every-night
    schedule:
      cron: ["0 3 * * *"]
entry: [a]
nodes:
  - id: a
    type: run
    command: echo hi
`

// Every mutating TriggerService rpc acts in the org of what it changes: a
// caller who has left the org, and a credential bound to another org, are
// refused, and nothing is stored, synced, fired or deleted. The rpcs are
// enumerated from the service descriptor, so a new mutating rpc fails this
// test until it has a case here.
func TestEveryTriggerMutationActsInItsOrg(t *testing.T) {
	t.Parallel()
	env, _ := setupInboundTriggerTest(t)
	env.withOrgs(t)
	acme := "acme"
	acmeProject := env.orgProject(t, &acme)
	env.saveWorkflow(t, "nightly", scheduledWorkflowYAML)
	scheduled, err := env.createIn(env.ctx, acmeProject, nil)
	require.NoError(t, err)
	webhook, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: webhookDefinition(env, func(d *reliantv1.TriggerDefinition) { d.ProjectId = acmeProject }),
	}))
	require.NoError(t, err)
	// An activation of the saved workflow, so DisableWorkflow has one to pause.
	enabled, err := env.svc.EnableWorkflow(env.ctx, connect.NewRequest(&reliantv1.EnableWorkflowRequest{
		Workflow: "nightly", ProjectId: acmeProject, DaemonId: env.daemonID, Message: "Run the nightly job.",
	}))
	require.NoError(t, err)
	require.True(t, enabled.Msg.GetEnabled(), "the activation the DisableWorkflow case pauses: %v", enabled.Msg.GetResults())

	id := scheduled.GetId()
	calls := map[string]func(ctx context.Context) error{
		"CreateTrigger": func(ctx context.Context) error {
			_, err := env.createIn(ctx, acmeProject, nil)
			return err
		},
		"UpdateTrigger": func(ctx context.Context) error {
			_, err := env.svc.UpdateTrigger(ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
				Id: id, Trigger: env.definition(func(d *reliantv1.TriggerDefinition) {
					d.ProjectId = acmeProject
					d.Message = "Reconfigured."
				}),
			}))
			return err
		},
		"DeleteTrigger": func(ctx context.Context) error {
			_, err := env.svc.DeleteTrigger(ctx, connect.NewRequest(&reliantv1.DeleteTriggerRequest{Id: id}))
			return err
		},
		"SetTriggerEnabled": func(ctx context.Context) error {
			_, err := env.svc.SetTriggerEnabled(ctx, connect.NewRequest(&reliantv1.SetTriggerEnabledRequest{Id: id, Enabled: false}))
			return err
		},
		"FireTrigger": func(ctx context.Context) error {
			_, err := env.svc.FireTrigger(ctx, connect.NewRequest(&reliantv1.FireTriggerRequest{Id: id}))
			return err
		},
		"RotateWebhookToken": func(ctx context.Context) error {
			_, err := env.svc.RotateWebhookToken(ctx, connect.NewRequest(&reliantv1.RotateWebhookTokenRequest{Id: webhook.Msg.GetTrigger().GetId()}))
			return err
		},
		"EnableWorkflow": func(ctx context.Context) error {
			_, err := env.svc.EnableWorkflow(ctx, connect.NewRequest(&reliantv1.EnableWorkflowRequest{
				Workflow: "nightly", ProjectId: acmeProject, DaemonId: env.daemonID, NamePrefix: "again", Message: "Again.",
			}))
			return err
		},
		// DisableWorkflow reports each activation it could not pause rather
		// than failing; the unchanged store is what proves the refusal.
		"DisableWorkflow": func(ctx context.Context) error {
			resp, err := env.svc.DisableWorkflow(ctx, connect.NewRequest(&reliantv1.DisableWorkflowRequest{Workflow: "nightly"}))
			if err == nil && len(resp.Msg.GetFailed()) > 0 {
				return svcerr.NotFound("trigger")
			}
			return err
		},
	}

	methods := reliantv1.File_reliant_v1_trigger_proto.Services().ByName("TriggerService").Methods()
	for i := range methods.Len() {
		name := string(methods.Get(i).Name())
		if !triggerReads[name] && calls[name] == nil {
			t.Errorf("TriggerService.%s is neither a read nor covered here: add a case that it acts in its org", name)
		}
	}

	snapshot := func() string {
		t.Helper()
		stored, err := env.repo.ListTriggers(context.Background(), core.TriggerFilters{UserID: env.userID})
		require.NoError(t, err)
		sort.Slice(stored, func(i, j int) bool { return stored[i].ID < stored[j].ID })
		env.backend.mu.Lock()
		defer env.backend.mu.Unlock()
		out, err := json.Marshal(map[string]any{
			"triggers": stored, "synced": len(env.backend.synced), "deleted": len(env.backend.deleted), "fires": len(env.backend.fires),
		})
		require.NoError(t, err)
		return string(out)
	}

	removed := orgs.New(leftAcmeSource{})
	callers := map[string]func() context.Context{
		"a removed member": func() context.Context {
			env.configure(t, func(d *Deps) { d.Orgs = removed })
			return authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), removed)
		},
		"a credential bound to another org": func() context.Context {
			live := orgs.New(triggerOrgSource{})
			env.configure(t, func(d *Deps) { d.Orgs = live })
			ctx := authz.WithMembership(context.WithValue(context.Background(), auth.UserIDContextKey, env.userID), live)
			return auth.WithMachineToken(ctx, &fat.Principal{OrgID: "home", ActingUserID: env.userID})
		},
	}
	for who, caller := range callers {
		ctx := caller()
		for name, call := range calls {
			before := snapshot()
			err := call(ctx)
			code := svcerr.Code(err)
			require.Truef(t, code == connect.CodeNotFound || code == connect.CodePermissionDenied,
				"%s: %s was not refused for its org: %v", who, name, err)
			require.Equal(t, before, snapshot(), "%s: %s changed what is stored", who, name)
		}
	}
}
