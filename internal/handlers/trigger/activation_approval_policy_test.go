package trigger

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/stretchr/testify/require"
)

// setApprovalMode stores the owner's automation-approvals setting: the
// account's when project is nil, else that project's override.
func (e *triggerTestEnv) setApprovalMode(t *testing.T, project *string, mode automationpolicy.ApprovalMode) {
	t.Helper()
	existing, err := e.repo.GetSetting(e.ctx, e.userID, project, automationpolicy.ApprovalSettingKey)
	if err == nil && existing != nil {
		existing.Value = string(mode)
		require.NoError(t, e.repo.UpdateSetting(e.ctx, existing))
		return
	}
	now := time.Now().UTC()
	require.NoError(t, e.repo.CreateSetting(e.ctx, &db.Setting{ID: uuid.NewString(), UserID: e.userID, ProjectID: project, Key: automationpolicy.ApprovalSettingKey, Value: string(mode), ValueType: "string", CreatedAt: now, UpdatedAt: now}))
}

// flipIdentity is identity and the other acting identity.
func flipIdentity(identity string) (string, string) {
	if identity == "owner" {
		return "owner", "app"
	}
	return identity, "owner"
}

// The default policy, on_capability_change: an edit that leaves the
// activation's power alone applies with no card, attended or not, and is
// recorded; one that widens it needs the card; a pause never does.
func TestAgentActivationApprovalOnCapabilityChange(t *testing.T) {
	testkit.Slow(t, "DB-backed activation approval policy")
	env := setupTriggerTest(t)
	// A control plane holds the GitHub App, so both acting identities are valid.
	env.svc.deps.ControlPlane = &fakeControlPlaneClient{}
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	machine := env.daemonID
	message := "Run nightly checks"
	create := tools.ActivationParams{Name: "nightly", Workflow: "triage", WorkflowTrigger: "nightly", ProjectID: env.projectID, MachineID: &machine, Message: &message}
	preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create)
	require.NoError(t, err)
	require.True(t, preview.CardRequired, "a new activation is new standing power")
	require.Contains(t, preview.Title, "Approval: A new activation")
	require.True(t, strings.HasPrefix(preview.Title, "create_trigger_activation triage · nightly\n"), "the card's first line names the change: %q", preview.Title)
	_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create, "")
	require.ErrorContains(t, err, tools.ApprovalRequiredMessage)
	created, err := env.svc.ManageActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create, preview.Hash)
	require.NoError(t, err)
	history, err := env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Equal(t, "card", history[0].Approval)
	stored, err := env.repo.GetTrigger(env.ctx, created.ID)
	require.NoError(t, err)
	identity, other := flipIdentity(string(stored.ActingIdentity.OrOwner()))
	require.Contains(t, history[0].CapabilitiesAdded, "identity:"+identity)
	require.Contains(t, history[0].CapabilitiesAdded, "machine:"+machine)

	// A definition change: new message and params. No card, from an
	// interactive or an unattended run, and audit-logged with the diff.
	edited := "Run the nightly checks, then summarize"
	edit := tools.ActivationParams{TriggerID: created.ID, Message: &edited, Params: map[string]any{"mode": "fast"}}
	next, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, edit)
	require.NoError(t, err)
	require.False(t, next.CardRequired, next.Reason)
	unattended := automationpolicy.WithExecution(env.ctx, automationpolicy.Execution{Unattended: true})
	_, err = env.svc.ManageActivation(unattended, env.userID, tools.ToolUpdateTriggerActivation, edit, "")
	require.NoError(t, err)
	history, err = env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, "update", history[0].Operation)
	require.Equal(t, "auto:on_capability_change", history[0].Approval)
	require.Empty(t, history[0].CapabilitiesAdded)
	require.Empty(t, history[0].CapabilitiesRemoved)
	require.NotEmpty(t, history[0].BeforeState)

	// A different acting identity widens: a card, which an unattended run
	// cannot get.
	widen := tools.ActivationParams{TriggerID: created.ID, ActingIdentity: &other}
	card, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, widen)
	require.NoError(t, err)
	require.True(t, card.CardRequired)
	require.Contains(t, card.Reason, "identity:"+other)
	require.Contains(t, card.Title, "identity:"+other)
	_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, widen, "")
	require.ErrorContains(t, err, tools.ApprovalRequiredMessage)
	_, err = env.svc.ManageActivation(unattended, env.userID, tools.ToolUpdateTriggerActivation, widen, card.Hash)
	require.ErrorContains(t, err, automationpolicy.Refusal())

	// A new machine widens too.
	newMachine := uuid.NewString()
	hostname := "cicd"
	require.NoError(t, env.repo.UpsertDaemon(env.ctx, &db.Daemon{ID: newMachine, UserID: env.userID, Name: "cicd", Hostname: &hostname}))
	move, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, tools.ActivationParams{TriggerID: created.ID, MachineID: &newMachine})
	require.NoError(t, err)
	require.True(t, move.CardRequired)
	require.Contains(t, move.Reason, "machine:"+newMachine)

	// Pausing and resuming leave its power alone: no card, unattended too.
	id := tools.ActivationParams{TriggerID: created.ID}
	_, err = env.svc.ManageActivation(unattended, env.userID, tools.ToolPauseTrigger, id, "")
	require.NoError(t, err)
	resume, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolEnableTrigger, id)
	require.NoError(t, err)
	require.False(t, resume.CardRequired, resume.Reason)
	got, err := env.svc.ManageActivation(unattended, env.userID, tools.ToolEnableTrigger, id, "")
	require.NoError(t, err)
	require.True(t, got.Enabled)
	history, err = env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Equal(t, "enable", history[0].Operation)
	require.Equal(t, "auto:on_capability_change", history[0].Approval)
	require.Equal(t, "pause", history[1].Operation)
	require.Equal(t, "auto:pause", history[1].Approval)
}

