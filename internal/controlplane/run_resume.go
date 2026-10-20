package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/toolexec"
)

// runResumePath is control-plane's DaemonInternalService/ResumeDaemonForRun.
// The service is withheld from the exported contract, so there is no
// generated client: this speaks Connect unary over plain HTTP+JSON, and
// control-plane's internal/handlers/daemon_internal wire test pins the path
// and field names it relies on.
const runResumePath = "/controlplane.v1.DaemonInternalService/ResumeDaemonForRun"

// RunResumeClient wakes a run owner's suspended machine for a run that resumes
// after a hold, with this process's internal-service identity rather than the
// owner's session token, which may have expired while the run was held.
// control-plane still runs the owner's own resume gates (ownership, workspace
// limit, funding).
type RunResumeClient struct {
	baseURL string
	http    *http.Client
	sign    func() (string, error)
}

// NewRunResumeClient returns a client for the control plane at endpoint, or
// nil when it has no URL or no internal-service secret to sign with.
func NewRunResumeClient(endpoint Endpoint, sign func(secret string) (string, error)) *RunResumeClient {
	base := strings.TrimRight(strings.TrimSpace(endpoint.URL), "/")
	secret := endpoint.InternalServiceSecret
	if base == "" || secret == "" || sign == nil {
		return nil
	}
	return &RunResumeClient{
		baseURL: base,
		http:    &http.Client{Timeout: 30 * time.Second},
		sign:    func() (string, error) { return sign(secret) },
	}
}

var _ toolexec.RunResumer = (*RunResumeClient)(nil)

// ResumeDaemonForRun asks control-plane to wake daemonID for userID's resuming
// run. A control plane without the door (404, or Unimplemented) answers with
// toolexec.ErrRunResumeUnsupported. Other refusals carry control-plane's
// client-safe message.
func (c *RunResumeClient) ResumeDaemonForRun(ctx context.Context, userID, daemonID string) (bool, error) {
	bearer, err := c.sign()
	if err != nil {
		return false, fmt.Errorf("controlplane: signing internal-service token: %w", err)
	}
	payload, err := json.Marshal(map[string]string{"userId": userID, "daemonId": daemonID})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+runResumePath, bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	req.Header.Set("Authorization", "Bearer "+bearer)

	resp, err := c.http.Do(req)
	if err != nil {
		return false, fmt.Errorf("controlplane: ResumeDaemonForRun(%s): %w", daemonID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, fmt.Errorf("controlplane: reading ResumeDaemonForRun response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var rpcErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &rpcErr)
		if (resp.StatusCode == http.StatusNotFound && rpcErr.Code == "") || rpcErr.Code == "unimplemented" {
			return false, toolexec.ErrRunResumeUnsupported
		}
		if rpcErr.Code == "" {
			rpcErr.Code = http.StatusText(resp.StatusCode)
		}
		return false, &RunResumeError{Code: rpcErr.Code, Message: rpcErr.Message}
	}
	var out struct {
		Resumed bool `json:"resumed"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, errors.New("controlplane: decoding ResumeDaemonForRun response failed")
	}
	return out.Resumed, nil
}

// RunResumeError is a refusal control-plane returned: a failed machine, a
// machine that is not the user's, or a resume gate (funding, workspace limit).
// Message is control-plane's client-safe text.
type RunResumeError struct {
	Code    string
	Message string
}

func (e *RunResumeError) Error() string {
	if e.Message == "" {
		return "control plane refused to wake the machine (" + e.Code + ")"
	}
	return e.Message
}
