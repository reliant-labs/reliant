package cli

// The MCP deploy tools: deploy_claimable, deploy_status and explain_error
// (whose table, explain_error.go, `forge explain` reads too).
//
// They are how an agent that has never heard of forge takes "put this live"
// to a URL and a claim link in one call. deploy_claimable is
// `forge env deploy <env> --claimable --json`, run in-process like every other
// tool, with three things the CLI cannot do for a tool call:
//
//   - ADOPT IN PLACE. A directory with no forge.yaml that holds a Vite or
//     Next.js app at its root, or an app in any language with its own
//     Dockerfile, is adopted first (`forge project new --in-place --kind
//     site`, or `--kind app --claimable`; both only add files), so the agent
//     need not know forge projects exist.
//   - STREAM. The deploy takes minutes. Every line it prints to stderr is a
//     notifications/progress message on the call's progress token, and the
//     call's context is the deploy's, so a client's cancel stops it.
//   - RELAY. The result is the claim (URL, claim link, expiry, limits) plus
//     next_steps: the text the agent says to its user. A failure carries the
//     exact next command (explainForgeError), never only a sentence.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// claimableDefaultEnv is the env deploy_claimable and deploy_status act on
// when the call names none: the one an adopted site and a `forge project new
// --claimable` project both size for the claimable plan.
const claimableDefaultEnv = "prod"

// claimableLimitsSummary is the claimable plan in one paragraph, for an agent
// to relay. The caps the deploy is actually judged by are the control plane's
// (claimable_limits.go); this is their shape, which does not move.
const claimableLimitsSummary = "An unclaimed deploy is live for 72 hours, then offline for 7 more days, then deleted " +
	"unless someone claims it. It may be one static site, or one service (one replica) with its migration job and " +
	"one Postgres database (no backups). An unclaimed service has no outbound internet. Claiming it (free) lifts all of this."

// reliantLLMProviderDocURL points to the published claim-first recovery flow.
// An unclaimed gateway route is not guaranteed; never promise it as an egress
// workaround. The linked page also explains the billing needed for a backend.
const reliantLLMProviderDocURL = "https://docs.reliantlabs.io/forge/deploy-without-an-account#what-claiming-gives-you"

// directLLMHosts are the vendor endpoints whose direct use the deploy warns
// about, with the SDK packages that call them by default.
var directLLMHosts = []struct {
	host string
	sdks []string
}{
	{"api.openai.com", []string{"openai", "github.com/openai/openai-go", "github.com/sashabaranov/go-openai"}},
	{"api.anthropic.com", []string{"@anthropic-ai/sdk", "github.com/anthropics/anthropic-sdk-go"}},
}

type deployClaimableInput struct {
	dirInput
	Env    string `json:"env,omitempty" jsonschema:"env to deploy (default prod)"`
	Name   string `json:"name,omitempty" jsonschema:"project name when adopting an app with no forge.yaml (default: the directory's name)"`
	NoWait bool   `json:"no_wait,omitempty" jsonschema:"return once the deploy is accepted, before it is live"`
}

type deployStatusInput struct {
	dirInput
	Env string `json:"env,omitempty" jsonschema:"env whose claimable deploy to show (default prod)"`
}

type explainErrorInput struct {
	ExitCode    *int   `json:"exit_code,omitempty" jsonschema:"forge's exit code, or exit_code from its --json document"`
	ErrorReason string `json:"error_reason,omitempty" jsonschema:"error_reason from the --json document, e.g. claimable_throttled"`
	Error       string `json:"error,omitempty" jsonschema:"the error text, when there is no error_reason"`
	Env         string `json:"env,omitempty" jsonschema:"env the failing command acted on (default prod)"`
}

