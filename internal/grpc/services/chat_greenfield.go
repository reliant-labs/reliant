// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
)

// Greenfield stack guidance.
//
// When the first message of a chat lands on a directory that holds no code, the
// user is asking for something to be built from nothing, and the stack is still
// an open question. This injects a hidden SYSTEM message telling the model that
// — and handing it the criteria for proposing forge — so the decision is made
// with the directory's actual contents in view rather than by defaulting to
// whatever the model reaches for first.
//
// Three deliberate properties:
//
//   - The MODEL decides. This message supplies an observation and the criteria;
//     it never says "use forge". A user who named a language or framework has
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
// Nothing is persisted to mark that the nudge fired. A second chat on a still-
// empty project gets it again, which is the intended behavior: the alternative
// is a flag that outlives its usefulness and silently suppresses guidance on a
// project the user did eventually want help starting.
//
// Once the project adopts forge, forge.yaml appears and the daemon's
// projectMemoryWithForgeFramework injects the real framework memory on every
// turn. That is the post-adoption path; this is the pre-adoption one, and the
// two never overlap because a forge project is never code-free.

// greenfieldProbeTimeoutMs bounds the daemon roundtrip. This runs on the send
// path, so it is latency the user feels on their first message; the probe is a
// bounded directory walk and should be far under this.
const greenfieldProbeTimeoutMs = 5_000

// forgeRepoURL is what the model must show the user when it adopts forge.
// Because the guidance itself is hidden, disclosure is the only thing that
// keeps the choice legible: without it the user gets a framework they never
// heard of and no way to look it up.
const forgeRepoURL = "https://github.com/reliant-labs/forge"

// codePresenceResult mirrors the daemon's project.code_presence response. It is
// redeclared here rather than imported: the API tier does not depend on the
// daemon runtime package, and only these fields are needed.
type codePresenceResult struct {
	HasCode     bool     `json:"has_code"`
	CodeFiles   []string `json:"code_files"`
	ConfigFiles []string `json:"config_files"`
	Error       string   `json:"error"`
}

// maybeGreenfieldGuidance returns a hidden system message when an EXISTING
// chat's next send is still its first turn, or nil otherwise.
//
// Used from the SendMessage fresh-start path. CreateChat has its own entry
// point below: there the chat row does not exist yet, so counting its messages
// would be both meaningless and a wasted query.
func (s *ChatService) maybeGreenfieldGuidance(ctx context.Context, userID string, chat *db.Chat) *reliantv1.InputMessage {
	if s == nil || s.daemonRouter == nil || chat == nil {
		return nil
	}

	// First turn only. The guidance is about how to START, so it is noise on
	// a conversation that is already underway — and on later turns the model
	// has the user's own words about the stack, which are better evidence
	// than a directory listing.
	count, err := s.database.CountMessagesInChat(ctx, chat.ID)
	if err != nil {
		logging.Warn("Greenfield probe: could not count chat messages; skipping",
			"error", err, "chatID", chat.ID)
		return nil
	}
	if count > 0 {
		return nil
	}

	return s.greenfieldGuidanceForChat(ctx, userID, chat)
}

// greenfieldGuidanceForChat probes the chat's working directory and builds the
// guidance when it holds no code. The caller is responsible for having
// established that this is the chat's first turn — CreateChat knows that by
// construction, SendMessage has to count.
//
// Every failure returns nil. A missing daemon, a timeout, a malformed response,
// a project whose path cannot be resolved — none of them are worth failing a
// user's message over, and the cost of skipping is that one chat does not get
// a stack suggestion.
func (s *ChatService) greenfieldGuidanceForChat(ctx context.Context, userID string, chat *db.Chat) *reliantv1.InputMessage {
	if s == nil || s.daemonRouter == nil || chat == nil {
		return nil
	}

	projectPath := s.getEffectiveWorkingPath(ctx, chat)
	if strings.TrimSpace(projectPath) == "" {
		return nil
	}

	presence, err := s.probeCodePresence(ctx, userID, projectPath)
	if err != nil {
		// An offline daemon is the common case here (onboarding races daemon
		// provisioning), not an anomaly. Debug, not Warn.
		logging.Debug("Greenfield probe: code presence unavailable; skipping",
			"error", err, "chatID", chat.ID, "projectPath", projectPath)
		return nil
	}
	if presence == nil || presence.HasCode {
		return nil
	}

	return &reliantv1.InputMessage{
		Role:         reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM,
		Content:      buildGreenfieldGuidance(presence.ConfigFiles),
		DisplayStyle: reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN.Enum(),
	}
}

// probeCodePresence asks the daemon whether the project directory holds code.
func (s *ChatService) probeCodePresence(ctx context.Context, userID, projectPath string) (*codePresenceResult, error) {
	payload, err := json.Marshal(map[string]string{"path": projectPath})
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	respBytes, err := s.daemonRouter.SendDaemonCommand(ctx, userID, "project.code_presence", payload, greenfieldProbeTimeoutMs)
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

// buildGreenfieldGuidance renders the injected message. configFiles are the
// non-code files the scan found that may still name a stack; when present the
// model is told to read them before recommending anything, because "no code"
// and "no stack opinion" are different facts.
func buildGreenfieldGuidance(configFiles []string) string {
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

	b.WriteString("If the user has NOT named one and the thing is SaaS-shaped — web or mobile app, ")
	b.WriteString("API, database, CRUD, auth — consider proposing forge (`reliant forge`). SQL ")
	b.WriteString("migrations are the schema truth, protobuf defines the API, and it generates the ")
	b.WriteString("ORM, Connect RPC handlers, typed frontend hooks, tests, CI and deploy from those ")
	b.WriteString("two inputs. It scales the whole way up: many services, web and React Native ")
	b.WriteString("frontends side by side, and deploy to k3d, Fly, Cloud Run, ECS or Lambda. Run ")
	b.WriteString("`reliant forge skill load forge` before starting.\n\n")

	b.WriteString("Propose it, do not impose it. It commits the project to Go, Postgres and Connect ")
	b.WriteString("RPC, with Next.js or React Native on top — opinionated, and silence about a ")
	b.WriteString("framework is not consent to it. Say in a sentence what it gives them and what it ")
	b.WriteString("commits them to, then proceed unless they redirect you.\n\n")

	b.WriteString("If you DO build with forge, say so explicitly in your reply — the user cannot see ")
	b.WriteString("this message, so an unannounced framework looks like it came from nowhere. Name ")
	b.WriteString("it, say in a sentence what it is, and link ")
	b.WriteString(forgeRepoURL)
	b.WriteString(" the first time you use it, not in a later summary.\n\n")

	b.WriteString("Do NOT suggest forge when: the user named a stack; the domain belongs elsewhere ")
	b.WriteString("(data science, ML, scientific computing, embedded, systems, games); the ask is a ")
	b.WriteString("script, a CLI, a library or a one-off; or the user is exploring rather than ")
	b.WriteString("building. Then say nothing about it and get on with the work.\n")
	b.WriteString("</greenfield_stack_guidance>")

	return b.String()
}
