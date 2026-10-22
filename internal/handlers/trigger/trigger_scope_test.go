// Copyright (c) 2025 Reliant Labs
package trigger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers"
)

const scopedActivationYAML = `name: triage
inputs:
  repos:
    type: github.repository
    multi: true
    required: true
    min_items: 1
triggers:
  - name: pr
    integration:
      integration: test
      events: [pull_request.opened]
      match: {repository: {input: repos}}
entry: [a]
nodes:
  - id: a
    type: approval
    args: {title: ok}
`

func (e *triggerTestEnv) storedScope(t *testing.T, id string) core.IntegrationConfig {
	t.Helper()
	stored, err := e.repo.GetTrigger(context.Background(), id)
	require.NoError(t, err)
	var cfg core.IntegrationConfig
	require.NoError(t, json.Unmarshal(stored.Config, &cfg))
	return cfg
}

func reposParam(t *testing.T, repos ...string) map[string]*structpb.Value {
	t.Helper()
	list := make([]any, len(repos))
	for i, r := range repos {
		list[i] = r
	}
	v, err := structpb.NewValue(list)
	require.NoError(t, err)
	return map[string]*structpb.Value{"repos": v}
}

// An activation stores its scope resolved from its params, on create and on
// every update of them, so routing never needs the workflow to decide.
func TestActivationProjectsTheScopeFromItsParams(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", scopedActivationYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)

	created := env.create(t, env.activation("pr", func(d *reliantv1.TriggerDefinition) {
		d.ConnectionId = &conn.ID
		d.Params = reposParam(t, "acme/app", "acme/web")
	}))
	cfg := env.storedScope(t, created.GetId())
	assert.Equal(t, map[string]string{"repository": "repos"}, cfg.MatchInput)
	assert.Equal(t, map[string][]string{"repository": {"acme/app", "acme/web"}}, cfg.MatchAny)

	update := func(repos ...string) {
		t.Helper()
		_, err := env.svc.UpdateTrigger(env.ctx, connect.NewRequest(&reliantv1.UpdateTriggerRequest{
			Id: created.GetId(),
			Trigger: env.activation("pr", func(d *reliantv1.TriggerDefinition) {
				d.ConnectionId = &conn.ID
				d.Params = reposParam(t, repos...)
			}),
		}))
		require.NoError(t, err)
	}
	update("acme/api")
	assert.Equal(t, map[string][]string{"repository": {"acme/api"}}, env.storedScope(t, created.GetId()).MatchAny,
		"editing the params re-projects the scope")

	// The scope as routing sees it.
	cfg = env.storedScope(t, created.GetId())
	assert.True(t, matches(cfg, "acme/api"))
	assert.False(t, matches(cfg, "acme/app"), "a repository removed from the params stops routing")
}

// An activation holding no repositories projects a scope that matches nothing:
// it is never wider than what the owner chose. A required scope input cannot
// be left out any more (T5 refuses it), so this is the optional form
// (`default: []`), which is how a workflow says "off until you pick some".
func TestActivationWithoutParamsProjectsAnEmptyScope(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", strings.Replace(scopedActivationYAML,
		"    required: true\n    min_items: 1\n", "    default: []\n", 1))
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)

	created, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.activation("pr", func(d *reliantv1.TriggerDefinition) { d.ConnectionId = &conn.ID }),
	}))
	require.NoError(t, err)
	cfg := env.storedScope(t, created.Msg.GetTrigger().GetId())
	assert.Equal(t, map[string][]string{"repository": {}}, cfg.MatchAny)
	assert.False(t, matches(cfg, "acme/app"), "an empty scope never fires")
}

// The required form refuses an activation with no repositories outright.
func TestRequiredScopeInputCannotBeLeftOutOfAnActivation(t *testing.T) {
	env, _ := setupInboundTriggerTest(t)
	env.saveWorkflow(t, "triage", scopedActivationYAML)
	conn := env.createConnection(t, env.userID, "test", "acct-1", core.ConnectionStatusActive)

	_, err := env.svc.CreateTrigger(env.ctx, connect.NewRequest(&reliantv1.CreateTriggerRequest{
		Trigger: env.activation("pr", func(d *reliantv1.TriggerDefinition) { d.ConnectionId = &conn.ID }),
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	assert.Contains(t, err.Error(), "repos is required")
}

func matches(cfg core.IntegrationConfig, repo string) bool {
	return triggers.IntegrationEventMatches(cfg, "pull_request.opened", map[string]string{"repository": repo})
}
