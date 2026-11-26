// Copyright (c) 2025 Reliant Labs
package changetools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/require"

	cpv1 "github.com/reliant-labs/reliant/gen/controlplane/controlplane/v1"
	deployv1 "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

type fakeChangeProjects struct{ project *db.Project }

func (f fakeChangeProjects) GetProject(context.Context, string) (*db.Project, error) {
	return f.project, nil
}

type fakeChangeEnvs struct {
	asked []*deployv1.ListDeployEnvironmentsRequest
}

func (f *fakeChangeEnvs) ListEnvironments(_ context.Context, r *connect.Request[deployv1.ListDeployEnvironmentsRequest]) (*connect.Response[deployv1.ListDeployEnvironmentsResponse], error) {
	f.asked = append(f.asked, r.Msg)
	return connect.NewResponse(&deployv1.ListDeployEnvironmentsResponse{Environments: []*cpv1.DeployEnvironment{
		{Id: "env-prod", Name: "prod"}, {Id: "env-staging", Name: "staging"},
	}}), nil
}

// changesServer answers each ChangesService method with handle's reply and
// records the request bodies it got.
func changesServer(t *testing.T, handle func(method string, body map[string]any, w http.ResponseWriter)) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer user-jwt", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		body["_method"] = r.URL.Path
		got = append(got, body)
		handle(r.URL.Path[len(changesServicePath):], body, w)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func testChangeController(srv *httptest.Server, envs *fakeChangeEnvs) *Controller {
	org, forgeName := "org-1", "shop"
	return &Controller{
		projects: fakeChangeProjects{project: &db.Project{OrgID: &org, ForgeProjectName: &forgeName}},
		envs:     envs,
		rpc:      &changesHTTP{client: srv.Client(), baseURL: srv.URL},
		session:  func(string) (string, bool) { return "user-jwt", true },
	}
}

const protoChange = `{"change":{"id":"chg-1","orgId":"org-1","environmentId":"env-prod","key":"new-checkout","mode":"CHANGE_MODE_EXPERIMENT","unit":"CHANGE_UNIT_ACCOUNT","percentBp":2500,"split":[{"variant":"control","weightBp":5000},{"variant":"treatment","weightBp":5000}],"preview":[{"unitHash":"1a2b3c4d","variant":"treatment","label":"alice"}],"metric":{"event":"checkout_completed","windowHours":48,"minSamplePerArm":"500"},"currentRun":{"id":"r_abcdefgh","kind":"experiment","startedAt":"2026-11-19T10:00:00Z"},"revision":"9","declaration":{"type":"bool","defaultVariant":"control","variants":[{"name":"control","valueJson":"false"}]},"approvalRequired":true}}`

func TestChangeControllerReadsProtojson(t *testing.T) {
	srv, _ := changesServer(t, func(_ string, _ map[string]any, w http.ResponseWriter) { _, _ = io.WriteString(w, protoChange) })
	c := testChangeController(srv, &fakeChangeEnvs{})
	got, _, err := c.GetChange(context.Background(), "owner", "chg-1", 0)
	require.NoError(t, err)
	require.Equal(t, "experiment", got.Mode)
	require.Equal(t, "account", got.Unit)
	require.Equal(t, int64(9), got.Revision)
	require.Equal(t, "prod", got.EnvironmentName)
	require.Equal(t, int64(500), got.Metric.MinSamplePerArm)
	require.Equal(t, []tools.ChangePreviewEntry{{UnitHash: "1a2b3c4d", Variant: "treatment", Label: "alice"}}, got.Preview)
	require.Equal(t, "r_abcdefgh", got.Run.ID)
	require.True(t, got.ApprovalRequired)
	require.Equal(t, "control", got.Declaration.DefaultVariant)
}

func TestChangeControllerSendsTheRevisionAndScope(t *testing.T) {
	srv, got := changesServer(t, func(_ string, _ map[string]any, w http.ResponseWriter) { _, _ = io.WriteString(w, protoChange) })
	envs := &fakeChangeEnvs{}
	c := testChangeController(srv, envs)

	_, err := c.ApplyChange(context.Background(), "owner", tools.ChangeOp{Action: "update_draft", ChangeID: "chg-1", ExpectedRevision: 9, Unit: "account"})
	require.NoError(t, err)
	_, err = c.ApplyChange(context.Background(), "owner", tools.ChangeOp{Action: "stop", ChangeID: "chg-1"})
	require.NoError(t, err)
	_, err = c.CreateChange(context.Background(), "owner", "proj", tools.ChangeCreate{Environment: "staging", Key: "new-checkout", Unit: "visitor"})
	require.NoError(t, err)

	require.Equal(t, map[string]any{"_method": changesServicePath + "UpdateChangeDraft", "changeId": "chg-1", "expectedRevision": "9", "unit": "CHANGE_UNIT_ACCOUNT"}, (*got)[0])
	require.Equal(t, map[string]any{"_method": changesServicePath + "StopChange", "changeId": "chg-1"}, (*got)[1], "stop sends no revision")
	require.Equal(t, map[string]any{"_method": changesServicePath + "CreateChange", "environmentId": "env-staging", "key": "new-checkout", "unit": "CHANGE_UNIT_VISITOR"}, (*got)[2])
	require.Equal(t, "shop", envs.asked[len(envs.asked)-1].GetProject(), "the chat's forge project scopes the environment names")
}

