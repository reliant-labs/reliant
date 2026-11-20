package tools

import (
	"context"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/ctxkeys"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/stretchr/testify/require"
)

// fakeChanges is one change, new-checkout in prod, and every op applied to it.
type fakeChanges struct {
	change   ChangeSummary
	ops      []ChangeOp
	creates  []ChangeCreate
	applyErr error
}

func newFakeChanges() *fakeChanges {
	return &fakeChanges{change: ChangeSummary{
		ID: "chg-1", Key: "new-checkout", EnvironmentID: "env-prod", EnvironmentName: "prod",
		Mode: "experiment", Unit: "user", PercentBP: 1000, Revision: 7, ApprovalRequired: true,
		Run:         &ChangeRunSummary{ID: "r_abcdefgh", Kind: "experiment", StartedAt: time.Now().Add(-3 * time.Hour)},
		Declaration: &ChangeDeclaration{Type: "bool", DefaultVariant: "control", Variants: []ChangeVariant{{Name: "control", ValueJSON: "false"}, {Name: "treatment", ValueJSON: "true"}}},
	}}
}

func (f *fakeChanges) ListChanges(context.Context, string, string, ChangeFilter) ([]ChangeSummary, error) {
	return []ChangeSummary{f.change}, nil
}
func (f *fakeChanges) GetChange(_ context.Context, _ string, id string, _ int) (ChangeSummary, []ChangeEvent, error) {
	if id != f.change.ID {
		return ChangeSummary{}, nil, &ChangeAPIError{Code: "not_found"}
	}
	return f.change, []ChangeEvent{{Revision: 7, ActorKind: "person", ActorID: "alice", Action: "launch", ApprovalMethod: "console_session", ApprovedBy: "alice"}}, nil
}
func (f *fakeChanges) CreateChange(_ context.Context, _ string, _ string, in ChangeCreate) (ChangeSummary, error) {
	f.creates = append(f.creates, in)
	return ChangeSummary{ID: "chg-new", Key: in.Key, EnvironmentName: in.Environment, Mode: "off", Unit: "user", Revision: 1}, nil
}
func (f *fakeChanges) ApplyChange(_ context.Context, _ string, op ChangeOp) (ChangeSummary, error) {
	f.ops = append(f.ops, op)
	if f.applyErr != nil {
		return ChangeSummary{}, f.applyErr
	}
	next := f.change
	next.Revision++
	if op.Action == "stop" {
		next.Mode, next.Run = "off", nil
	}
	if op.PercentBP > 0 {
		next.PercentBP = op.PercentBP
	}
	return next, nil
}
func (f *fakeChanges) RequestChangeApproval(context.Context, string, string) (ChangeApprovalCode, error) {
	return ChangeApprovalCode{SentTo: "a***@example.com"}, nil
}

func newTestChangeTool(f *fakeChanges) Tool {
	return NewManageChangeTool(triggerCallerStore{chat: &db.Chat{UserID: "owner", ProjectID: "proj"}}, f)
}

// changeCtx is a tool context for call id "call" with exec's approvals.
func changeCtx(exec automationpolicy.Execution) *rctx.ToolContext {
	ctx := machineToolCtx(exec)
	ctx.Context = context.WithValue(ctx.Context, ctxkeys.ToolCallContextKey, &ctxkeys.ToolCallContext{CurrentToolCallID: "call"})
	return ctx
}

func prepareChange(t *testing.T, tool Tool, input string, unattended bool) (ActivationPreview, bool, error) {
	t.Helper()
	return tool.(AutomationApprovalTool).PrepareAutomation(changeCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolManageChange, Input: input}, unattended)
}

func runChange(t *testing.T, tool Tool, input string, exec automationpolicy.Execution) ToolResponse {
	t.Helper()
	resp, err := tool.Run(changeCtx(exec), ToolCall{ID: "call", Name: ToolManageChange, Input: input})
	require.NoError(t, err)
	return resp
}

