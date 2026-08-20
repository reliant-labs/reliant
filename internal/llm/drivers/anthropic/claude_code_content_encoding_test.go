// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The driver advertises "br, gzip, deflate" to match Claude Code's wire
// fingerprint. Go's transport only decompresses transparently when IT chose the
// encoding, so once the header is supplied by hand the body arrives encoded and
// decoding becomes this driver's job. These tests pin that: a compressed body
// must reach the SDK as plain JSON.

// encodedUpstream serves a fixed body under a given Content-Encoding.
type encodedUpstream struct {
	encoding string
	body     []byte

	mu     sync.Mutex
	seenAE []string
}

func (u *encodedUpstream) RoundTrip(req *http.Request) (*http.Response, error) {
	u.mu.Lock()
	//nolint:staticcheck // SA1008: lowercase casing is what the driver writes
	u.seenAE = append(u.seenAE, req.Header["accept-encoding"]...)
	u.mu.Unlock()

	header := http.Header{}
	header.Set("Content-Type", "application/json")
	if u.encoding != "" {
		header.Set("Content-Encoding", u.encoding)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(u.body)),
		Request:    req,
	}, nil
}

func gzipBytes(t *testing.T, payload string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, err := gz.Write([]byte(payload))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// newTestDecodingTransport builds the driver's transport chain around a fake
// upstream: the decoding wrapper over lowercaseHeaderTransport, exactly as
// NewClaudeCodeClient assembles it.
func newTestDecodingTransport(upstream http.RoundTripper) http.RoundTripper {
	headers := map[string]string{
		"accept-encoding": claudeCodeAcceptEncoding,
	}
	return &decompressingTransport{
		base: &lowercaseHeaderTransport{
			base:    upstream,
			headers: headers,
			mu:      &sync.RWMutex{},
		},
	}
}

// TestClaudeCodeTransport_DecompressesGzipResponse is the regression test for
// titles silently falling back to the truncated first message: the API returned
// a compressed body, the SDK fed those raw bytes to encoding/json, and every
// call failed with `invalid character '\x03' looking for beginning of value`.
func TestClaudeCodeTransport_DecompressesGzipResponse(t *testing.T) {
	payload := `{"id":"msg_01","content":[{"type":"tool_use","name":"set_title","input":{"title":"Debugging Workflows"}}]}`
	upstream := &encodedUpstream{encoding: "gzip", body: gzipBytes(t, payload)}

	resp, err := newTestDecodingTransport(upstream).RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// The caller must be able to decode it as plain JSON.
	var decoded map[string]any
	require.NoError(t, json.NewDecoder(bytes.NewReader(body)).Decode(&decoded),
		"body handed to the SDK must be decompressed JSON")
	assert.Equal(t, "msg_01", decoded["id"])

	// Content-Encoding must be cleared, or a caller could double-decode.
	assert.Empty(t, resp.Header.Get("Content-Encoding"))
	assert.Empty(t, resp.Header.Get("Content-Length"))
}

// An identity (uncompressed) response must pass through untouched.
func TestClaudeCodeTransport_PassesThroughUnencodedResponse(t *testing.T) {
	payload := `{"id":"msg_02"}`
	upstream := &encodedUpstream{encoding: "", body: []byte(payload)}

	resp, err := newTestDecodingTransport(upstream).RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.JSONEq(t, payload, string(body))
}

// This is the one that reproduces the production failure, and it needs a real
// server and a real transport because the bug lives in an interaction the
// stubs above cannot show.
//
// lowercaseHeaderTransport writes req.Header["accept-encoding"] by direct map
// access. net/http looks the header up canonically ("Accept-Encoding"), does
// not find it, and appends its OWN "gzip" — so two Accept-Encoding headers go
// out. Go therefore still auto-decodes gzip, which is why gzip responses
// always worked and hid the defect. When the list advertised br, the upstream
// CDN answered in brotli, Go decoded nothing, and the SDK fed a raw brotli
// frame to encoding/json: exactly the `invalid character '\x03'` /
// `'\u0083'` failures in the worker log. (Gzip would have been '\x1f'.)
//
// The guarantee under test: whatever this driver advertises, the body reaching
// the SDK is plain JSON.
func TestClaudeCodeTransport_RealServerBrotliRegression(t *testing.T) {
	payload := `{"id":"msg_03","content":[{"type":"tool_use","name":"set_title","input":{"title":"Debugging Workflows"}}]}`

	var sawAcceptEncoding []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAcceptEncoding = r.Header.Values("Accept-Encoding")
		offered := strings.Join(sawAcceptEncoding, ",")

		// A CDN that prefers brotli whenever the client says it can take it.
		// Nothing in Go's stack decodes brotli.
		if strings.Contains(offered, "br") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "br")
			_, _ = w.Write([]byte{0x03, 0x83, 0x00, 0x01})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		_, _ = gz.Write([]byte(payload))
	}))
	defer srv.Close()

	// The same chain NewClaudeCodeClient builds, over the real transport.
	client := &http.Client{
		Transport: &decompressingTransport{
			base: &lowercaseHeaderTransport{
				base:    http.DefaultTransport,
				headers: map[string]string{"accept-encoding": claudeCodeAcceptEncoding},
				mu:      &sync.RWMutex{},
			},
		},
	}

	resp, err := client.Get(srv.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// If br is ever re-added to the advertised list, this decode fails with the
	// original production error.
	var decoded map[string]any
	require.NoError(t, json.NewDecoder(bytes.NewReader(body)).Decode(&decoded),
		"body reaching the SDK must be plain JSON; advertised %q", sawAcceptEncoding)
	assert.Equal(t, "msg_03", decoded["id"])
}

// The advertised encodings must all be ones the driver can actually decode.
// Advertising br without decoding it is what produced the original bug.
func TestClaudeCodeTransport_OnlyAdvertisesDecodableEncodings(t *testing.T) {
	upstream := &encodedUpstream{encoding: "", body: []byte(`{}`)}

	_, err := newTestDecodingTransport(upstream).RoundTrip(newMessagesRequest(t))
	require.NoError(t, err)

	require.Len(t, upstream.seenAE, 1)
	for _, enc := range strings.Split(upstream.seenAE[0], ",") {
		enc = strings.TrimSpace(enc)
		assert.Contains(t, []string{"gzip", "deflate", "identity"}, enc,
			"advertised %q but the transport cannot decode it", enc)
	}
}
