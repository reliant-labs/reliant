// Copyright (c) 2026 Reliant Labs

package email_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/automationpolicy"
	"github.com/reliant-labs/reliant/internal/featuregate"
	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/email"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/store"
)

const tenantZero = "55949942-3ee9-4cfc-bb7c-817bdba75186"

var agent = gtm.Actor{UserID: "user-1", OrgID: tenantZero, Kind: gtm.ActorAgent, ChatID: "chat-1"}

// memItems is one gtm_items row behind the real gtm.Service, through the
// generated MockStore: the approver is tested against the service's own
// checks, hold and record, not a stand-in for them.
type memItems struct {
	mu       sync.Mutex
	row      store.GtmItem
	channels map[string]store.GtmChannel
}

func (m *memItems) get(orgID, id string) (*store.GtmItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.row.OrgID != orgID || m.row.ID != id {
		return nil, gtm.ErrNotFound
	}
	c := m.row
	c.EmailTo = slices.Clone(m.row.EmailTo)
	return &c, nil
}

func (m *memItems) snapshot() store.GtmItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.row
}

// edit changes the row as a person's UpdateItem would: a new revision, and
// the hold cleared.
func (m *memItems) edit(fn func(*store.GtmItem)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&m.row)
	m.row.Revision++
	m.row.ApprovalHash = ""
}

func (m *memItems) store() *gtm.MockStore {
	tx := func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
	return &gtm.MockStore{
		InTxFunc:    tx,
		GetItemFunc: func(_ context.Context, orgID, id string) (*store.GtmItem, error) { return m.get(orgID, id) },
		UpdateItemFunc: func(_ context.Context, row *store.GtmItem, expected int32) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.row.ID != row.ID || m.row.Revision != expected {
				return gtm.ErrRevisionConflict
			}
			c := *row
			c.EmailTo = slices.Clone(row.EmailTo)
			c.Revision = expected + 1
			m.row = c
			return nil
		},
		HoldItemSendFunc: func(_ context.Context, orgID, id string, revision int32, hash string, at time.Time) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.row.OrgID != orgID || m.row.ID != id || m.row.Revision != revision ||
				m.row.Status != string(gtm.ItemApproved) || m.row.ApprovalHash != "" {
				return gtm.ErrRevisionConflict
			}
			m.row.ApprovalHash, m.row.UpdatedAt = hash, at
			return nil
		},
		ReleaseItemSendFunc: func(_ context.Context, orgID, id string, revision int32, hash string, at time.Time, invalidate bool) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.row.OrgID != orgID || m.row.ID != id || m.row.Revision != revision ||
				m.row.Status != string(gtm.ItemApproved) || m.row.ApprovalHash != hash {
				return gtm.ErrRevisionConflict
			}
			m.row.ApprovalHash, m.row.UpdatedAt = "", at
			if invalidate {
				m.row.Revision++
			}
			return nil
		},
		CountChannelsFunc: func(context.Context, string) (int64, error) { return 1, nil },
		GetChannelFunc: func(_ context.Context, orgID, id string) (*store.GtmChannel, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			ch, ok := m.channels[id]
			if !ok || ch.OrgID != orgID {
				return nil, gtm.ErrNotFound
			}
			return &ch, nil
		},
	}
}

func rowOf(it gtm.Item) store.GtmItem {
	var channelID *string
	if it.ChannelID != "" {
		channelID = &it.ChannelID
	}
	return store.GtmItem{
		ID: it.ID, OrgID: it.OrgID, Format: string(it.Format), ChannelID: channelID, Title: it.Title, BodyMd: it.BodyMD,
		Status: string(it.Status), EmailTo: slices.Clone(it.EmailTo), EmailSubject: it.EmailSubject,
		ApprovalHash: it.ApprovalHash, Revision: it.Revision,
	}
}

// recordingEmail is gtm.Email, counting sends.
type recordingEmail struct {
	mu   sync.Mutex
	sent []gtm.EmailMessage
	runs []gtm.EmailRun
	err  error
}

func (e *recordingEmail) Send(_ context.Context, _ gtm.Actor, run gtm.EmailRun, msg gtm.EmailMessage) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil {
		return "", e.err
	}
	e.sent = append(e.sent, msg)
	e.runs = append(e.runs, run)
	return "gmail-msg-1", nil
}

