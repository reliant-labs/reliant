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

	"connectrpc.com/connect"
)

// hasCoverageBlobPath is control-plane's TestsService/HasCoverageBlob
// (control-plane#3286). The vendored contract (proto-vendor/controlplane) has
// no TestsService until control-plane publishes it, so there is no generated
// client yet: this speaks Connect unary over plain HTTP+JSON, as
// run_resume.go does, and moves onto the generated client at the re-vendor
// that brings TestsService in.
const hasCoverageBlobPath = "/controlplane.v1.TestsService/HasCoverageBlob"

// ErrCoverageBlobUnsupported is a control plane that does not answer
// HasCoverageBlob (it predates TestsService, or does not mount it yet). Its
// caller fails closed: without the answer, no blob is read.
var ErrCoverageBlobUnsupported = errors.New("controlplane: this control plane cannot say which blobs a coverage report holds yet")

// CoverageBlob is control-plane's answer about one blob of one forge project.
type CoverageBlob struct {
	// Present: a retained coverage report of the project names the blob.
	Present bool
	// Uncommitted: every retained report that names it recorded the file
	// with uncommitted changes, so the blob is in no commit. An older
	// control plane never sets it.
	Uncommitted bool
}

// HasCoverageBlob implements Service; see its contract.
func (c *client) HasCoverageBlob(ctx context.Context, token, orgID, project, blobID string) (CoverageBlob, error) {
	in := map[string]string{"project": project, "blobId": blobID}
	if orgID != "" {
		in["orgId"] = orgID
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return CoverageBlob{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+hasCoverageBlobPath, bytes.NewReader(payload))
	if err != nil {
		return CoverageBlob{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	if t := strings.TrimSpace(token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return CoverageBlob{}, fmt.Errorf("controlplane: HasCoverageBlob: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return CoverageBlob{}, fmt.Errorf("controlplane: reading HasCoverageBlob response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var rpcErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &rpcErr)
		if (resp.StatusCode == http.StatusNotFound && rpcErr.Code == "") || rpcErr.Code == "unimplemented" {
			return CoverageBlob{}, ErrCoverageBlobUnsupported
		}
		// The refusal keeps control-plane's code, so the caller can tell
		// PermissionDenied (no tests:read) and NotFound (not the org's
		// project) from a failure.
		var code connect.Code
		if err := code.UnmarshalText([]byte(rpcErr.Code)); err != nil {
			code = connect.CodeUnavailable
			if resp.StatusCode < http.StatusInternalServerError {
				code = connect.CodeUnknown
			}
		}
		if rpcErr.Message == "" {
			rpcErr.Message = "control plane answered " + http.StatusText(resp.StatusCode)
		}
		return CoverageBlob{}, connect.NewError(code, errors.New(rpcErr.Message))
	}
	var out struct {
		Present     bool `json:"present"`
		Uncommitted bool `json:"uncommitted"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return CoverageBlob{}, errors.New("controlplane: decoding HasCoverageBlob response failed")
	}
	return CoverageBlob{Present: out.Present, Uncommitted: out.Uncommitted}, nil
}
