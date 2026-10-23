// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// Greenfield stack guidance.
//
// When the first message of a chat lands on a directory that holds no code, the
// user is asking for something to be built from nothing, and the stack is still
// an open question. This injects a hidden SYSTEM message telling the model that
// — and handing it two sets of criteria — so the decision is made with the
// directory's actual contents in view rather than by defaulting to whatever the
// model reaches for first.
//
// The first is automations. Work that should run on its own — on a schedule, on
// a webhook or app event, or as an LLM agent acting unattended (a bot, a
// monitor, a digest) — is a Reliant workflow with a trigger, however small the
// code is. Without this the model reads "a bot that wakes up twice a day" as
// "a script" and recommends cron on a VPS or GitHub Actions, never mentioning
// the platform it is running on. It points at the workflow-builder skill and
// says that forge is where any state-holding code lives, with the workflow's
// agent calling it.
//
// The second is forge, which is not SaaS-only and not backend-only. It hosts
// anything deployed or kept running: backends, workers, jobs, cron jobs,
// operators, CLIs, web and mobile apps, and a frontend alone (a landing page,
// marketing or docs site, SPA or PWA: one static frontend on hosted static
// hosting). The frontend-only case is named in the opening sentence and gets
// its own paragraph BEFORE the app details: when it came after them, as a
// "static site is a forge case too" addendum, models read forge as a backend
// framework with a static-site exception and handed landing pages to other
// hosts with no environments or deploy story. Deploy targets named
// here must be ones forge has today: forge.External (Fly, Cloud Run, ECS,
// Lambda) was removed and is tracked in forge#400. Reliant hosting refuses
// cron-kind workloads and operators, and the guidance says so.
//
// Three deliberate properties:
//
//   - The model PROPOSES, the user decides. When the user named nothing, the
//     model proposes forge by default — for a frontend alone as much as for an
//     app — says in a sentence what it gives and commits them to, and proceeds
//     unless redirected. A user who named a language, framework or host has
//     already decided, and the guidance says so explicitly.
//   - HIDDEN, so the transcript is not polluted by harness framing the user did
//     not write. SYSTEM role rather than USER because this is machine framing;
//     mid-history System messages reach every provider (each driver's
//     system_history_test.go pins that). Because it is hidden, the guidance
//     carries its own disclosure requirement: a model that adopts forge must
//     say so and link the repo, or the user gets a framework chosen by a
//     message they were never shown.
//   - Point-in-time phrasing. The row persists, so thirty turns later the app
//     exists and "this directory is empty" would be false. "When this chat
//     started" stays true forever.
//
// WHERE it runs. The run asks, not the request: a chat's start must not wait on
// a round trip to a daemon that may be remote, suspended or cold. The run is
// started with WorkflowInput.GreenfieldProbe on its first turn and calls this
// activity once, after preflight and before its first LLM call. The guidance
// therefore lands AFTER the user's first message rather than before it, which
// is the position the run already uses for its other framing about a request:
// the model reads the ask, then the note about the directory it is being asked
// to build in. Every driver delivers a trailing System message (as a <system> user turn, or a
// developer message), and CallLLM's end-of-history guard only refuses an
// assistant tail.
//
// Nothing is persisted to mark that the nudge fired beyond the message itself.
// A second chat on a still-empty project gets it again, which is the intended
// behavior: the alternative is a flag that outlives its usefulness and
// silently suppresses guidance on a project the user did eventually want help
// starting.
//
// Once the project adopts forge, forge.yaml appears and the daemon's
// projectMemoryWithForgeFramework injects the real framework memory on every
// turn, and every agent that preloads skills also gets forge's start-here
// skill (handlers.withForgeStartHere). That is the post-adoption path; this is
// the pre-adoption one, and the two never overlap because a forge project is
// never code-free.
//
// Both are keyed on forge.yaml at the project ROOT, so the guidance asks for
// `project new --in-place`. The default `project new <name>` nests the app in
// a subdirectory, where neither the memory nor the preload ever finds it.