// launch, roll_out and a ramp up widen who gets a change: each runs only
// with an approval for exactly that action, change, revision and percentage.
func TestManageChangeWideningActionsNeedTheirOwnApproval(t *testing.T) {
	for _, tc := range []struct{ name, input, title string }{
		{"launch", `{"action":"launch","change_id":"chg-1","percent":25}`, "Launch experiment new-checkout in prod to 25% of users?"},
		{"ramp up", `{"action":"ramp","change_id":"chg-1","percent":20}`, "Ramp new-checkout in prod from 10% to 20% of users?"},
		{"roll out", `{"action":"roll_out","change_id":"chg-1","variant":"treatment","everyone":true}`, "Roll out treatment of new-checkout to everyone in prod?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeChanges()
			tool := newTestChangeTool(f)
			preview, ask, err := prepareChange(t, tool, tc.input, false)
			require.NoError(t, err)
			require.True(t, ask)
			require.Contains(t, preview.Title, tc.title)
			require.Contains(t, preview.Title, "protected environment")

			resp := runChange(t, tool, tc.input, automationpolicy.Execution{})
			require.True(t, resp.IsError)
			require.Contains(t, resp.Content, "approval card")
			resp = runChange(t, tool, tc.input, automationpolicy.Execution{Approvals: map[string]string{"call": changeApprovalHash("launch", "chg-1", 7, 9999, "", false)}})
			require.True(t, resp.IsError, "an approval for another percentage does not carry over")
			require.Empty(t, f.ops)

			resp = runChange(t, tool, tc.input, automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}})
			require.False(t, resp.IsError, resp.Content)
			require.Len(t, f.ops, 1)
			require.Equal(t, int64(7), f.ops[0].ExpectedRevision, "the write is checked against the revision the card showed")
		})
	}
}

// A card approves the change as it was. One that moved since is not what
// the user approved, so nothing runs.
func TestManageChangeApprovalIsBoundToTheRevision(t *testing.T) {
	f := newFakeChanges()
	tool := newTestChangeTool(f)
	input := `{"action":"launch","change_id":"chg-1","percent":25}`
	preview, ask, err := prepareChange(t, tool, input, false)
	require.NoError(t, err)
	require.True(t, ask)

	f.change.Revision = 8
	resp := runChange(t, tool, input, automationpolicy.Execution{Approvals: map[string]string{"call": preview.Hash}})
	require.True(t, resp.IsError)
	require.Empty(t, f.ops)
}

// Reducing exposure never waits for anyone: stop, end_run and a ramp down
// raise no card, and stop sends no revision (the kill switch never loses a race).
func TestManageChangeReducingExposureNeedsNoCard(t *testing.T) {
	for _, tc := range []struct{ name, input, action string }{
		{"stop", `{"action":"stop","change_id":"chg-1"}`, "stop"},
		{"end run", `{"action":"end_run","change_id":"chg-1"}`, "end_run"},
		{"ramp down", `{"action":"ramp","change_id":"chg-1","percent":5}`, "ramp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeChanges()
			tool := newTestChangeTool(f)
			_, ask, err := prepareChange(t, tool, tc.input, false)
			require.NoError(t, err)
			require.False(t, ask)

			resp := runChange(t, tool, tc.input, automationpolicy.Execution{})
			require.False(t, resp.IsError, resp.Content)
			require.Len(t, f.ops, 1)
			require.Equal(t, tc.action, f.ops[0].Action)
			if tc.action == "stop" {
				require.Zero(t, f.ops[0].ExpectedRevision)
				require.Contains(t, resp.Content, "everyone gets the default")
			}
		})
	}
}

func TestManageChangeUnattendedOnlyReducesExposure(t *testing.T) {
	f := newFakeChanges()
	tool := newTestChangeTool(f)
	for _, input := range []string{
		`{"action":"launch","change_id":"chg-1","percent":25}`,
		`{"action":"ramp","change_id":"chg-1","percent":50}`,
		`{"action":"roll_out","change_id":"chg-1","variant":"treatment","everyone":true}`,
	} {
		_, _, err := prepareChange(t, tool, input, true)
		require.Error(t, err, input)
		resp := runChange(t, tool, input, automationpolicy.Execution{Unattended: true})
		require.True(t, resp.IsError, input)
	}
	require.Empty(t, f.ops)

	for _, input := range []string{`{"action":"stop","change_id":"chg-1"}`, `{"action":"ramp","change_id":"chg-1","percent":5}`} {
		_, ask, err := prepareChange(t, tool, input, true)
		require.NoError(t, err, input)
		require.False(t, ask)
		resp := runChange(t, tool, input, automationpolicy.Execution{Unattended: true})
		require.False(t, resp.IsError, resp.Content)
	}
	require.Len(t, f.ops, 2)
}

