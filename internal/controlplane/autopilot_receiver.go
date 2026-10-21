package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// control-plane's AutopilotReceiverInternalService. Like DaemonInternalService
// it is withheld from the exported contract, so there is no generated client:
// this speaks Connect unary over plain HTTP+JSON, and control-plane's
// internal/handlers/autopilot_receiver_internal wire test pins the paths and
// field names it relies on.
const (
	autopilotUpsertPath = "/controlplane.v1.AutopilotReceiverInternalService/UpsertAutopilotReceiver"
	autopilotDeletePath = "/controlplane.v1.AutopilotReceiverInternalService/DeleteAutopilotReceiver"
	autopilotListPath   = "/controlplane.v1.AutopilotReceiverInternalService/ListAutopilotReceivers"
)

// ReasonAutopilotNoOrg is control-plane's x-forge-error-reason for an owner
// who belongs to no organization yet: there are no alerts to receive.
const ReasonAutopilotNoOrg = "autopilot_no_org"

// ErrAutopilotUnsupported: the control plane has no autopilot receivers (an
// older one, or one without an alerting database), or this adapter has no
// internal-service identity to call it with.
var ErrAutopilotUnsupported = errors.New("controlplane: autopilot alert receivers are not available")

// AutopilotReceiver is one automation's alert receiver, as reliant asks the
// control plane to provision it.
type AutopilotReceiver struct {
	// ActivationID is the trigger id; the receiver's identity.
	ActivationID string
	// UserID is the automation owner's id; the receiver belongs to their
	// primary organization.
	UserID string
	// URL is the trigger's webhook URL, without the token; Token is sent as
	// its bearer.
	URL, Token string
	// Projects (forge project names) and Repos (git remotes, any spelling)
	// scope it to the automation's project's environments. Both empty: every
	// environment of the organization.
	Projects, Repos []string
}

// AutopilotError is a refusal of the control plane's: Code is the Connect
// code, Reason its x-forge-error-reason (ReasonAutopilotNoOrg), Message its
// client-safe text.
type AutopilotError struct {
	Code, Reason, Message string
}

func (e *AutopilotError) Error() string {
	if e.Message == "" {
		return "control plane refused the alert receiver (" + e.Code + ")"
	}
	return e.Message
}

// UpsertAutopilotReceiver implements Service; see its contract.
func (c *client) UpsertAutopilotReceiver(ctx context.Context, r AutopilotReceiver) error {
	req := map[string]any{
		"activationId": r.ActivationID, "userId": r.UserID, "url": r.URL, "token": r.Token,
		"scope": map[string][]string{"projects": nonNil(r.Projects), "repos": nonNil(r.Repos)},
	}
	return c.autopilotCall(ctx, autopilotUpsertPath, req, nil)
}

// DeleteAutopilotReceiver implements Service; see its contract.
func (c *client) DeleteAutopilotReceiver(ctx context.Context, activationID string) error {
	return c.autopilotCall(ctx, autopilotDeletePath, map[string]string{"activationId": activationID}, nil)
}

// ListAutopilotReceivers implements Service; see its contract.
func (c *client) ListAutopilotReceivers(ctx context.Context) ([]string, error) {
	var ids []string
	token := ""
	// A bound well past any real fleet, so a control plane that kept
	// returning the same page could not spin this forever.
	for page := 0; page < 10000; page++ {
		var resp struct {
			Receivers []struct {
				ActivationID string `json:"activationId"`
			} `json:"receivers"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.autopilotCall(ctx, autopilotListPath, map[string]any{"pageToken": token, "pageSize": 1000}, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Receivers {
			ids = append(ids, r.ActivationID)
		}
		if resp.NextPageToken == "" || resp.NextPageToken == token {
			return ids, nil
		}
		token = resp.NextPageToken
	}
	return nil, errors.New("controlplane: ListAutopilotReceivers did not finish paging")
}

// autopilotCall is one internal-service-signed Connect unary call. out may be
// nil when the response carries nothing the caller reads.
func (c *client) autopilotCall(ctx context.Context, path string, in, out any) error {
	if c.sign == nil {
		return fmt.Errorf("controlplane: no internal-service identity: %w", ErrAutopilotUnsupported)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
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
			return ErrAutopilotUnsupported
		}
		if rpcErr.Code == "" {
			rpcErr.Code = http.StatusText(resp.StatusCode)
		}
		return &AutopilotError{Code: rpcErr.Code, Reason: resp.Header.Get("X-Forge-Error-Reason"), Message: rpcErr.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("controlplane: decoding %s response: %w", path, err)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
