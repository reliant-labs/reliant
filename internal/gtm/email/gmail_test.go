// Copyright (c) 2026 Reliant Labs

package email_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/email"
	"github.com/reliant-labs/reliant/internal/integrations/gmail"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/netguard"
)

// fakeCredential is a resolved Gmail connection; it never touches a request.
type fakeCredential struct{ id string }

func (c fakeCredential) ConnectionID() string    { return c.id }
func (fakeCredential) Apply(*http.Request) error { return nil }
func (fakeCredential) Scrub(s string) string     { return s }
func (fakeCredential) Params() map[string]string { return nil }

// fakeSource is the run's credential source (connauth in production): it
// records what was asked and answers with a connection or a refusal.
type fakeSource struct {
	mu   sync.Mutex
	reqs []httpaction.CredentialRequest
	err  error
}

func (s *fakeSource) Credential(_ context.Context, req httpaction.CredentialRequest) (httpaction.Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return nil, s.err
	}
	id := req.ConnectionID
	if id == "" {
		id = "conn-default"
	}
	return fakeCredential{id: id}, nil
}

// fakeExecutor stands in for gmail.message_send: the real runner validates
// the params against the manifest, resolves the credential and dispatches
// here instead of to Gmail.
type fakeExecutor struct {
	mu     sync.Mutex
	calls  []httpaction.ExecutorCall
	result *httpaction.Result
	err    error
}

func (e *fakeExecutor) send(_ context.Context, call httpaction.ExecutorCall) (*httpaction.Result, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call)
	if e.err != nil {
		return nil, e.err
	}
	if e.result != nil {
		return e.result, nil
	}
	return &httpaction.Result{StatusCode: 200, Data: map[string]any{"id": "gmail-msg-1", "thread_id": "thread-1"}}, nil
}

