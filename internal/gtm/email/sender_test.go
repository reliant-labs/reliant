// Copyright (c) 2026 Reliant Labs

package email_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/email"
	"github.com/reliant-labs/reliant/internal/store"
)

// fakeConns is the saved connections' metadata: user-1's default Gmail
// (conn-default, founder@example.com), their second Gmail (conn-channel,
// team@example.com), and tenant zero's shared Gmail (conn-org,
// sales@example.com).
type fakeConns struct {
	mu   sync.Mutex
	user map[string]*core.Connection
	org  map[string]*core.Connection
	def  string
	err  error
}

func gmailConn(id, addr string) *core.Connection {
	return &core.Connection{ID: id, UserID: "user-1", IntegrationID: "gmail", Status: core.ConnectionStatusActive, SenderID: &addr}
}

func newFakeConns() *fakeConns {
	return &fakeConns{
		user: map[string]*core.Connection{
			"conn-default": gmailConn("conn-default", "founder@example.com"),
			"conn-channel": gmailConn("conn-channel", "team@example.com"),
		},
		org: map[string]*core.Connection{"conn-org": gmailConn("conn-org", "sales@example.com")},
		def: "conn-default",
	}
}

func (f *fakeConns) GetConnection(_ context.Context, userID, id string) (*core.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if c, ok := f.user[id]; ok && userID == "user-1" {
		cp := *c
		return &cp, nil
	}
	return nil, core.ErrConnectionNotFound
}

func (f *fakeConns) GetOwnedConnection(_ context.Context, owner core.ConnectionOwner, id string) (*core.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if c, ok := f.org[id]; ok && owner == core.OrgConnectionOwner(tenantZero) {
		cp := *c
		return &cp, nil
	}
	return nil, core.ErrConnectionNotFound
}

func (f *fakeConns) DefaultConnection(_ context.Context, userID, integrationID string) (*core.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if c, ok := f.user[f.def]; ok && userID == "user-1" && integrationID == "gmail" {
		cp := *c
		return &cp, nil
	}
	return nil, core.ErrConnectionNotFound
}

func (f *fakeConns) setDefault(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.def = id
}

// The card names the account the email goes out from, by address, and the
// send is pinned to that connection: the channel's own, else the actor's
// default.
func TestExecute_SendsFromTheAccountTheCardNames(t *testing.T) {
	for name, tc := range map[string]struct {
		channels []store.GtmChannel
		from     string
		conn     string
	}{
		"no channel":                 {nil, "From: founder@example.com (Gmail, your default connection)", "conn-default"},
		"channel pins a connection":  {[]store.GtmChannel{gmailChannel("ch-1", "conn-channel")}, "From: team@example.com (Gmail, channel Founder newsletter)", "conn-channel"},
		"channel pins the org's":     {[]store.GtmChannel{gmailChannel("ch-1", "conn-org")}, "From: sales@example.com (Gmail, channel Founder newsletter)", "conn-org"},
		"channel without connection": {[]store.GtmChannel{gmailChannel("ch-1", "")}, "From: founder@example.com (Gmail, your default connection, channel Founder newsletter)", "conn-default"},
	} {
		t.Run(name, func(t *testing.T) {
			it := approvedEmail()
			if len(tc.channels) > 0 {
				it.ChannelID = "ch-1"
			}
			sender := &recordingEmail{}
			f := newFixture(t, it, sender, tc.channels...)

			card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
			require.NoError(t, err)
			assert.Contains(t, card.Title, "\n"+tc.from+"\n")
			_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
			require.NoError(t, err)
			require.Equal(t, 1, sender.count())
			assert.Equal(t, tc.conn, sender.runs[0].ConnectionID, "pinned to the account resolved for the send")
		})
	}
}

