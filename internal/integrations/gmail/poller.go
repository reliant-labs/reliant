// Copyright (c) 2025 Reliant Labs

package gmail

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
	"github.com/reliant-labs/reliant/internal/triggers"
)

// The new-email trigger, polled from users.history.list. The cursor is a
// Gmail historyId (a uint64 as a decimal string), which Google documents as
// "returns history records after the specified startHistoryId":
//
//   - Baseline (empty cursor): users.getProfile's historyId — the mailbox's
//     current position — and no items, so enabling a trigger never fires on
//     mail that was already there.
//   - Poll: history.list from the cursor, historyTypes=messageAdded,
//     labelId=INBOX, following nextPageToken. The new cursor is the
//     response's top-level historyId, taken only after every page succeeded.
//   - Each new message (deduped across records, SENT/DRAFT skipped) is read
//     with messages.get?format=metadata for headers and the snippet. Bodies
//     are never fetched here: message.get reads one when a run needs it.
//   - Stale cursor: history.list answers 404 when startHistoryId is older
//     than Gmail keeps ("typically valid for at least a week, but in some
//     rare circumstances ... only a few hours"). Google's prescription is a
//     full sync; a trigger has no use for replaying a week of mail, so the
//     poll re-baselines from getProfile, fires NOTHING for the gap, and
//     reports it (PollResult.Gap), which trigger health shows.
//   - A poll delivers at most MaxItemsPerPoll messages. Past that it stops
//     at a page boundary with the cursor at the last history record it fully
//     delivered, and the next poll carries on from there: a burst is
//     delivered over several polls, never dropped.

// ID is the integration id.
const ID = "gmail"

// EventMessageReceived is the one event type the poller emits; it is the
// manifest trigger's (gmail/message.received@1) only event.
const EventMessageReceived = "message.received"

// MaxItemsPerPoll bounds one poll's messages.get calls (5 quota units each).
const MaxItemsPerPoll = 100

// historyPageSize is history.list's maxResults (its maximum).
const historyPageSize = 500

// maxHistoryPages bounds one poll's history.list pages.
const maxHistoryPages = 20

// Poller is Gmail's triggers.Poller.
type Poller struct {
	baseURL string
	runner  *httpaction.Runner
	now     func() time.Time
}

// NewPoller builds the poller over the manifest's base_url and the guarded
// runner whose client every call goes through.
func NewPoller(m *reliantv1.IntegrationManifest, runner *httpaction.Runner) (*Poller, error) {
	if m.GetId() != ID {
		return nil, fmt.Errorf("gmail: poller built over manifest %q", m.GetId())
	}
	if runner == nil {
		return nil, fmt.Errorf("gmail: poller needs a runner")
	}
	return &Poller{baseURL: m.GetConnection().GetBaseUrl(), runner: runner, now: time.Now}, nil
}

type profile struct {
	EmailAddress string `json:"emailAddress"`
	HistoryID    string `json:"historyId"`
}

type historyList struct {
	History []struct {
		ID            string `json:"id"`
		MessagesAdded []struct {
			Message struct {
				ID       string   `json:"id"`
				ThreadID string   `json:"threadId"`
				LabelIDs []string `json:"labelIds"`
			} `json:"message"`
		} `json:"messagesAdded"`
	} `json:"history"`
	NextPageToken string `json:"nextPageToken"`
	HistoryID     string `json:"historyId"`
}

// Poll implements triggers.Poller.
func (p *Poller) Poll(ctx context.Context, req triggers.PollRequest) (*triggers.PollResult, error) {
	c, err := newAPI(p.baseURL, p.runner.Client(), req.Credential)
	if err != nil {
		return nil, err
	}
	if req.Cursor == "" {
		pos, err := p.position(ctx, c)
		if err != nil {
			return nil, err
		}
		return &triggers.PollResult{Cursor: pos}, nil
	}
	if _, err := strconv.ParseUint(req.Cursor, 10, 64); err != nil {
		// Not a history id (a corrupted or foreign cursor): start over from
		// now rather than fail every poll forever.
		return p.rebaseline(ctx, c, "the stored cursor "+strconv.Quote(truncate(req.Cursor, 40))+" is not a Gmail history id")
	}

	var (
		pending []pendingMessage
		seen    = map[string]bool{}
		token   string
		cursor  = req.Cursor
		latest  string // the last record id fully read, for a capped stop
	)
	for page := 0; ; page++ {
		if page >= maxHistoryPages {
			// More history than one poll reads: resume after what was read.
			cursor = latest
			break
		}
		q := url.Values{
			"startHistoryId": {req.Cursor},
			"historyTypes":   {"messageAdded"},
			"labelId":        {"INBOX"},
			"maxResults":     {strconv.Itoa(historyPageSize)},
		}
		if token != "" {
			q.Set("pageToken", token)
		}
		var h historyList
		if err := c.do(ctx, http.MethodGet, "/history", q, nil, &h); err != nil {
			var ae *apiError
			if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
				return p.rebaseline(ctx, c, "Gmail no longer had history from "+req.Cursor+
					" (history ids expire after about a week, sometimes sooner); mail that arrived in the gap did not fire")
			}
			return nil, err
		}
		for _, rec := range h.History {
			for _, added := range rec.MessagesAdded {
				m := added.Message
				if m.ID == "" || seen[m.ID] || hasLabel(m.LabelIDs, "SENT", "DRAFT") {
					continue
				}
				seen[m.ID] = true
				pending = append(pending, pendingMessage{id: m.ID, threadID: m.ThreadID, labels: m.LabelIDs})
			}
			if rec.ID != "" {
				latest = rec.ID
			}
		}
		if h.NextPageToken == "" {
			// Every page read: the mailbox's position now. An empty answer
			// (no `history` key) still carries it.
			if h.HistoryID != "" {
				cursor = h.HistoryID
			}
			break
		}
		if len(pending) >= MaxItemsPerPoll && latest != "" {
			cursor = latest
			break
		}
		token = h.NextPageToken
	}
	// The cap is checked at page boundaries only, so a poll can deliver up to
	// one page past it: a cursor cannot point into the middle of a page, and
	// dropping the rest of one would lose mail.
	items := make([]triggers.PollItem, 0, len(pending))
	for _, pm := range pending {
		item, ok, err := p.item(ctx, c, pm)
		if err != nil {
			// Do not advance past messages that were never delivered: the
			// next poll re-reads this window, and what was recorded dedupes.
			return nil, err
		}
		if ok {
			items = append(items, item)
		}
	}
	return &triggers.PollResult{Cursor: cursor, Items: items}, nil
}