func (e *recordingEmail) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.sent)
}

type fixture struct {
	items    *memItems
	store    *gtm.MockStore
	email    gtm.Email
	svc      gtm.Service
	conns    *fakeConns
	approver *email.Approver
}

// gmailChannel is an ACTIVE, INTEGRATED gmail channel of tenant zero.
func gmailChannel(id, connectionID string) store.GtmChannel {
	kind := gtm.GmailIntegration
	ch := store.GtmChannel{
		ID: id, OrgID: tenantZero, Key: id, Name: "Founder newsletter",
		Mode: string(gtm.ChannelIntegrated), IntegrationKind: &kind,
		Status: string(gtm.ChannelActive), Revision: 1,
	}
	if connectionID != "" {
		ch.ConnectionID = &connectionID
	}
	return ch
}

func newFixture(t *testing.T, it gtm.Item, sender gtm.Email, channels ...store.GtmChannel) *fixture {
	t.Helper()
	items := &memItems{row: rowOf(it), channels: map[string]store.GtmChannel{}}
	for _, ch := range channels {
		items.channels[ch.ID] = ch
	}
	st := items.store()
	svc := gtm.New(gtm.Deps{
		Store: st,
		Gate: &gtm.MockGate{AllowedFunc: func(_ context.Context, f featuregate.Feature, _, orgID string) (bool, error) {
			return f == featuregate.GTM && orgID == tenantZero, nil
		}},
		Clock: &gtm.MockClock{NowFunc: func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) }},
		Email: sender,
	})
	conns := newFakeConns()
	return &fixture{items: items, store: st, email: sender, svc: svc, conns: conns,
		approver: email.NewApprover(svc, email.NewConnectionAccounts(conns))}
}

var sendReq = email.SendRequest{ItemID: "item-1", ExpectedRevision: 3}

var sendRun = gtm.EmailRun{RunID: "run-1", NodeID: "node-1", ToolCallID: "call-1"}

// approvedCtx is the runtime's word that the owner approved hash for
// sendRun's call, in an attended run.
func approvedCtx(hash string) context.Context {
	return automationpolicy.WithExecution(context.Background(), automationpolicy.Execution{
		Approvals: map[string]string{sendRun.ToolCallID: hash},
	})
}

// Attended, every send needs a card: Prepare always returns one, over the
// stored content.
func TestPrepare_AttendedAlwaysNeedsACard(t *testing.T) {
	f := newFixture(t, approvedEmail(), &recordingEmail{})
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)
	assert.Equal(t, email.CardHash(approvedEmail(), founder), card.Hash)
	assert.Contains(t, card.Title, "To: ann@example.com, bo@example.com")
	assert.Contains(t, card.Title, "Subject: Launch week")
	assert.Contains(t, card.Title, "│ We **shipped** it.")
	assert.Zero(t, f.email.(*recordingEmail).count(), "preparing sends nothing")
}

// Unattended, it is refused before anything is read.
func TestPrepare_RefusedUnattended(t *testing.T) {
	f := newFixture(t, approvedEmail(), &recordingEmail{})
	_, err := f.approver.Prepare(context.Background(), agent, sendReq, true)
	require.ErrorIs(t, err, email.ErrUnattended)
	assert.Zero(t, f.store.CallCount("GetItem"))
}

