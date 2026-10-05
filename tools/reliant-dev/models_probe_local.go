// Copyright (c) 2025 Reliant Labs
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

// Local cells exercise a local model through the same resolveLLMCall path a
// workflow uses, over a relay transport. They are separate cell kinds because
// their assertions (thinking surfaced, window respected) differ from the
// hosted providers'.
const (
	cellLocalBasic    = "local-basic"
	cellLocalTool     = "local-tool"
	cellLocalThinking = "local-thinking"
	cellLocalTemp     = "local-temp"
	cellLocalLong     = "local-long-context"
	cellLocalOver     = "local-over-window"

	localProbeDaemonID = "local-dev"
	localProbeEndpoint = "dev"
	localNeedle        = "7341"
)

// localProbeTarget is one model on the probe's (fake or real) daemon.
type localProbeTarget struct {
	Model  string
	Window int64
	Tools  bool
	Think  bool
}

func buildLocalCells(targets []localProbeTarget) []probeCell {
	var cells []probeCell
	for _, t := range targets {
		base := probeCell{Model: t.Model, Provider: "local"}
		for _, kind := range []string{cellLocalBasic, cellLocalTemp, cellLocalLong, cellLocalOver} {
			c := base
			c.Kind = kind
			if kind == cellLocalTemp {
				temp := 0.0
				c.Temp = &temp
			}
			cells = append(cells, c)
		}
		if t.Tools {
			c := base
			c.Kind = cellLocalTool
			cells = append(cells, c)
		}
		if t.Think {
			c := base
			c.Kind = cellLocalThinking
			cells = append(cells, c)
		}
	}
	return cells
}

// needleHaystack returns prose-like text of about tokens tokens (4 chars per
// token, Reliant's own estimate) with the needle on one line in the middle.
// Lines are distinct so the model cannot guess its way to the answer.
func needleHaystack(tokens int) string {
	words := []string{"the", "harbor", "lantern", "drifted", "past", "quiet", "orchard", "while", "travelers", "counted", "seasons", "beneath", "copper", "clouds"}
	targetChars := tokens * 4
	var sb strings.Builder
	for i := 0; sb.Len() < targetChars; i++ {
		fmt.Fprintf(&sb, "Entry %d: %s %s %s %s %s %s.\n", i, words[i%len(words)], words[(i*3+1)%len(words)], words[(i*5+2)%len(words)], words[(i*7+3)%len(words)], words[(i*11+4)%len(words)], words[(i*13+5)%len(words)])
		if sb.Len() >= targetChars/2 && !strings.Contains(sb.String(), "NEEDLE") {
			fmt.Fprintf(&sb, "Entry %d: THE SECRET NEEDLE IS %s.\n", i, localNeedle)
		}
	}
	return sb.String()
}

func needleQuestion(tokens int) string {
	return needleHaystack(tokens) + "\nWhat is the secret needle number? Reply with only the number."
}

