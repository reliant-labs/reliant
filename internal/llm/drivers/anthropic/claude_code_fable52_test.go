// Copyright (c) 2025 Reliant Labs
package anthropic

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fable52MinCLI is the lowest claude-cli release the API accepts
// claude-fable-5-2 from. Below it the API answers
// "Claude Code 2.1.280 requires >=2.1.290".
const fable52MinCLI = "2.1.290"

func fable52Opts() llm.DriverOptions {
	return llm.DriverOptions{
		Model:           models.Model{APIModel: apiModelFable52, ThinkingMode: "adaptive", CanReason: true},
		ReasoningEffort: "medium",
		// Small enough for the SDK to allow a non-streaming SendMessages.
		MaxTokens: 4096,
		ApiKey:    "sk-ant-oat01-test",
	}
}

// cliVersionAtLeast compares dotted numeric versions ("2.1.321" >= "2.1.290").
// A malformed version is never at least the floor.
func cliVersionAtLeast(got, floor string) bool {
	g, f := strings.Split(got, "."), strings.Split(floor, ".")
	if len(g) != len(f) {
		return false
	}
	for i := range g {
		gi, err1 := strconv.Atoi(g[i])
		fi, err2 := strconv.Atoi(f[i])
		if err1 != nil || err2 != nil {
			return false
		}
		if gi != fi {
			return gi > fi
		}
	}
	return true
}

// TestClaudeCodeFable52_ClearsCLIVersionFloor pins fable-5.2's profile: it
// claims the published 2.1.321 release (above the API's 2.1.290 floor) with that
// release's bundled SDK version, and keeps the newest captured prompt bodies
// (2.1.280's) rather than any invented text.
func TestClaudeCodeFable52_ClearsCLIVersionFloor(t *testing.T) {
	got := claudeCodeProfileFor(apiModelFable52)

	assert.Equal(t, "2.1.321", got.cliVersion)
	assert.True(t, cliVersionAtLeast(got.cliVersion, fable52MinCLI),
		"fable-5.2 claims claude-cli %s, below the API's %s floor", got.cliVersion, fable52MinCLI)
	assert.Equal(t, "0.128.0", got.stainlessVersion, "2.1.321 bundles @anthropic-ai/sdk 0.128.0")
	assert.Empty(t, got.billingVersion, "no 2.1.321 billing suffix was ever captured; it must be derived, not invented")

	assert.Equal(t, profileOpus55.agent, got.agent, "prompt bodies stay the captured 2.1.280 ones")
	assert.Equal(t, profileOpus55.output, got.output, "prompt bodies stay the captured 2.1.280 ones")
	assert.Empty(t, got.reportingOutcomes, "2.1.280 sends no reporting-outcomes block")
	assert.True(t, got.billingPromptID)
	assert.True(t, got.billingTurnOrigin)
	assert.True(t, got.thinkingDisplay)
	assert.Equal(t, "v2d", got.dispatchID)
	assert.Equal(t, "main", got.requestClass)

	// The override is fable-5.2's alone.
	assert.Equal(t, "2.1.280", claudeCodeProfileFor(apiModelOpus55).cliVersion)
	assert.Equal(t, "2.1.261", claudeCodeProfileFor(apiModelFable51).cliVersion)
}

// TestClaudeCodeFingerprint_MatchesCLI checks the port against claude-cli
// 2.1.321's own td() function, copied out of the published bundle and run under
// Node. The last two vectors cover JavaScript's UTF-16 indexing: a non-BMP rune
// is two code units, and index 4 of "abcd😀fgh" is a lone high surrogate.
func TestClaudeCodeFingerprint_MatchesCLI(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"hi", "cf7"},
		{"Fix the failing build in internal/llm please", "d3a"},
		{"naïve café — résumé 😀😀😀😀😀😀😀😀😀😀", "6ea"},
		{"abcd😀fgh", "c33"},
		{"abcd😀😀", "8ef"}, // selected high/low halves combine before UTF-8 encoding
	} {
		assert.Equal(t, tc.want, claudeCodeFingerprint(tc.text, "2.1.321"), "text %q", tc.text)
	}
}

// TestClaudeCodeCCVersion_CapturedProfilesKeepTheirLiteral: only a profile with
// no captured cc_version derives one. Every captured profile sends its literal,
// whatever the conversation says.
func TestClaudeCodeCCVersion_CapturedProfilesKeepTheirLiteral(t *testing.T) {
	for apiModel, want := range map[string]string{
		apiModelOpus55:    "2.1.280.790",
		apiModelFable51:   "2.1.261.a78",
		"claude-opus-5":   "2.1.204.281",
		"claude-sonnet-5": "2.1.204.281",
	} {
		assert.Equal(t, want, claudeCodeProfileFor(apiModel).ccVersion("Fix the failing build"), apiModel)
	}
	assert.Equal(t, "2.1.321.d3a",
		claudeCodeProfileFor(apiModelFable52).ccVersion("Fix the failing build in internal/llm please"))
	assert.Equal(t, "2.1.321."+claudeCodeFingerprint("", "2.1.321"),
		claudeCodeProfileFor(apiModelFable52).ccVersion(""), "no user text fingerprints as all \"0\"")
}

// TestClaudeCodeBetaHeader_Fable52 pins fable-5.2's beta string as the 2.1.280
// set verbatim, so it carries context-1m and is served at its 1M window.
func TestClaudeCodeBetaHeader_Fable52(t *testing.T) {
	got := claudeCodeBetaHeader(apiModelFable52)
	if got != betaOpus55 {
		t.Fatalf("beta header mismatch\n got: %s\nwant: %s", got, betaOpus55)
	}
	if !strings.Contains(got, "context-1m-2025-08-07") {
		t.Error("fable-5.2 beta header must contain context-1m-2025-08-07")
	}
}

