// Copyright (c) 2025 Reliant Labs

package toolexec

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/egresslimit"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ErrorCodeEgressRateLimited marks a tool result for an egress call the
// limiter refused. The call was not made and may be retried; the result is
// an ordinary tool error the model reads, so the conversation continues.
const ErrorCodeEgressRateLimited = "EGRESS_RATE_LIMITED"

// EgressLimiter admits server-side egress tool calls (egresslimit.Service).
type EgressLimiter interface {
	Acquire(ctx context.Context, req egresslimit.Request) (release func(), err error)
}

// SetEgressLimiter bounds the egress tools this executor runs in-process.
// Nil (the default) admits everything.
func (e *RemoteExecutor) SetEgressLimiter(l EgressLimiter) { e.egress = l }

// admitEgress is the one admission point for every server-executed egress
// tool: executeOnServer runs every PlacementServer / PlacementAny call, and an
// egress tool is one that dials a third party on a user's behalf. It returns
// a result to hand back instead of running the tool, or the release to call
// when the tool finishes.
func (e *RemoteExecutor) admitEgress(ctx context.Context, req *ToolRequest, startTime time.Time) (release func(), refusal *ToolResult) {
	if e.egress == nil {
		return func() {}, nil
	}
	host, isEgress := egressDestination(req.ToolName, req.ToolInput)
	if !isEgress {
		return func() {}, nil
	}
	release, err := e.egress.Acquire(ctx, egresslimit.Request{
		Tool:    req.ToolName,
		UserID:  req.UserID,
		Host:    host,
		TurnKey: req.ChatID + "/" + req.Thread,
	})
	if err == nil {
		return release, nil
	}
	code := ErrorCodeEgressRateLimited
	var limited *egresslimit.LimitedError
	if !errors.As(err, &limited) {
		// Cancelled while waiting for a turn slot.
		code = "EGRESS_WAIT_CANCELLED"
		logging.WarnContext(ctx, "egress call abandoned while waiting for a turn slot", "error", err, "toolName", req.ToolName)
	}
	return nil, &ToolResult{
		Success:      false,
		IsError:      true,
		Content:      err.Error(),
		ErrorMessage: err.Error(),
		ErrorCode:    code,
		StartTime:    startTime,
		EndTime:      time.Now(),
	}
}

var (
	egressOnce  sync.Once
	egressTools map[string]string // tool name -> fixed destination host ("" when the call names it)
)

// webSearchHost and sourcegraphHost are the fixed destinations of the two
// built-in tools that do not take a URL.
const (
	webSearchHost   = "html.duckduckgo.com"
	sourcegraphHost = "sourcegraph.com"
)

func loadEgressTools() {
	egressTools = map[string]string{
		tools.ToolFetch:       "",
		tools.ToolWebSearch:   webSearchHost,
		tools.ToolSourcegraph: sourcegraphHost,
	}
	cat, err := catalog.Builtin()
	if err != nil {
		return
	}
	for _, m := range cat.Manifests() {
		for _, a := range m.GetActions() {
			if !a.GetTool().GetExpose() {
				continue
			}
			host := ""
			if !m.GetConnection().GetAllowAnyPublicHost() {
				host = literalHost(m.GetConnection().GetBaseUrl())
			}
			egressTools[manifest.ToolName(m, a)] = host
		}
	}
}

// egressDestination reports whether the named tool dials out on a user's
// behalf, and the host it dials ("" when that is not known up front).
func egressDestination(toolName, toolInput string) (host string, isEgress bool) {
	egressOnce.Do(loadEgressTools)
	fixed, ok := egressTools[strings.TrimSpace(toolName)]
	if !ok {
		return "", false
	}
	if fixed != "" {
		return fixed, true
	}
	// fetch and http__request name their own destination.
	var in struct {
		URL string `json:"url"`
	}
	if json.Unmarshal([]byte(toolInput), &in) == nil {
		return hostOf(in.URL), true
	}
	return "", true
}

// hostOf is the lowercase hostname of a URL, or "" when it has none.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// literalHost is the host of a manifest base_url with any leading templated
// label dropped: https://{{ connection.params.region }}.sentry.io/... is
// "sentry.io", the part a user cannot choose.
func literalHost(baseURL string) string {
	h := baseURL
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	for {
		start := strings.Index(h, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(h[start:], "}}")
		if end < 0 {
			return ""
		}
		h = h[:start] + h[start+end+2:]
	}
	return strings.Trim(strings.ToLower(h), ".")
}