func TestManageChangeValidatesArguments(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{`{"action":"explode","change_id":"chg-1"}`, "explode"},
		{`{"action":"launch","change_id":"chg-1"}`, "launch needs percent"},
		{`{"action":"launch"}`, "needs change_id"},
		{`{"action":"launch","change_id":"chg-1","percent":120}`, "between 0 and 100"},
		{`{"action":"stop","change_id":"chg-1","percent":10}`, "does not apply to stop"},
		{`{"action":"ramp","change_id":"chg-1","percent":5,"variant":"treatment"}`, "does not apply to ramp"},
		{`{"action":"roll_out","change_id":"chg-1","variant":"treatment"}`, "everyone=true or percent"},
		{`{"action":"roll_out","change_id":"chg-1","everyone":true}`, "roll_out needs variant"},
		{`{"action":"draft","key":"new-checkout"}`, "needs key and environment"},
		{`{"action":"draft","change_id":"chg-1","key":"other"}`, "omit them to edit"},
		{`{"action":"draft","change_id":"chg-1","split":[{"variant":"control","weight_percent":50},{"variant":"treatment","weight_percent":40}]}`, "add up to 90%"},
		{`{"action":"launch","change_id":"chg-1","percent":5,"description":"x"}`, "does not apply to launch"},
		{`{"action":"stop","change_id":"chg-1","preview":[{"unit_id":"u","variant":"treatment"}]}`, "does not apply to stop"},
	} {
		f := newFakeChanges()
		resp := runChange(t, newTestChangeTool(f), tc.input, automationpolicy.Execution{})
		require.True(t, resp.IsError, tc.input)
		require.Contains(t, resp.Content, tc.want, tc.input)
		require.Empty(t, f.ops, tc.input)
	}
}

// The server's refusals become what to do next, never its raw code.
func TestManageChangeExplainsRefusals(t *testing.T) {
	for _, tc := range []struct {
		err  *ChangeAPIError
		want []string
	}{
		{&ChangeAPIError{Code: "permission_denied", Reason: "approval_required"}, []string{"Experiments page", "request_approval", "step_up_code"}},
		{&ChangeAPIError{Code: "failed_precondition", Reason: "change_revision_conflict"}, []string{"get_change", "new revision"}},
		{&ChangeAPIError{Code: "failed_precondition", Reason: "variant_not_declared"}, []string{"does not declare that variant"}},
		{&ChangeAPIError{Code: "unimplemented"}, []string{"does not run Forge Changes yet"}},
	} {
		f := newFakeChanges()
		f.change.PercentBP = 5000
		f.applyErr = tc.err
		resp := runChange(t, newTestChangeTool(f), `{"action":"ramp","change_id":"chg-1","percent":10}`, automationpolicy.Execution{})
		require.True(t, resp.IsError)
		for _, w := range tc.want {
			require.Contains(t, resp.Content, w)
		}
		require.Contains(t, resp.Content, "Nothing changed")
	}
}

func TestManageChangeDraftCreatesThenSavesTheDraft(t *testing.T) {
	f := newFakeChanges()
	resp := runChange(t, newTestChangeTool(f), `{"action":"draft","key":"new-checkout","environment":"staging","unit":"account","split":[{"variant":"control","weight_percent":50},{"variant":"treatment","weight_percent":50}],"metric":{"event":"checkout_completed"}}`, automationpolicy.Execution{})
	require.False(t, resp.IsError, resp.Content)
	require.Equal(t, []ChangeCreate{{Environment: "staging", Key: "new-checkout", Unit: "account"}}, f.creates)
	require.Len(t, f.ops, 1)
	op := f.ops[0]
	require.Equal(t, "update_draft", op.Action)
	require.Equal(t, "chg-new", op.ChangeID)
	require.Equal(t, int64(1), op.ExpectedRevision)
	require.Equal(t, []ChangeSplitArm{{Variant: "control", WeightBP: 5000}, {Variant: "treatment", WeightBP: 5000}}, op.Split)
	require.Equal(t, "checkout_completed", op.Metric.Event)
	require.Contains(t, resp.Content, "does not declare it yet")
}

func TestChangeToolsAreRegisteredAsServerTools(t *testing.T) {
	factory := NewToolsFactory(nil)
	for _, name := range []string{ToolListChanges, ToolGetChange, ToolManageChange} {
		require.NotNil(t, factory.GetToolByName(name, nil), name)
		require.False(t, NeedsMachine(name), name)
	}
	require.Equal(t, PermissionOrchestrator, MinimumPermissionForTool(ToolManageChange))
	require.NotEmpty(t, UnattendedWithholding(ToolManageChange))
	require.Empty(t, UnattendedWithholding(ToolListChanges))
}

func TestListChangesSummarizesEachChange(t *testing.T) {
	tool := NewListChangesTool(triggerCallerStore{chat: &db.Chat{UserID: "owner", ProjectID: "proj"}}, newFakeChanges())
	resp, err := tool.Run(changeCtx(automationpolicy.Execution{}), ToolCall{ID: "call", Name: ToolListChanges, Input: `{}`})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)
	require.Contains(t, resp.Content, "new-checkout (id chg-1) in prod: experiment on 10% of users; run r_abcdefgh open 3h; revision 7")
}