// always asks for every change but a pause; never applies everything and
// records it; a project's setting overrides the account's.
func TestAgentActivationApprovalModes(t *testing.T) {
	testkit.Slow(t, "DB-backed activation approval policy")
	env := setupTriggerTest(t)
	// A control plane holds the GitHub App, so both acting identities are valid.
	env.svc.deps.ControlPlane = &fakeControlPlaneClient{}
	env.saveWorkflow(t, "triage", declaringWorkflowYAML)
	machine := env.daemonID
	message := "Run nightly checks"
	create := tools.ActivationParams{Name: "nightly", Workflow: "triage", WorkflowTrigger: "nightly", ProjectID: env.projectID, MachineID: &machine, Message: &message}

	// Account: never. A create needs no card, unattended included.
	env.setApprovalMode(t, nil, automationpolicy.ApprovalNever)
	preview, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolCreateTriggerActivation, create)
	require.NoError(t, err)
	require.False(t, preview.CardRequired, preview.Reason)
	unattended := automationpolicy.WithExecution(env.ctx, automationpolicy.Execution{Unattended: true})
	created, err := env.svc.ManageActivation(unattended, env.userID, tools.ToolCreateTriggerActivation, create, "")
	require.NoError(t, err)
	history, err := env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Equal(t, "auto:never", history[0].Approval)
	require.Contains(t, history[0].CapabilitiesAdded, "machine:"+machine)

	stored, err := env.repo.GetTrigger(env.ctx, created.ID)
	require.NoError(t, err)
	identity, other := flipIdentity(string(stored.ActingIdentity.OrOwner()))
	widen := tools.ActivationParams{TriggerID: created.ID, ActingIdentity: &other}
	_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, widen, "")
	require.NoError(t, err, "never applies a widening change too")
	history, err = env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Equal(t, "auto:never", history[0].Approval)
	require.Equal(t, []string{"identity:" + other}, history[0].CapabilitiesAdded)
	require.Equal(t, []string{"identity:" + identity}, history[0].CapabilitiesRemoved)

	// This project: always. Its override wins over the account's never.
	env.setApprovalMode(t, &env.projectID, automationpolicy.ApprovalAlways)
	edited := "a definition change"
	edit := tools.ActivationParams{TriggerID: created.ID, Message: &edited}
	card, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, edit)
	require.NoError(t, err)
	require.True(t, card.CardRequired)
	_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, edit, "")
	require.ErrorContains(t, err, tools.ApprovalRequiredMessage)
	_, err = env.svc.ManageActivation(env.ctx, env.userID, tools.ToolUpdateTriggerActivation, edit, card.Hash)
	require.NoError(t, err)
	history, err = env.repo.ListTriggerChanges(env.ctx, env.userID, created.ID)
	require.NoError(t, err)
	require.Equal(t, "card", history[0].Approval)

	// A pause is never a card, even under always.
	id := tools.ActivationParams{TriggerID: created.ID}
	pause, err := env.svc.PrepareActivation(env.ctx, env.userID, tools.ToolPauseTrigger, id)
	require.NoError(t, err)
	require.False(t, pause.CardRequired)
	_, err = env.svc.ManageActivation(unattended, env.userID, tools.ToolPauseTrigger, id, "")
	require.NoError(t, err)
}