type pendingMessage struct {
	id, threadID string
	labels       []string
}

// item reads one new message's metadata. A message deleted between the
// history record and this read (404) is skipped: there is nothing to fire on.
func (p *Poller) item(ctx context.Context, c *api, pm pendingMessage) (triggers.PollItem, bool, error) {
	q := url.Values{"format": {"metadata"}, "metadataHeaders": pollHeaders}
	var m apiMessage
	if err := c.do(ctx, http.MethodGet, "/messages/"+url.PathEscape(pm.id), q, nil, &m); err != nil {
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return triggers.PollItem{}, false, nil
		}
		return triggers.PollItem{}, false, err
	}
	if m.ID == "" {
		m.ID = pm.id
	}
	if m.ThreadID == "" {
		m.ThreadID = pm.threadID
	}
	if len(m.LabelIDs) == 0 {
		m.LabelIDs = pm.labels
	}
	if hasLabel(m.LabelIDs, "SENT", "DRAFT") {
		return triggers.PollItem{}, false, nil
	}
	f := flatten(&m)
	var headers []apiHeader
	if m.Payload != nil {
		headers = m.Payload.Headers
	}
	occurred := p.now().UTC()
	if t, err := time.Parse(time.RFC3339, str(f["internal_date"])); err == nil {
		occurred = t
	}
	data := map[string]any{
		"message_id": m.ID, "thread_id": m.ThreadID, "label_ids": anyStrings(m.LabelIDs), "snippet": m.Snippet,
		"from": f["from"], "to": f["to"], "cc": f["cc"], "subject": f["subject"], "date": f["date"],
		"rfc822_message_id": f["message_id"], "internal_date": f["internal_date"],
	}
	return triggers.PollItem{
		ID:         m.ID,
		Type:       EventMessageReceived,
		OccurredAt: occurred,
		Attributes: map[string]string{
			"from": str(f["from"]), "to": str(f["to"]), "subject": str(f["subject"]),
			"label_ids": strings.Join(m.LabelIDs, ","), "thread_id": m.ThreadID,
		},
		Data:   data,
		Sender: emailSender(headers),
	}, true, nil
}

// pollHeaders are the metadata headers a new message is read with: the
// flattened ones, plus Gmail's Authentication-Results, which decides whether
// its From is a verified sender (emailSender).
var pollHeaders = append(append([]string(nil), metadataHeaders...), authResultsHeader)

// position is the mailbox's current history id.
func (p *Poller) position(ctx context.Context, c *api) (string, error) {
	var prof profile
	if err := c.do(ctx, http.MethodGet, "/profile", nil, nil, &prof); err != nil {
		return "", err
	}
	if _, err := strconv.ParseUint(prof.HistoryID, 10, 64); err != nil {
		return "", fmt.Errorf("gmail: users.getProfile returned no history id")
	}
	return prof.HistoryID, nil
}

// rebaseline restarts from the mailbox's current position, firing nothing,
// and reports the gap.
func (p *Poller) rebaseline(ctx context.Context, c *api, why string) (*triggers.PollResult, error) {
	pos, err := p.position(ctx, c)
	if err != nil {
		return nil, err
	}
	return &triggers.PollResult{Cursor: pos, Gap: why}, nil
}

func hasLabel(labels []string, any ...string) bool {
	for _, l := range labels {
		for _, want := range any {
			if l == want {
				return true
			}
		}
	}
	return false
}
