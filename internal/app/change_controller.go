// Copyright (c) 2025 Reliant Labs
package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/observe"

	deployv1 "github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1"
	"github.com/reliant-labs/reliant/gen/controlplane/services/deploy/v1/controlplanev1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/tools"
)

// changeController is tools.ChangeController for the worker: Forge Changes
// through the control plane's ChangesService. Browser sessions are not agent
// credentials: until a scoped agent credential contract is wired here, launch,
// ramp and roll-out fail closed. Other operations use the owner's session.
//
// ── WHY HAND-WRITTEN JSON, FOR NOW ──────────────────────────────────────────
// ChangesService (control-plane proto/services/changes/v1/changes.proto) is
// not on control-plane main yet, so it is not in proto-vendor/controlplane/
// and there is no generated client: `task contract:controlplane:check`
// refuses a vendored proto its SOURCE commit does not export. Until it is,
// the rpcs are called in the Connect protocol's JSON form (protojson field
// names, enums by name, int64 as strings), the way projectOrgMoveCall
// reaches the move rpcs (internal/controlplane/project_org_move.go). Once the
// proto is vendored, changesRPC becomes the generated client and nothing
// above it changes.
type changeController struct {
	projects changeProjects
	envs     changeEnvironments
	rpc      changeRPC
	// session is the user's live session token (auth.GetUserJWT).
	session func(userID string) (string, bool)
}

// changeProjects is how the calling chat's project names its org and forge
// project, the defaults for a list or a new change.
type changeProjects interface {
	GetProject(ctx context.Context, id string) (*db.Project, error)
}

// changeEnvironments lists an org's deploy environments, to name an
// environment by the name a person uses for it.
type changeEnvironments interface {
	ListEnvironments(context.Context, *connect.Request[deployv1.ListDeployEnvironmentsRequest]) (*connect.Response[deployv1.ListDeployEnvironmentsResponse], error)
}

// changeRPC calls one ChangesService rpc as the bearer of jwt: in and out
// are its request and response in protojson form.
type changeRPC interface {
	call(ctx context.Context, jwt, method string, in, out any) error
}

// newChangeController is nil when self-hosted: Changes live in the control
// plane, and the tools say so.
func newChangeController(repo changeProjects, router *WorkerDaemonRouter) tools.ChangeController {
	if router == nil || router.ControlPlaneURL == "" {
		return nil
	}
	httpClient := observe.NewHTTPClient(observe.HTTPClientOptions{Timeout: 30 * time.Second})
	base := strings.TrimRight(router.ControlPlaneURL, "/")
	return &changeController{
		projects: repo,
		envs:     controlplanev1connect.NewDeployServiceClient(httpClient, base),
		rpc:      &changesHTTP{client: httpClient, baseURL: base},
		session:  auth.GetUserJWT,
	}
}

const changesServicePath = "/controlplane.v1.ChangesService/"

// changesHTTP is changeRPC over the Connect protocol's unary JSON form.
type changesHTTP struct {
	client  *http.Client
	baseURL string
}

