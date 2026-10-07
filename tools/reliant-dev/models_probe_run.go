// Copyright (c) 2025 Reliant Labs
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/drivers/imagegen"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/activities/handlers"
)

const (
	probeDefaultDBURL = "postgres://postgres:postgres@localhost:5434/reliant?sslmode=disable" //nolint:gosec // G101: the local dev database
	probeOwnerUserID  = "a6e15ec0-d1be-40c2-9c17-8d775613c904"
	probePrompt       = "Reply with exactly: PONG"
)

// probeOutcome is the recorded result of one cell.
type probeOutcome struct {
	Cell         probeCell `json:"cell"`
	Label        string    `json:"label"`
	Status       string    `json:"status"` // pass | fail
	Class        string    `json:"class,omitempty"`
	Error        string    `json:"error,omitempty"`
	Note         string    `json:"note,omitempty"`
	TextExcerpt  string    `json:"text_excerpt,omitempty"`
	Resolved     string    `json:"resolved_model,omitempty"`
	Provider     string    `json:"resolved_provider,omitempty"`
	ServedBy     string    `json:"served_by,omitempty"` // Name() of the driver that actually ran it
	APIModel     string    `json:"api_model,omitempty"`
	Effective    string    `json:"effective_thinking,omitempty"`
	EffectiveT   *float64  `json:"effective_temperature,omitempty"`
	TempOmitted  bool      `json:"temperature_omitted_by_catalog,omitempty"`
	LatencyMs    int64     `json:"latency_ms"`
	InputTokens  int64     `json:"input_tokens,omitempty"`
	OutputTokens int64     `json:"output_tokens,omitempty"`
	ThinkingLen  int       `json:"thinking_chars,omitempty"`
	HasSignature bool      `json:"has_signature,omitempty"`
	ToolSig      bool      `json:"tool_call_has_thought_signature,omitempty"`

	// Reasoning-pass evidence.
	Correct        *bool          `json:"correct,omitempty"`
	ThinkingEvents int            `json:"thinking_delta_events,omitempty"`
	Redacted       bool           `json:"has_redacted_thinking,omitempty"`
	Finish         string         `json:"finish_reason,omitempty"`
	EventTypes     map[string]int `json:"event_types,omitempty"`
	CacheRead      int64          `json:"cache_read_tokens,omitempty"`
}

// envOverlayRepo adds credentials that exist only as environment variables to
// the repo's provider keys, without changing production code.
type envOverlayRepo struct {
	db.Repository
	extra map[string]string
}

func (r envOverlayRepo) GetProviderAPIKeys(ctx context.Context, userID string) (map[string]string, error) {
	keys, err := r.Repository.GetProviderAPIKeys(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(keys)+len(r.extra))
	for k, v := range keys {
		out[k] = v
	}
	for k, v := range r.extra {
		if _, ok := out[k]; !ok {
			out[k] = v
		}
	}
	return out, nil
}

func (r envOverlayRepo) GetProviderAPIKey(ctx context.Context, userID, provider string) (string, error) {
	key, err := r.Repository.GetProviderAPIKey(ctx, userID, provider)
	if err == nil && strings.TrimSpace(key) != "" {
		return key, nil
	}
	if v, ok := r.extra[provider]; ok {
		return v, nil
	}
	return key, err
}

// envProviderKeys reads credentials that exist only in the environment.
func envProviderKeys() map[string]string {
	m := map[string]string{}
	for provider, env := range map[string]string{
		"openrouter": "OPENROUTER_API_KEY",
		"openai":     "OPENAI_API_KEY",
		"gemini":     "GEMINI_API_KEY",
		"xai":        "XAI_API_KEY",
		"reliant":    "RELIANT_PROBE_API_KEY",
	} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			m[provider] = v
		}
	}
	return m
}

func catalogProbeModels(reg *models.ModelRegistry) []probeModel {
	var out []probeModel
	for _, def := range reg.GetUserVisibleModels() {
		if !def.Capabilities.CanOutput(models.ModalityText) || def.Capabilities.CanOutput(models.ModalityImage) && !def.Capabilities.CanOutput(models.ModalityText) {
			continue
		}
		seen := map[string]bool{}
		var drv []string
		for _, p := range def.Providers {
			if !seen[p.Driver] {
				seen[p.Driver] = true
				drv = append(drv, p.Driver)
			}
		}
		out = append(out, probeModel{
			ID:       def.ID,
			Drivers:  drv,
			Levels:   models.SupportedThinkingLevels(def.Capabilities),
			TempOmit: def.DriverSettings != nil && def.DriverSettings.TemperatureMode == "omit",
		})
	}
	return out
}

