// Copyright (c) 2026 Reliant Labs

package email

import (
	"errors"
	"fmt"
	"net/mail"
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

// BuildCard lays out the approval card for it, sent from the Gmail account
// from on channel ch (nil when the item names no channel). It is pure and
// checks only that the item can be shown faithfully and sent:
//
//   - an email-type item (EMAIL or COLD_OUTREACH) with recipients, a subject
//     and a body;
//   - every recipient, and the sender, a bare ASCII address (no IDN for
//     now), so a look-alike or invisible character cannot pass for another
//     address;
//   - a subject of one line with no control, format (unicode.Cf, which holds
//     the bidi overrides and zero-width characters) or line-separator
//     characters, and no curly quotes, which would break the lead's quoting.
//
// The body is shown whole. Its invisible characters (Cf, line and paragraph
// separators, controls other than tab) are shown escaped, as \u202E: the
// card shows that they are there rather than letting them reorder or hide
// what is around them. The hash binds the body's exact bytes either way.
//
// Whether the item may be sent (APPROVED, at the revision named, never
// SENT, unheld, on a sendable channel) is Approver.Prepare's to check.
func BuildCard(it gtm.Item, ch *gtm.Channel, from Account) (Card, error) {
	if !it.Format.EmailType() {
		return Card{}, gtm.ErrNotApproved
	}
	if len(it.EmailTo) == 0 {
		return Card{}, &gtm.ValidationError{Field: "email_to", Reason: "an email needs recipients"}
	}
	if strings.TrimSpace(it.BodyMD) == "" {
		return Card{}, &gtm.ValidationError{Field: "body_md", Reason: "an email needs a body"}
	}
	if err := checkSubject(it.EmailSubject); err != nil {
		return Card{}, err
	}
	for _, addr := range it.EmailTo {
		if checkAddress(addr) != nil {
			return Card{}, &gtm.ValidationError{Field: "email_to", Reason: "a recipient is one bare ASCII address"}
		}
	}
	if checkAddress(from.Address) != nil {
		return Card{}, ErrNoGmailAccount
	}
	hash := gtm.ApprovalHash(it)
	lines := []string{
		fmt.Sprintf("Send email “%s” to %s?", clip(it.EmailSubject, leadSubjectLimit), recipients(len(it.EmailTo))),
		"To: " + strings.Join(it.EmailTo, ", "),
		"Subject: " + it.EmailSubject,
		"From: " + fromRow(from, ch),
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

// fromRow names the account and how it was chosen: the channel's own
// connection, or the actor's default.
func fromRow(from Account, ch *gtm.Channel) string {
	switch {
	case ch == nil:
		return from.Address + " (Gmail, your default connection)"
	case ch.ConnectionID != "":
		return from.Address + " (Gmail, channel " + channelName(ch) + ")"
	default:
		return from.Address + " (Gmail, your default connection, channel " + channelName(ch) + ")"
	}
}

// channelName is ch's name on one line, its invisible characters escaped.
func channelName(ch *gtm.Channel) string {
	return clip(escapeInvisible(strings.Join(strings.Fields(ch.Name), " ")), leadSubjectLimit)
}

// checkSubject refuses a subject the card cannot show as it is sent.
func checkSubject(subject string) error {
	switch {
	case strings.TrimSpace(subject) == "":
		return &gtm.ValidationError{Field: "email_subject", Reason: "an email needs a subject"}
	case strings.IndexFunc(subject, func(r rune) bool { return unicode.IsControl(r) || invisible(r) }) >= 0:
		return &gtm.ValidationError{Field: "email_subject", Reason: "a subject is one line with no control, invisible or bidi characters"}
	case strings.ContainsAny(subject, "“”"):
		return &gtm.ValidationError{Field: "email_subject", Reason: "a subject may not contain curly double quotes (“ ”); use straight quotes"}
	}
	return nil
}

// checkAddress accepts one bare ASCII address, as net/mail parses it back
// unchanged: printable ASCII, no space or comma, an @.
func checkAddress(addr string) error {
	if addr == "" || !strings.Contains(addr, "@") {
		return errors.New("not an address")
	}
	for i := 0; i < len(addr); i++ {
		if c := addr[i]; c <= ' ' || c >= 0x7f || c == ',' {
			return errors.New("not a bare ASCII address")
		}
	}
	if a, err := mail.ParseAddress(addr); err != nil || a.Address != addr {
		return errors.New("not a bare address")
	}
	return nil
}

// invisible reports a rune that changes or hides text without showing
// itself: a format character (unicode.Cf: bidi overrides and isolates,
// zero-width joiners and spaces, the BOM, tag characters) or a line or
// paragraph separator.
func invisible(r rune) bool {
	return unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r)
}

// escapeInvisible shows each invisible character, and each control but tab,
// as its escape (\u202E, \U000E0041).
func escapeInvisible(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return invisible(r) || (unicode.IsControl(r) && r != '\t') }) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !invisible(r) && !(unicode.IsControl(r) && r != '\t'):
			b.WriteRune(r)
		case r > 0xFFFF:
			fmt.Fprintf(&b, "\\U%08X", r)
		default:
			fmt.Fprintf(&b, "\\u%04X", r)
		}
	}
	return b.String()
}

// bodyLines is the body as the card shows it: every line, blank ones too,
// behind bodyMarker, invisible characters escaped.
func bodyLines(body string) []string {
	raw := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	out := make([]string, len(raw))
	for i, line := range raw {
		line = escapeInvisible(strings.TrimRight(line, " \t"))
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