func (h *changesHTTP) call(ctx context.Context, jwt, method string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.baseURL+changesServicePath+method, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := h.client.Do(req)
	if err != nil {
		return fmt.Errorf("controlplane: %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("controlplane: reading %s response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return changeErrorOf(resp, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("controlplane: decoding %s response: %w", method, err)
	}
	return nil
}

// changeErrorOf is a Connect error response as a *tools.ChangeAPIError. A
// 404 without a Connect body is a control plane that does not serve the
// service at all: unimplemented.
func changeErrorOf(resp *http.Response, body []byte) error {
	var rpcErr struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &rpcErr)
	if rpcErr.Code == "" {
		if resp.StatusCode == http.StatusNotFound {
			rpcErr.Code = "unimplemented"
		} else {
			rpcErr.Code = strings.ToLower(strings.ReplaceAll(http.StatusText(resp.StatusCode), " ", "_"))
		}
	}
	return &tools.ChangeAPIError{Code: rpcErr.Code, Reason: resp.Header.Get("X-Forge-Error-Reason"), Message: rpcErr.Message}
}

// asUserSession is the user's live session token, or the error a tool shows.
func (c *changeController) asUserSession(userID string) (string, error) {
	jwt, ok := c.session(userID)
	if !ok || strings.TrimSpace(jwt) == "" {
		return "", errors.New("no live sign-in to act as the user: they can do this in the Experiments page of the Forge console, or send a message in this chat to refresh the session")
	}
	return strings.TrimSpace(jwt), nil
}

// scope fills an org and forge project the caller left out from the calling
// chat's project.
func (c *changeController) scope(ctx context.Context, projectID, orgID, project string) (string, string, error) {
	if (orgID == "" || project == "") && projectID != "" && c.projects != nil {
		if p, err := c.projects.GetProject(ctx, projectID); err == nil && p != nil {
			if orgID == "" && p.OrgID != nil {
				orgID = *p.OrgID
			}
			if project == "" && p.ForgeProjectName != nil {
				project = *p.ForgeProjectName
			}
		}
	}
	if orgID == "" {
		return "", "", errors.New("the calling chat's project belongs to no org; name org_id")
	}
	return orgID, project, nil
}

// environments is the org's environments (one forge project's when project
// is set), by id.
func (c *changeController) environments(ctx context.Context, jwt, orgID, project string) (map[string]string, error) {
	req := &deployv1.ListDeployEnvironmentsRequest{OrgId: &orgID}
	if project != "" {
		req.Project = &project
	}
	r := connect.NewRequest(req)
	r.Header().Set("Authorization", "Bearer "+jwt)
	resp, err := c.envs.ListEnvironments(ctx, r)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(resp.Msg.GetEnvironments()))
	for _, e := range resp.Msg.GetEnvironments() {
		out[e.GetId()] = e.GetName()
	}
	return out, nil
}

// resolveEnvironment is the id of the environment env names: a name of the
// project's environments, or one of their ids.
func (c *changeController) resolveEnvironment(ctx context.Context, jwt, orgID, project, env string) (string, error) {
	envs, err := c.environments(ctx, jwt, orgID, project)
	if err != nil {
		return "", fmt.Errorf("could not list the environments: %w", err)
	}
	if _, ok := envs[env]; ok {
		return env, nil
	}
	var names []string
	for id, name := range envs {
		if name == env {
			return id, nil
		}
		names = append(names, name)
	}
	where := "this org"
	if project != "" {
		where = "project " + project
	}
	return "", fmt.Errorf("%s has no environment %q (it has: %s)", where, env, strings.Join(names, ", "))
}

func (c *changeController) ListChanges(ctx context.Context, userID, projectID string, f tools.ChangeFilter) ([]tools.ChangeSummary, error) {
	jwt, err := c.asUserSession(userID)
	if err != nil {
		return nil, err
	}
	orgID, project, err := c.scope(ctx, projectID, f.OrgID, f.Project)
	if err != nil {
		return nil, err
	}
	req := map[string]any{"orgId": orgID, "pageSize": 500}
	if project != "" {
		req["project"] = project
	}
	if f.Environment != "" {
		id, err := c.resolveEnvironment(ctx, jwt, orgID, project, f.Environment)
		if err != nil {
			return nil, err
		}
		req["environmentId"] = id
	}
	var out []tools.ChangeSummary
	for page := 0; page < 10; page++ {
		var resp struct {
			Changes       []changeJSON `json:"changes"`
			NextPageToken string       `json:"nextPageToken"`
		}
		if err := c.rpc.call(ctx, jwt, "ListChanges", req, &resp); err != nil {
			return nil, err
		}
		for _, ch := range resp.Changes {
			out = append(out, ch.summary())
		}
		if resp.NextPageToken == "" {
			break
		}
		req["pageToken"] = resp.NextPageToken
	}
	// Best effort: an id is still an answer when the names cannot be read.
	if envs, err := c.environments(ctx, jwt, orgID, project); err == nil {
		for i := range out {
			out[i].EnvironmentName = envs[out[i].EnvironmentID]
		}
	}
	return out, nil
}

