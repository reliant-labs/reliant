# Frozen: requires.cli_login and a run node's llm_gateway (before their removal)

Both keys shipped in v1.29.0 and were removed afterwards (owner decision,
2026-11-25). These two histories were recorded on `origin/main` at `0d3f7684e`,
the last commit that still had both keys, by the generator scenarios
reproduced below. They were run from a throwaway file, because neither
scenario compiles once the keys are gone.

| Fixture | Shape it pins | Kept replaying by |
|---|---|---|
| `requires_cli_login.json` | A user draft whose only requirement is `requires: {cli_login: [claude]}`, with a template in its run node, so the run re-parses its recorded YAML. `PreflightRequirements` is scheduled after the `preflight-requirements` marker, and fails here because the harness machine holds no checkout of the project. Then `WorkflowError`, `Cleanup` and the failed `WorkflowStatus`. | `cliLoginRemovedChangeID` (`runtime/removed_definition_keys.go`). The recorded definition (`ActivityLoadWorkflow`'s `workflow_json`) still declares `cliLogin`, and the history has no marker, so the run counts the sign-ins as a machine requirement and schedules the check it recorded. The recorded YAML's `cli_login` is dropped before the replayed template resolution parses it (`dropRemovedDefinitionKeys`). Without the gate, the removal skips `PreflightRequirements` (`requirements.Empty`) and this history fails to replay with TMPRL1100. |
| `run_llm_gateway.json` | A user draft whose run node sets `llm_gateway: {cli: claude-code, budget_usd: 2}`. The harness has no step-key issuer, so `ExecuteRunStep` refuses the step (`exit_code` -1), and the run completes. | No gate: `llm_gateway` changed no workflow command, because minting and revoking happened inside `ExecuteRunStep`. What replay needs is the recorded YAML's `llm_gateway`, written on the node as the load activity re-marshals it, dropped before the template resolution parses it (`dropRemovedDefinitionKeys`). Without that, the replayed parse fails with "unknown key llm_gateway" and the run fails where it had completed. |

Retire this set once no run recorded by a release with these keys can still
be open (v1.29.x and the release candidates after it).

## The scenarios that recorded it

They were placed in `internal/workflow/runtime/replaytest/` (build tag
`replayfixtures`) at `0d3f7684e`, and run with
`go test -tags replayfixtures -run 'TestGenerateFixture_(RequiresCLILogin|RunLLMGateway)$'`:

```go
const replayCLILoginWorkflowYAML = `name: replay-cli-login
description: requires.cli_login and nothing else the machine is checked for
inputs:
  model:
    type: model
    default:
      tags: [flagship]
  greeting:
    type: string
    default: hello
requires:
  cli_login: [claude]
entry: [say]
nodes:
  - id: say
    type: run
    args:
      command: echo "{{inputs.greeting}}"
`

const replayLLMGatewayWorkflowYAML = `name: replay-llm-gateway
description: a run node with an llm_gateway step key
inputs:
  model:
    type: model
    default:
      tags: [flagship]
  greeting:
    type: string
    default: hello
entry: [bench]
nodes:
  - id: bench
    type: run
    args:
      command: echo "{{inputs.greeting}}"
      llm_gateway:
        cli: claude-code
        budget_usd: 2
`

func recordRemovedKeyFixture(t *testing.T, name, yaml, fixture string) {
	h := newHarness(t, NewScriptedLLM())
	now := time.Now().UTC()
	require.NoError(t, h.Stack.Repo.CreateWorkflowDraft(h.Ctx, &db.WorkflowDraft{
		ID: uuid.New().String(), UserID: h.UserID, Name: name, Slug: name,
		Definition: yaml, Status: db.WorkflowDraftStatusComplete, CreatedAt: now, UpdatedAt: now,
	}))
	created := h.StartChat(name, "go", nil)
	// Done either way: the preflight may fail the run (the harness machine
	// holds no checkout of the project); the schedule is what is pinned.
	ctx, cancel := context.WithTimeout(h.Ctx, 2*time.Minute)
	defer cancel()
	err := h.Stack.Temporal.GetWorkflow(ctx, created.WorkflowId, "").Get(ctx, nil)
	t.Logf("%s ended: %v", fixture, err)
	h.ExportHistory(created.WorkflowId, fixture)
}

func TestGenerateFixture_RequiresCLILogin(t *testing.T) {
	recordRemovedKeyFixture(t, "replay-cli-login", replayCLILoginWorkflowYAML, "requires_cli_login")
}

func TestGenerateFixture_RunLLMGateway(t *testing.T) {
	recordRemovedKeyFixture(t, "replay-llm-gateway", replayLLMGatewayWorkflowYAML, "run_llm_gateway")
}
```