// greenfieldProbeTimeout bounds the daemon round trip. It is in front of the
// run's first LLM call, so it is latency on the user's first answer, and the
// cost of giving up is only that one chat gets no stack suggestion.
//
// The probe is a bounded directory walk (codePresenceScanLimit entries) that
// normally answers in well under 200ms; the slowest observed was ~780ms
// end to end, before the scan learned to exit early. 2s keeps better than 2x
// headroom over that worst case while capping what a connected-but-wedged
// daemon can cost at well under the 5s the request path used to wait. A daemon
// that is not connected at all fails fast at resolution and never reaches
// this bound. The workflow's own schedule-to-close budget sits above it
// (runtime.greenfieldProbeBudget), so this is the bound that normally fires.
const greenfieldProbeTimeout = 2 * time.Second

// forgeRepoURL is what the model must show the user when it adopts forge.
// Because the guidance itself is hidden, disclosure is the only thing that
// keeps the choice legible: without it the user gets a framework they never
// heard of and no way to look it up.
const forgeRepoURL = "https://github.com/reliant-labs/forge"

// Greenfield probe outcomes, as logged and returned. Only
// GreenfieldOutcomeGuidanceSeeded writes anything.
const (
	GreenfieldOutcomeGuidanceSeeded = "guidance_seeded"
	GreenfieldOutcomeHasCode        = "has_code"
	GreenfieldOutcomeAlreadySeeded  = "already_seeded"
	GreenfieldOutcomeNoMachine      = "skipped_no_machine"
	GreenfieldOutcomeNoPath         = "skipped_no_path"
	GreenfieldOutcomeNoDaemonRouter = "skipped_no_daemon_router"
	GreenfieldOutcomeProbeFailed    = "probe_failed"
	GreenfieldOutcomeSaveFailed     = "save_failed"
)

// codePresenceResult mirrors the daemon's project.code_presence response. It is
// redeclared here rather than imported: the worker does not depend on the
// daemon runtime package, and only these fields are needed.
type codePresenceResult struct {
	HasCode     bool     `json:"has_code"`
	CodeFiles   []string `json:"code_files"`
	ConfigFiles []string `json:"config_files"`
	Error       string   `json:"error"`
}

// greenfieldStore is the slice of the repository the probe touches.
type greenfieldStore interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetMessage(ctx context.Context, id string) (*db.Message, error)
	SaveMessageToThreadWithID(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32, messageID string) (*db.Message, error)
}

