// Copyright (c) 2026 Reliant Labs

// Package email sends GTM emails through Gmail, behind an approval card that
// binds exactly what is sent.
//
// Two halves:
//
//   - Gmail implements gtm.Email: one message through the Gmail
//     integration's message.send action (catalog/gmail/manifest.yaml), with
//     the acting user's own Gmail connection, resolved from their run
//     (connauth). A run that is not the actor's, or has no usable Gmail
//     connection, is refused; nothing is sent unauthenticated or as anyone
//     else.
//   - Approver is what the send_gtm_email tool calls: Prepare builds the
//     approval card for an APPROVED email-type item and the hash that binds
//     it (gtm.ApprovalHash), and Execute sends the item only when the owner
//     approved a card whose hash still matches the item as it is now.
//
// The send never goes through the generic gmail__message_send tool: a person
// can auto-approve that tool's permission prompt, and an unattended step that
// names it runs it without one. A GTM email always needs its own card, and an
// unattended run cannot show one, so it is refused.
//
// # The send_gtm_email tool (internal/llm/tools, PR-7)
//
// The tool is a thin AutomationApprovalTool over Approver. It cannot import
// this package's types (an import cycle through the Gmail poller), so it
// calls Approver.PrepareSend and Approver.SendApproved (tool.go), which take
// only gtm types and primitives, through an interface of its own. It must
// keep to these rules:
//
//   - Its params are item_id and expected_revision only. Nothing the model
//     sends names a mailbox, a connection, a hash or a run.
//   - The actor is the resolved run caller (resolveRunCaller plus the chat's
//     org) as gtm.Actor{Kind: gtm.ActorAgent, UserID, OrgID, ChatID}.
//   - PrepareAutomation decodes the call as Run will, calls PrepareSend with
//     its unattended flag, and returns ActivationPreview{Title: title, Hash:
//     hash} with needsApproval TRUE. It never returns needsApproval false (a
//     NoApproval preview): every send needs a card. Every error is returned
//     as a tools.RefuseActivation whose reason is sanitized: this package's
//     own errors name ids and revisions only, never the email, but an error
//     from a store or a driver can carry anything, so never pass raw error
//     text as the reason.
//   - The hash is opaque: CardHash, the item's gtm.ApprovalHash, ".", and a
//     hash of the sender the card shows. The tool passes it through as the
//     preview hash unchanged and never parses or rebuilds it; the runtime
//     hands the approved value back to Execute.
//   - Execute builds gtm.EmailRun{RunID: the run (integrationRunID(chat,
//     thread)), NodeID, ToolCallID: the runtime's ToolCall.ID for this call
//     (ctxkeys.ToolCallContext.CurrentToolCallID), ConnectionID: ""} and
//     calls SendApproved with the ctx the runtime handed it, which carries
//     automationpolicy's Unattended flag and Approvals. The ToolCallID is
//     never a model value: it is what keys the approved hash.
//   - Errors from SendApproved are the tool's error response, as text.
//
// forge:outbound-io
//
// Gmail calls the Gmail API through the integration runner (httpaction), at
// the manifest connection's base_url, authenticated by the run's connection.
//
// Gmail is the package's contract (//forge:contract, the derived Service):
// internal/app's GTM provider (PR-12) builds it by hand as gtm.Deps.Email,
// and the send_gtm_email tool builds its Approver over gtm.Service.
//
//forge:provided
package email

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/integrations/catalog"

	// Importing gmail also registers the gmail.message_send executor the
	// action dispatches to.
	"github.com/reliant-labs/reliant/internal/integrations/gmail"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/writeledger"
)

// SendAction is the catalog action every GTM email is sent with.
const SendAction = "gmail/message.send@1"

// maxErrorContent bounds how much of a failed response an error quotes.
const maxErrorContent = 512

// ErrNotConfigured is a Gmail built without a runner or a credential source:
// it refuses every send rather than send unauthenticated (Unavailable). It
// is returned wrapped with gtm.ErrEmailNotSent.
var ErrNotConfigured = svcerr.Unavailable("email sending is not configured on this server")

// ErrNotConnected is a send from a run whose owner has no usable Gmail
// connection: none saved, one that needs reconnecting, one the run may not
// use, or an org policy that does not allow Gmail (FailedPrecondition). It
// is returned wrapped with gtm.ErrEmailNotSent: nothing was sent.
var ErrNotConnected = svcerr.FailedPrecondition("no usable Gmail connection for this run; connect Gmail and try again")