func excerpt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// servedByAliases maps a driver Name() onto the provider id it serves when the
// two differ. Claude OAuth registers under the "anthropic" driver id (its
// sk-ant-oat token routes there) but its client reports "claude-code".
var servedByAliases = map[string]string{"claude-code": "anthropic"}

// servedElsewhere reports whether a cell pinned to provider was actually run by
// a different driver. A cell that passes on the wrong provider is not a pass:
// it is exactly the failure this check exists to surface.
func servedElsewhere(provider, servedBy string) bool {
	if provider == "" || servedBy == "" {
		return false
	}
	if alias, ok := servedByAliases[servedBy]; ok {
		servedBy = alias
	}
	return servedBy != provider
}

func fillFromResult(o *probeOutcome, r handlers.ProbeResult) {
	o.Resolved, o.Provider, o.APIModel = r.ResolvedModelID, r.ProviderDriver, r.APIModel
	o.ServedBy = r.ServedBy
	o.Effective, o.EffectiveT = r.EffectiveThinking, r.EffectiveTemp
	o.TempOmitted = !r.ModelTemperatureOK
	o.InputTokens, o.OutputTokens = r.Usage.InputTokens, r.Usage.OutputTokens
	o.ThinkingLen = len(r.Thinking)
	o.HasSignature = r.Signature != ""
	o.TextExcerpt = excerpt(r.Text, 80)
	o.ThinkingEvents = r.ThinkingEvents
	o.Redacted = r.RedactedThking != ""
	o.Finish = string(r.FinishReason)
	o.EventTypes = r.EventTypes
}

// runProbeCell runs one cell and then refuses to call it a pass if the request
// was carried by a driver other than the provider the cell pins.
func runProbeCell(ctx context.Context, userID string, cell probeCell, timeout time.Duration) probeOutcome {
	out := runProbeCellUnchecked(ctx, userID, cell, timeout)
	if out.Status == "pass" && cell.Kind != cellTag && servedElsewhere(cell.Provider, out.ServedBy) {
		out.Status = "fail"
		out.Class = classDriverBug
		out.Error = fmt.Sprintf("pinned to %s but served by %s: driver selection re-routed the request", cell.Provider, out.ServedBy)
	}
	return out
}

func runProbeCellUnchecked(ctx context.Context, userID string, cell probeCell, timeout time.Duration) probeOutcome {
	out := probeOutcome{Cell: cell, Label: cell.Label()}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	fail := func(err error, provider string) {
		out.Status = "fail"
		out.Error = excerpt(err.Error(), 400)
		out.Class = classifyProbeFailure(err.Error(), provider)
	}
	failAssert := func(msg string) {
		out.Status = "fail"
		out.Error = msg
		out.Class = classDriverBug
	}

	spec := handlers.ProbeSpec{
		UserID:        userID,
		ThinkingLevel: cell.Level,
		Temperature:   cell.Temp,
		SystemPrompts: nil,
		History:       []message.Message{handlers.ProbeUserMessage(probePrompt)},
	}
	if cell.Kind == cellTag {
		spec.Tags = []string{cell.Tag}
	} else {
		spec.ModelID = cell.Model + "@" + cell.Provider
	}

	switch cell.Kind {
	case cellReasoning:
		spec.History = []message.Message{handlers.ProbeUserMessage(reasoningPrompt)}
		r := handlers.ProbeLLMCall(cctx, spec)
		fillFromResult(&out, r)
		out.LatencyMs = time.Since(start).Milliseconds()
		if err := r.Err(); err != nil {
			fail(err, cell.Provider)
			return out
		}
		ok := assertReasoningAnswer(r.Text)
		out.Correct = &ok
		out.Status = "pass" // the call worked; correctness is reported separately
	case cellBasic, cellTemp, cellTag:
		r := handlers.ProbeLLMCall(cctx, spec)
		fillFromResult(&out, r)
		out.LatencyMs = time.Since(start).Milliseconds()
		if err := r.Err(); err != nil {
			fail(err, cell.Provider)
			return out
		}
		if !assertPong(r.Text) {
			failAssert(fmt.Sprintf("no PONG in reply (finish=%s, text=%q, thinking_chars=%d, out_tokens=%d)", r.FinishReason, excerpt(r.Text, 60), len(r.Thinking), r.Usage.OutputTokens))
			return out
		}
		out.Status = "pass"
	case cellTool:
		tool := handlers.ProbeSecretWordTool{}
		spec.Tools = []tools.Tool{tool}
		spec.History = []message.Message{handlers.ProbeUserMessage("Call the get_secret_word tool, then tell me the secret word it returned.")}
		first := handlers.ProbeLLMCall(cctx, spec)
		fillFromResult(&out, first)
		if err := first.Err(); err != nil {
			out.LatencyMs = time.Since(start).Milliseconds()
			fail(err, cell.Provider)
			out.Note = "turn 1"
			return out
		}
		if len(first.ToolCalls) == 0 {
			out.LatencyMs = time.Since(start).Milliseconds()
			failAssert(fmt.Sprintf("turn 1: model made no tool call (finish=%s, text=%q)", first.FinishReason, excerpt(first.Text, 60)))
			return out
		}
		for _, tc := range first.ToolCalls {
			if tc.ThoughtSignature != "" {
				out.ToolSig = true
			}
		}
		spec.History = append(spec.History,
			handlers.ProbeAssistantTurn(first),
			handlers.ProbeToolResultMessage(first.ToolCalls, "pineapple"))
		second := handlers.ProbeLLMCall(cctx, spec)
		out.LatencyMs = time.Since(start).Milliseconds()
		out.InputTokens += second.Usage.InputTokens
		out.OutputTokens += second.Usage.OutputTokens
		out.TextExcerpt = excerpt(second.Text, 80)
		if err := second.Err(); err != nil {
			fail(err, cell.Provider)
			out.Note = "turn 2 (history replay)"
			return out
		}
		if !assertSecretWord(second.Text) {
			failAssert(fmt.Sprintf("turn 2: final answer lacks tool result (finish=%s, text=%q)", second.FinishReason, excerpt(second.Text, 60)))
			return out
		}
		out.Status = "pass"
	}
	return out
}