// runLocalCell runs one local cell. window is the context the server honors
// (the figure the inventory publishes), which sizes the long-context cells.
func runLocalCell(ctx context.Context, userID string, cell probeCell, window int64, spec *handlers.LocalModelSpec, timeout time.Duration) probeOutcome {
	out := probeOutcome{Cell: cell, Label: cell.Label()}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fail := func(class, msg string) { out.Status, out.Class, out.Error = "fail", class, msg }

	ps := handlers.ProbeSpec{
		UserID:  userID,
		ModelID: cell.Model + "@local",
		Local:   spec,
	}
	finish := func(r handlers.ProbeResult) bool {
		fillFromResult(&out, r)
		out.LatencyMs = time.Since(start).Milliseconds()
		if err := r.Err(); err != nil {
			fail(classifyProbeFailure(err.Error(), "local"), excerpt(err.Error(), 400))
			return false
		}
		return true
	}

	switch cell.Kind {
	case cellLocalBasic:
		ps.History = []message.Message{handlers.ProbeUserMessage(probePrompt)}
		r := handlers.ProbeLLMCall(cctx, ps)
		if !finish(r) {
			return out
		}
		if !assertPong(r.Text) {
			fail(classDriverBug, fmt.Sprintf("no PONG (text=%q)", excerpt(r.Text, 80)))
			return out
		}
		if strings.Contains(r.Text, "<think>") || strings.Contains(r.Text, "</think>") {
			fail(classDriverBug, "think markup leaked into the answer text")
			return out
		}
		if r.Usage.InputTokens == 0 || r.Usage.OutputTokens == 0 {
			fail(classDriverBug, fmt.Sprintf("usage not reported: %+v", r.Usage))
			return out
		}

	case cellLocalTemp:
		ps.Temperature = cell.Temp
		ps.History = []message.Message{handlers.ProbeUserMessage(probePrompt)}
		r := handlers.ProbeLLMCall(cctx, ps)
		if !finish(r) {
			return out
		}
		if !assertPong(r.Text) {
			fail(classDriverBug, fmt.Sprintf("no PONG at temperature 0 (text=%q)", excerpt(r.Text, 80)))
			return out
		}
		if r.EffectiveTemp == nil || *r.EffectiveTemp != 0 {
			fail(classDriverBug, "temperature 0 was not carried to the driver")
			return out
		}

	case cellLocalTool:
		ps.Tools = []tools.Tool{handlers.ProbeSecretWordTool{}}
		ps.History = []message.Message{handlers.ProbeUserMessage("Call the get_secret_word tool, then tell me the secret word it returned.")}
		first := handlers.ProbeLLMCall(cctx, ps)
		if !finish(first) {
			out.Note = "turn 1"
			return out
		}
		if len(first.ToolCalls) == 0 {
			fail(classDriverBug, fmt.Sprintf("turn 1: no tool call (text=%q)", excerpt(first.Text, 60)))
			return out
		}
		ps.History = append(ps.History, handlers.ProbeAssistantTurn(first), handlers.ProbeToolResultMessage(first.ToolCalls, "pineapple"))
		second := handlers.ProbeLLMCall(cctx, ps)
		out.TextExcerpt = excerpt(second.Text, 80)
		out.LatencyMs = time.Since(start).Milliseconds()
		if err := second.Err(); err != nil {
			fail(classifyProbeFailure(err.Error(), "local"), excerpt(err.Error(), 400))
			out.Note = "turn 2 (history replay)"
			return out
		}
		if !assertSecretWord(second.Text) {
			fail(classDriverBug, fmt.Sprintf("turn 2 lacks the tool result (text=%q)", excerpt(second.Text, 60)))
			return out
		}

	case cellLocalThinking:
		ps.History = []message.Message{handlers.ProbeUserMessage(reasoningPrompt)}
		r := handlers.ProbeLLMCall(cctx, ps)
		if !finish(r) {
			return out
		}
		if strings.Contains(r.Text, "<think>") || strings.Contains(r.Text, "</think>") {
			fail(classDriverBug, "think markup leaked into the answer text")
			return out
		}
		if r.ThinkingEvents == 0 || strings.TrimSpace(r.Thinking) == "" {
			fail(classDriverBug, "a thinking model produced no thinking events")
			return out
		}
		ok := assertReasoningAnswer(r.Text)
		out.Correct = &ok

	case cellLocalLong:
		// Under the published window: nothing may be trimmed, and the needle
		// must come back.
		tokens := int(float64(window) * 0.6)
		ps.History = []message.Message{handlers.ProbeUserMessage(needleQuestion(tokens))}
		r := handlers.ProbeLLMCall(cctx, ps)
		if !finish(r) {
			return out
		}
		out.Note = fmt.Sprintf("~%d tokens sent, window %d, input_tokens %d", tokens, window, r.Usage.InputTokens)
		if !strings.Contains(r.Text, localNeedle) {
			fail(classDriverBug, fmt.Sprintf("needle %s not found in a prompt under the window (text=%q)", localNeedle, excerpt(r.Text, 60)))
			return out
		}

	case cellLocalOver:
		// Over the published window: Reliant must trim, so the server never
		// sees more than it will honor. A server that truncated silently
		// reports input_tokens == its window and loses the head of the prompt.
		tokens := int(float64(window) * 1.6)
		ps.History = []message.Message{handlers.ProbeUserMessage(needleQuestion(tokens))}
		r := handlers.ProbeLLMCall(cctx, ps)
		if !finish(r) {
			return out
		}
		out.Note = fmt.Sprintf("~%d tokens sent, window %d, input_tokens %d", tokens, window, r.Usage.InputTokens)
		if r.Usage.InputTokens >= window {
			fail(classDriverBug, fmt.Sprintf("input_tokens %d >= window %d: the server saw (and silently truncated) an over-window prompt; Reliant did not trim", r.Usage.InputTokens, window))
			return out
		}
	}
	out.Status = "pass"
	return out
}

