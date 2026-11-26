// Copyright (c) 2026 Reliant Labs

package email

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/reliant-labs/reliant/internal/gtm"
)

// ToolName is the agent tool that sends a GTM email through this package.
//
// TODO(PR-7): names.ToolSendGtmEmail replaces this once the tool is
// registered; until then the runtime wiring spells the same literal.
const ToolName = "send_gtm_email"

// Card is the approval card for sending one email item: what the owner sees
// and the hash their approval binds.
type Card struct {
	// Title is the card as the approval shows it (the activation-card layout
	// the web client reads in lib/approvalCard.ts): a lead sentence, the
	// To and Subject rows, the body, then the technical lines.
	Title string
	// Hash is gtm.ApprovalHash of the item the card shows: what
	// SendItemEmail checks the approval against.
	Hash string
	// ItemID and Revision are the item and the revision the card shows.
	ItemID   string
	Revision int32
}

// Card layout, in step with the web client (frontends/web/src/lib/
// approvalCard.ts and internal/handlers/trigger/activation_card.go): a
// "Label: value" line under the lead is a row, and "\n\n" + cardDetailsHeading
// + "\n" starts the technical lines.
const (
	cardDetailsHeading = "Technical details"
	// bodyMarker starts every body line, so no line of the body can read as a
	// row ("To: someone@else") or, with an empty line before it, as the
	// technical-details heading.
	bodyMarker = "│"
	// leadSubjectLimit bounds the subject quoted in the lead; the Subject row
	// carries it whole.
	leadSubjectLimit = 120
)

// BuildCard lays out the approval card for it, sent from ch (nil when the
// item names no channel: the actor's default Gmail connection). It is pure
// and checks only that the item can be shown faithfully and sent: an
// email-type item (EMAIL or COLD_OUTREACH) with recipients, a one-line
// subject and a body. Whether it may be sent (APPROVED, at the revision
// named, never SENT, unheld, on a sendable channel) is Approver.Prepare's to
// check.
func BuildCard(it gtm.Item, ch *gtm.Channel) (Card, error) {
	if !it.Format.EmailType() {
		return Card{}, gtm.ErrNotApproved
	}
	if len(it.EmailTo) == 0 {
		return Card{}, &gtm.ValidationError{Field: "email_to", Reason: "an email needs recipients"}
	}
	if strings.TrimSpace(it.EmailSubject) == "" {
		return Card{}, &gtm.ValidationError{Field: "email_subject", Reason: "an email needs a subject"}
	}
	if strings.TrimSpace(it.BodyMD) == "" {
		return Card{}, &gtm.ValidationError{Field: "body_md", Reason: "an email needs a body"}
	}
	if strings.IndexFunc(it.EmailSubject, unicode.IsControl) >= 0 {
		// A subject is one header line; one the card would have to reflow is
		// refused rather than shown other than it is sent.
		return Card{}, &gtm.ValidationError{Field: "email_subject", Reason: "a subject is one line with no control characters"}
	}
	for _, addr := range it.EmailTo {
		if addr == "" || strings.IndexFunc(addr, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) || r == ',' }) >= 0 {
			return Card{}, &gtm.ValidationError{Field: "email_to", Reason: "a recipient is one bare address"}
		}
	}
	from := "Your default Gmail connection, under your name"
	if ch != nil {
		from = fmt.Sprintf("Your Gmail on channel “%s”, under your name", clip(strings.Join(strings.Fields(ch.Name), " "), leadSubjectLimit))
	}
	hash := gtm.ApprovalHash(it)
	lines := []string{
		fmt.Sprintf("Send email “%s” to %s?", clip(it.EmailSubject, leadSubjectLimit), recipients(len(it.EmailTo))),
		"To: " + strings.Join(it.EmailTo, ", "),
		"Subject: " + it.EmailSubject,
		"From: " + from,
		"Approval needed: Sending cannot be undone. The email goes out exactly as shown, once.",
	}
	lines = append(lines, bodyLines(it.BodyMD)...)
	lines = append(lines,
		"",
		cardDetailsHeading,
		"Operation: "+ToolName,
		"Item: "+it.ID,
		"Revision: "+strconv.FormatInt(int64(it.Revision), 10),
		"Approval hash: "+hash,
	)
	return Card{Title: strings.Join(lines, "\n"), Hash: hash, ItemID: it.ID, Revision: it.Revision}, nil
}

// bodyLines is the body as the card shows it: every line, blank ones too,
// behind bodyMarker.
func bodyLines(body string) []string {
	raw := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	out := make([]string, len(raw))
	for i, line := range raw {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			out[i] = bodyMarker
			continue
		}
		out[i] = bodyMarker + " " + line
	}
	return out
}

func recipients(n int) string {
	if n == 1 {
		return "1 recipient"
	}
	return strconv.Itoa(n) + " recipients"
}
