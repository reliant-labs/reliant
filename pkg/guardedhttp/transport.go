// Package guardedhttp supplies the bounded request transport shared by native
// integration actions and hosted secret execution. It has no protobuf or
// product-service dependencies, so it is safe to embed in other platform apps.
package guardedhttp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Credential attaches authentication after the caller has checked destination
// policy. The credential must independently enforce its own destination scope.
type Credential interface{ Apply(*http.Request) error }

// Response is a bounded HTTP response. Secret-holding callers must scrub it
// before serialization, logging or returning it across their trust boundary.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Do is the single bounded-send implementation. client must use a guarded
// transport and an appropriate redirect policy; callers own those policies.
// No request or response is logged, and no application retry is performed.
func Do(ctx context.Context, client *http.Client, method string, target *url.URL, headers http.Header, body []byte, maxBytes int64, cred Credential) (*Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	req.Header = headers.Clone()
	if cred != nil {
		if err = cred.Apply(req); err != nil {
			return nil, fmt.Errorf("applying credential: %w", err)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if int64(len(raw)) > maxBytes {
		return nil, fmt.Errorf("response body exceeds the %d byte limit", maxBytes)
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: raw}, nil
}
