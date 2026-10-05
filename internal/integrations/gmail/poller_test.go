// Copyright (c) 2025 Reliant Labs

package gmail_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/integrations/gmail"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/integrations/manifest"
	"github.com/reliant-labs/reliant/internal/triggers"
)

func (f *fakeGmail) poller() *gmail.Poller {
	f.t.Helper()
	p, err := gmail.NewPoller(f.manifest(), f.runner())
	require.NoError(f.t, err)
	return p
}

func (f *fakeGmail) poll(p *gmail.Poller, cursor string) (*triggers.PollResult, error) {
	return p.Poll(context.Background(), triggers.PollRequest{TriggerID: "trg-1", OwnerUserID: "alice", ConnectionID: "conn_gmail", Credential: staticCred{}, Cursor: cursor})
}

func (f *fakeGmail) inbox(id, from, subject string) {
	f.addMessage(id, "th"+id, []string{"UNREAD", "INBOX", "CATEGORY_PERSONAL"}, map[string]string{
		"From": from, "To": "me@example.com", "Subject": subject, "Date": "Mon, 5 Oct 2026 09:00:00 +0000",
		"Message-ID": "<" + id + "@mail.example.com>",
	}, map[string]any{"mimeType": "text/plain", "body": map[string]any{"data": b64url("THE BODY OF " + id)}})
}

// Enabling a trigger never fires on mail that was already there.
func TestPollBaselineIsTheCurrentPositionAndNoItems(t *testing.T) {
	f := newFakeGmail(t)
	f.inbox("a0001", "ann@x.com", "old one")
	f.inbox("a0002", "ann@x.com", "old two")
	res, err := f.poll(f.poller(), "")
	require.NoError(t, err)
	assert.Equal(t, strconv.FormatUint(f.history, 10), res.Cursor)
	assert.Empty(t, res.Items)
	assert.Empty(t, res.Gap)
	reqs := f.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "/gmail/v1/users/me/profile", reqs[0].Path)
}

func TestPollReturnsExactlyTheNewMessages(t *testing.T) {
	f := newFakeGmail(t)
	p := f.poller()
	f.inbox("a0001", "ann@x.com", "before")
	base, err := f.poll(p, "")
	require.NoError(t, err)

	f.inbox("b0001", "Bob <bob@x.com>", "Invoice 1234")
	f.inbox("b0002", "cy@x.com", "Lunch?")
	// A message we sent ourselves is recorded in history too; it never fires.
	f.addMessage("b0003", "thb0003", []string{"SENT"}, map[string]string{"Subject": "sent by me"}, nil)
	f.reset()

	res, err := f.poll(p, base.Cursor)
	require.NoError(t, err)
	require.Len(t, res.Items, 2)
	assert.Equal(t, strconv.FormatUint(f.history, 10), res.Cursor, "the cursor is history.list's historyId")
	assert.Empty(t, res.Gap)

	first := res.Items[0]
	assert.Equal(t, "b0001", first.ID, "the message id is the dedupe key")
	assert.Equal(t, gmail.EventMessageReceived, first.Type)
	assert.Equal(t, map[string]string{
		"from": "Bob <bob@x.com>", "to": "me@example.com", "subject": "Invoice 1234",
		"label_ids": "UNREAD,INBOX,CATEGORY_PERSONAL", "thread_id": "thb0001",
	}, first.Attributes)
	assert.Equal(t, "Invoice 1234", first.Data["subject"])
	assert.Equal(t, "snippet of b0001", first.Data["snippet"])
	assert.Equal(t, "<b0001@mail.example.com>", first.Data["rfc822_message_id"])
	assert.Equal(t, "b0001", first.Data["message_id"])
	assert.NotContains(t, fmt.Sprint(first.Data), "THE BODY OF", "the payload carries the snippet, never the body")
	assert.Equal(t, "2026-10-03T04:00:00Z", first.OccurredAt.UTC().Format("2006-01-02T15:04:05Z"))

	// What went over the wire: history from the cursor, then metadata reads.
	reqs := f.requests()
	require.Len(t, reqs, 3)
	h := reqs[0]
	assert.Equal(t, "/gmail/v1/users/me/history", h.Path)
	assert.Equal(t, base.Cursor, h.Query.Get("startHistoryId"))
	assert.Equal(t, "messageAdded", h.Query.Get("historyTypes"))
	assert.Equal(t, "INBOX", h.Query.Get("labelId"))
	assert.Equal(t, "500", h.Query.Get("maxResults"))
	for _, get := range reqs[1:] {
		assert.Equal(t, "metadata", get.Query.Get("format"))
		assert.Subset(t, get.Query["metadataHeaders"], []string{"From", "To", "Subject", "Date"})
		assert.Equal(t, "Bearer "+token, get.Header.Get("Authorization"))
	}

	// Nothing new: a history answer with no `history` key still advances.
	f.history += 7
	res, err = f.poll(p, res.Cursor)
	require.NoError(t, err)
	assert.Empty(t, res.Items)
	assert.Equal(t, strconv.FormatUint(f.history, 10), res.Cursor)
}

