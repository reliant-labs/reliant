// Copyright (c) 2026 Reliant Labs
package tools_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/email"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/rctx"
	"github.com/reliant-labs/reliant/internal/store"
)

// send_gtm_email end to end over the real pieces: internal/gtm/email's
// Approver (which this package's tests may import, though the package may
// not) over the real gtm.Service, with one gtm_items row behind the
// generated MockStore, a Gmail connection store and a recording gtm.Email.

var _ tools.GTMEmailSender = (*email.Approver)(nil)

const sendOrg = "55949942-3ee9-4cfc-bb7c-817bdba75186"

type sendChats struct{}

func (sendChats) GetChat(_ context.Context, id string) (*db.Chat, error) {
	org := sendOrg
	return &db.Chat{ID: id, UserID: "user-1", OrgID: &org}, nil
}

// sendRow is the item, with the store calls the send path makes.
type sendRow struct {
	mu  sync.Mutex
	row store.GtmItem
}

func (m *sendRow) edit(fn func(*store.GtmItem)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.row)
	m.row.Revision++
}

func (m *sendRow) store() *gtm.MockStore {
	return &gtm.MockStore{
		InTxFunc: func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) },
		GetItemFunc: func(_ context.Context, org, id string) (*store.GtmItem, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.row.OrgID != org || m.row.ID != id {
				return nil, gtm.ErrNotFound
			}
			c := m.row
			c.EmailTo = slices.Clone(m.row.EmailTo)
			return &c, nil
		},
		UpdateItemFunc: func(_ context.Context, row *store.GtmItem, expected int32) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.row.Revision != expected {
				return gtm.ErrRevisionConflict
			}
			c := *row
			c.Revision = expected + 1
			m.row = c
			return nil
		},
		HoldItemSendFunc: func(_ context.Context, org, id string, revision int32, hash string, at time.Time) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.row.Revision != revision || m.row.Status != string(gtm.ItemApproved) || m.row.ApprovalHash != "" {
				return gtm.ErrRevisionConflict
			}
			m.row.ApprovalHash, m.row.UpdatedAt = hash, at
			return nil
		},
		ReleaseItemSendFunc: func(_ context.Context, org, id string, revision int32, hash string, at time.Time, invalidate bool) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			m.row.ApprovalHash = ""
			return nil
		},
		CountChannelsFunc: func(context.Context, string) (int64, error) { return 1, nil },
	}
}

type sendMail struct {
	mu   sync.Mutex
	sent []gtm.EmailMessage
}

func (e *sendMail) Send(_ context.Context, _ gtm.Actor, _ gtm.EmailRun, msg gtm.EmailMessage) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sent = append(e.sent, msg)
	return "gmail-1", nil
}

func (e *sendMail) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.sent)
}

// sendConns is the user's saved connections: a default Gmail one, or none.
type sendConns struct{ connected bool }

func (c sendConns) conn() (*core.Connection, error) {
	if !c.connected {
		return nil, core.ErrConnectionNotFound
	}
	addr := "founder@example.com"
	return &core.Connection{ID: "conn-default", UserID: "user-1", IntegrationID: "gmail", Status: core.ConnectionStatusActive, SenderID: &addr}, nil
}

func (c sendConns) GetConnection(context.Context, string, string) (*core.Connection, error) {
	return c.conn()
}

func (c sendConns) GetOwnedConnection(context.Context, core.ConnectionOwner, string) (*core.Connection, error) {
	return nil, core.ErrConnectionNotFound
}

func (c sendConns) DefaultConnection(context.Context, string, string) (*core.Connection, error) {
	return c.conn()
}

type sendFixture struct {
	row  *sendRow
	mail *sendMail
	tool tools.Tool
}

func newSendFixture(t *testing.T, connected bool, mutate ...func(*store.GtmItem)) *sendFixture {
	t.Helper()
	row := &sendRow{row: store.GtmItem{
		ID: "item-1", OrgID: sendOrg, Format: string(gtm.FormatEmail), Title: "Launch week email",
		Status: string(gtm.ItemApproved), EmailTo: []string{"ann@example.com"}, EmailSubject: "Launch week",
		BodyMd: "We shipped it.", Revision: 3,
	}}
	for _, fn := range mutate {
		fn(&row.row)
	}
	mail := &sendMail{}
	svc := gtm.New(gtm.Deps{
		Store: row.store(),
		Gate: &gtm.MockGate{AllowedFunc: func(_ context.Context, f featuregate.Feature, _, org string) (bool, error) {
			return f == featuregate.GTM && org == sendOrg, nil
		}},
		Clock: &gtm.MockClock{NowFunc: func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) }},
		Email: mail,
	})
	approver := email.NewApprover(svc, email.NewConnectionAccounts(sendConns{connected: connected}))
	return &sendFixture{row: row, mail: mail, tool: tools.NewSendGtmEmailTool(sendChats{}, approver)}
}

const sendInput = `{"item_id":"item-1","expected_revision":3}`

func toolCtx(ctx context.Context) *rctx.ToolContext {
	return rctx.NewToolContext(ctx, "chat-1", "thread-1", nil, nil)
}

// card is what the runtime's approval preview raises for call-1.
func (f *sendFixture) card(t *testing.T, unattended bool) (tools.ActivationPreview, bool, error) {
	t.Helper()
	return f.tool.(tools.AutomationApprovalTool).PrepareAutomation(toolCtx(context.Background()), tools.ToolCall{ID: "call-1", Name: tools.ToolSendGtmEmail, Input: sendInput}, unattended)
}