type imageModelCell struct {
	model, provider string
}

func runImageCell(ctx context.Context, userID string, c imageModelCell, timeout time.Duration) probeOutcome {
	cell := probeCell{Kind: cellImage, Model: c.model, Provider: c.provider}
	out := probeOutcome{Cell: cell, Label: cell.Label()}
	start := time.Now()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fail := func(err error) {
		out.Status, out.Error = "fail", excerpt(err.Error(), 400)
		out.Class = classifyProbeFailure(err.Error(), c.provider)
	}
	gen, err := drivers.ResolveImageGenerator(cctx, userID, models.ModelSelector{ID: c.model + "@" + c.provider})
	if err != nil {
		out.LatencyMs = time.Since(start).Milliseconds()
		fail(err)
		return out
	}
	resp, err := gen.GenerateImage(cctx, imagegen.Request{Prompt: "a single red circle on a white background", Size: "1024x1024", Count: 1, Quality: "low"})
	out.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		fail(err)
		return out
	}
	if resp == nil || len(resp.Images) == 0 || len(resp.Images[0].Bytes) == 0 {
		out.Status, out.Class, out.Error = "fail", classDriverBug, "200 but no image bytes returned"
		return out
	}
	out.Status = "pass"
	out.Resolved, out.Provider, out.APIModel = resp.ModelID, resp.Driver, resp.APIModel
	out.Note = fmt.Sprintf("%d bytes %s", len(resp.Images[0].Bytes), resp.Images[0].MIMEType)
	return out
}

func newModelsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "models", Short: "Model catalog tooling"}
	cmd.AddCommand(newModelsProbeCmd())
	return cmd
}

