// Copyright (c) 2026 Reliant Labs

// Package email sends GTM emails through Gmail, behind an approval card that
// binds exactly what is sent.
//
// Two halves:
//
//   - Gmail implements gtm.Email: one message through the Gmail
//     integration's message.send action (catalog/gmail/manifest.yaml), with
//     the run owner's own Gmail connection, resolved from the run (connauth).
//     A run with no usable Gmail connection is refused; nothing is sent
//     unauthenticated or as anyone else.
//   - Approver is what the send_gtm_email tool calls: Prepare builds the
//     approval card for an APPROVED email item and the hash that binds it
//     (ApprovalHash), and Execute sends the item only when the owner approved
//     a card whose hash still matches the item as it is now.
//
// The send never goes through the generic gmail__message_send tool: a person
// can auto-approve that tool's permission prompt, and an unattended step that
// names it runs it without one. A GTM email always needs its own card, and an
// unattended run cannot show one, so it is refused.
//
// forge:outbound-io
//
// Gmail calls the Gmail API through the integration runner (httpaction), at
// the manifest connection's base_url, authenticated by the run's connection.
//
//forge:not-a-component: a library; internal/app's GTM provider (PR-12) builds Gmail as gtm.Deps.Email, and the send_gtm_email tool builds its Approver over gtm.Service
package email

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

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
	// Catalog resolves SendAction; nil is the builtin catalog.
	Catalog *catalog.Catalog
}

// Gmail sends GTM emails through the Gmail integration. It implements
// gtm.Email.
type Gmail struct {
	runner  writeledger.Runner
	creds   httpaction.CredentialSource
	catalog *catalog.Catalog
}

var _ gtm.Email = (*Gmail)(nil)

// NewGmail builds the sender. A missing runner or credential source is an
// error: a process that cannot authenticate a send must not build one.
func NewGmail(deps Deps) (*Gmail, error) {
	if deps.Runner == nil || deps.Credentials == nil {
		return nil, errors.New("email: a runner and a credential source are required")
	}
	c := deps.Catalog
	if c == nil {
		var err error
		if c, err = catalog.Builtin(); err != nil {
			return nil, fmt.Errorf("email: load the integrations catalog: %w", err)
		}
	}
	if _, err := c.Resolve(SendAction); err != nil {
		return nil, fmt.Errorf("email: %w", err)
	}
	return &Gmail{runner: deps.Runner, creds: deps.Credentials, catalog: c}, nil
}

// Send implements gtm.Email: it sends msg from the run owner's Gmail and
// returns Gmail's message id.
//
// The body is sent as plain text, byte for byte the markdown the approval
// card showed: what the person approved is what goes out, with no rendering
// step between them that could add, drop or reorder anything.
//
// The credential is the run's: the owner is read from the run record by the
// credential source, and run.ConnectionID, when set, names one of the owner's
// own Gmail connections (the source refuses anyone else's).
//
// An error wraps gtm.ErrEmailNotSent only when nothing can have left Gmail,
// which lets SendItemEmail release the item for a retry:
//
//   - the sender is not configured (ErrNotConfigured), or the message fails
//     validation before any request (no run, no recipients, a subject or
//     address Gmail would refuse, a body that is not valid UTF-8);
//   - no usable Gmail connection (ErrNotConnected): every credential
//     failure is decided before the message is posted, or is Gmail's 401;
//   - Gmail answered 4xx (other than 408 Request Timeout), which is Gmail
//     refusing the request.
//
// Every other failure (a transport error, a timeout, a 5xx or 408, a 2xx
// without a message id) may have sent, and does not wrap it.
func (g *Gmail) Send(ctx context.Context, run gtm.EmailRun, msg gtm.EmailMessage) (string, error) {
	if g == nil || g.runner == nil || g.creds == nil || g.catalog == nil {
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
	to := make([]any, len(msg.To))
	for i, addr := range msg.To {
		to[i] = addr
	}
	params := map[string]any{"to": to, "subject": msg.Subject, "text": msg.BodyMD}
	if run.ConnectionID != "" {
		params[httpaction.ConnectionParam] = run.ConnectionID
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

// notSent marks err as certain that nothing left: it is gtm.ErrEmailNotSent
// (errors.Is), and keeps err's own svcerr code and text.
func notSent(err error) error { return &notSentError{err: err} }

type notSentError struct{ err error }

func (e *notSentError) Error() string        { return e.err.Error() }
func (e *notSentError) Unwrap() error        { return e.err }
func (e *notSentError) Is(target error) bool { return target == gtm.ErrEmailNotSent }

// refused reports whether a failed response is Gmail refusing the request,
// so nothing was sent: a 4xx, except 408 (the request may have been read).
func refused(res *httpaction.Result) bool {
	if res.RateLimited != "" {
		return true
	}
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
