// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.temporal.io/sdk/temporal"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gitpublish"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/provenance"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/netguard"
	"github.com/reliant-labs/reliant/internal/nomachine"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime/schema"
)

// A git_publish node commits a worktree's changes and pushes them to a feature
// branch, with no shell ever holding a credential.
//
// Where each decision is made:
//
//	this activity (server)  policy: which branch, GitHub's view of it; the
//	                        ledger intent; fetching the credential
//	git.publish_prepare     the machine: stage and commit, no credential
//	git.publish             the machine: ONE push, credential on a private pipe
//
// The credential is fetched after the policy and the intent, immediately before
// the push command, and goes nowhere but that command's payload.

// GitPublishActivity implements the git_publish node type.
type GitPublishActivity struct {
	repo         GitPublishStore
	daemonRouter toolexec.DaemonRouter
	catalog      *catalog.Catalog
	runner       writeledger.Runner
	credentials  httpaction.CredentialSource
	ledger       writeledger.Service
	// handoff is deliberately nil in app wiring until durable author wait exists.
	handoff publishHandoff
	// token returns the credential for one push. It is a field so tests can
	// stand in for control-plane.
	token pushTokenSource
	// github answers the branch policy's questions. nil builds one over the
	// github integration with the run's identity.
	github func(site httpaction.CallSite) gitpublish.GitHubReader
}

// pushTokenSource yields the credential a push to repo is made with, and whose
// it is (core.IntegrationActorOwner or core.IntegrationActorApp). A run whose
// activation chose the Reliant bot pushes with an installation token scoped to
// repo, or not at all: an error here is never answered with the owner's token.
type pushTokenSource interface {
	PushToken(ctx context.Context, site httpaction.CallSite, repo gitpublish.Repo) (token string, actor string, err error)
}

// NewGitPublishActivity builds the activity. credentials resolves the GitHub
// credential the same way an action node's does (a saved connection, else the
// delegated broker); nil makes every publish fail with a FailedPrecondition.
func NewGitPublishActivity(repo GitPublishStore, daemonRouter toolexec.DaemonRouter, credentials httpaction.CredentialSource) *GitPublishActivity {
	cat := catalog.MustBuiltin()
	return &GitPublishActivity{
		repo: repo, daemonRouter: daemonRouter, catalog: cat,
		runner: httpaction.NewRunner(netguard.New()), credentials: credentials,
		token: runPushToken{credentials: credentials},
	}
}

// WithLedger records every push in the write ledger. A nil ledger records
// nothing.
func (a *GitPublishActivity) WithLedger(l writeledger.Service) *GitPublishActivity {
	clone := *a
	clone.ledger = l
	return &clone
}

func (a *GitPublishActivity) Name() string        { return "GitPublish" }
func (a *GitPublishActivity) DisplayName() string { return "Publish Branch" }
func (a *GitPublishActivity) Description() string {
	return "Commit a worktree's changes and push them to a feature branch"
}
func (a *GitPublishActivity) Category() schema.ActivityCategory { return schema.CategoryWorktree }