// Gmail's own limits on message.send (catalog/gmail/manifest.yaml), checked
// before the request so a message Gmail's schema would refuse is a certain
// not-sent rather than an error from inside the call.
const (
	maxRecipients   = 100
	maxAddressLen   = 320
	minAddressLen   = 3
	maxSubjectBytes = 998
)

// Deps are Gmail's collaborators.
type Deps struct {
	// Runner runs the catalog action: *httpaction.Runner, or the write
	// ledger's decorator of it (writeledger.Service.Wrap), which records the
	// send as the run's write. Required.
	Runner writeledger.Runner
	// Credentials resolves the run owner's Gmail connection: connauth's
	// Service. Required: there is no unauthenticated send.
	Credentials httpaction.CredentialSource
	// Runs reads whose run a send is from, from the run record: the same
	// read the credential source resolves the owner with.
	// *connections.Resolver satisfies it. Required: a send is refused unless
	// the run is the acting user's, in the acting org.
	Runs RunRecords
	// Catalog resolves SendAction; nil is the builtin catalog.
	Catalog ActionCatalog
}

// ActionCatalog resolves a catalog action by its uses reference.
// *catalog.Catalog satisfies it.
type ActionCatalog interface {
	Resolve(uses string) (*catalog.Action, error)
}

// RunRecords reads a run's owner and org from the run record (a workflow
// run, else its thread's workflow, else its chat). *connections.Resolver
// satisfies it.
type RunRecords interface {
	// OwnerOf is the user the run acts for.
	OwnerOf(ctx context.Context, runID string) (string, error)
	// RunOrgOf is the org the run belongs to; "" for none.
	RunOrgOf(ctx context.Context, runID string) (string, error)
}

// Gmail sends GTM emails through the Gmail integration. It implements
// gtm.Email.
//
//forge:contract
type Gmail struct {
	runner  writeledger.Runner
	creds   httpaction.CredentialSource
	runs    RunRecords
	catalog ActionCatalog
}

var _ gtm.Email = (*Gmail)(nil)

// NewGmail builds the sender. A missing runner, credential source or run
// records is an error: a process that cannot authenticate a send, or tell
// whose run it is from, must not build one. Service is gtm.Email's Send.
//
//forge:constructor
func NewGmail(deps Deps) (Service, error) {
	if deps.Runner == nil || deps.Credentials == nil || deps.Runs == nil {
		return nil, errors.New("email: a runner, a credential source and run records are required")
	}
	c := deps.Catalog
	if c == nil {
		builtin, err := catalog.Builtin()
		if err != nil {
			return nil, fmt.Errorf("email: load the integrations catalog: %w", err)
		}
		c = builtin
	}
	if _, err := c.Resolve(SendAction); err != nil {
		return nil, fmt.Errorf("email: %w", err)
	}
	return &Gmail{runner: deps.Runner, creds: deps.Credentials, runs: deps.Runs, catalog: c}, nil
}

