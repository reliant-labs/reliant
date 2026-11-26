// Copyright (c) 2026 Reliant Labs

package email

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
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

// HashPrefix starts every approval hash: the scheme and its version. A
// change to the encoding below is a new version, so a card approved under
// the old one never matches.
const HashPrefix = "gtm_email:v1:"

// hashDomain is the first field hashed, so no other sha256 in Reliant over
// the same bytes can be mistaken for an email approval.
const hashDomain = "reliant/gtm/email-approval/v1"

// ApprovalHash is the hash an email item's approval card binds: HashPrefix
// followed by the lowercase hex sha256 of the length-prefixed encoding of
//
//	hashDomain, item id, revision (decimal), recipient count (decimal),
//	each recipient in order, subject, body
//
// where each field is its byte length as an unsigned 64-bit big-endian
// integer followed by its bytes. The length prefix makes the encoding
// injective: no two different items encode alike, however their fields
// split (a recipient "a,b" against two recipients "a" and "b", a subject
// ending where the body would begin). Recipients are hashed in order, as
// they are sent.
//
// It is pure and stable: the same item content gives the same hash in every
// process and release of v1. Any change to the id, revision, recipients,
// subject or body changes it.
func ApprovalHash(it gtm.Item) string {
	h := sha256.New()
	writeField(h, hashDomain)
	writeField(h, it.ID)
	writeField(h, strconv.FormatInt(int64(it.Revision), 10))
	writeField(h, strconv.Itoa(len(it.EmailTo)))
	for _, addr := range it.EmailTo {
		writeField(h, addr)
	}
	writeField(h, it.EmailSubject)
	writeField(h, it.BodyMD)
	return HashPrefix + hex.EncodeToString(h.Sum(nil))
}

func writeField(h hash.Hash, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	h.Write(n[:])
	h.Write([]byte(s))
}

// Card is the approval card for sending one email item: what the owner sees
// and the hash their approval binds.
type Card struct {
	// Title is the card as the approval shows it (the activation-card layout
	// the web client reads in lib/approvalCard.ts): a lead sentence, the
	// To and Subject rows, the body, then the technical lines.
	Title string
	// Hash is ApprovalHash of the item the card shows.
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

// BuildCard lays out the approval card for it. It is pure and checks only
// that the item can be shown faithfully and sent: an EMAIL item with
// recipients, a one-line subject and a body. Whether it may be sent (APPROVED, at the revision named,
// never SENT) is Approver.Prepare's to check.
func BuildCard(it gtm.Item) (Card, error) {
	if it.Kind != gtm.ItemEmail {
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
	hash := ApprovalHash(it)
	lines := []string{
		fmt.Sprintf("Send email “%s” to %s?", clip(it.EmailSubject, leadSubjectLimit), recipients(len(it.EmailTo))),
		"To: " + strings.Join(it.EmailTo, ", "),
		"Subject: " + it.EmailSubject,
		"From: Your connected Gmail account, under your name",
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