func (c *changeController) GetChange(ctx context.Context, userID, changeID string, events int) (tools.ChangeSummary, []tools.ChangeEvent, error) {
	jwt, err := c.asUserSession(userID)
	if err != nil {
		return tools.ChangeSummary{}, nil, err
	}
	var resp struct {
		Change changeJSON `json:"change"`
	}
	if err := c.rpc.call(ctx, jwt, "GetChange", map[string]any{"changeId": changeID}, &resp); err != nil {
		return tools.ChangeSummary{}, nil, err
	}
	out := resp.Change.summary()
	if envs, err := c.environments(ctx, jwt, resp.Change.OrgID, ""); err == nil {
		out.EnvironmentName = envs[out.EnvironmentID]
	}
	if events <= 0 {
		return out, nil, nil
	}
	var trail struct {
		Events []changeEventJSON `json:"events"`
	}
	if err := c.rpc.call(ctx, jwt, "ListChangeEvents", map[string]any{"changeId": changeID, "pageSize": events}, &trail); err != nil {
		// The change is the answer; its trail is extra.
		return out, nil, nil
	}
	list := make([]tools.ChangeEvent, 0, len(trail.Events))
	for _, e := range trail.Events {
		list = append(list, tools.ChangeEvent{Revision: int64(e.Revision), ActorKind: e.ActorKind, ActorID: e.ActorID, Action: e.Action,
			DiffJSON: e.DiffJSON, RunID: e.RunID, ApprovalMethod: e.ApprovalMethod, ApprovedBy: e.ApprovedBy, At: e.CreatedAt})
	}
	return out, list, nil
}

func (c *changeController) CreateChange(ctx context.Context, userID, projectID string, in tools.ChangeCreate) (tools.ChangeSummary, error) {
	jwt, err := c.asUserSession(userID)
	if err != nil {
		return tools.ChangeSummary{}, err
	}
	orgID, project, err := c.scope(ctx, projectID, in.OrgID, in.Project)
	if err != nil {
		return tools.ChangeSummary{}, err
	}
	envID, err := c.resolveEnvironment(ctx, jwt, orgID, project, in.Environment)
	if err != nil {
		return tools.ChangeSummary{}, err
	}
	req := map[string]any{"environmentId": envID, "key": in.Key}
	if in.Description != "" {
		req["description"] = in.Description
	}
	if unit := unitEnum(in.Unit); unit != "" {
		req["unit"] = unit
	}
	var resp struct {
		Change changeJSON `json:"change"`
	}
	if err := c.rpc.call(ctx, jwt, "CreateChange", req, &resp); err != nil {
		return tools.ChangeSummary{}, err
	}
	out := resp.Change.summary()
	if envs, err := c.environments(ctx, jwt, orgID, project); err == nil {
		out.EnvironmentName = envs[out.EnvironmentID]
	}
	return out, nil
}

// changeMethods are the ChangesService rpc each ChangeOp action calls.
var changeMethods = map[string]string{
	"update_draft": "UpdateChangeDraft",
	"set_preview":  "SetChangePreview",
	"launch":       "LaunchChange",
	"ramp":         "RampChange",
	"roll_out":     "RollOutChange",
	"stop":         "StopChange",
	"end_run":      "EndChangeRun",
}