// ---- direct dev mode: an in-process fake relay straight at one server ----

// directLocalDirectory presents one always-online daemon whose single endpoint
// is the server at base URL, with capabilities read from the server itself.
type directLocalDirectory struct {
	inventory *reliantv1.LocalModelInventory
}

func (d directLocalDirectory) LocalDaemons(context.Context, string) ([]local.DaemonInventory, error) {
	return []local.DaemonInventory{{DaemonID: localProbeDaemonID, Machine: "probe-dev", Online: true, Inventory: d.inventory}}, nil
}

// directRelay is the "relay": it sends the request to baseURL instead of a
// daemon. The path is rewritten exactly as the daemon would (relative to /v1).
func directRelay(baseURL string) http.RoundTripper {
	root, _ := url.Parse(strings.TrimSuffix(strings.TrimSuffix(baseURL, "/"), "/v1"))
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host = root.Scheme, root.Host
		clone.URL.Path = strings.TrimSuffix(root.Path, "/") + "/v1" + strings.TrimPrefix(clone.URL.Path, "/v1")
		clone.RequestURI = ""
		return http.DefaultTransport.RoundTrip(clone)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// probeDirectInventory asks an Ollama-compatible server what it serves and
// what each model can do. window overrides the context the server honors when
// > 0; otherwise Ollama's /api/ps loaded context, else its 4096 default.
func probeDirectInventory(ctx context.Context, baseURL string, window int64) (*reliantv1.LocalModelInventory, error) {
	root := strings.TrimSuffix(strings.TrimSuffix(baseURL, "/"), "/v1")
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := getJSON(ctx, root+"/api/tags", &tags); err != nil {
		return nil, fmt.Errorf("listing models at %s: %w", root, err)
	}

	honored := window
	if honored <= 0 {
		var ps struct {
			Models []struct {
				ContextLength int64 `json:"context_length"`
			} `json:"models"`
		}
		if getJSON(ctx, root+"/api/ps", &ps) == nil && len(ps.Models) > 0 && ps.Models[0].ContextLength > 0 {
			honored = ps.Models[0].ContextLength
		} else {
			honored = 4096
		}
	}

	ep := &reliantv1.LocalModelEndpoint{Id: localProbeEndpoint, Kind: "ollama", BaseUrl: baseURL, Source: "configured"}
	for _, m := range tags.Models {
		var show struct {
			Capabilities []string `json:"capabilities"`
		}
		body, _ := json.Marshal(map[string]string{"model": m.Name})
		if err := postJSON(ctx, root+"/api/show", body, &show); err != nil {
			continue
		}
		has := func(c string) bool {
			for _, got := range show.Capabilities {
				if got == c {
					return true
				}
			}
			return false
		}
		ep.Models = append(ep.Models, &reliantv1.LocalModelInfo{
			Name: m.Name, ContextWindow: honored,
			SupportsChat: has("completion"), SupportsTools: has("tools"),
			SupportsVision: has("vision"), SupportsThinking: has("thinking"),
		})
	}
	return &reliantv1.LocalModelInventory{Endpoints: []*reliantv1.LocalModelEndpoint{ep}}, nil
}

func getJSON(ctx context.Context, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return doJSON(req, into)
}

func postJSON(ctx context.Context, u string, body []byte, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON(req, into)
}

func doJSON(req *http.Request, into any) error {
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s: %s", resp.Status, raw)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

// directLocalSpec builds the probe's LocalModelSpec and cell targets for a
// server at baseURL.
func directLocalSpec(ctx context.Context, baseURL string, window int64) (*handlers.LocalModelSpec, []localProbeTarget, error) {
	inv, err := probeDirectInventory(ctx, baseURL, window)
	if err != nil {
		return nil, nil, err
	}
	var targets []localProbeTarget
	for _, m := range inv.GetEndpoints()[0].GetModels() {
		if !m.GetSupportsChat() {
			continue
		}
		targets = append(targets, localProbeTarget{Model: m.GetName(), Window: m.GetContextWindow(), Tools: m.GetSupportsTools(), Think: m.GetSupportsThinking()})
	}
	relay := directRelay(baseURL)
	return &handlers.LocalModelSpec{
		Directory: directLocalDirectory{inventory: inv},
		Transport: func(_, _, _ string) http.RoundTripper { return relay },
	}, targets, nil
}