func newModelsProbeCmd() *cobra.Command {
	var (
		dbURL       string
		userID      string
		filter      string
		jsonPath    string
		dryRun      bool
		images      bool
		skipText    bool
		reasoning   bool
		wireDir     string
		localProbe  bool
		localURL    string
		localWindow int64
		concurrency int
		timeout     time.Duration
	)
	cmd := &cobra.Command{
		Use:   "probe",
		Short: "Live-probe every model x provider x thinking level through the production request path",
		Long: `Issues real LLM requests through handlers.resolveLLMCall -> drivers.GetDriver ->
driver.StreamResponse, the same path every workflow call_llm uses.

Credentials come from the dev database (read-only; the drivers' own OAuth
compare-and-swap refresh is the only write) plus OPENROUTER_API_KEY /
OPENAI_API_KEY / GEMINI_API_KEY / XAI_API_KEY / RELIANT_PROBE_API_KEY from the
environment. Cost is a handful of tiny prompts per cell.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			var re *regexp.Regexp
			if filter != "" {
				var err error
				if re, err = regexp.Compile(filter); err != nil {
					return fmt.Errorf("bad --filter: %w", err)
				}
			}
			url := dbURL
			if url == "" {
				url = os.Getenv("DATABASE_URL")
			}
			if url == "" {
				url = probeDefaultDBURL
			}
			repo, err := db.OpenReadOnlyRepo(url)
			if err != nil {
				return err
			}
			defer repo.Close() //nolint:errcheck
			drivers.InitializeAPIKeyProvider(envOverlayRepo{Repository: repo, extra: envProviderKeys()})
			if err := models.InitGlobalRegistryWithUserConfig(nil); err != nil {
				return fmt.Errorf("model registry: %w", err)
			}
			reg := models.MustGetRegistry()

			avail := drivers.GetAvailableDrivers(ctx, userID)
			providers := map[string]bool{}
			var providerList []string
			for id, cfg := range avail.Drivers {
				if cfg.IsConfigured() {
					providers[string(id)] = true
					providerList = append(providerList, string(id))
				}
			}
			sort.Strings(providerList)
			fmt.Fprintf(cmd.ErrOrStderr(), "provider set (credentialed): %s\n", strings.Join(providerList, ", "))

			var uncredentialed []string
			seen := map[string]bool{}
			for _, def := range reg.ListAll() {
				for _, p := range def.Providers {
					if !providers[p.Driver] && !seen[p.Driver] {
						seen[p.Driver] = true
						uncredentialed = append(uncredentialed, p.Driver)
					}
				}
			}
			sort.Strings(uncredentialed)
			fmt.Fprintf(cmd.ErrOrStderr(), "not testable (no credential): %s\n", strings.Join(uncredentialed, ", "))

			var cells []probeCell
			var imageCells []imageModelCell
			if !skipText {
				cells = buildProbeMatrix(catalogProbeModels(reg), providers, re)
				cells = append(cells, buildTagCells(probeCoreTags, re)...)
			}
			if reasoning {
				cells = append(cells, buildReasoningMatrix(catalogProbeModels(reg), providers, re)...)
			}
			if images {
				for _, def := range reg.GetUserVisibleModelsForModality(models.ModalityImage) {
					seenDrv := map[string]bool{}
					for _, p := range def.Providers {
						if !providers[p.Driver] || seenDrv[p.Driver] {
							continue
						}
						seenDrv[p.Driver] = true
						if re != nil && !re.MatchString(def.ID+"@"+p.Driver) {
							continue
						}
						imageCells = append(imageCells, imageModelCell{def.ID, p.Driver})
					}
				}
			}

			var localSpec *handlers.LocalModelSpec
			var localTargets []localProbeTarget
			if localProbe || localURL != "" {
				if localURL == "" {
					return fmt.Errorf("--local needs --local-base-url: probing through the real daemon router is not wired into this tool yet")
				}
				var err error
				localSpec, localTargets, err = directLocalSpec(ctx, localURL, localWindow)
				if err != nil {
					return fmt.Errorf("local probe: %w", err)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "local: %d chat model(s) via an in-process fake relay straight at %s (NOT the daemon router)\n", len(localTargets), localURL)
			}
			localCells := buildLocalCells(localTargets)
			localWindows := map[string]int64{}
			for _, t := range localTargets {
				localWindows[t.Model] = t.Window
			}
			if re != nil {
				kept := localCells[:0]
				for _, c := range localCells {
					if re.MatchString(c.Model + "@local") {
						kept = append(kept, c)
					}
				}
				localCells = kept
			}
			if localProbe && skipText {
				cells = nil
			}

			if wireDir != "" && !dryRun {
				if err := installWireDump(wireDir); err != nil {
					return err
				}
			}
			if dryRun {
				w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
				for _, c := range cells {
					fmt.Fprintf(w, "%s\t%s\n", c.Kind, c.Label())
				}
				for _, c := range imageCells {
					fmt.Fprintf(w, "image\t%s@%s\n", c.model, c.provider)
				}
				for _, c := range localCells {
					fmt.Fprintf(w, "%s\t%s\n", c.Kind, c.Label())
				}
				_ = w.Flush()
				fmt.Fprintf(cmd.ErrOrStderr(), "%d text cells, %d image cells (dry run)\n", len(cells), len(imageCells))
				return nil
			}

			var jobs []func() probeOutcome
			for _, c := range cells {
				c := c
				jobs = append(jobs, func() probeOutcome { return runProbeCell(ctx, userID, c, timeout) })
			}
			// Local models serve one request at a time on most machines, so the
			// local cells run serially after the hosted ones.
			outcomes := runJobs(jobs, concurrency, cmd)
			if len(localCells) > 0 {
				var localJobs []func() probeOutcome
				for _, c := range localCells {
					c := c
					localJobs = append(localJobs, func() probeOutcome {
						return runLocalCell(ctx, userID, c, localWindows[c.Model], localSpec, timeout)
					})
				}
				outcomes = append(outcomes, runJobs(localJobs, 1, cmd)...)
			}
			if len(imageCells) > 0 {
				var imgJobs []func() probeOutcome
				for _, c := range imageCells {
					c := c
					imgJobs = append(imgJobs, func() probeOutcome { return runImageCell(ctx, userID, c, timeout) })
				}
				outcomes = append(outcomes, runJobs(imgJobs, 1, cmd)...)
			}

			printProbeTable(cmd, outcomes)
			if jsonPath != "" {
				doc := map[string]any{
					"run_at":         time.Now().UTC().Format(time.RFC3339),
					"providers":      providerList,
					"uncredentialed": uncredentialed,
					"outcomes":       outcomes,
				}
				b, _ := json.MarshalIndent(doc, "", "  ")
				if err := os.WriteFile(jsonPath, b, 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", jsonPath)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dbURL, "db-url", "", "Database URL (default: $DATABASE_URL, then the local dev stack)")
	f.StringVar(&userID, "user", probeOwnerUserID, "User whose credentials are probed")
	f.StringVar(&filter, "filter", "", "Regex over model@provider (and tag:<name>)")
	f.StringVar(&jsonPath, "json", "", "Write full results as JSON to this path")
	f.BoolVar(&dryRun, "dry-run", false, "Print the matrix and exit; no requests are made")
	f.BoolVar(&images, "images", false, "Also probe image-generation models (1 small image each, run last)")
	f.BoolVar(&skipText, "skip-text", false, "Skip the text matrix (use with --images)")
	f.BoolVar(&reasoning, "reasoning", false, "Add the reasoning pass: a multi-step arithmetic puzzle at default, lowest and highest declared level")
	f.BoolVar(&localProbe, "local", false, "Add local-model cells (basic, tool, thinking, temperature, long context, over-window); needs --local-base-url")
	f.StringVar(&localURL, "local-base-url", "", "Probe a local server directly through an in-process fake relay (e.g. http://localhost:11434/v1)")
	f.Int64Var(&localWindow, "local-window", 0, "Context window the server honors, for sizing the long-context cells (default: Ollama /api/ps, else 4096)")
	f.StringVar(&wireDir, "wire-dump", "", "Record raw plaintext of every provider connection to this dir (forces HTTP/1.1; credentials scrubbed)")
	f.IntVar(&concurrency, "concurrency", 4, "Parallel cells")
	f.DurationVar(&timeout, "timeout", 120*time.Second, "Per-call timeout")
	return cmd
}

func runJobs(jobs []func() probeOutcome, concurrency int, cmd *cobra.Command) []probeOutcome {
	if concurrency < 1 {
		concurrency = 1
	}
	results := make([]probeOutcome, len(jobs))
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	sem := make(chan struct{}, concurrency)
	for i, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, j func() probeOutcome) {
			defer wg.Done()
			defer func() { <-sem }()
			o := j()
			mu.Lock()
			results[i] = o
			done++
			fmt.Fprintf(cmd.ErrOrStderr(), "[%d/%d] %-4s %s (%dms)\n", done, len(jobs), o.Status, o.Label, o.LatencyMs)
			mu.Unlock()
		}(i, j)
	}
	wg.Wait()
	return results
}

func printProbeTable(cmd *cobra.Command, outcomes []probeOutcome) {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tCLASS\tCELL\tEFFECTIVE\tOUT_TOK\tTHINK\tMS\tDETAIL")
	for _, o := range outcomes {
		detail := o.Error
		if o.Status == "pass" {
			detail = o.TextExcerpt
			if o.Correct != nil {
				detail = fmt.Sprintf("correct=%v sig=%v redacted=%v think_events=%d %s", *o.Correct, o.HasSignature, o.Redacted, o.ThinkingEvents, o.TextExcerpt)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n", o.Status, o.Class, o.Label, o.Effective, o.OutputTokens, o.ThinkingLen, o.LatencyMs, excerpt(detail, 110))
	}
	_ = w.Flush()
	t := tallyOutcomes(outcomes)
	fmt.Fprintf(cmd.OutOrStdout(), "\n%d cells: %d pass, %d fail", t.Total, t.Passed, t.Failed)
	for class, n := range t.ByClass {
		fmt.Fprintf(cmd.OutOrStdout(), " [%s=%d]", class, n)
	}
	fmt.Fprintln(cmd.OutOrStdout())
}
