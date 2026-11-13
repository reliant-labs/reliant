package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Control-plane's half of moving a project to another org
// (ProjectOrgMoveInternalService, control-plane PR M2;
// docs/design/org-project-move.md §3.1). The service is withheld from the
// exported contract, so there is no generated client: this speaks Connect
// unary over plain HTTP+JSON, signed with this process's internal-service
// identity (Deps.Sign). Every rpc is idempotent by the move id.
const (
	projectOrgMovePreparePath = "/controlplane.v1.ProjectOrgMoveInternalService/PrepareProjectOrgMove"
	projectOrgMoveAbortPath   = "/controlplane.v1.ProjectOrgMoveInternalService/AbortProjectOrgMove"
	projectOrgMoveRecordPath  = "/controlplane.v1.ProjectOrgMoveInternalService/RecordProjectOrgMove"
)

// Reasons control-plane refuses a move step for (x-forge-error-reason).
const (
	ReasonMoveSourceAdminRequired     = "source_admin_required"
	ReasonMoveTargetMembershipMissing = "target_membership_required"
	ReasonMoveAborted                 = "move_aborted"
	ReasonMoveRecorded                = "move_recorded"
	ReasonMoveMismatch                = "move_mismatch"
	ReasonMoveNotPrepared             = "move_not_prepared"
)

// ErrProjectOrgMoveUnsupported is a control plane without the service, or an
// adapter with no internal-service identity to call it with.
var ErrProjectOrgMoveUnsupported = errors.New("controlplane: moving a project between organizations is not available")

// ProjectOrgMoveStep names one move in control-plane's rpcs.
type ProjectOrgMoveStep struct {
	MoveID    string
	ProjectID string
	FromOrgID string
	ToOrgID   string
}

// ProjectOrgMoveError is a refusal control-plane returned: Code is the
// Connect code ("permission_denied", "failed_precondition", ...), Reason its
// x-forge-error-reason.
type ProjectOrgMoveError struct {
	Code, Reason, Message string
}

func (e *ProjectOrgMoveError) Error() string {
	if e.Message == "" {
		return "control plane refused the project move step (" + e.Code + ")"
	}
	return e.Message
}

// Permanent reports whether retrying the step cannot change the answer: a
// refusal, as opposed to an outage.
func (e *ProjectOrgMoveError) Permanent() bool {
	switch e.Code {
	case "invalid_argument", "permission_denied", "failed_precondition", "not_found", "already_exists":
		return true
	}
	return false
}

// PrepareProjectOrgMove re-checks the actor's authority at control-plane and
// revokes the source org's secret access for the project, answering how many
// entries it revoked (legacy hosted-use grants included).
func (c *client) PrepareProjectOrgMove(ctx context.Context, s ProjectOrgMoveStep, actorUserID string) (int, error) {
	var out struct {
		RevokedSecretAccess     int `json:"revokedSecretAccess"`
		RevokedHostedHTTPGrants int `json:"revokedHostedHttpGrants"`
	}
	in := moveStepJSON(s)
	in["actorUserId"] = actorUserID
	if err := c.projectOrgMoveCall(ctx, projectOrgMovePreparePath, in, &out); err != nil {
		return 0, err
	}
	return out.RevokedSecretAccess + out.RevokedHostedHTTPGrants, nil
}

// AbortProjectOrgMove restores exactly what Prepare revoked for the move.
func (c *client) AbortProjectOrgMove(ctx context.Context, s ProjectOrgMoveStep) error {
	return c.projectOrgMoveCall(ctx, projectOrgMoveAbortPath, moveStepJSON(s), nil)
}

// RecordProjectOrgMove writes the move to both orgs' activity streams.
func (c *client) RecordProjectOrgMove(ctx context.Context, s ProjectOrgMoveStep, projectName string, counts map[string]int64) error {
	in := moveStepJSON(s)
	in["projectName"] = projectName
	// protojson reads a map<string, int64> value as a decimal string.
	wire := make(map[string]string, len(counts))
	for k, v := range counts {
		wire[k] = strconv.FormatInt(v, 10)
	}
	in["counts"] = wire
	return c.projectOrgMoveCall(ctx, projectOrgMoveRecordPath, in, nil)
}

func moveStepJSON(s ProjectOrgMoveStep) map[string]any {
	return map[string]any{
		"moveId": s.MoveID, "projectId": s.ProjectID, "fromOrgId": s.FromOrgID, "toOrgId": s.ToOrgID,
	}
}

func (c *client) projectOrgMoveCall(ctx context.Context, path string, in, out any) error {
	if c.sign == nil {
		return fmt.Errorf("controlplane: no internal-service identity: %w", ErrProjectOrgMoveUnsupported)
	}
	bearer, err := c.sign()
	if err != nil {
		return fmt.Errorf("controlplane: signing internal-service token: %w", err)
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("controlplane: %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("controlplane: reading %s response: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		var rpcErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &rpcErr)
		if (resp.StatusCode == http.StatusNotFound && rpcErr.Code == "") || rpcErr.Code == "unimplemented" {
			return ErrProjectOrgMoveUnsupported
		}
		if rpcErr.Code == "" {
			rpcErr.Code = http.StatusText(resp.StatusCode)
		}
		return &ProjectOrgMoveError{Code: rpcErr.Code, Reason: resp.Header.Get("X-Forge-Error-Reason"), Message: rpcErr.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("controlplane: decoding %s response: %w", path, err)
	}
	return nil
}