// No card is shown for an email whose sender cannot be named.
func TestPrepare_RefusesWhenTheSenderCannotBeNamed(t *testing.T) {
	for name, mutate := range map[string]func(*fakeConns){
		"no default Gmail":   func(c *fakeConns) { c.def = "" },
		"needs reconnecting": func(c *fakeConns) { c.user["conn-default"].Status = core.ConnectionStatusNeedsReauth },
		"no address":         func(c *fakeConns) { c.user["conn-default"].SenderID = nil },
		"address not ASCII":  func(c *fakeConns) { addr := "founder@exämple.com"; c.user["conn-default"].SenderID = &addr },
		"address with Cf":    func(c *fakeConns) { addr := "founder\u200b@example.com"; c.user["conn-default"].SenderID = &addr },
		"not gmail":          func(c *fakeConns) { c.user["conn-default"].IntegrationID = "slack" },
	} {
		t.Run(name, func(t *testing.T) {
			sender := &recordingEmail{}
			f := newFixture(t, approvedEmail(), sender)
			mutate(f.conns)
			_, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
			require.ErrorIs(t, err, email.ErrNoGmailAccount)
			_, err = f.approver.Execute(approvedCtx(gtm.ApprovalHash(approvedEmail())), agent, sendReq, sendRun)
			require.ErrorIs(t, err, email.ErrNoGmailAccount)
			assert.Zero(t, sender.count())
		})
	}
	t.Run("label when no sender id", func(t *testing.T) {
		f := newFixture(t, approvedEmail(), &recordingEmail{})
		label := "founder@example.com"
		f.conns.user["conn-default"].SenderID = nil
		f.conns.user["conn-default"].AccountLabel = &label
		card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
		require.NoError(t, err)
		assert.Contains(t, card.Title, "From: founder@example.com (Gmail, your default connection)")
	})
	t.Run("connections unreadable", func(t *testing.T) {
		f := newFixture(t, approvedEmail(), &recordingEmail{})
		f.conns.err = errors.New("db down")
		_, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
		require.Error(t, err)
		assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	})
}

// The residual risk, pinned down: the default changes between the card and
// the approval. The send goes from the default resolved at Execute (and is
// pinned to it for the send itself), which the card did not show.
func TestExecute_DefaultChangedAfterTheCard(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)
	assert.Contains(t, card.Title, "From: founder@example.com")

	f.conns.setDefault("conn-channel")
	_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.NoError(t, err)
	assert.Equal(t, "conn-channel", sender.runs[0].ConnectionID)
}

// A run that names a connection other than the one resolved for the send is
// refused: the model never picks the mailbox.
func TestExecute_RunCannotPickTheMailbox(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	run := sendRun
	run.ConnectionID = "conn-channel"
	_, err := f.approver.Execute(approvedCtx(gtm.ApprovalHash(approvedEmail())), agent, sendReq, run)
	require.ErrorIs(t, err, gtm.ErrChannelNotSendable)
	assert.Zero(t, sender.count())
}

// recordingItems is gtm.Service, recording SendItemEmail's input; doctor,
// when set, changes what GetItem returns, as a read that lies would.
type recordingItems struct {
	gtm.Service
	mu     sync.Mutex
	sends  []gtm.SendItemEmailInput
	doctor func(*gtm.Item)
}

func (r *recordingItems) GetItem(ctx context.Context, actor gtm.Actor, in gtm.GetItemInput) (gtm.Item, error) {
	it, err := r.Service.GetItem(ctx, actor, in)
	if err == nil && r.doctor != nil {
		r.doctor(&it)
	}
	return it, err
}

func (r *recordingItems) SendItemEmail(ctx context.Context, actor gtm.Actor, in gtm.SendItemEmailInput) (gtm.Item, error) {
	r.mu.Lock()
	r.sends = append(r.sends, in)
	r.mu.Unlock()
	return r.Service.SendItemEmail(ctx, actor, in)
}

// Execute hands SendItemEmail the hash the runtime approved, not one it
// recomputed.
func TestExecute_PassesTheApprovedHash(t *testing.T) {
	f := newFixture(t, approvedEmail(), &recordingEmail{})
	items := &recordingItems{Service: f.svc}
	approver := email.NewApprover(items, email.NewConnectionAccounts(f.conns))
	card, err := approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)

	_, err = approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.NoError(t, err)
	require.Len(t, items.sends, 1)
	assert.Equal(t, card.Hash, items.sends[0].ApprovalHash)
}

// The service's ErrApprovalMismatch is a real second guard: a card for other
// content that gets past the approver's own compare (here a read that lies
// to it) is refused by the service, and nothing is sent or held.
func TestExecute_ServiceRefusesAMismatchedApprovalTheApproverMissed(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	doctored := approvedEmail()
	doctored.BodyMD = "Not what is stored.\n"
	items := &recordingItems{Service: f.svc, doctor: func(it *gtm.Item) { it.BodyMD = doctored.BodyMD }}
	approver := email.NewApprover(items, email.NewConnectionAccounts(f.conns))

	_, err := approver.Execute(approvedCtx(gtm.ApprovalHash(doctored)), agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrApprovalMismatch)
	require.Len(t, items.sends, 1, "the approver's compare passed")
	assert.Equal(t, gtm.ApprovalHash(doctored), items.sends[0].ApprovalHash)
	assert.Zero(t, sender.count())
	assert.Empty(t, f.items.snapshot().ApprovalHash, "never held")
}