func TestPrepareAndExecute_RefuseUnlessAnApprovedEmailAtTheRevision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gtm.Item)
		want   error
	}{
		{"revision moved", func(it *gtm.Item) { it.Revision = 4 }, gtm.ErrRevisionConflict},
		{"draft", func(it *gtm.Item) { it.Status = gtm.ItemDraft }, gtm.ErrNotApproved},
		{"review", func(it *gtm.Item) { it.Status = gtm.ItemReview }, gtm.ErrNotApproved},
		{"paused", func(it *gtm.Item) { it.Status = gtm.ItemPaused }, gtm.ErrNotApproved},
		{"not an email", func(it *gtm.Item) { it.Format = gtm.FormatLongPost; it.EmailTo = nil; it.EmailSubject = "" }, gtm.ErrNotApproved},
		{"sent", func(it *gtm.Item) { it.Status = gtm.ItemSent }, gtm.ErrAlreadySent},
		{"sent, other revision", func(it *gtm.Item) { it.Status = gtm.ItemSent; it.Revision = 9 }, gtm.ErrAlreadySent},
		{"held by a send in doubt", func(it *gtm.Item) { it.ApprovalHash = "earlier-card" }, gtm.ErrSendInDoubt},
		{"held, other revision", func(it *gtm.Item) { it.ApprovalHash = "earlier-card"; it.Revision = 9 }, gtm.ErrSendInDoubt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := approvedEmail()
			tc.mutate(&it)
			sender := &recordingEmail{}
			f := newFixture(t, it, sender)

			_, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
			require.ErrorIs(t, err, tc.want)

			// Execute re-checks on its own read, whatever card it was given.
			_, err = f.approver.Execute(approvedCtx(gtm.ApprovalHash(it)), agent, sendReq, sendRun)
			require.ErrorIs(t, err, tc.want)
			assert.Zero(t, sender.count())
		})
	}
}

func TestPrepare_OtherOrgIsNotFound(t *testing.T) {
	f := newFixture(t, approvedEmail(), &recordingEmail{})
	other := agent
	other.OrgID = "another-org"
	_, err := f.approver.Prepare(context.Background(), other, sendReq, false)
	require.ErrorIs(t, err, gtm.ErrNotFound)
}

func TestExecute_SendsUnderTheApprovedCard(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)

	sent, err := f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.NoError(t, err)
	assert.Equal(t, gtm.ItemSent, sent.Status)
	assert.Equal(t, "gmail-msg-1", sent.GmailMessageID)
	assert.Equal(t, gtm.ApprovalHash(approvedEmail()), sent.ApprovalHash, "the record keeps the email's hash")
	assert.Equal(t, "user-1", sent.SentBy)

	require.Equal(t, 1, sender.count())
	assert.Equal(t, gtm.EmailMessage{To: []string{"ann@example.com", "bo@example.com"}, Subject: "Launch week", BodyMD: approvedEmail().BodyMD}, sender.sent[0])
	pinned := sendRun
	pinned.ConnectionID = "conn-default"
	assert.Equal(t, pinned, sender.runs[0], "sent through the run, pinned to the default resolved for it")
	assert.Equal(t, string(gtm.ItemSent), f.items.snapshot().Status)
}

func TestExecute_RefusedUnattended(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	ctx := automationpolicy.WithExecution(context.Background(), automationpolicy.Execution{
		Unattended: true,
		Approvals:  map[string]string{sendRun.ToolCallID: gtm.ApprovalHash(approvedEmail())},
	})
	_, err := f.approver.Execute(ctx, agent, sendReq, sendRun)
	require.ErrorIs(t, err, email.ErrUnattended)
	assert.Zero(t, sender.count())
	assert.Zero(t, f.store.CallCount("GetItem"))
}

func TestExecute_NeedsTheCardForThisCall(t *testing.T) {
	hash := gtm.ApprovalHash(approvedEmail())
	for name, ctx := range map[string]context.Context{
		"no runtime context": context.Background(),
		"no approval":        automationpolicy.WithExecution(context.Background(), automationpolicy.Execution{}),
		"another call's":     automationpolicy.WithExecution(context.Background(), automationpolicy.Execution{Approvals: map[string]string{"call-2": hash}}),
	} {
		t.Run(name, func(t *testing.T) {
			sender := &recordingEmail{}
			f := newFixture(t, approvedEmail(), sender)
			_, err := f.approver.Execute(ctx, agent, sendReq, sendRun)
			require.ErrorIs(t, err, email.ErrNoApproval)
			assert.Zero(t, sender.count())
		})
	}
	t.Run("no call id", func(t *testing.T) {
		sender := &recordingEmail{}
		f := newFixture(t, approvedEmail(), sender)
		run := sendRun
		run.ToolCallID = ""
		ctx := automationpolicy.WithExecution(context.Background(), automationpolicy.Execution{Approvals: map[string]string{"": hash}})
		_, err := f.approver.Execute(ctx, agent, sendReq, run)
		require.ErrorIs(t, err, email.ErrNoApproval)
		assert.Zero(t, sender.count())
	})
}