func (c *changeController) ApplyChange(ctx context.Context, userID string, op tools.ChangeOp) (tools.ChangeSummary, error) {
	// Never send an exposure-increasing agent action as a human console
	// session, even with a step-up code or a prior interactive confirmation.
	// Block every ramp: deciding whether it increases exposure requires state.
	switch op.Action {
	case "launch", "ramp", "roll_out":
		return tools.ChangeSummary{}, &tools.ChangeAPIError{
			Code: "permission_denied", Reason: "agent_credential_required",
			Message: "agent launch, ramp and roll-out are unavailable until scoped agent credentials are supported; use the Experiments page of the Forge console",
		}
	}
	method, ok := changeMethods[op.Action]
	if !ok {
		return tools.ChangeSummary{}, fmt.Errorf("unknown change action %q", op.Action)
	}
	jwt, err := c.asUserSession(userID)
	if err != nil {
		return tools.ChangeSummary{}, err
	}
	var resp struct {
		Change changeJSON `json:"change"`
	}
	if err := c.rpc.call(ctx, jwt, method, changeOpRequest(op), &resp); err != nil {
		return tools.ChangeSummary{}, err
	}
	out := resp.Change.summary()
	if envs, err := c.environments(ctx, jwt, resp.Change.OrgID, ""); err == nil {
		out.EnvironmentName = envs[out.EnvironmentID]
	}
	return out, nil
}

// changeOpRequest is op as its rpc's protojson request.
func changeOpRequest(op tools.ChangeOp) map[string]any {
	req := map[string]any{"changeId": op.ChangeID}
	if op.ExpectedRevision > 0 {
		req["expectedRevision"] = strconv.FormatInt(op.ExpectedRevision, 10)
	}
	if op.StepUpCode != "" {
		req["stepUpCode"] = op.StepUpCode
	}
	switch op.Action {
	case "update_draft":
		if op.Description != nil {
			req["description"] = *op.Description
		}
		if unit := unitEnum(op.Unit); unit != "" {
			req["unit"] = unit
		}
		if op.Audience != nil {
			req["audience"] = map[string]any{"attribute": op.Audience.Attribute, "in": op.Audience.In}
		}
		if op.ClearAudience {
			req["clearAudience"] = true
		}
		if op.Split != nil {
			arms := make([]map[string]any, 0, len(op.Split))
			for _, arm := range op.Split {
				arms = append(arms, map[string]any{"variant": arm.Variant, "weightBp": arm.WeightBP})
			}
			req["split"] = map[string]any{"arms": arms}
		}
		if m := op.Metric; m != nil {
			metric := map[string]any{"event": m.Event, "prop": m.Prop, "propValue": m.PropValue, "windowHours": m.WindowHours, "controlVariant": m.ControlVariant}
			if m.MinSamplePerArm > 0 {
				metric["minSamplePerArm"] = strconv.FormatInt(m.MinSamplePerArm, 10)
			}
			req["metric"] = metric
		}
		if op.ClearMetric {
			req["clearMetric"] = true
		}
	case "set_preview":
		entries := make([]map[string]any, 0, len(op.Preview))
		for _, e := range op.Preview {
			entries = append(entries, map[string]any{"unitId": e.UnitID, "variant": e.Variant, "label": e.Label})
		}
		req["entries"] = entries
		if op.Activate {
			req["activate"] = true
		}
	case "launch", "ramp":
		req["percentBp"] = op.PercentBP
	case "roll_out":
		req["variant"] = op.Variant
		if op.Everyone {
			req["everyone"] = true
		} else {
			req["percentBp"] = op.PercentBP
		}
	}
	return req
}