// claimableDeployResult is deploy_claimable's and deploy_status's structured
// result.
type claimableDeployResult struct {
	OK  bool   `json:"ok"`
	Env string `json:"env"`
	// Adopted is true when this call added the forge files to an app that
	// had none; AdoptedFiles says exactly what it wrote.
	Adopted      bool   `json:"adopted,omitempty"`
	AdoptedFiles string `json:"adopted_files,omitempty"`
	// The claim, as `forge env deploy --claimable --json` reports it. State
	// is the claim's lifetime (live, suspended, claimed, expired), not the
	// rollout's: "live" means the unclaimed window is open.
	State string `json:"state,omitempty"`
	// NothingDeployed is true when the claim exists but no deploy of it has
	// published: every attempt failed. LastAttemptError is the newest one's
	// error, when it is known.
	NothingDeployed  bool   `json:"nothing_deployed,omitempty"`
	LastAttemptError string `json:"last_attempt_error,omitempty"`
	// ClaimRequired is set when the platform publishes this deploy only
	// once it is claimed, and says why (today always "content"). The claim
	// link is then the way forward.
	ClaimRequired string `json:"claim_required,omitempty"`
	// RolloutVerified is true only when this call waited for the rollout and
	// it came up healthy. A no_wait deploy and deploy_status leave it false:
	// the URL is not proven to answer, and the tool never calls it live.
	RolloutVerified bool   `json:"rollout_verified"`
	URL             string `json:"url,omitempty"`
	// APIURL is where the app's API answers on the site's own host
	// (<site origin>/_api), when the platform serves it there.
	APIURL     string `json:"api_url,omitempty"`
	ClaimURL   string `json:"claim_url,omitempty"`
	ExpiresAt  string `json:"expires_at,omitempty"`
	PurgeAfter string `json:"purge_after,omitempty"`
	// Limits is the claimable plan, to relay; NoOutboundInternet is true when
	// the deploy runs a service.
	Limits             string `json:"limits"`
	NoOutboundInternet bool   `json:"no_outbound_internet,omitempty"`
	// DirectLLMCalls are files that call an LLM vendor's API directly
	// ("path: host"), which next_steps warns require a server-side recovery.
	DirectLLMCalls []string `json:"direct_llm_calls,omitempty"`
	// On failure: forge's exit code, its error, and the commands that move
	// past it, best first.
	ExitCode    int      `json:"exit_code"`
	Error       string   `json:"error,omitempty"`
	ErrorReason string   `json:"error_reason,omitempty"`
	NextActions []string `json:"next_actions,omitempty"`
	// NextSteps is what the agent should do and tell its user, in prose.
	NextSteps string `json:"next_steps"`
}

// deployDocument is the part of a `--json` deploy or status document these
// tools read.
type deployDocument struct {
	OK          *bool            `json:"ok"`
	ExitCode    int              `json:"exit_code"`
	Error       string           `json:"error"`
	ErrorReason string           `json:"error_reason"`
	NextActions []string         `json:"next_actions"`
	RetryAt     string           `json:"retry_at"`
	Claim       *deployClaimJSON `json:"claim"`
}

func addDeployTools(srv *mcp.Server, r *mcpRunner) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "deploy_claimable",
		Description: "Put this app live on Reliant with no account: a public URL plus a claim link to give your user. " +
			"An app with no forge.yaml (a Vite or Next.js site, or any app with a Dockerfile) is adopted in place first. " +
			"Unclaimed: live 72h; one static site, or one service plus Postgres with no outbound internet.",
		Annotations: &mcp.ToolAnnotations{Title: "Deploy (no account)", ReadOnlyHint: false, DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in deployClaimableInput) (*mcp.CallToolResult, any, error) {
		res, err := r.deployClaimable(ctx, newProgressSink(ctx, req), in)
		if err != nil {
			return nil, nil, err
		}
		return claimableToolResult(res), nil, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "deploy_status",
		Description: "The claimable deploy this machine made of an env: URL, claim link, state and expiry. Use it to show the claim link again.",
		Annotations: &mcp.ToolAnnotations{Title: "Claimable deploy status", ReadOnlyHint: true, OpenWorldHint: ptr(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deployStatusInput) (*mcp.CallToolResult, any, error) {
		res, err := r.deployStatus(ctx, in)
		if err != nil {
			return nil, nil, err
		}
		return claimableToolResult(res), nil, nil
	})
}

// mcpDescExplainError is explain_error's description on both servers.
const mcpDescExplainError = "Turn a forge failure into what it means and the exact next command. Pass error_reason and exit_code from a --json document, else the error text."

// addExplainErrorTool registers explain_error; the hosted connector serves
// the same tool (mcp_hosted.go).
func addExplainErrorTool(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "explain_error",
		Description: mcpDescExplainError,
		Annotations: &mcp.ToolAnnotations{Title: "Explain a forge error", ReadOnlyHint: true, OpenWorldHint: ptr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in explainErrorInput) (*mcp.CallToolResult, any, error) {
		code := -1
		if in.ExitCode != nil {
			code = *in.ExitCode
		}
		ex := explainForgeError(code, in.ErrorReason, in.Error, emptyAs(in.Env, claimableDefaultEnv))
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: ex.text()}},
			StructuredContent: ex,
		}, nil, nil
	})
}