func TestPollFollowsHistoryPagesAndDedupes(t *testing.T) {
	f := newFakeGmail(t)
	f.historyPageSize = 2
	p := f.poller()
	base, err := f.poll(p, "")
	require.NoError(t, err)
	for i := 1; i <= 5; i++ {
		f.inbox(fmt.Sprintf("c%04d", i), "ann@x.com", "n")
	}
	// The same message added twice (moved back into the inbox) fires once.
	f.records = append(f.records, historyRecord{id: f.history + 1, messages: []map[string]any{{"id": "c0001", "threadId": "thc0001", "labelIds": []string{"INBOX"}}}})
	f.history += 2

	res, err := f.poll(p, base.Cursor)
	require.NoError(t, err)
	var ids []string
	for _, it := range res.Items {
		ids = append(ids, it.ID)
	}
	assert.Equal(t, []string{"c0001", "c0002", "c0003", "c0004", "c0005"}, ids)
	assert.Equal(t, strconv.FormatUint(f.history, 10), res.Cursor)
	var pages []string
	for _, r := range f.requests() {
		if r.Path == "/gmail/v1/users/me/history" {
			pages = append(pages, r.Query.Get("pageToken"))
		}
	}
	assert.Equal(t, []string{"", "page-2", "page-4"}, pages)
}

// Google: "A historyId is typically valid for at least a week, but in some
// rare circumstances may be valid for only a few hours. If you receive an
// HTTP 404 error response, your application should perform a full sync." A
// trigger re-baselines instead: nothing fires for the gap, and the gap is
// reported.
func TestPollStaleHistoryRebaselinesWithoutFiring(t *testing.T) {
	f := newFakeGmail(t)
	p := f.poller()
	base, err := f.poll(p, "")
	require.NoError(t, err)
	f.inbox("d0001", "ann@x.com", "arrived during the outage")
	stale, _ := strconv.ParseUint(base.Cursor, 10, 64)
	f.expiredBefore = stale + 1

	res, err := f.poll(p, base.Cursor)
	require.NoError(t, err)
	assert.Empty(t, res.Items, "the gap is never replayed")
	assert.Equal(t, strconv.FormatUint(f.history, 10), res.Cursor, "a fresh baseline")
	assert.Contains(t, res.Gap, base.Cursor)
	assert.Contains(t, res.Gap, "did not fire")

	// From the fresh position, polling carries on normally.
	f.inbox("d0002", "ann@x.com", "after")
	res, err = f.poll(p, res.Cursor)
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Equal(t, "d0002", res.Items[0].ID)
	assert.Empty(t, res.Gap)
}

func TestPollGarbageCursorRebaselines(t *testing.T) {
	f := newFakeGmail(t)
	res, err := f.poll(f.poller(), "not-a-history-id")
	require.NoError(t, err)
	assert.Empty(t, res.Items)
	assert.NotEmpty(t, res.Gap)
	assert.Equal(t, strconv.FormatUint(f.history, 10), res.Cursor)
}

// A message deleted between its history record and the metadata read has
// nothing to fire on; the rest of the poll goes on.
func TestPollSkipsAMessageDeletedMeanwhile(t *testing.T) {
	f := newFakeGmail(t)
	p := f.poller()
	base, _ := f.poll(p, "")
	f.inbox("e0001", "ann@x.com", "gone")
	f.inbox("e0002", "ann@x.com", "kept")
	delete(f.messages, "e0001")
	res, err := f.poll(p, base.Cursor)
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.Equal(t, "e0002", res.Items[0].ID)
}

// A revoked token is reported as needs_reauth, so the poll activity records
// "reconnect" and does not retry.
func TestPollUnauthorizedIsNeedsReauth(t *testing.T) {
	f := newFakeGmail(t)
	f.unauthorized = true
	for _, cursor := range []string{"", "12345"} {
		_, err := f.poll(f.poller(), cursor)
		var ce *httpaction.CredentialError
		require.True(t, errors.As(err, &ce), "%v", err)
		assert.Equal(t, httpaction.CodeNeedsReauth, ce.Code)
		assert.Contains(t, ce.Message, "reconnect Gmail")
	}
}

// Any other failure is an error, and the cursor the activity holds stays put
// (it advances only on success), so nothing is lost.
func TestPollTransientFailureIsAnError(t *testing.T) {
	f := newFakeGmail(t)
	p := f.poller()
	base, _ := f.poll(p, "")
	f.inbox("f0001", "ann@x.com", "x")
	f.srv.Config.Handler = failing{}
	_, err := f.poll(p, base.Cursor)
	require.Error(t, err)
	var ce *httpaction.CredentialError
	assert.False(t, errors.As(err, &ce))
}

