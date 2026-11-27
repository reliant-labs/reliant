// Copyright (c) 2026 Reliant Labs

package email_test

import (
	"context"
	"errors"
	"strings"
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

// The card binds its sender: a sender that changes between the card and the
// approval voids it, and nothing is sent or held.
func TestExecute_SenderChangedAfterTheCardIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		channels []store.GtmChannel
		change   func(*fixture)
	}{
		"default connection changed": {nil, func(f *fixture) { f.conns.setDefault("conn-channel") }},
		"reconnected to another account": {nil, func(f *fixture) {
			addr := "someone-else@example.com"
			f.conns.mu.Lock()
			f.conns.user["conn-default"].SenderID = &addr
			f.conns.mu.Unlock()
		}},
		"channel re-pointed": {[]store.GtmChannel{gmailChannel("ch-1", "conn-channel")}, func(f *fixture) {
			f.items.mu.Lock()
			ch := f.items.channels["ch-1"]
			conn := "conn-org"
			ch.ConnectionID = &conn
			f.items.channels["ch-1"] = ch
			f.items.mu.Unlock()
		}},
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

			tc.change(f)
			_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
			require.ErrorIs(t, err, email.ErrCardStale)
			assert.Zero(t, sender.count())
			assert.Empty(t, f.items.snapshot().ApprovalHash, "never held")

			// A fresh card names the new sender and sends.
			card, err = f.approver.Prepare(context.Background(), agent, sendReq, false)
			require.NoError(t, err)
			_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
			require.NoError(t, err)
			assert.Equal(t, 1, sender.count())
		})
	}
}

// An approved value that is not a whole card hash is no approval of this
// card: the item hash alone, a sender hash alone, or garbage.
func TestExecute_RefusesAnIncompleteCardHash(t *testing.T) {
	card := email.CardHash(approvedEmail(), founder)
	item, sender, _ := strings.Cut(card, ".")
	for name, approved := range map[string]string{
		"item part only":   item,
		"sender part only": "." + sender,
		"no sender part":   item + ".",
		"other sender":     email.CardHash(approvedEmail(), email.Account{ConnectionID: "conn-channel", Address: "team@example.com"}),
		"garbage":          "x.y",
		// Strict equality: anything but the exact card hash is refused.
		"extra dot":           card + ".",
		"extra dot inside":    item + ".." + sender,
		"trailing character":  card + "0",
		"trailing space":      card + " ",
		"truncated":           card[:len(card)-1],
		"truncated item part": item[:len(item)-1] + "." + sender,
		"upper-cased":         strings.ToUpper(card),
		"leading space":       " " + card,
	} {
		t.Run(name, func(t *testing.T) {
			s := &recordingEmail{}
			f := newFixture(t, approvedEmail(), s)
			_, err := f.approver.Execute(approvedCtx(approved), agent, sendReq, sendRun)
			require.ErrorIs(t, err, email.ErrCardStale)
			assert.Zero(t, s.count())
		})
	}
}

// fixedAccounts is Accounts answering with one account, whatever it is asked.
type fixedAccounts struct{ account email.Account }

func (a fixedAccounts) GmailAccount(context.Context, gtm.Actor, string) (email.Account, error) {
	return a.account, nil
}

// An Accounts that resolves a channel's pinned connection to some other
// connection is not believed: no card, no send.
func TestPrepare_RefusesAnAccountOtherThanThePinnedConnection(t *testing.T) {
	it := approvedEmail()
	it.ChannelID = "ch-1"
	sender := &recordingEmail{}
	f := newFixture(t, it, sender, gmailChannel("ch-1", "conn-channel"))
	approver := email.NewApprover(f.svc, fixedAccounts{email.Account{ConnectionID: "conn-default", Address: "founder@example.com"}})

	_, err := approver.Prepare(context.Background(), agent, sendReq, false)
	require.ErrorIs(t, err, email.ErrNoGmailAccount)
	_, err = approver.Execute(approvedCtx(email.CardHash(it, founder)), agent, sendReq, sendRun)
	require.ErrorIs(t, err, email.ErrNoGmailAccount)
	assert.Zero(t, sender.count())

	// The same Accounts with no pin in play is believed.
	f2 := newFixture(t, approvedEmail(), &recordingEmail{})
	_, err = email.NewApprover(f2.svc, fixedAccounts{founder}).Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)
	_, err = email.NewApprover(f2.svc, fixedAccounts{email.Account{Address: "founder@example.com"}}).Prepare(context.Background(), agent, sendReq, false)
	require.ErrorIs(t, err, email.ErrNoGmailAccount, "an account with no connection id")
}

// A run that names a connection other than the one resolved for the send is
// refused: the model never picks the mailbox.
func TestExecute_RunCannotPickTheMailbox(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	run := sendRun
	run.ConnectionID = "conn-channel"
	_, err := f.approver.Execute(approvedCtx(email.CardHash(approvedEmail(), founder)), agent, sendReq, run)
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
	assert.Equal(t, gtm.ApprovalHash(approvedEmail()), items.sends[0].ApprovalHash, "the item part of the approved hash")
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

	_, err := approver.Execute(approvedCtx(email.CardHash(doctored, founder)), agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrApprovalMismatch)
	require.Len(t, items.sends, 1, "the approver's compare passed")
	assert.Equal(t, gtm.ApprovalHash(doctored), items.sends[0].ApprovalHash)
	assert.Zero(t, sender.count())
	assert.Empty(t, f.items.snapshot().ApprovalHash, "never held")
}