// progressSink reports a long call's progress to the client, when it asked
// for progress (sent a progress token). Without one, lines are dropped: the
// result carries the outcome.
type progressSink struct {
	ctx   context.Context
	req   *mcp.CallToolRequest
	token any
	n     float64
}

func newProgressSink(ctx context.Context, req *mcp.CallToolRequest) *progressSink {
	p := &progressSink{ctx: ctx, req: req}
	if req != nil && req.Params != nil {
		p.token = req.Params.GetProgressToken()
	}
	return p
}

func (p *progressSink) line(s string) {
	s = strings.TrimSpace(s)
	if p == nil || p.token == nil || s == "" || p.req.Session == nil {
		return
	}
	p.n++
	_ = p.req.Session.NotifyProgress(p.ctx, &mcp.ProgressNotificationParams{
		ProgressToken: p.token, Progress: p.n, Message: s,
	})
}

// deployClaimable is the deploy_claimable tool: adopt when needed, deploy,
// and turn the --json document into the result.
func (r *mcpRunner) deployClaimable(ctx context.Context, progress *progressSink, in deployClaimableInput) (*claimableDeployResult, error) {
	env := emptyAs(in.Env, claimableDefaultEnv)
	res := &claimableDeployResult{Env: env, Limits: claimableLimitsSummary}
	abs, err := r.resolveDir(in.Dir)
	if err != nil {
		return failedResult(res, exitWrong, err.Error(), "", nil), nil
	}
	before := snapshotAppFiles(abs)
	adopted, refusal, err := r.adoptForClaimable(ctx, abs, in.Name, progress)
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return refusal(res), nil
	}
	res.Adopted = adopted
	if adopted {
		res.AdoptedFiles = describeAdoption(before, changedAppFiles(abs, before))
	}
	res.DirectLLMCalls = findDirectLLMCalls(abs)

	args := []string{"env", "deploy", env, "--claimable", "--json"}
	if in.NoWait {
		args = append(args, "--no-wait")
	}
	progress.line(fmt.Sprintf("running %s %s", Name(), strings.Join(args, " ")))
	stdout, stderr, runErr := r.execStreams(ctx, abs, progress.line, args...)
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	doc, ok := lastDeployDocument(stdout)
	if !ok {
		// No document: forge failed before the deploy could report (a flag,
		// a render). Its own words are the error.
		msg := strings.TrimSpace(stderr)
		if runErr != nil {
			msg = strings.TrimSpace(msg + "\n" + runErr.Error())
		}
		return failedResult(res, exitCodeForError(runErr), msg, "", nil), nil
	}
	if doc.Claim != nil {
		fillClaim(res, doc.Claim)
	}
	if runErr != nil || (doc.OK != nil && !*doc.OK) {
		code := doc.ExitCode
		if code == 0 {
			code = exitCodeForError(runErr)
		}
		msg := emptyAs(doc.Error, errMessage(runErr))
		failedResult(res, code, msg, doc.ErrorReason, doc.NextActions)
		if res.ClaimRequired != "" && res.ClaimURL != "" {
			// Refused until claimed: the claim link is the next step.
			res.NextSteps = claimNextSteps(res)
		}
		if doc.RetryAt != "" {
			res.NextSteps += fmt.Sprintf(" Do not retry before %s.", doc.RetryAt)
		}
		return res, nil
	}
	res.OK = true
	// Without --no-wait a successful deploy passed the health gate; with it,
	// the control plane only accepted the deploy.
	res.RolloutVerified = !in.NoWait
	res.NextSteps = claimNextSteps(res)
	return res, nil
}

// adoptForClaimable makes dir a forge project when it is an app forge can
// adopt with none: a static Vite or Next.js app (`--kind site`), or an app in
// any language with its own Dockerfile (`--kind app --claimable`, which sizes
// prod to the claimable plan). adopted reports that it did; refusal is set
// when dir is neither a forge project nor an app forge can adopt, and
// renders the result saying what to run instead.
func (r *mcpRunner) adoptForClaimable(ctx context.Context, dir, name string, progress *progressSink) (adopted bool, refusal func(*claimableDeployResult) *claimableDeployResult, err error) {
	plan := planClaimableAdoption(dir, name)
	if plan.refusal != nil {
		ref := plan.refusal
		return false, func(res *claimableDeployResult) *claimableDeployResult {
			return failedResult(res, exitWrong, ref.msg, "", []string{ref.next})
		}, nil
	}
	if !plan.adopt {
		return false, nil, nil
	}
	progress.line(fmt.Sprintf("adopting the app in %s as forge project %q (your own files are kept)", dir, plan.name))
	args := plan.command()
	out, err := r.execStreamsCombined(ctx, dir, progress.line, args...)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil, ctx.Err()
		}
		msg, next := plan.failure(out, err)
		return false, func(res *claimableDeployResult) *claimableDeployResult {
			return failedResult(res, exitCodeForError(err), msg, "", []string{next})
		}, nil
	}
	return true, nil, nil
}