func TestChangeControllerMapsRefusals(t *testing.T) {
	srv, _ := changesServer(t, func(method string, _ map[string]any, w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Forge-Error-Reason", "approval_required")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"permission_denied","message":"a person must approve this launch"}`)
	})
	c := testChangeController(srv, &fakeChangeEnvs{})
	_, err := c.ApplyChange(context.Background(), "owner", tools.ChangeOp{Action: "update_draft", ChangeID: "chg-1", ExpectedRevision: 9})
	var apiErr *tools.ChangeAPIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, tools.ChangeAPIError{Code: "permission_denied", Reason: "approval_required", Message: "a person must approve this launch"}, *apiErr)
}

func TestChangeControllerTreatsAMissingServiceAsUnimplemented(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	c := testChangeController(srv, &fakeChangeEnvs{})
	_, _, err := c.GetChange(context.Background(), "owner", "chg-1", 0)
	var apiErr *tools.ChangeAPIError
	require.True(t, errors.As(err, &apiErr))
	require.Equal(t, "unimplemented", apiErr.Code)
}

func TestChangeControllerBlocksExposureWithoutAgentCredentials(t *testing.T) {
	for _, action := range []string{"launch", "ramp", "roll_out"} {
		t.Run(action, func(t *testing.T) {
			// Nil collaborators ensure rejection happens before session lookup,
			// environment resolution or any RPC, even with human approval input.
			c := &Controller{}
			_, err := c.ApplyChange(context.Background(), "owner", tools.ChangeOp{
				Action: action, ChangeID: "chg-1", ExpectedRevision: 9,
				PercentBP: 10000, Everyone: true, StepUpCode: "123456",
			})
			var apiErr *tools.ChangeAPIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, "permission_denied", apiErr.Code)
			require.Equal(t, "agent_credential_required", apiErr.Reason)
			require.Contains(t, apiErr.Message, "Experiments page")
		})
	}
}

func TestChangeControllerNeedsALiveSession(t *testing.T) {
	c := &Controller{session: func(string) (string, bool) { return "", false }}
	_, err := c.ListChanges(context.Background(), "owner", "proj", tools.ChangeFilter{})
	require.ErrorContains(t, err, "no live sign-in")
	// The user's state to fix, so INFO, not an ERROR issue (prod 0fb2d2a9).
	require.True(t, svcerr.IsUserError(err))
}

// A ChangesService refusal is graded by who must act on it: a change id that
// does not exist is the caller's, a reasonless permission refusal is ours.
func TestChangeAPIErrorClass(t *testing.T) {
	for _, tc := range []struct {
		err  *tools.ChangeAPIError
		want svcerr.Class
	}{
		{&tools.ChangeAPIError{Code: "not_found", Message: "change not found"}, svcerr.ClassUser},
		{&tools.ChangeAPIError{Code: "failed_precondition", Reason: "change_revision_conflict"}, svcerr.ClassUser},
		{&tools.ChangeAPIError{Code: "permission_denied", Reason: "agent_credential_required"}, svcerr.ClassUser},
		{&tools.ChangeAPIError{Code: "permission_denied"}, svcerr.ClassServer},
		{&tools.ChangeAPIError{Code: "internal"}, svcerr.ClassServer},
	} {
		require.Equal(t, tc.want, svcerr.Classify(tc.err), "%+v", tc.err)
	}
}

const protoReadout = `{"changeId":"chg-1","key":"new-checkout","run":{"id":"r_abcdefgh","kind":"experiment"},"verdict":"CHANGE_READOUT_VERDICT_INCONCLUSIVE","reason":"below_min_sample","arms":[{"variant":"control","weightBp":5000,"assigned":"40","windowComplete":"30","conversions":"3","rate":0.1,"ciLow":0.034,"ciHigh":0.256,"pending":"10","expectedAssigned":41.5},{"variant":"treatment","weightBp":5000,"assigned":"43","windowComplete":"31","rate":0,"ciHigh":0.11}],"comparisons":[{"treatment":"treatment","control":"control","absLift":-0.1,"diffCiLow":-0.25,"diffCiHigh":0.02,"pValue":0.07,"alpha":0.05,"verdict":"CHANGE_READOUT_VERDICT_INCONCLUSIVE","reason":"below_min_sample"}],"srm":{"skipped":true},"assigned":"83","pending":"22","readoutStart":"2026-11-19T10:00:00Z","readoutEnd":"2026-11-20T10:00:00Z","readoutEndReason":"now","metric":{"event":"checkout_completed","windowHours":168,"minSamplePerArm":"100","controlVariant":"control"},"alpha":0.05,"horizonNote":"Decide once."}`

func TestChangeControllerReadsTheReadout(t *testing.T) {
	srv, got := changesServer(t, func(_ string, _ map[string]any, w http.ResponseWriter) { _, _ = io.WriteString(w, protoReadout) })
	c := testChangeController(srv, &fakeChangeEnvs{})
	r, err := c.GetChangeReadout(context.Background(), "owner", "chg-1", "")
	require.NoError(t, err)
	require.Equal(t, map[string]any{"_method": changesServicePath + "GetChangeReadout", "changeId": "chg-1"}, (*got)[0], "no run id lets the server pick")
	require.Equal(t, "inconclusive", r.Verdict)
	require.Equal(t, "r_abcdefgh", r.RunID)
	require.Equal(t, int64(40), r.Arms[0].Assigned)
	require.Equal(t, int64(0), r.Arms[1].Conversions, "a zero value protojson omits")
	require.InDelta(t, 0.256, r.Arms[0].CIHigh, 1e-9)
	require.Nil(t, r.Comparisons[0].RelLift)
	require.Equal(t, "inconclusive", r.Comparisons[0].Verdict)
	require.True(t, r.SRM.Skipped)
	require.Equal(t, int64(100), r.Metric.MinSamplePerArm)
	require.Equal(t, int64(83), r.Assigned)
}