// Execute runs one publish. A refusal (policy, or the remote) does NOT fail the
// node: it is the output, `refused` with a `refusal`, and the graph decides what
// to do about it. Errors are for what could not be evaluated or reached.
func (a *GitPublishActivity) Execute(ctx context.Context, input ActivityInput) (*reliantv1.GitPublishOutput, error) {
	rtx := input.Runtime
	args := input.Node.GetGitPublish()
	if args == nil {
		return nil, fmt.Errorf("expected git_publish node, got %s", model.NodeType(input.Node))
	}
	refuse := func(format string, a ...any) (*reliantv1.GitPublishOutput, error) {
		return &reliantv1.GitPublishOutput{Refused: true, Refusal: fmt.Sprintf(format, a...)}, nil
	}

	worktree := strings.TrimSpace(model.CelStringValue(args.GetWorktree()))
	repoName := strings.TrimSpace(model.CelStringValue(args.GetRepo()))
	message := model.CelStringValue(args.GetMessage())
	if worktree == "" || repoName == "" || strings.TrimSpace(message) == "" {
		return refuse("git_publish needs a worktree, a repo and a commit message; one of them is empty")
	}
	owner, name, ok := strings.Cut(repoName, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return refuse("repo must be owner/name, got %q", repoName)
	}
	repo := gitpublish.Repo{Owner: owner, Name: name}

	chat, err := a.repo.GetChat(ctx, rtx.ChatID)
	if err != nil {
		return nil, fmt.Errorf("failed to get chat: %w", err)
	}
	if chat.NoMachine {
		return nil, temporal.NewNonRetryableApplicationError(
			nomachine.ErrNoMachine.Error()+": git_publish needs the checkout on the user's machine", "NoMachine", nil)
	}
	if a.daemonRouter == nil {
		return nil, errors.New("daemon router not available; git_publish requires a daemon connection")
	}
	site := httpaction.CallSite{RunID: rtx.WorkflowID, NodeID: rtx.StepID,
		ToolCallID:     invokeToolCallID(rtx.WorkflowID, rtx.StepID, rtx.LoopNodeID, rtx.LoopIteration),
		ActingIdentity: rtx.ActingIdentity, Automation: rtx.Automation}

	// 1. Policy, on the server, with GitHub's own answers.
	var gh gitpublish.GitHubReader = &githubReader{catalog: a.catalog, runner: a.runner, credentials: a.credentials, site: site}
	if a.github != nil {
		gh = a.github(site)
	}
	decision, err := gitpublish.Check(ctx, gh, gitpublish.Request{
		Repo:     repo,
		Branch:   model.CelStringValue(args.GetBranch()),
		PRHeadOf: model.CelIntValue(args.GetPrHeadOf()),
		Allow:    model.CelStringListValue(args.GetAllow()),
	})
	if err != nil {
		// Could not ask GitHub: nothing is pushed, and the step may be retried.
		var ce *httpaction.CredentialError
		if errors.As(err, &ce) {
			return refuse("%s", ce.Message)
		}
		return nil, fmt.Errorf("git_publish: evaluating the branch policy: %w", err)
	}
	if !decision.Allowed {
		return refuse("%s", decision.Reason)
	}

	// 2. The machine: the run's own daemon, chosen as the run's tools are.
	machine, err := a.machineFor(ctx, chat, rtx)
	if err != nil {
		return nil, err
	}

	prep, err := a.prepare(ctx, machine, worktree, message, rtx, chat)
	if err != nil {
		return nil, err
	}
	out := &reliantv1.GitPublishOutput{
		Branch: decision.Branch, Sha: prep.HeadSHA, BaseSha: prep.BaseSHA,
		FilesChanged: int32(prep.FilesChanged), Insertions: int32(prep.Insertions), Deletions: int32(prep.Deletions),
	}
	if prep.Commits == 0 {
		// No successful push receipt: never claim an existing remote branch.
		// Ambiguous previous pushes need explicit receipt reconciliation.
		return out, nil
	}

	previousHead := ""
	if a.handoff != nil {
		previousHead, err = a.handoffPreviousHead(ctx, site, owner, name, decision.Branch)
		if err != nil {
			return nil, err
		}
	}

	// 3. Record the intent BEFORE pushing, with the sha the push will end on.
	// The credential is not yet fetched: a ledger outage costs nothing.
	token, actor, err := a.token.PushToken(ctx, site, repo)
	if err != nil {
		var ce *httpaction.CredentialError
		if errors.As(err, &ce) {
			return refuse("%s", ce.Message)
		}
		return nil, fmt.Errorf("git_publish: fetching the credential: %w", err)
	}
	intent, err := a.beginPush(ctx, writeledger.Push{
		Repo: repo.String(), Branch: decision.Branch, SHA: prep.HeadSHA,
		PullNumber: model.CelIntValue(args.GetPrHeadOf()),
		RunID:      rtx.WorkflowID, NodeID: rtx.StepID, Actor: actor,
	})
	if err != nil {
		return refuse("%v", err)
	}

	// 4. The push. The token lives in this payload and nowhere else.
	push, err := a.push(ctx, machine, gitpublish.PushRequest{
		Worktree: worktree, RemoteURL: gitpublish.RemoteURL(repo), Branch: decision.Branch,
		ExpectSHA: prep.HeadSHA, Token: token,
	})
	if err != nil {
		// The machine may have pushed before it went away: ambiguous.
		intent.Settle(ctx, writeledger.PushOutcome{Ambiguous: true, Error: err.Error()})
		return nil, fmt.Errorf("git_publish: pushing: %w", err)
	}
	intent.Settle(ctx, writeledger.PushOutcome{Pushed: push.Pushed, Error: push.Refusal})
	if push.Refusal != "" {
		out.Refused, out.Refusal = true, push.Refusal
		return out, nil
	}
	out.Pushed = push.Pushed
	if push.Pushed && a.handoff != nil {
		if err := a.recordHandoff(ctx, rtx, repo.String(), worktree, decision.Branch, prep.HeadSHA, previousHead); err != nil {
			return nil, temporal.NewNonRetryableApplicationError(err.Error(), "PRHandoffNeedsHuman", err)
		}
	}
	logging.InfoContext(ctx, "[GitPublish] Completed", "stepID", rtx.StepID, "repo", repo.String(),
		"branch", decision.Branch, "sha", prep.HeadSHA, "pushed", push.Pushed, "ledger", intent.IDs())
	return out, nil
}

// machineFor is the daemon the run's tools execute on, chosen exactly as
// create_worktree and execute_tools choose it.
type publishMachine struct {
	router   toolexec.DaemonRouter
	userID   string
	daemonID string
}

func (a *GitPublishActivity) machineFor(ctx context.Context, chat *core.Chat, rtx RuntimeContext) (publishMachine, error) {
	owner, err := worktreeOwnerOf(ctx, a.repo, chat.WorktreeID)
	if err != nil {
		return publishMachine{}, err
	}
	id, err := toolexec.ResolveDaemonIDForSelector(ctx, a.daemonRouter, chat.UserID, toolDaemonSelector(owner, chatDaemonSelector(chat, rtx.DaemonSelector)))
	if err != nil {
		return publishMachine{}, fmt.Errorf("git_publish: resolving the machine: %w", err)
	}
	return publishMachine{router: a.daemonRouter, userID: chat.UserID, daemonID: id}, nil
}