// The card binds content: an approval of other content, or of this item
// before it changed, sends nothing.
func TestExecute_RefusesACardForOtherContent(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)

	other := approvedEmail()
	other.EmailTo = []string{"eve@example.com"}
	_, err = f.approver.Execute(approvedCtx(gtm.ApprovalHash(other)), agent, sendReq, sendRun)
	require.ErrorIs(t, err, email.ErrCardStale)

	// A person edits and re-approves after the card was raised: the revision
	// moves, so the old revision is a conflict, and the old card does not
	// cover the new revision's content.
	f.items.edit(func(r *store.GtmItem) { r.BodyMd = "Different body\n" })
	_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrRevisionConflict)
	_, err = f.approver.Execute(approvedCtx(card.Hash), agent, email.SendRequest{ItemID: "item-1", ExpectedRevision: 4}, sendRun)
	require.ErrorIs(t, err, email.ErrCardStale)
	assert.Zero(t, sender.count())
}

// A second send of a SENT item is refused, by the approver and by the
// service, and Gmail is called once.
func TestExecute_IdempotentAfterSent(t *testing.T) {
	sender := &recordingEmail{}
	f := newFixture(t, approvedEmail(), sender)
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)
	ctx := approvedCtx(card.Hash)

	_, err = f.approver.Execute(ctx, agent, sendReq, sendRun)
	require.NoError(t, err)
	_, err = f.approver.Execute(ctx, agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrAlreadySent)
	_, err = f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.ErrorIs(t, err, gtm.ErrAlreadySent, "no card is raised for a sent item")
	assert.Equal(t, 1, sender.count())
}

// No Gmail connection: nothing is sent, the failure says so
// (gtm.ErrEmailNotSent), and the item is released, not held: still
// APPROVED with no hold, so a new card can send it once Gmail is connected.
func TestExecute_FailsClosedWithNoGmailConnection(t *testing.T) {
	exec := &fakeExecutor{}
	src := &fakeSource{err: &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "Gmail is not connected"}}
	f := newFixture(t, approvedEmail(), newGmail(t, src, exec))
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)

	_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.Error(t, err)
	// The service answers with the bare sentinel and logs the cause
	// (Gmail's own test pins ErrNotConnected).
	assert.ErrorIs(t, err, gtm.ErrEmailNotSent)
	assert.NotErrorIs(t, err, gtm.ErrSendInDoubt)
	assert.Zero(t, exec.count(), "nothing reached Gmail")

	row := f.items.snapshot()
	assert.Equal(t, string(gtm.ItemApproved), row.Status)
	assert.Empty(t, row.ApprovalHash, "the hold is released: retryable")
	assert.Empty(t, row.GmailMessageID)

	// Connected now: the same item sends under a fresh card.
	src.err = nil
	card, err = f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)
	sent, err := f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.NoError(t, err)
	assert.Equal(t, gtm.ItemSent, sent.Status)
	assert.Equal(t, 1, exec.count())
}

// A send that may have left (here a Gmail 5xx) holds the item: no second
// card, no second send, until a person re-approves.
func TestExecute_SendInDoubtHoldsTheItem(t *testing.T) {
	exec := &fakeExecutor{result: &httpaction.Result{IsError: true, StatusCode: 500, Content: "Backend Error"}}
	f := newFixture(t, approvedEmail(), newGmail(t, &fakeSource{}, exec))
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)

	_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrSendInDoubt)
	assert.Equal(t, gtm.ApprovalHash(approvedEmail()), f.items.snapshot().ApprovalHash, "held")

	_, err = f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.ErrorIs(t, err, gtm.ErrSendInDoubt)
	_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrSendInDoubt)
	assert.Equal(t, 1, exec.count(), "never sent twice")
}

func TestApprover_NoServiceIsUnavailable(t *testing.T) {
	_, err := email.NewApprover(nil, email.NewConnectionAccounts(newFakeConns())).Prepare(context.Background(), agent, sendReq, false)
	require.ErrorIs(t, err, gtm.ErrEmailUnavailable)
	f := newFixture(t, approvedEmail(), &recordingEmail{})
	_, err = email.NewApprover(f.svc, nil).Prepare(context.Background(), agent, sendReq, false)
	require.ErrorIs(t, err, gtm.ErrEmailUnavailable, "no accounts: no card")
	_, err = (*email.Approver)(nil).Execute(approvedCtx("h"), agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrEmailUnavailable)
}