func TestPollWithoutACredentialRefuses(t *testing.T) {
	f := newFakeGmail(t)
	_, err := f.poller().Poll(context.Background(), triggers.PollRequest{TriggerID: "t"})
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Empty(t, f.requests(), "nothing is sent unauthenticated")
}

// A burst larger than one poll delivers is spread over polls: the cursor stops
// at the last history record fully delivered, so nothing is dropped.
func TestPollCapsABurstAndResumes(t *testing.T) {
	f := newFakeGmail(t)
	f.historyPageSize = 40
	p := f.poller()
	base, _ := f.poll(p, "")
	total := gmail.MaxItemsPerPoll + 50
	for i := 0; i < total; i++ {
		f.inbox(fmt.Sprintf("%06x", 0xa00000+i), "ann@x.com", "burst")
	}
	seen := map[string]bool{}
	cursor := base.Cursor
	for polls := 0; polls < 5 && len(seen) < total; polls++ {
		res, err := f.poll(p, cursor)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(res.Items), gmail.MaxItemsPerPoll+f.historyPageSize, "bounded per poll")
		for _, it := range res.Items {
			seen[it.ID] = true
		}
		cursor = res.Cursor
	}
	assert.Len(t, seen, total, "every message of the burst fires")
	assert.Equal(t, strconv.FormatUint(f.history, 10), cursor)
}

// The provider and the manifest agree: every attribute the poller sets is
// declared, and the payload validates against TriggerPayloadSchema — what a
// trigger's CEL filter is checked against at save time.
func TestPollerItemsMatchTheManifestTrigger(t *testing.T) {
	f := newFakeGmail(t)
	p := f.poller()
	base, _ := f.poll(p, "")
	f.inbox("a0f001", "Ann <ann@x.com>", "hello")
	res, err := f.poll(p, base.Cursor)
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	item := res.Items[0]

	m := f.manifest()
	spec, ok := manifest.Trigger(m, "message.received")
	require.True(t, ok)
	assert.Equal(t, []string{item.Type}, spec.GetEvents())
	declared := map[string]bool{}
	for _, a := range spec.GetAttributes() {
		declared[a.GetName()] = true
		assert.Contains(t, item.Attributes, a.GetName(), "every declared attribute is set")
	}
	for k := range item.Attributes {
		assert.True(t, declared[k], "attribute %q is declared", k)
	}
	attrs := map[string]any{}
	for k, v := range item.Attributes {
		attrs[k] = v
	}
	payload := map[string]any{
		"integration": "gmail", "event": item.Type, "account": "me@example.com", "delivery_id": item.ID,
		"attributes": attrs, "data": item.Data,
	}
	require.NoError(t, validateAgainst(manifest.TriggerPayloadSchema(m, spec), payload))
}

// failing answers every call with a 503.
type failing struct{}

func (failing) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	googleErr(w, 503, "UNAVAILABLE", "backendError", "The service is currently unavailable.")
}

// validateAgainst checks v, as JSON, against a JSON Schema.
func validateAgainst(schemaMap map[string]any, v any) error {
	rawSchema, err := json.Marshal(schemaMap)
	if err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var asJSON any
	if err := json.Unmarshal(raw, &asJSON); err != nil {
		return err
	}
	return resolved.Validate(asJSON)
}

// A token Google revoked before it expired is refreshed and the poll carries
// on: one rejection, one refresh, the rest of the poll on the new token.
func TestPollRecoversFromARevokedTokenByRefreshing(t *testing.T) {
	f := newFakeGmail(t)
	p := f.poller()
	base, err := f.poll(p, "")
	require.NoError(t, err)
	f.inbox("a1b001", "ann@x.com", "after the revoke")
	f.inbox("a1b002", "ann@x.com", "and another")

	calls := 0
	fresh := token // the fake accepts exactly this one
	cred := rejectableCred{tok: "ya29.revoked", next: &fresh, calls: &calls}
	res, err := p.Poll(context.Background(), triggers.PollRequest{TriggerID: "t", Credential: cred, Cursor: base.Cursor})
	require.NoError(t, err)
	assert.Len(t, res.Items, 2)
	assert.Equal(t, 1, calls, "refreshed once, then reused for every later call")

	// A dead grant: the refresh is refused, so needs_reauth.
	calls = 0
	_, err = p.Poll(context.Background(), triggers.PollRequest{TriggerID: "t", Credential: rejectableCred{tok: "ya29.revoked", dead: true, calls: &calls}, Cursor: res.Cursor})
	var ce *httpaction.CredentialError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeNeedsReauth, ce.Code)
	assert.Equal(t, 1, calls)

	// The replacement refused too: final, no loop.
	calls = 0
	stillBad := "ya29.also-revoked"
	_, err = p.Poll(context.Background(), triggers.PollRequest{TriggerID: "t", Credential: rejectableCred{tok: "ya29.revoked", next: &stillBad, calls: &calls}, Cursor: res.Cursor})
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, httpaction.CodeNeedsReauth, ce.Code)
	assert.Equal(t, 1, calls, "one refresh per rejection, never a retry loop")
}