// execStreams' stdout is irrelevant to `project new`; this keeps the call
// site of adoption readable.
func (r *mcpRunner) execStreamsCombined(ctx context.Context, dir string, onLine func(string), args ...string) (string, error) {
	stdout, stderr, err := r.execStreams(ctx, dir, onLine, args...)
	return stdout + stderr, err
}

var projectNameInvalid = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// adoptionName is the project name an adopted app gets: the one asked for,
// else the directory's, made a valid forge name.
func adoptionName(dir, name string) string {
	if name != "" {
		return name
	}
	n := strings.Trim(projectNameInvalid.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-"), "-_")
	if n == "" || !(n[0] >= 'a' && n[0] <= 'z') {
		n = "site-" + n
	}
	return strings.TrimRight(n, "-")
}

// deployStatus is the deploy_status tool: `forge env status <env> --claimable --json`.
func (r *mcpRunner) deployStatus(ctx context.Context, in deployStatusInput) (*claimableDeployResult, error) {
	env := emptyAs(in.Env, claimableDefaultEnv)
	res := &claimableDeployResult{Env: env, Limits: claimableLimitsSummary}
	stdout, stderr, runErr := r.execStreams(ctx, in.Dir, nil, "env", "status", env, "--claimable", "--json")
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	doc, ok := lastDeployDocument(stdout)
	if runErr != nil || !ok || doc.Claim == nil {
		msg := strings.TrimSpace(stderr + "\n" + errMessage(runErr))
		if ok && doc.Error != "" {
			msg = doc.Error
		}
		return failedResult(res, exitCodeForError(runErr), emptyAs(msg, "no claimable deploy reported"), doc.ErrorReason, doc.NextActions), nil
	}
	fillClaim(res, doc.Claim)
	res.OK = true
	// The status document reports the claim's lifetime, never the rollout's,
	// so RolloutVerified stays false and claimNextSteps says so.
	res.NextSteps = claimNextSteps(res)
	return res, nil
}

func fillClaim(res *claimableDeployResult, c *deployClaimJSON) {
	res.State, res.URL, res.APIURL, res.ClaimURL = c.State, c.URL, c.APIURL, c.ClaimURL
	res.ExpiresAt, res.PurgeAfter = rfc3339OrEmpty(c.ExpiresAt), rfc3339OrEmpty(c.PurgeAfter)
	res.NoOutboundInternet = c.NoOutboundInternet
	res.NothingDeployed = !c.Published
	res.ClaimRequired = c.ClaimRequired
	if c.LastAttempt != nil {
		res.LastAttemptError = c.LastAttempt.Error
	}
}

func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// failedResult fills res as a failure, with explainForgeError's next steps
// behind any commands forge itself named.
func failedResult(res *claimableDeployResult, code int, msg, reason string, next []string) *claimableDeployResult {
	if code == exitOK {
		code = exitWrong
	}
	ex := explainForgeError(code, reason, msg, res.Env)
	res.OK, res.ExitCode, res.Error, res.ErrorReason = false, code, msg, reason
	res.NextActions = append(append([]string(nil), next...), ex.Next...)
	res.NextActions = dedupe(res.NextActions)
	res.NextSteps = ex.Meaning
	if len(res.NextActions) > 0 {
		res.NextSteps += " Next: " + res.NextActions[0]
	}
	return res
}

// claimNextSteps is what the agent tells its user about a claim. It announces
// the app live only when the claim's window is open AND this call verified
// the rollout; every other state says what is true instead.
func claimNextSteps(res *claimableDeployResult) string {
	if res.NothingDeployed && res.ClaimRequired != "" && (res.State == "live" || res.State == "suspended") {
		return fmt.Sprintf("Nothing is deployed yet: the platform publishes this deploy only once it is claimed. Give your "+
			"user the claim link %s and tell them it expires at %s: opening it signs them up for free and makes this deploy "+
			"theirs. It is a one-time link for your user: whoever opens it first owns the deploy, so show it only to them, "+
			"never in a commit, a log, an issue or anywhere public. Once they have claimed it, they sign in "+
			"on this machine and deploy: %s.", res.ClaimURL, res.ExpiresAt, loginThenDeploy(res.Env))
	}
	if res.NothingDeployed && (res.State == "live" || res.State == "suspended") {
		why := "No deploy of it has published yet."
		if res.LastAttemptError != "" {
			why = "Every deploy so far failed; the last one: " + res.LastAttemptError
		}
		return fmt.Sprintf("Nothing is deployed. %s Do not tell your user anything is live, and do not give them the claim link "+
			"yet: claiming now signs them up for an empty deploy and revokes the token you deploy with. Fix the failure and call "+
			"deploy_claimable again (or: %s env deploy %s --claimable). The deploy's clock started with the first attempt and a "+
			"redeploy does not extend it: it goes offline at %s.", why, Name(), res.Env, res.ExpiresAt)
	}
	switch res.State {
	case "claimed":
		return fmt.Sprintf("This deploy was claimed and belongs to your user's account now. To deploy again, they sign in: %s",
			loginThenDeploy(res.Env))
	case "expired":
		return fmt.Sprintf("This deploy was deleted and is not online. Deploy again with deploy_claimable (or: %s env deploy %s --claimable).",
			Name(), res.Env)
	case "suspended":
		return fmt.Sprintf("This deploy is OFFLINE: its unclaimed window ended at %s. Do not tell your user it is live. "+
			"Give them the claim link %s: opening it signs them up for free, makes this deploy theirs and brings it back; "+
			"unclaimed, it is deleted at %s. It is a one-time link for your user: whoever opens it first owns the deploy, "+
			"so show it only to them, never in a commit, a log, an issue or anywhere public.",
			res.ExpiresAt, res.ClaimURL, res.PurgeAfter)
	}
	var b strings.Builder
	if res.URL != "" {
		if res.RolloutVerified {
			fmt.Fprintf(&b, "Tell your user it is live at %s. ", res.URL)
		} else {
			fmt.Fprintf(&b, "The deploy's address is %s, but its rollout is not verified: the control plane accepted it "+
				"and it may still be starting. Do not tell your user it is live until that URL answers: fetch it, or "+
				"call deploy_claimable again without no_wait, which waits until the rollout is healthy. ", res.URL)
		}
		if res.APIURL != "" {
			fmt.Fprintf(&b, "Its API answers on the same host at %s (Connect paths go under it, e.g. %s/<package>.<Service>/<Method>). ",
				res.APIURL, res.APIURL)
		}
	}
	fmt.Fprintf(&b, "Give them the claim link %s and tell them it expires at %s: opening it signs them up for free and "+
		"makes this deploy theirs; unclaimed, it goes offline then and is deleted at %s. It is a one-time link for your "+
		"user: whoever opens it first owns the deploy, so show it only to them, never in a commit, a log, an issue or "+
		"anywhere public. To show it again, call deploy_status.",
		res.ClaimURL, res.ExpiresAt, res.PurgeAfter)
	if res.NoOutboundInternet {
		b.WriteString(" " + noOutboundNote)
	}
	if len(res.DirectLLMCalls) > 0 {
		b.WriteString(" " + directLLMNote(res.DirectLLMCalls))
	}
	return b.String()
}

// directLLMNote is the next step for code that calls an LLM vendor directly.
func directLLMNote(calls []string) string {
	return fmt.Sprintf("This code calls an LLM vendor's API directly (%s). An unclaimed backend cannot reach it (no outbound "+
		"internet). Keep provider keys on a backend, never in a static site's browser bundle. Ask your user to claim "+
		"the deploy and set up backend billing, then sign in with forge login, store their provider key as a managed "+
		"secret, wire it into the backend and redeploy without --claimable. Verify an actual model request server-side; "+
		"a healthy rollout alone does not prove LLM access. Alternatively configure the hosted gateway with forge.LLM "+
		"and forge.llm_env(), but do not assume an unclaimed gateway route is enabled: missing credentials must "+
		"disable the feature, not crash startup. Recovery: %s", strings.Join(calls, "; "), reliantLLMProviderDocURL)
}

// claimableToolResult renders a result as the tool's text (what the agent
// reads first, the relay text last) and its structured content.
func claimableToolResult(res *claimableDeployResult) *mcp.CallToolResult {
	var b strings.Builder
	if res.OK {
		switch res.State {
		case "claimed", "expired":
			fmt.Fprintf(&b, "State:      %s\n", res.State)
		case "suspended":
			fmt.Fprintf(&b, "Offline:    %s (since %s; deleted %s unless claimed)\nClaim link: %s\nState:      %s\n",
				res.URL, res.ExpiresAt, res.PurgeAfter, res.ClaimURL, res.State)
		default:
			if res.URL != "" && res.RolloutVerified {
				fmt.Fprintf(&b, "Live:       %s\n", res.URL)
			} else if res.URL != "" {
				fmt.Fprintf(&b, "URL:        %s (rollout not verified yet)\n", res.URL)
			}
			fmt.Fprintf(&b, "Claim link: %s\nState:      %s\nOffline at: %s (deleted %s unless claimed)\n",
				res.ClaimURL, res.State, res.ExpiresAt, res.PurgeAfter)
		}
		if res.Adopted {
			fmt.Fprintf(&b, "Adopted:    %s\n", emptyAs(res.AdoptedFiles, "forge's files were added to the app"))
		}
	} else {
		fmt.Fprintf(&b, "Deploy failed (exit %d", res.ExitCode)
		if res.ErrorReason != "" {
			fmt.Fprintf(&b, ", %s", res.ErrorReason)
		}
		fmt.Fprintf(&b, "):\n%s\n", res.Error)
		for i, a := range res.NextActions {
			label := "or:  "
			if i == 0 {
				label = "run: "
			}
			fmt.Fprintf(&b, "  %s%s\n", label, a)
		}
	}
	fmt.Fprintf(&b, "\nLimits: %s\n\nNext steps: %s", res.Limits, res.NextSteps)
	data, _ := json.Marshal(res)
	var structured map[string]any
	_ = json.Unmarshal(data, &structured)
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: b.String()}},
		StructuredContent: structured,
		IsError:           !res.OK,
	}
}