func (m publishMachine) send(ctx context.Context, command string, req, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", command, err)
	}
	raw, err := m.router.SendDaemonCommandToDaemon(ctx, m.userID, m.daemonID, command, payload, timeoutMs)
	if err != nil {
		// err can carry the daemon's text; the payload (and so the token) is not in it.
		return fmt.Errorf("daemon command %s: %w", command, err)
	}
	if err := json.Unmarshal(raw, resp); err != nil {
		return fmt.Errorf("unmarshal %s response: %w", command, err)
	}
	return nil
}

const (
	publishPrepareTimeoutMs = 120_000
	publishPushTimeoutMs    = 300_000
)

func (a *GitPublishActivity) prepare(ctx context.Context, m publishMachine, worktree, message string, rtx RuntimeContext, chat *core.Chat) (gitpublish.PrepareResponse, error) {
	req := gitpublish.PrepareRequest{
		Worktree: worktree, Message: message,
		Trailers: []gitpublish.Trailer{{Key: provenance.RunTrailerKey, Value: runTrailerValue(rtx, chat)}},
	}
	if wf := workflowNameOf(chat); wf != "" {
		req.Trailers = append(req.Trailers, gitpublish.Trailer{Key: "Reliant-Workflow", Value: wf})
	}
	var resp gitpublish.PrepareResponse
	if err := m.send(ctx, gitpublish.CommandPrepare, req, &resp, publishPrepareTimeoutMs); err != nil {
		return resp, fmt.Errorf("git_publish: preparing the commit: %w", err)
	}
	return resp, nil
}

func (a *GitPublishActivity) push(ctx context.Context, m publishMachine, req gitpublish.PushRequest) (gitpublish.PushResponse, error) {
	var resp gitpublish.PushResponse
	err := m.send(ctx, gitpublish.CommandPush, req, &resp, publishPushTimeoutMs)
	return resp, err
}

// runTrailerValue is what Reliant-Run carries: the run's id, which the origin
// enricher resolves back to the run. A claim anyone can write, so it only ever
// suppresses; the ledger row is what attributes.
func runTrailerValue(rtx RuntimeContext, chat *core.Chat) string {
	if rtx.WorkflowID != "" {
		return rtx.WorkflowID
	}
	return chat.ID
}

func workflowNameOf(chat *core.Chat) string {
	if chat.WorkflowName != nil {
		return strings.TrimSpace(*chat.WorkflowName)
	}
	return ""
}

// runPushToken is the identity the run was activated with. An owner run uses
// the owner's token, through the github integration's delegated broker (or a
// saved connection), exactly as an action node authenticates. A run whose site
// says httpaction.IdentityApp asks the same source for the bot's installation
// token as a write to this repository, so it is subject to the same rules as
// any other write the bot makes: there is no second, looser path to the owner.
type runPushToken struct{ credentials httpaction.CredentialSource }

func (o runPushToken) PushToken(ctx context.Context, site httpaction.CallSite, repo gitpublish.Repo) (string, string, error) {
	if o.credentials == nil {
		return "", "", &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "connections are not available in this process"}
	}
	m, err := catalog.MustBuiltin().Manifest("github", 1)
	if err != nil {
		return "", "", err
	}
	req := httpaction.CredentialRequest{
		RunID: site.RunID, NodeID: site.NodeID, ToolCallID: site.ToolCallID,
		IntegrationID: "github", Connection: m.GetConnection(), ServerPlaced: true,
	}
	actor := core.IntegrationActorOwner
	switch site.ActingIdentity {
	case "":
	case httpaction.IdentityApp:
		req.ActingIdentity, req.Automation = site.ActingIdentity, site.Automation
		req.ActionID, req.Mutates = writeledger.GitPushAction, true
		req.Params = map[string]any{"owner": repo.Owner, "repo": repo.Name}
		actor = core.IntegrationActorApp
	default:
		return "", "", &httpaction.CredentialError{Code: httpaction.CodeInvalidArgument,
			Message: fmt.Sprintf("unknown acting identity %q: a push is made as the owner or as the Reliant bot", site.ActingIdentity)}
	}
	cred, err := o.credentials.Credential(ctx, req)
	if err != nil {
		return "", "", err
	}
	if cred == nil {
		return "", "", &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "the connection resolved to no credential"}
	}
	token, err := tokenOf(cred)
	if err != nil {
		return "", "", err
	}
	return token, actor, nil
}

// beginPush records the push intent. No ledger in this process (self-hosted
// without one, tests) records nothing and does not refuse.
func (a *GitPublishActivity) beginPush(ctx context.Context, p writeledger.Push) (*writeledger.PushIntent, error) {
	if a.ledger == nil {
		return nil, nil
	}
	return a.ledger.BeginPush(ctx, p)
}