func (e *fakeExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// newGmail is the real sender over the real runner and catalog, with the
// Gmail executor faked.
func newGmail(t *testing.T, src httpaction.CredentialSource, exec *fakeExecutor) *email.Gmail {
	t.Helper()
	reg := httpaction.NewExecutorRegistry()
	reg.MustRegister(gmail.SendExecutor, exec.send)
	g, err := email.NewGmail(email.Deps{
		Runner:      httpaction.NewRunner(netguard.New()).WithExecutors(reg),
		Credentials: src,
	})
	require.NoError(t, err)
	return g
}

var testRun = gtm.EmailRun{RunID: "run-1", NodeID: "node-1", ToolCallID: "call-1"}

var testMessage = gtm.EmailMessage{
	To:      []string{"ann@example.com", "bo@example.com"},
	Subject: "Launch week",
	BodyMD:  "Hi,\n\nWe **shipped** it.\n",
}

func TestGmailSend_SendsAsTheRunsOwner(t *testing.T) {
	src := &fakeSource{}
	exec := &fakeExecutor{}
	g := newGmail(t, src, exec)

	id, err := g.Send(context.Background(), testRun, testMessage)
	require.NoError(t, err)
	assert.Equal(t, "gmail-msg-1", id)

	require.Len(t, src.reqs, 1)
	req := src.reqs[0]
	assert.Equal(t, "run-1", req.RunID, "the owner is read from the run")
	assert.Equal(t, "node-1", req.NodeID)
	assert.Equal(t, "call-1", req.ToolCallID)
	assert.Equal(t, "gmail", req.IntegrationID)
	assert.Equal(t, "message.send", req.ActionID)
	assert.True(t, req.Mutates)
	assert.True(t, req.ServerPlaced)
	assert.Empty(t, req.ConnectionID, "no connection named: the owner's default")
	assert.Empty(t, req.ActingIdentity, "a GTM email is the owner's own")

	require.Equal(t, 1, exec.count())
	call := exec.calls[0]
	assert.Equal(t, "conn-default", call.Credential.ConnectionID())
	assert.Equal(t, []any{"ann@example.com", "bo@example.com"}, call.Params["to"])
	assert.Equal(t, "Launch week", call.Params["subject"])
	assert.Equal(t, testMessage.BodyMD, call.Params["text"], "the body goes out byte for byte as the card showed it")
	assert.NotContains(t, call.Params, "html")
	assert.NotContains(t, call.Params, "cc")
	assert.NotContains(t, call.Params, "bcc")
}

func TestGmailSend_NamedConnection(t *testing.T) {
	src := &fakeSource{}
	exec := &fakeExecutor{}
	g := newGmail(t, src, exec)

	run := testRun
	run.ConnectionID = "conn-work"
	_, err := g.Send(context.Background(), run, testMessage)
	require.NoError(t, err)
	require.Len(t, src.reqs, 1)
	assert.Equal(t, "conn-work", src.reqs[0].ConnectionID)
	assert.Equal(t, "conn-work", exec.calls[0].Credential.ConnectionID())
}

// No Gmail connection, one that needs reconnecting, or one the run may not
// use: the send is refused, nothing reaches Gmail, and the error says so
// (gtm.ErrEmailNotSent), so SendItemEmail releases the item for a retry.
func TestGmailSend_FailsClosedWithNoGmailConnection(t *testing.T) {
	for _, code := range []string{httpaction.CodeFailedPrecondition, httpaction.CodeNotFound, httpaction.CodeNeedsReauth, httpaction.CodeInvalidArgument} {
		t.Run(code, func(t *testing.T) {
			src := &fakeSource{err: &httpaction.CredentialError{Code: code, Message: "Gmail is not connected"}}
			exec := &fakeExecutor{}
			g := newGmail(t, src, exec)

			id, err := g.Send(context.Background(), testRun, testMessage)
			require.Error(t, err)
			assert.Empty(t, id)
			assert.ErrorIs(t, err, email.ErrNotConnected)
			assert.ErrorIs(t, err, gtm.ErrEmailNotSent, "certain not sent: the hold is released")
			assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
			assert.Zero(t, exec.count(), "nothing is sent without the run's connection")
		})
	}
	t.Run("resolution outage", func(t *testing.T) {
		src := &fakeSource{err: errors.New("connections db down")}
		exec := &fakeExecutor{}
		g := newGmail(t, src, exec)

		_, err := g.Send(context.Background(), testRun, testMessage)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
		assert.ErrorIs(t, err, gtm.ErrEmailNotSent, "the credential is resolved before anything is posted")
		assert.Zero(t, exec.count())
	})
}

func TestGmailSend_RefusesWithoutARun(t *testing.T) {
	src := &fakeSource{}
	exec := &fakeExecutor{}
	g := newGmail(t, src, exec)

	_, err := g.Send(context.Background(), gtm.EmailRun{ToolCallID: "call-1"}, testMessage)
	require.Error(t, err)
	assert.ErrorIs(t, err, gtm.ErrEmailNotSent)
	assert.Empty(t, src.reqs, "no run: no owner to resolve a credential for")
	assert.Zero(t, exec.count())
}

func TestGmailSend_RefusesAnIncompleteMessage(t *testing.T) {
	for name, msg := range map[string]gtm.EmailMessage{
		"no recipients":     {Subject: "s", BodyMD: "b"},
		"no subject":        {To: []string{"ann@example.com"}, BodyMD: "b"},
		"no body":           {To: []string{"ann@example.com"}, Subject: "s"},
		"subject two lines": {To: []string{"ann@example.com"}, Subject: "s\r\nBcc: x@example.com", BodyMD: "b"},
		"body not utf-8":    {To: []string{"ann@example.com"}, Subject: "s", BodyMD: "\xff"},
		"short address":     {To: []string{"a@"}, Subject: "s", BodyMD: "b"},
	} {
		t.Run(name, func(t *testing.T) {
			src := &fakeSource{}
			exec := &fakeExecutor{}
			g := newGmail(t, src, exec)
			_, err := g.Send(context.Background(), testRun, msg)
			require.Error(t, err)
			assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
			assert.ErrorIs(t, err, gtm.ErrEmailNotSent, "refused before any request")
			assert.Empty(t, src.reqs)
			assert.Zero(t, exec.count())
		})
	}
}

// Only a failure that proves nothing left is ErrEmailNotSent: Gmail refusing
// the request (4xx other than 408). A 5xx, a 408, a transport error or a
// timeout, or a success with no id, may have sent, and leaves the item held.
func TestGmailSend_NotSentOnlyWhenCertain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exec    *fakeExecutor
		notSent bool
	}{
		{"400 invalid", &fakeExecutor{result: &httpaction.Result{IsError: true, StatusCode: 400, Content: "Invalid To header"}}, true},
		{"403 forbidden", &fakeExecutor{result: &httpaction.Result{IsError: true, StatusCode: 403, Content: "Insufficient permission"}}, true},
		{"429 rate limited", &fakeExecutor{result: &httpaction.Result{IsError: true, StatusCode: 429, Retryable: true, RateLimited: "rate_limited"}}, true},
		{"408 request timeout", &fakeExecutor{result: &httpaction.Result{IsError: true, StatusCode: 408}}, false},
		{"500", &fakeExecutor{result: &httpaction.Result{IsError: true, StatusCode: 500, Content: "Backend Error"}}, false},
		{"503", &fakeExecutor{result: &httpaction.Result{IsError: true, StatusCode: 503, Retryable: true}}, false},
		{"transport error", &fakeExecutor{err: errors.New("read tcp: connection reset by peer")}, false},
		{"timeout", &fakeExecutor{err: context.DeadlineExceeded}, false},
		{"no message id", &fakeExecutor{result: &httpaction.Result{StatusCode: 200, Data: map[string]any{}}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGmail(t, &fakeSource{}, tc.exec)
			id, err := g.Send(context.Background(), testRun, testMessage)
			require.Error(t, err)
			assert.Empty(t, id)
			assert.Equal(t, 1, tc.exec.count())
			assert.Equal(t, tc.notSent, errors.Is(err, gtm.ErrEmailNotSent), "%v", err)
		})
	}
}

func TestNewGmail_RequiresARunnerAndCredentials(t *testing.T) {
	_, err := email.NewGmail(email.Deps{Runner: httpaction.NewRunner(netguard.New())})
	require.Error(t, err, "no credential source: no Gmail")
	_, err = email.NewGmail(email.Deps{Credentials: &fakeSource{}})
	require.Error(t, err, "no runner: no Gmail")

	var zero *email.Gmail
	_, err = zero.Send(context.Background(), testRun, testMessage)
	assert.ErrorIs(t, err, email.ErrNotConfigured)
	assert.ErrorIs(t, err, gtm.ErrEmailNotSent)
	_, err = (&email.Gmail{}).Send(context.Background(), testRun, testMessage)
	assert.ErrorIs(t, err, email.ErrNotConfigured)
}

// The production runner dispatches to the real executor: importing this
// package registers it.
func TestGmailExecutorIsRegistered(t *testing.T) {
	err := httpaction.Executors().Register(gmail.SendExecutor, func(context.Context, httpaction.ExecutorCall) (*httpaction.Result, error) {
		return nil, nil
	})
	require.Error(t, err, "gmail.message_send is already registered by the gmail package")
}