// greenfieldProber reaches the user's filesystem, which the worker cannot see
// itself. Satisfied by toolexec.DaemonRouter.
type greenfieldProber interface {
	SendDaemonCommand(ctx context.Context, userID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SendDaemonCommandToDaemon(ctx context.Context, userID string, daemonID string, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// GreenfieldProbeInput is what the run hands the probe.
type GreenfieldProbeInput struct {
	ChatID     string `json:"chat_id" reliant:"-"`
	WorkflowID string `json:"workflow_id" reliant:"-"`
	Thread     string `json:"thread" reliant:"-"`
	// ProjectPath is the run's working directory (inputs.project_path): the
	// worktree when the chat has one, else the project root.
	ProjectPath string `json:"project_path,omitempty" reliant:"-"`
	// DaemonID is the daemon the run's tools execute on, when the run has
	// pinned one. Empty probes the user's default daemon.
	DaemonID string `json:"daemon_id,omitempty" reliant:"-"`
}

// GreenfieldProbeOutput reports what the probe did. It never carries an error:
// every failure is an outcome, because none of them may change the run.
type GreenfieldProbeOutput struct {
	Outcome    string `json:"outcome"`
	DurationMs int64  `json:"duration_ms"`
}

// GreenfieldProbeActivity asks the daemon whether the run's working directory
// holds code and, when it does not, seeds the hidden guidance message.
type GreenfieldProbeActivity struct {
	repo   greenfieldStore
	prober greenfieldProber
}

// NewGreenfieldProbeActivity builds the probe. prober may be nil (a worker
// with no daemon router), which skips the probe.
func NewGreenfieldProbeActivity(repo greenfieldStore, prober greenfieldProber) *GreenfieldProbeActivity {
	return &GreenfieldProbeActivity{repo: repo, prober: prober}
}

func (a *GreenfieldProbeActivity) Name() string { return "GreenfieldProbe" }

func (a *GreenfieldProbeActivity) DisplayName() string { return "Greenfield Probe" }

func (a *GreenfieldProbeActivity) Description() string {
	return "Check whether the working directory holds code and seed stack guidance when it does not"
}

func (a *GreenfieldProbeActivity) Category() schema.ActivityCategory {
	return schema.CategoryUtility
}

// Execute never returns an error. The probe is best-effort, and an outcome
// carries everything the logs need without failing an activity in the run's
// history.
func (a *GreenfieldProbeActivity) Execute(ctx context.Context, input GreenfieldProbeInput) (GreenfieldProbeOutput, error) {
	started := time.Now()
	outcome, probeDuration, err := a.run(ctx, input)
	out := GreenfieldProbeOutput{Outcome: outcome, DurationMs: time.Since(started).Milliseconds()}

	attrs := []any{
		"chatID", input.ChatID,
		"thread", input.Thread,
		"outcome", outcome,
		"duration_ms", out.DurationMs,
		"daemon_roundtrip_ms", probeDuration.Milliseconds(),
		"daemonID", input.DaemonID,
		"projectPath", input.ProjectPath,
	}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	logging.InfoContext(ctx, "[GreenfieldProbe] Probe finished", attrs...)
	return out, nil
}

// run returns the outcome, how long the daemon round trip took (zero when
// the daemon was never asked), and the cause of a failed outcome.
func (a *GreenfieldProbeActivity) run(ctx context.Context, input GreenfieldProbeInput) (string, time.Duration, error) {
	if input.ChatID == "" || input.Thread == "" {
		return GreenfieldOutcomeProbeFailed, 0, errors.New("chat_id and thread are required")
	}
	chat, err := a.repo.GetChat(ctx, input.ChatID)
	if err != nil {
		return GreenfieldOutcomeProbeFailed, 0, fmt.Errorf("load chat: %w", err)
	}
	// A run with no machine never reaches one, not even to ask a question.
	if chat.NoMachine {
		return GreenfieldOutcomeNoMachine, 0, nil
	}
	if strings.TrimSpace(input.ProjectPath) == "" {
		return GreenfieldOutcomeNoPath, 0, nil
	}
	if a.prober == nil {
		return GreenfieldOutcomeNoDaemonRouter, 0, nil
	}

	// A reset that replays from before this activity runs it again; the
	// thread-derived id makes the second run a no-op instead of a duplicate.
	messageID := greenfieldGuidanceMessageID(input.Thread)
	if existing, err := a.repo.GetMessage(ctx, messageID); err == nil && existing != nil {
		return GreenfieldOutcomeAlreadySeeded, 0, nil
	}

	probeStarted := time.Now()
	presence, err := a.probeCodePresence(ctx, chat.UserID, input.DaemonID, input.ProjectPath)
	probeDuration := time.Since(probeStarted)
	if err != nil {
		// An offline or slow daemon is an expected outcome here (onboarding
		// races daemon provisioning), not an anomaly.
		return GreenfieldOutcomeProbeFailed, probeDuration, err
	}
	if presence.HasCode {
		return GreenfieldOutcomeHasCode, probeDuration, nil
	}

	hidden := int32(reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN)
	workflowID := input.WorkflowID
	if _, err := a.repo.SaveMessageToThreadWithID(ctx, input.ChatID, input.Thread,
		int32(reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM), BuildGreenfieldGuidance(presence.ConfigFiles),
		&workflowID, nil, &hidden, messageID); err != nil {
		return GreenfieldOutcomeSaveFailed, probeDuration, fmt.Errorf("save guidance: %w", err)
	}
	return GreenfieldOutcomeGuidanceSeeded, probeDuration, nil
}

// probeCodePresence asks the daemon whether the directory holds code.
func (a *GreenfieldProbeActivity) probeCodePresence(ctx context.Context, userID, daemonID, projectPath string) (*codePresenceResult, error) {
	payload, err := json.Marshal(map[string]string{"path": projectPath})
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	probeCtx, cancel := context.WithTimeout(ctx, greenfieldProbeTimeout)
	defer cancel()
	timeoutMs := int32(greenfieldProbeTimeout / time.Millisecond)

	var respBytes []byte
	if daemonID != "" {
		respBytes, err = a.prober.SendDaemonCommandToDaemon(probeCtx, userID, daemonID, "project.code_presence", payload, timeoutMs)
	} else {
		respBytes, err = a.prober.SendDaemonCommand(probeCtx, userID, "project.code_presence", payload, timeoutMs)
	}
	if err != nil {
		return nil, fmt.Errorf("daemon command project.code_presence: %w", err)
	}

	var result codePresenceResult
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("unmarshal code presence response: %w", err)
	}
	if result.Error != "" {
		return nil, fmt.Errorf("code presence scan failed: %s", result.Error)
	}
	return &result, nil
}

// greenfieldGuidanceNamespace scopes greenfieldGuidanceMessageID.
var greenfieldGuidanceNamespace = uuid.MustParse("6f1d6a3e-5b0c-4f3e-9a51-2f8c0c3b7d41")

// greenfieldGuidanceMessageID is the id the guidance is saved under. One per
// thread: a thread is seeded at most once, however often its run replays.
func greenfieldGuidanceMessageID(thread string) string {
	return uuid.NewSHA1(greenfieldGuidanceNamespace, []byte(thread)).String()
}

// BuildGreenfieldGuidance renders the injected message. configFiles are the
// non-code files the scan found that may still name a stack; when present the
// model is told to read them before recommending anything, because "no code"
// and "no stack opinion" are different facts.
func BuildGreenfieldGuidance(configFiles []string) string {
	var b strings.Builder

	b.WriteString("<greenfield_stack_guidance>\n")
	b.WriteString("When this chat started, the project directory contained no source code, so the ")
	b.WriteString("stack is still an open question and the choice is yours to make with the user.\n\n")

	if len(configFiles) > 0 {
		b.WriteString("Read these first — no code, but they may already name a stack, which counts ")
		b.WriteString("as the user having chosen:\n")
		for _, f := range configFiles {
			b.WriteString("  - ")
			b.WriteString(f)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("If what the user describes should run on its own — on a schedule (\"every morning\", ")
	b.WriteString("\"twice a day\", cron), on a webhook or an app event, or as an LLM agent acting unattended ")
	b.WriteString("(a bot, a monitor, a digest, triage) — then however small the code is, it runs as a Reliant ")
	b.WriteString("workflow with a trigger: schedules, credentials, integrations, approvals and run history are ")
	b.WriteString("built in, and there is no server to keep alive. Load the `workflow-builder` skill before ")
	b.WriteString("designing it, and tell the user that is what you are proposing. Reach for cron on a server, ")
	b.WriteString("GitHub Actions or Lambda only if they ask for it. When it also needs code that keeps state — ")
	b.WriteString("a database, an API, a dashboard — that code is a forge project (below) and the workflow's agent ")
	b.WriteString("calls it: the agent decides, the app holds the data and enforces the rules.\n\n")

	b.WriteString("If the user has NOT named a stack and they are building something that will be deployed or ")
	b.WriteString("kept running, propose forge (`reliant forge`) by default. That holds for a frontend with no ")
	b.WriteString("backend — a landing page, marketing or docs site, SPA or PWA — exactly as much as for a ")
	b.WriteString("backend or API, background workers, jobs or cron jobs, Kubernetes operators, a CLI, or a web ")
	b.WriteString("or mobile app. It starts the project with production practices in place: structured logs, ")
	b.WriteString("traces and metrics on every boundary, health probes, auth that fails closed, errors that never ")
	b.WriteString("leak internals, secrets kept out of git, and dev, staging and prod environments from day one, ")
	b.WriteString("with releases built once and promoted by digest.\n\n")

	b.WriteString("For a frontend alone — a static site, SPA or PWA — a forge project is one static frontend and ")
	b.WriteString("no backend: `reliant forge env up dev` runs it locally with no database or cluster, and it is ")
	b.WriteString("hosted on Reliant's static hosting (bucket, CDN and hostname handled) with the same ")
	b.WriteString("environments, releases and custom domains, a component library and the `frontend/design` ")
	b.WriteString("skill, browser console logs in .forge/logs, and room to add an API to the same project when it ")
	b.WriteString("needs a waitlist or a checkout. For a PWA, forge builds, hosts and ships it; the service worker ")
	b.WriteString("and manifest are yours to write. Run `reliant forge skill load deploy/static-site` for that ")
	b.WriteString("path instead of the service sequence.\n\n")

	b.WriteString("For an app, SQL migrations are the schema truth, protobuf defines the API, and forge ")
	b.WriteString("generates the ORM, Connect RPC handlers, typed frontend hooks and tests from those two inputs. ")
	b.WriteString("It scales the whole way up: many services, Next.js and React Native (Expo) frontends side by ")
	b.WriteString("side, deployed as host processes, to a Kubernetes cluster they operate, or hosted on Reliant's ")
	b.WriteString("infrastructure with a managed Postgres, managed secrets and custom domains. Run ")
	b.WriteString("`reliant forge skill load forge` before starting.\n\n")

	b.WriteString("Build it IN this directory: `reliant forge project new --in-place --mod <module>` ")
	b.WriteString("(add `--name <product>` when the directory is not named after the product), not ")
	b.WriteString("`project new <name>`, which nests the app one level down. The harness recognizes a ")
	b.WriteString("forge project by forge.yaml at the project root — that is what gives every later ")
	b.WriteString("turn, and every sub-agent, forge's framework memory and its start-here skill — and ")
	b.WriteString("--in-place keeps files already here rather than overwriting them.\n\n")

	b.WriteString("Publishing defaults to Reliant hosting: a new forge project's staging and prod are ")
	b.WriteString("already bound to it, and `reliant forge env deploy <env>` ships it. A static site alone ")
	b.WriteString("has a free tier (one site per org, and each environment's site counts — prod alone is free, ")
	b.WriteString("staging too needs billing); hosted services and the managed database need billing, and without it ")
	b.WriteString("the deploy is queued (exit 7) and goes live once the user sets billing up — hand them ")
	b.WriteString("the link it prints. Reliant hosting runs services, workers and jobs; a cron-kind ")
	b.WriteString("workload or an operator needs a cluster they operate (a worker that runs its own ")
	b.WriteString("schedule is fine hosted). Run `reliant forge skill load deploy/hosting` before deploying.\n\n")

	b.WriteString("Propose it, do not impose it. For an app it commits the project to Go, Postgres ")
	b.WriteString("and Connect RPC, with Next.js or React Native on top; for a static site, to a ")
	b.WriteString("Next.js (or Vite) frontend inside a forge project. Opinionated, and silence about ")
	b.WriteString("a framework is not consent to it. Say in a sentence what it gives them and what ")
	b.WriteString("it commits them to, then proceed unless they redirect you.\n\n")

	b.WriteString("If you DO build with forge, say so explicitly in your reply — the user cannot see ")
	b.WriteString("this message, so an unannounced framework looks like it came from nowhere. Name ")
	b.WriteString("it, say in a sentence what it is, and link ")
	b.WriteString(forgeRepoURL)
	b.WriteString(" the first time you use it, not in a later summary.\n\n")

	b.WriteString("Do NOT suggest forge when: the user named a stack or a host; the domain belongs elsewhere (data ")
	b.WriteString("science, ML, scientific computing, embedded, games); the ask is a library or a one-off ")
	b.WriteString("script; the page is a throwaway mockup that will never be deployed; or the user is exploring ")
	b.WriteString("rather than building. A script that should run on a schedule or react to events is not a ")
	b.WriteString("one-off — it is an automation (above). Otherwise say nothing about forge and get on with the work.\n")
	b.WriteString("</greenfield_stack_guidance>")

	return b.String()
}