func (c *changeController) RequestChangeApproval(ctx context.Context, userID, changeID string) (tools.ChangeApprovalCode, error) {
	jwt, err := c.asUserSession(userID)
	if err != nil {
		return tools.ChangeApprovalCode{}, err
	}
	var resp struct {
		SentTo    string    `json:"sentTo"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := c.rpc.call(ctx, jwt, "RequestChangeApproval", map[string]any{"changeId": changeID}, &resp); err != nil {
		return tools.ChangeApprovalCode{}, err
	}
	return tools.ChangeApprovalCode{SentTo: resp.SentTo, ExpiresAt: resp.ExpiresAt}, nil
}

func (c *changeController) GetChangeReadout(ctx context.Context, userID, changeID, runID string) (tools.ChangeReadout, error) {
	jwt, err := c.asUserSession(userID)
	if err != nil {
		return tools.ChangeReadout{}, err
	}
	req := map[string]any{"changeId": changeID}
	if runID != "" {
		req["runId"] = runID
	}
	var resp readoutJSON
	if err := c.rpc.call(ctx, jwt, "GetChangeReadout", req, &resp); err != nil {
		return tools.ChangeReadout{}, err
	}
	return resp.readout(), nil
}

// ── protojson shapes (controlplane.v1.Change and friends) ──────────────────

// jsonFloat reads a protojson double: a number, or the strings "NaN",
// "Infinity", "-Infinity" (read as 0, which the readout never sends for a
// value it means).
type jsonFloat float64

func (f *jsonFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || s == "NaN" || strings.HasSuffix(s, "Infinity") {
		*f = 0
		return nil
	}
	*f = jsonFloat(v)
	return nil
}

type readoutJSON struct {
	ChangeID string `json:"changeId"`
	Key      string `json:"key"`
	Run      struct {
		ID string `json:"id"`
	} `json:"run"`
	Verdict     string `json:"verdict"`
	Reason      string `json:"reason"`
	BestVariant string `json:"bestVariant"`
	Arms        []struct {
		Variant          string    `json:"variant"`
		WeightBP         int32     `json:"weightBp"`
		Assigned         jsonInt64 `json:"assigned"`
		WindowComplete   jsonInt64 `json:"windowComplete"`
		Conversions      jsonInt64 `json:"conversions"`
		Rate             jsonFloat `json:"rate"`
		CILow            jsonFloat `json:"ciLow"`
		CIHigh           jsonFloat `json:"ciHigh"`
		Pending          jsonInt64 `json:"pending"`
		ExpectedAssigned jsonFloat `json:"expectedAssigned"`
	} `json:"arms"`
	Comparisons []struct {
		Treatment   string     `json:"treatment"`
		Control     string     `json:"control"`
		AbsLift     jsonFloat  `json:"absLift"`
		RelLift     *jsonFloat `json:"relLift"`
		DiffCILow   jsonFloat  `json:"diffCiLow"`
		DiffCIHigh  jsonFloat  `json:"diffCiHigh"`
		PValue      jsonFloat  `json:"pValue"`
		Alpha       jsonFloat  `json:"alpha"`
		Significant bool       `json:"significant"`
		Verdict     string     `json:"verdict"`
		Reason      string     `json:"reason"`
	} `json:"comparisons"`
	SRM struct {
		PValue            jsonFloat `json:"pValue"`
		ChiSquare         jsonFloat `json:"chiSquare"`
		Failed            bool      `json:"failed"`
		Skipped           bool      `json:"skipped"`
		UnplannedVariants []string  `json:"unplannedVariants"`
	} `json:"srm"`
	Conflicted           jsonInt64         `json:"conflicted"`
	Assigned             jsonInt64         `json:"assigned"`
	Pending              jsonInt64         `json:"pending"`
	UndeclaredVariants   []string          `json:"undeclaredVariants"`
	ReadoutStart         time.Time         `json:"readoutStart"`
	ReadoutEnd           time.Time         `json:"readoutEnd"`
	ReadoutEndReason     string            `json:"readoutEndReason"`
	Final                bool              `json:"final"`
	Metric               *changeMetricJSON `json:"metric"`
	Alpha                jsonFloat         `json:"alpha"`
	HorizonNote          string            `json:"horizonNote"`
	TruncatedByRetention bool              `json:"truncatedByRetention"`
}

func (r readoutJSON) readout() tools.ChangeReadout {
	out := tools.ChangeReadout{
		ChangeID: r.ChangeID, Key: r.Key, RunID: r.Run.ID,
		Verdict: enumWord(r.Verdict, "CHANGE_READOUT_VERDICT_"), Reason: r.Reason, BestVariant: r.BestVariant,
		SRM: tools.ChangeReadoutSRM{PValue: float64(r.SRM.PValue), ChiSquare: float64(r.SRM.ChiSquare), Failed: r.SRM.Failed,
			Skipped: r.SRM.Skipped, UnplannedVariants: r.SRM.UnplannedVariants},
		Conflicted: int64(r.Conflicted), Assigned: int64(r.Assigned), Pending: int64(r.Pending), UndeclaredVariants: r.UndeclaredVariants,
		ReadoutStart: r.ReadoutStart, ReadoutEnd: r.ReadoutEnd, ReadoutEndReason: r.ReadoutEndReason, Final: r.Final,
		Alpha: float64(r.Alpha), HorizonNote: r.HorizonNote, TruncatedByRetention: r.TruncatedByRetention,
	}
	if out.Verdict == "" {
		out.Verdict = "no_data"
	}
	for _, a := range r.Arms {
		out.Arms = append(out.Arms, tools.ChangeReadoutArm{Variant: a.Variant, WeightBP: a.WeightBP, Assigned: int64(a.Assigned),
			WindowComplete: int64(a.WindowComplete), Conversions: int64(a.Conversions), Rate: float64(a.Rate), CILow: float64(a.CILow),
			CIHigh: float64(a.CIHigh), Pending: int64(a.Pending), ExpectedAssigned: float64(a.ExpectedAssigned)})
	}
	for _, c := range r.Comparisons {
		cmp := tools.ChangeReadoutComparison{Treatment: c.Treatment, Control: c.Control, AbsLift: float64(c.AbsLift),
			DiffCILow: float64(c.DiffCILow), DiffCIHigh: float64(c.DiffCIHigh), PValue: float64(c.PValue), Alpha: float64(c.Alpha),
			Significant: c.Significant, Verdict: enumWord(c.Verdict, "CHANGE_READOUT_VERDICT_"), Reason: c.Reason}
		if c.RelLift != nil {
			v := float64(*c.RelLift)
			cmp.RelLift = &v
		}
		out.Comparisons = append(out.Comparisons, cmp)
	}
	if m := r.Metric; m != nil {
		out.Metric = &tools.ChangeMetric{Event: m.Event, Prop: m.Prop, PropValue: m.PropValue, WindowHours: m.WindowHours,
			MinSamplePerArm: int64(m.MinSamplePerArm), ControlVariant: m.ControlVariant}
	}
	return out
}

// jsonInt64 reads a protojson int64: a string ("7"), or a number from a
// lenient encoder.
type jsonInt64 int64

func (n *jsonInt64) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return err
	}
	*n = jsonInt64(v)
	return nil
}

type changeJSON struct {
	ID              string                `json:"id"`
	OrgID           string                `json:"orgId"`
	EnvironmentID   string                `json:"environmentId"`
	Key             string                `json:"key"`
	Description     string                `json:"description"`
	Mode            string                `json:"mode"`
	Unit            string                `json:"unit"`
	Audience        *tools.ChangeAudience `json:"audience"`
	PercentBP       int32                 `json:"percentBp"`
	Split           []changeArmJSON       `json:"split"`
	EveryoneVariant string                `json:"everyoneVariant"`
	RolloutVariant  string                `json:"rolloutVariant"`
	Preview         []changePreviewJSON   `json:"preview"`
	Metric          *changeMetricJSON     `json:"metric"`
	CurrentRun      *struct {
		ID             string    `json:"id"`
		Kind           string    `json:"kind"`
		StartedAt      time.Time `json:"startedAt"`
		StartedBy      string    `json:"startedBy"`
		ApprovedBy     string    `json:"approvedBy"`
		ApprovalMethod string    `json:"approvalMethod"`
	} `json:"currentRun"`
	Revision    jsonInt64 `json:"revision"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Declaration *struct {
		Type           string `json:"type"`
		DefaultVariant string `json:"defaultVariant"`
		Variants       []struct {
			Name      string `json:"name"`
			ValueJSON string `json:"valueJson"`
		} `json:"variants"`
		Description string `json:"description"`
	} `json:"declaration"`
	ApprovalRequired bool `json:"approvalRequired"`
}

type changeArmJSON struct {
	Variant  string `json:"variant"`
	WeightBP int32  `json:"weightBp"`
}

type changePreviewJSON struct {
	UnitHash string `json:"unitHash"`
	Variant  string `json:"variant"`
	Label    string `json:"label"`
}

type changeMetricJSON struct {
	Event           string    `json:"event"`
	Prop            string    `json:"prop"`
	PropValue       string    `json:"propValue"`
	WindowHours     int32     `json:"windowHours"`
	MinSamplePerArm jsonInt64 `json:"minSamplePerArm"`
	ControlVariant  string    `json:"controlVariant"`
}

type changeEventJSON struct {
	Revision       jsonInt64 `json:"revision"`
	ActorKind      string    `json:"actorKind"`
	ActorID        string    `json:"actorId"`
	Action         string    `json:"action"`
	DiffJSON       string    `json:"diffJson"`
	RunID          string    `json:"runId"`
	ApprovalMethod string    `json:"approvalMethod"`
	ApprovedBy     string    `json:"approvedBy"`
	CreatedAt      time.Time `json:"createdAt"`
}

func (c changeJSON) summary() tools.ChangeSummary {
	out := tools.ChangeSummary{
		ID: c.ID, Key: c.Key, EnvironmentID: c.EnvironmentID, Description: c.Description,
		Mode: enumWord(c.Mode, "CHANGE_MODE_"), Unit: enumWord(c.Unit, "CHANGE_UNIT_"),
		Audience: c.Audience, PercentBP: c.PercentBP, EveryoneVariant: c.EveryoneVariant, RolloutVariant: c.RolloutVariant,
		Revision: int64(c.Revision), ApprovalRequired: c.ApprovalRequired, UpdatedAt: c.UpdatedAt,
	}
	if out.Mode == "" {
		out.Mode = "off"
	}
	if out.Unit == "" {
		out.Unit = "user"
	}
	for _, e := range c.Preview {
		out.Preview = append(out.Preview, tools.ChangePreviewEntry{UnitHash: e.UnitHash, Variant: e.Variant, Label: e.Label})
	}
	for _, arm := range c.Split {
		out.Split = append(out.Split, tools.ChangeSplitArm{Variant: arm.Variant, WeightBP: arm.WeightBP})
	}
	if m := c.Metric; m != nil {
		out.Metric = &tools.ChangeMetric{Event: m.Event, Prop: m.Prop, PropValue: m.PropValue, WindowHours: m.WindowHours,
			MinSamplePerArm: int64(m.MinSamplePerArm), ControlVariant: m.ControlVariant}
	}
	if r := c.CurrentRun; r != nil && r.ID != "" {
		out.Run = &tools.ChangeRunSummary{ID: r.ID, Kind: r.Kind, StartedAt: r.StartedAt, StartedBy: r.StartedBy, ApprovedBy: r.ApprovedBy, ApprovalMethod: r.ApprovalMethod}
	}
	if d := c.Declaration; d != nil {
		decl := &tools.ChangeDeclaration{Type: d.Type, DefaultVariant: d.DefaultVariant, Description: d.Description}
		for _, v := range d.Variants {
			decl.Variants = append(decl.Variants, tools.ChangeVariant{Name: v.Name, ValueJSON: v.ValueJSON})
		}
		out.Declaration = decl
	}
	return out
}

// enumWord is a protojson enum name as the tools spell it:
// CHANGE_MODE_EXPERIMENT → experiment. UNSPECIFIED is "".
func enumWord(name, prefix string) string {
	w := strings.ToLower(strings.TrimPrefix(name, prefix))
	if w == "unspecified" {
		return ""
	}
	return w
}

// unitEnum is a unit word as the proto enum names it, or "" for none.
func unitEnum(unit string) string {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "user":
		return "CHANGE_UNIT_USER"
	case "account":
		return "CHANGE_UNIT_ACCOUNT"
	case "visitor":
		return "CHANGE_UNIT_VISITOR"
	}
	return ""
}