// Send implements gtm.Email: it sends msg from actor's Gmail, for an email
// of actor's run, and returns Gmail's message id.
//
// The body is sent as plain text, byte for byte the markdown the approval
// card showed: what the person approved is what goes out, with no rendering
// step between them that could add, drop or reorder anything.
//
// It fails closed unless run is actor.UserID's in actor.OrgID, read from the
// run record (ErrNotRunOwner). The Gmail connection is then the run owner's,
// resolved by the credential source from that same record: the owner's
// default, or run.ConnectionID, which the source refuses unless it is the
// owner's own.
//
// An error wraps gtm.ErrEmailNotSent only when nothing can have left Gmail,
// which lets SendItemEmail release the item:
//
//   - the sender is not configured (ErrNotConfigured), or the message fails
//     validation before any request (no run, no recipients, a subject or
//     address Gmail would refuse, a body that is not valid UTF-8);
//   - the run is not the actor's (ErrNotRunOwner), or its record could not
//     be read;
//   - no usable Gmail connection (ErrNotConnected): every credential
//     failure is decided before the message is posted, or is Gmail's 401;
//   - Gmail answered 4xx (other than 408 Request Timeout), which is Gmail
//     refusing the request;
//   - ctx ended before the request was started.
//
// Every other failure (a transport error, a 5xx or 408, a 2xx without a
// message id, ctx ending while the request runs) may have sent, and does not
// wrap it.
//
// Send returns by ctx's deadline, as gtm.Email requires: a request still
// running then is abandoned (its ctx is the caller's, so it is cancelled
// with it) and reported as in doubt.
func (g *Gmail) Send(ctx context.Context, actor gtm.Actor, run gtm.EmailRun, msg gtm.EmailMessage) (string, error) {
	if g == nil || g.runner == nil || g.creds == nil || g.runs == nil || g.catalog == nil {
		return "", notSent(ErrNotConfigured)
	}
	if strings.TrimSpace(run.RunID) == "" {
		// The credential's owner is read from the run; without one there is
		// nobody to send as.
		return "", notSent(svcerr.InvalidArgument("an email is sent from a run, and none was named"))
	}
	if err := checkMessage(msg); err != nil {
		return "", notSent(err)
	}
	action, err := g.catalog.Resolve(SendAction)
	if err != nil {
		return "", notSent(svcerr.WithCause(svcerr.Internal("the Gmail send action is missing from the catalog"), err))
	}
	type outcome struct {
		id  string
		err error
	}
	// stage decides, once, whether the request starts or the send is
	// abandoned before it: whichever moves it off stagePending first wins.
	var stage atomic.Int32
	done := make(chan outcome, 1)
	go func() {
		id, err := g.deliver(ctx, actor, run, msg, action, &stage)
		done <- outcome{id, err}
	}()
	select {
	case o := <-done:
		return o.id, o.err
	case <-ctx.Done():
		// A send that finished as the deadline passed is reported as it
		// finished, not as in doubt: both cases may be ready at once, and
		// select picks one at random.
		select {
		case o := <-done:
			return o.id, o.err
		default:
		}
		if stage.CompareAndSwap(stagePending, stageAbandoned) {
			return "", notSent(svcerr.WithCause(svcerr.DeadlineExceeded("the email send ran out of time before it started"), ctx.Err()))
		}
		return "", fmt.Errorf("gmail send: abandoned at the deadline, the request may have been delivered: %w", ctx.Err())
	}
}

// Send's stages: the request has not started, it has (and may deliver), or
// Send gave up on it before it started (and it never will).
const (
	stagePending int32 = iota
	stagePosted
	stageAbandoned
)

// deliver is Send past validation: the run's owner, then the request. It
// moves stage to stagePosted just before the request may leave, unless Send
// abandoned it first.
func (g *Gmail) deliver(ctx context.Context, actor gtm.Actor, run gtm.EmailRun, msg gtm.EmailMessage, action *catalog.Action, stage *atomic.Int32) (string, error) {
	if err := g.checkRunOwner(ctx, actor.UserID, actor.OrgID, run.RunID); err != nil {
		return "", notSent(err)
	}
	if err := ctx.Err(); err != nil {
		return "", notSent(svcerr.WithCause(svcerr.DeadlineExceeded("the email send ran out of time before it started"), err))
	}
	to := make([]any, len(msg.To))
	for i, addr := range msg.To {
		to[i] = addr
	}
	params := map[string]any{"to": to, "subject": msg.Subject, "text": msg.BodyMD}
	if run.ConnectionID != "" {
		params[httpaction.ConnectionParam] = run.ConnectionID
	}
	if !stage.CompareAndSwap(stagePending, stagePosted) {
		return "", notSent(svcerr.DeadlineExceeded("the email send ran out of time before it started"))
	}
	res, err := g.runner.RunAuthenticated(ctx, action.Manifest, action.Spec, params, g.creds, httpaction.CallSite{
		RunID: run.RunID, NodeID: run.NodeID, ToolCallID: run.ToolCallID,
	})
	if err != nil {
		var ce *httpaction.CredentialError
		if errors.As(err, &ce) {
			switch ce.Code {
			case httpaction.CodeFailedPrecondition, httpaction.CodeNeedsReauth, httpaction.CodeNotFound, httpaction.CodeInvalidArgument:
				return "", notSent(svcerr.WithCause(fmt.Errorf("%w: %s", ErrNotConnected, ce.Message), err))
			}
			return "", notSent(svcerr.WithCause(svcerr.Unavailable("could not resolve the Gmail connection: "+ce.Message), err))
		}
		// Possibly after the request went out: in doubt.
		return "", fmt.Errorf("gmail send: %w", err)
	}
	if res == nil {
		return "", svcerr.Internal("the Gmail send returned no result")
	}
	if res.IsError {
		failure := fmt.Errorf("gmail send: Gmail answered %d: %s", res.StatusCode, clip(res.Content, maxErrorContent))
		if refused(res) {
			return "", notSent(failure)
		}
		return "", failure
	}
	id, _ := res.Data["id"].(string)
	if id == "" {
		return "", svcerr.Internal("the Gmail send returned no message id")
	}
	return id, nil
}