// lastDeployDocument reads the last top-level JSON object on stdout: a deploy
// may print a line before its document.
func lastDeployDocument(stdout string) (deployDocument, bool) {
	s := strings.TrimSpace(stdout)
	for end := len(s); end >= 0; {
		i := strings.LastIndex(s[:end], "\n{")
		start := i + 1
		if i < 0 {
			if !strings.HasPrefix(s, "{") {
				break
			}
			start = 0
		}
		var doc deployDocument
		if json.Unmarshal([]byte(s[start:]), &doc) == nil {
			return doc, true
		}
		if i < 0 {
			break
		}
		end = i
	}
	return deployDocument{}, false
}

func errMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// findDirectLLMCalls lists where the project calls an LLM vendor's API
// directly: the vendor host in source, or its SDK as a dependency. Bounded,
// best-effort, and silent on a tree it cannot read.
func findDirectLLMCalls(dir string) []string {
	skip := map[string]bool{"node_modules": true, ".git": true, "dist": true, "out": true, "build": true, ".next": true,
		".forge": true, "vendor": true, "gen": true, "coverage": true, ".turbo": true, ".svelte-kit": true}
	exts := map[string]bool{".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".mts": true,
		".go": true, ".py": true, ".vue": true, ".svelte": true, ".astro": true}
	found := map[string]bool{}
	files := 0
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !exts[filepath.Ext(name)] && name != "package.json" && name != "go.mod" {
			return nil
		}
		if files++; files > 5000 {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		for _, h := range directLLMHosts {
			switch name {
			case "package.json":
				var pkg struct {
					Deps    map[string]string `json:"dependencies"`
					DevDeps map[string]string `json:"devDependencies"`
				}
				if json.Unmarshal(data, &pkg) != nil {
					continue
				}
				for _, sdk := range h.sdks {
					if _, ok := pkg.Deps[sdk]; ok {
						found[fmt.Sprintf("%s: %s (calls %s)", rel, sdk, h.host)] = true
					}
				}
			case "go.mod":
				for _, sdk := range h.sdks {
					if strings.Contains(sdk, "/") && strings.Contains(string(data), sdk) {
						found[fmt.Sprintf("%s: %s (calls %s)", rel, sdk, h.host)] = true
					}
				}
			default:
				if strings.Contains(string(data), h.host) {
					found[fmt.Sprintf("%s: %s", rel, h.host)] = true
				}
			}
		}
		return nil
	})
	out := make([]string, 0, len(found))
	for f := range found {
		out = append(out, f)
	}
	sort.Strings(out)
	if len(out) > 10 {
		out = append(out[:10], fmt.Sprintf("and %d more", len(out)-10))
	}
	return out
}