// TestClaudeCodeMaxTokens_Fable52 pins the 128000 max_tokens the fable-5.2
// capture sends; a copied 64000 would silently halve the output ceiling.
func TestClaudeCodeMaxTokens_Fable52(t *testing.T) {
	if got := claudeCodeMaxTokens(apiModelFable52); got != 128000 {
		t.Fatalf("claudeCodeMaxTokens(%s) = %d, want 128000", apiModelFable52, got)
	}
}

// wireRequest is one request exactly as it reached the socket.
type wireRequest struct {
	header http.Header
	body   []byte
}

// versionFloorServer is a local stand-in for the API's claude-cli version
// floor: it refuses claude-fable-5-2 from a User-Agent below fable52MinCLI with
// the error users saw, and answers anything else with a minimal message. It is
// a raw listener rather than httptest because the driver writes an exact-cased
// "host" header beside Go's own Host line, which net/http rejects as a
// duplicate.
type versionFloorServer struct {
	t  *testing.T
	ln net.Listener

	mu   sync.Mutex
	reqs []wireRequest
}

var claudeCLIUserAgent = regexp.MustCompile(`^claude-cli/(\d+\.\d+\.\d+) \(external, cli\)$`)

func newVersionFloorServer(t *testing.T) *versionFloorServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &versionFloorServer{t: t, ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go s.serve()
	return s
}

func (s *versionFloorServer) url() string { return "http://" + s.ln.Addr().String() }

func (s *versionFloorServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *versionFloorServer) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	// Parsed by hand: http.ReadRequest refuses the two Host lines too.
	r := textproto.NewReader(bufio.NewReader(conn))
	if _, err := r.ReadLine(); err != nil { // request line
		return
	}
	mime, err := r.ReadMIMEHeader()
	if err != nil {
		return
	}
	header := http.Header(mime)
	n, err := strconv.Atoi(header.Get("Content-Length"))
	if err != nil {
		return
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r.R, body); err != nil {
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, wireRequest{header: header, body: body})
	s.mu.Unlock()

	var payload struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &payload)

	status, resp := http.StatusOK,
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-fable-5-2","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`
	if payload.Model == apiModelFable52 {
		m := claudeCLIUserAgent.FindStringSubmatch(header.Get("User-Agent"))
		if m == nil || !cliVersionAtLeast(m[1], fable52MinCLI) {
			version := "unknown"
			if m != nil {
				version = m[1]
			}
			status = http.StatusBadRequest
			resp = fmt.Sprintf(`{"type":"error","error":{"type":"invalid_request_error","message":"Claude Code %s requires >=%s"}}`,
				version, fable52MinCLI)
		}
	}
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, http.StatusText(status), len(resp), resp)
}

func (s *versionFloorServer) last() wireRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	require.NotEmpty(s.t, s.reqs, "no request reached the server")
	return s.reqs[len(s.reqs)-1]
}

var billingCCVersion = regexp.MustCompile(`cc_version=([0-9.]+)\.([0-9a-f]{3});`)

// TestClaudeCodeFable52_WireClearsVersionFloor drives the real driver,
// transport chain included, against the local version-floor contract. The
// 2.1.280 fingerprint fable-5.2 used to send is refused the way the API refused
// it; the fable-5.2 profile is accepted, and its User-Agent, bundled-SDK version
// and billing cc_version all name the same release. This proves the request
// shape against a local contract, not against the live API.
func TestClaudeCodeFable52_WireClearsVersionFloor(t *testing.T) {
	srv := newVersionFloorServer(t)
	t.Setenv("ANTHROPIC_BASE_URL", srv.url())

	const firstUserText = "Fix the failing build in internal/llm please"
	turn := []message.Message{
		{ID: "u-1", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: firstUserText}}},
		{ID: "a-1", Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "On it."}}},
		{ID: "u-2", Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "and the tests"}}},
	}

	t.Run("the 2.1.280 fingerprint is refused", func(t *testing.T) {
		claudeCodeProfiles[apiModelFable52] = profileOpus55
		t.Cleanup(func() { claudeCodeProfiles[apiModelFable52] = profileFable52 })

		_, err := NewClaudeCodeClient(fable52Opts()).SendMessages(context.Background(), nil, turn, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Claude Code 2.1.280 requires >=2.1.290")
	})

	t.Run("the fable-5.2 profile is accepted, coherently", func(t *testing.T) {
		resp, err := NewClaudeCodeClient(fable52Opts()).SendMessages(context.Background(), nil, turn, nil)
		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Content)

		req := srv.last()
		assert.Equal(t, "claude-cli/2.1.321 (external, cli)", req.header.Get("User-Agent"))
		assert.Equal(t, "0.128.0", req.header.Get("X-Stainless-Package-Version"))

		var body struct {
			Model  string `json:"model"`
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		require.NoError(t, json.Unmarshal(req.body, &body))
		assert.Equal(t, apiModelFable52, body.Model)
		require.NotEmpty(t, body.System)
		m := billingCCVersion.FindStringSubmatch(body.System[0].Text)
		require.NotNil(t, m, "billing header has no cc_version: %q", body.System[0].Text)
		assert.Equal(t, "2.1.321", m[1], "billing cc_version must name the User-Agent's release")
		assert.Equal(t, claudeCodeFingerprint(firstUserText, "2.1.321"), m[2],
			"billing suffix is fingerprinted from the FIRST user message")
		assert.Contains(t, body.System[0].Text, "cc_turn_origin=human;")
		assert.Equal(t, ccAgentOpus55, body.System[2].Text, "agent block is the captured 2.1.280 body")
		assert.Equal(t, ccOutputOpus55, body.System[3].Text, "output block is the captured 2.1.280 body")
	})
}