// ErrNotRunOwner is a send whose run is not the acting user's, in the
// acting org (PermissionDenied). It is returned wrapped with
// gtm.ErrEmailNotSent: nothing was sent.
var ErrNotRunOwner = svcerr.PermissionDenied("the run this email would be sent from is not yours in this organization; nothing was sent")

// checkRunOwner refuses a run that is not userID's in orgID, read from the
// run record. The credential source resolves the Gmail connection for that
// same owner (a named connection must be the owner's own), so a run that is
// the actor's sends only from the actor's Gmail.
func (g *Gmail) checkRunOwner(ctx context.Context, userID, orgID, runID string) error {
	if userID == "" || orgID == "" {
		return ErrNotRunOwner
	}
	owner, err := g.runs.OwnerOf(ctx, runID)
	if err != nil {
		return svcerr.WithCause(svcerr.Unavailable("could not read the run this email would be sent from"), err)
	}
	org, err := g.runs.RunOrgOf(ctx, runID)
	if err != nil {
		return svcerr.WithCause(svcerr.Unavailable("could not read the run this email would be sent from"), err)
	}
	if owner != userID || org != orgID {
		return svcerr.WithCause(ErrNotRunOwner, fmt.Errorf("run %s is user %q's in org %q, not user %q's in org %q", runID, owner, org, userID, orgID))
	}
	return nil
}

// notSent marks err as certain that nothing left: it is gtm.ErrEmailNotSent
// (errors.Is), and keeps err's own svcerr code and text.
func notSent(err error) error { return &notSentError{err: err} }

type notSentError struct{ err error }

func (e *notSentError) Error() string        { return e.err.Error() }
func (e *notSentError) Unwrap() error        { return e.err }
func (e *notSentError) Is(target error) bool { return target == gtm.ErrEmailNotSent }

// refused reports whether a failed response is Gmail refusing the request,
// so nothing was sent: a 4xx, except 408 (the request may have been read). A
// rate limit (RateLimited) is always a 429 or a 403, so it is one of these.
//
// The write ledger's refusal (writeledger: the intent could not be recorded,
// so the call was not made) is also certain nothing was sent, but it is an
// IsError result with no status and only its text says so; it is not told
// apart here, so it reads as in doubt and holds the item for a person to
// resolve. Mapping it to ErrEmailNotSent needs a typed marker on that result.
func refused(res *httpaction.Result) bool {
	return res.StatusCode >= 400 && res.StatusCode < 500 && res.StatusCode != http.StatusRequestTimeout
}

// checkMessage refuses, before any request, a message message.send's schema
// or gmail.Build would refuse inside the call.
func checkMessage(msg gtm.EmailMessage) error {
	if len(msg.To) == 0 {
		return svcerr.InvalidArgument("an email needs recipients")
	}
	if len(msg.To) > maxRecipients {
		return svcerr.InvalidArgument(fmt.Sprintf("an email has at most %d recipients", maxRecipients))
	}
	for _, addr := range msg.To {
		if n := len(addr); n < minAddressLen || n > maxAddressLen {
			return svcerr.InvalidArgument("a recipient address has the wrong length")
		}
	}
	if strings.TrimSpace(msg.Subject) == "" {
		return svcerr.InvalidArgument("an email needs a subject")
	}
	if len(msg.Subject) > maxSubjectBytes {
		return svcerr.InvalidArgument("the subject is too long")
	}
	if _, err := gmail.Build(gmail.Message{To: msg.To, Subject: msg.Subject, Text: msg.BodyMD}); err != nil {
		return svcerr.WithCause(svcerr.InvalidArgument("Gmail would refuse this message: "+err.Error()), err)
	}
	return nil
}

// clip bounds s to n runes.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