func TestApprover_RefusesAnIncompleteRequest(t *testing.T) {
	f := newFixture(t, approvedEmail(), &recordingEmail{})
	for name, req := range map[string]email.SendRequest{
		"no item":     {ExpectedRevision: 3},
		"no revision": {ItemID: "item-1"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.approver.Prepare(context.Background(), agent, req, false)
			require.ErrorIs(t, err, gtm.ErrInvalidArgument)
		})
	}
}

// COLD_OUTREACH is an email-type format: it is carded and sent like EMAIL.
func TestColdOutreachIsSendable(t *testing.T) {
	it := approvedEmail()
	it.Format = gtm.FormatColdOutreach
	sender := &recordingEmail{}
	f := newFixture(t, it, sender)

	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)
	assert.Equal(t, email.CardHash(it, founder), card.Hash)
	sent, err := f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.NoError(t, err)
	assert.Equal(t, gtm.ItemSent, sent.Status)
	assert.Equal(t, 1, sender.count())
}

// A channel that cannot send email is refused at Prepare, so no card is
// ever shown for the item, and again at Execute.
func TestPrepare_RefusesAnUnsendableChannel(t *testing.T) {
	paused := gmailChannel("ch-1", "")
	paused.Status = string(gtm.ChannelPaused)
	archived := gmailChannel("ch-1", "")
	archived.Status = string(gtm.ChannelArchived)
	manual := gmailChannel("ch-1", "")
	manual.Mode, manual.IntegrationKind = string(gtm.ChannelManual), nil
	slack := gmailChannel("ch-1", "")
	kind := "slack"
	slack.IntegrationKind = &kind
	for name, tc := range map[string]struct {
		channels []store.GtmChannel
		req      email.SendRequest
		want     error
	}{
		"paused":                    {[]store.GtmChannel{paused}, sendReq, gtm.ErrChannelNotSendable},
		"archived":                  {[]store.GtmChannel{archived}, sendReq, gtm.ErrChannelNotSendable},
		"manual":                    {[]store.GtmChannel{manual}, sendReq, gtm.ErrChannelNotSendable},
		"not gmail":                 {[]store.GtmChannel{slack}, sendReq, gtm.ErrChannelNotSendable},
		"pinned connection unknown": {[]store.GtmChannel{gmailChannel("ch-1", "conn-gone")}, sendReq, email.ErrNoGmailAccount},
		"channel gone":              {nil, sendReq, gtm.ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			it := approvedEmail()
			it.ChannelID = "ch-1"
			sender := &recordingEmail{}
			f := newFixture(t, it, sender, tc.channels...)

			_, err := f.approver.Prepare(context.Background(), agent, tc.req, false)
			require.ErrorIs(t, err, tc.want)
			_, err = f.approver.Execute(approvedCtx(gtm.ApprovalHash(it)), agent, tc.req, sendRun)
			require.ErrorIs(t, err, tc.want)
			assert.Zero(t, sender.count())
			assert.Empty(t, f.items.snapshot().ApprovalHash, "never held")
		})
	}
}

// The run the email would go out through is someone else's: nothing is
// sent, the failure is ErrEmailNotSent, and the item is released.
func TestExecute_FailsClosedOnAnotherUsersRun(t *testing.T) {
	src := &fakeSource{}
	exec := &fakeExecutor{}
	g := newGmailRuns(t, src, exec, &fakeRuns{owner: "user-2", org: tenantZero})
	f := newFixture(t, approvedEmail(), g)
	card, err := f.approver.Prepare(context.Background(), agent, sendReq, false)
	require.NoError(t, err)

	_, err = f.approver.Execute(approvedCtx(card.Hash), agent, sendReq, sendRun)
	require.ErrorIs(t, err, gtm.ErrEmailNotSent)
	assert.NotErrorIs(t, err, gtm.ErrSendInDoubt)
	assert.Empty(t, src.reqs, "no credential is resolved")
	assert.Zero(t, exec.count())
	row := f.items.snapshot()
	assert.Equal(t, string(gtm.ItemApproved), row.Status)
	assert.Empty(t, row.ApprovalHash, "released, not held")
}