// send executes the call as the runtime does after the card, with approvals.
func (f *sendFixture) send(t *testing.T, callID string, exec automationpolicy.Execution) tools.ToolResponse {
	t.Helper()
	resp, err := f.tool.Run(toolCtx(automationpolicy.WithExecution(context.Background(), exec)), tools.ToolCall{ID: callID, Name: tools.ToolSendGtmEmail, Input: sendInput})
	require.NoError(t, err)
	return resp
}

func approved(callID, hash string) automationpolicy.Execution {
	return automationpolicy.Execution{Approvals: map[string]string{callID: hash}}
}

func TestSendGtmEmail_SendsOnceUnderTheApprovedCard(t *testing.T) {
	f := newSendFixture(t, true)
	preview, needs, err := f.card(t, false)
	require.NoError(t, err)
	assert.True(t, needs, "an attended send always shows the card")
	assert.Contains(t, preview.Title, "To: ann@example.com")
	assert.Contains(t, preview.Title, "From: founder@example.com")
	assert.True(t, strings.HasPrefix(preview.Hash, gtm.ApprovalHash(gtm.Item{
		ID: "item-1", Revision: 3, EmailTo: []string{"ann@example.com"}, EmailSubject: "Launch week", BodyMD: "We shipped it.",
	})), "the hash binds the email's content")
	assert.Zero(t, f.mail.count(), "the card sends nothing")

	resp := f.send(t, "call-1", approved("call-1", preview.Hash))
	require.False(t, resp.IsError, resp.Content)
	assert.Contains(t, resp.Content, `"status":"SENT"`)
	assert.Equal(t, 1, f.mail.count())

	// Idempotent: a SENT item is refused, card and send alike.
	_, _, err = f.card(t, false)
	require.Error(t, err)
	assert.Equal(t, gtm.ErrAlreadySent.Error(), tools.ActivationRefusalReason(err))
	again := f.send(t, "call-1", approved("call-1", preview.Hash))
	assert.True(t, again.IsError)
	assert.Equal(t, 1, f.mail.count(), "never sent twice")
}

func TestSendGtmEmail_RefusedUnattended(t *testing.T) {
	f := newSendFixture(t, true)
	_, needs, err := f.card(t, true)
	require.Error(t, err)
	assert.False(t, needs)
	assert.Contains(t, tools.ActivationRefusalReason(err), "an unattended run cannot show one")

	exec := approved("call-1", "any")
	exec.Unattended = true
	resp := f.send(t, "call-1", exec)
	assert.True(t, resp.IsError)
	assert.Zero(t, f.mail.count())
}

// The approval binds the call and the content: another call's approval, a
// hash for other content, and an edit after the card all send nothing.
func TestSendGtmEmail_TheApprovalBindsCallAndContent(t *testing.T) {
	t.Run("no approval for this call", func(t *testing.T) {
		f := newSendFixture(t, true)
		preview, _, err := f.card(t, false)
		require.NoError(t, err)
		resp := f.send(t, "call-2", approved("call-1", preview.Hash))
		assert.True(t, resp.IsError)
		assert.Zero(t, f.mail.count())
	})
	t.Run("a hash of other content", func(t *testing.T) {
		f := newSendFixture(t, true)
		resp := f.send(t, "call-1", approved("call-1", email.CardHash(gtm.Item{ID: "item-1", Revision: 3, EmailTo: []string{"eve@example.com"}, EmailSubject: "Launch week", BodyMD: "We shipped it."}, email.Account{ConnectionID: "conn-default", Address: "founder@example.com"})))
		assert.True(t, resp.IsError)
		assert.Zero(t, f.mail.count())
	})
	t.Run("the item moved after the card", func(t *testing.T) {
		f := newSendFixture(t, true)
		preview, _, err := f.card(t, false)
		require.NoError(t, err)
		f.row.edit(func(r *store.GtmItem) { r.BodyMd = "Something else." })
		resp := f.send(t, "call-1", approved("call-1", preview.Hash))
		assert.True(t, resp.IsError)
		assert.Contains(t, resp.Content, "revision")
		assert.Zero(t, f.mail.count())
	})
}

func TestSendGtmEmail_RefusedUnlessApprovedAtTheRevision(t *testing.T) {
	for name, mutate := range map[string]func(*store.GtmItem){
		"in review":      func(r *store.GtmItem) { r.Status = string(gtm.ItemReview) },
		"a draft":        func(r *store.GtmItem) { r.Status = string(gtm.ItemDraft) },
		"revision moved": func(r *store.GtmItem) { r.Revision = 4 },
		"not an email":   func(r *store.GtmItem) { r.Format = string(gtm.FormatLongPost) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newSendFixture(t, true, mutate)
			_, needs, err := f.card(t, false)
			require.Error(t, err)
			assert.False(t, needs)
			var refusal *tools.ActivationRefusal
			assert.True(t, errors.As(err, &refusal))
			resp := f.send(t, "call-1", approved("call-1", "whatever"))
			assert.True(t, resp.IsError)
			assert.Zero(t, f.mail.count())
		})
	}
}

// With no Gmail connection there is no sender to name: no card, no send.
func TestSendGtmEmail_FailsClosedWithoutGmail(t *testing.T) {
	f := newSendFixture(t, false)
	_, needs, err := f.card(t, false)
	require.Error(t, err)
	assert.False(t, needs)
	assert.Equal(t, email.ErrNoGmailAccount.Error(), tools.ActivationRefusalReason(err))
	resp := f.send(t, "call-1", approved("call-1", "whatever"))
	assert.True(t, resp.IsError)
	assert.Zero(t, f.mail.count())
}
