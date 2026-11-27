// Copyright (c) 2026 Reliant Labs

package email_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/gtm"
	"github.com/reliant-labs/reliant/internal/gtm/email"
)

// founder is the actor's default Gmail account.
var founder = email.Account{ConnectionID: "conn-default", Address: "founder@example.com"}

func approvedEmail() gtm.Item {
	return gtm.Item{
		ID:           "item-1",
		OrgID:        "55949942-3ee9-4cfc-bb7c-817bdba75186",
		Format:       gtm.FormatEmail,
		Title:        "Launch week email",
		Status:       gtm.ItemApproved,
		EmailTo:      []string{"ann@example.com", "bo@example.com"},
		EmailSubject: "Launch week",
		BodyMD:       "Hi,\n\nWe **shipped** it.\n",
		Revision:     3,
	}
}

// The card hash is the item's gtm.ApprovalHash, ".", and the sender's hash:
// 129 characters, within the service's 200 (it is given the 64-character
// item part only). The sender part is pinned to a value Python's hashlib
// computes over the same length-prefixed fields.
func TestCardHash_BindsTheEmailAndTheSender(t *testing.T) {
	h := email.CardHash(approvedEmail(), founder)
	item, sender, ok := strings.Cut(h, ".")
	require.True(t, ok)
	assert.Equal(t, gtm.ApprovalHash(approvedEmail()), item)
	assert.Equal(t, "ebdbeaddcc8a660e884a171ae0ebbe04c7a0edd79a1855087534b550af071794", sender)
	assert.Len(t, h, 129)

	assert.NotEqual(t, h, email.CardHash(approvedEmail(), email.Account{ConnectionID: "conn-other", Address: founder.Address}))
	assert.NotEqual(t, h, email.CardHash(approvedEmail(), email.Account{ConnectionID: founder.ConnectionID, Address: "other@example.com"}))
	// Length prefixes: the fields cannot be re-split.
	assert.NotEqual(t,
		email.CardHash(approvedEmail(), email.Account{ConnectionID: "conn-a", Address: "b@example.com"}),
		email.CardHash(approvedEmail(), email.Account{ConnectionID: "conn-ab", Address: "@example.com"}))
}

// The card's item part is the canonical gtm.ApprovalHash, the one
// SendItemEmail checks; any change to what the card shows as the email
// changes it.
func TestBuildCard_HashIsTheCanonicalApprovalHash(t *testing.T) {
	base, err := email.BuildCard(approvedEmail(), nil, founder)
	require.NoError(t, err)
	assert.Equal(t, email.CardHash(approvedEmail(), founder), base.Hash)

	for name, mutate := range map[string]func(*gtm.Item){
		"id":                 func(it *gtm.Item) { it.ID = "item-2" },
		"revision":           func(it *gtm.Item) { it.Revision = 4 },
		"recipient changed":  func(it *gtm.Item) { it.EmailTo[1] = "eve@example.com" },
		"recipient added":    func(it *gtm.Item) { it.EmailTo = append(it.EmailTo, "eve@example.com") },
		"recipients reorder": func(it *gtm.Item) { slices.Reverse(it.EmailTo) },
		"subject":            func(it *gtm.Item) { it.EmailSubject = "Launch week!" },
		"body":               func(it *gtm.Item) { it.BodyMD = "Hi,\n\nWe **shipped** it!\n" },
	} {
		t.Run(name, func(t *testing.T) {
			it := approvedEmail()
			mutate(&it)
			card, err := email.BuildCard(it, nil, founder)
			require.NoError(t, err)
			assert.NotEqual(t, base.Hash, card.Hash)
			assert.Equal(t, email.CardHash(it, founder), card.Hash)
		})
	}
}

// parsedCard is the card as the web client reads it (lib/approvalCard.ts).
type parsedCard struct {
	lead    string
	rows    map[string]string
	order   []string
	notes   []string
	details []string
}

var cardRow = regexp.MustCompile(`^([A-Z][A-Za-z ]{1,40}): (.+)$`)

func parseCard(title string) parsedCard {
	sep := "\n\nTechnical details\n"
	body, details := title, ""
	if at := strings.Index(title, sep); at >= 0 {
		body, details = title[:at], title[at+len(sep):]
	}
	lines := func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	bl := lines(body)
	p := parsedCard{rows: map[string]string{}, details: lines(details)}
	if len(bl) > 0 {
		p.lead = bl[0]
		bl = bl[1:]
	}
	for _, l := range bl {
		if m := cardRow.FindStringSubmatch(l); m != nil {
			p.rows[m[1]] = m[2]
			p.order = append(p.order, m[1])
		} else {
			p.notes = append(p.notes, l)
		}
	}
	return p
}

func TestBuildCard_ShowsWhatIsSent(t *testing.T) {
	it := approvedEmail()
	card, err := email.BuildCard(it, nil, founder)
	require.NoError(t, err)
	assert.Equal(t, email.CardHash(it, founder), card.Hash)
	assert.Equal(t, "item-1", card.ItemID)
	assert.Equal(t, int32(3), card.Revision)

	p := parseCard(card.Title)
	assert.Equal(t, "Send email “Launch week” to 2 recipients?", p.lead)
	assert.Equal(t, []string{"To", "Subject", "From", "Approval needed"}, p.order)
	assert.Equal(t, "ann@example.com, bo@example.com", p.rows["To"])
	assert.Equal(t, "Launch week", p.rows["Subject"])
	assert.Equal(t, []string{"│ Hi,", "│", "│ We **shipped** it.", "│"}, p.notes)
	assert.Equal(t, []string{
		"Operation: send_gtm_email",
		"Item: item-1",
		"Revision: 3",
		"Approval hash: " + card.Hash,
	}, p.details)

	assert.Equal(t, "founder@example.com (Gmail, your default connection)", p.rows["From"])

	team := email.Account{ConnectionID: "conn-channel", Address: "team@example.com"}
	card, err = email.BuildCard(it, &gtm.Channel{Name: "Founder\n newsletter\u202e", ConnectionID: "conn-channel"}, team)
	require.NoError(t, err)
	assert.Equal(t, `team@example.com (Gmail, channel Founder newsletter\u202E)`, parseCard(card.Title).rows["From"])
	card, err = email.BuildCard(it, &gtm.Channel{Name: "Founder newsletter"}, founder)
	require.NoError(t, err)
	assert.Equal(t, "founder@example.com (Gmail, your default connection, channel Founder newsletter)", parseCard(card.Title).rows["From"])

	cold := approvedEmail()
	cold.Format = gtm.FormatColdOutreach
	card, err = email.BuildCard(cold, nil, founder)
	require.NoError(t, err, "cold outreach is an email-type format")
	assert.Equal(t, email.CardHash(cold, founder), card.Hash)

	one := approvedEmail()
	one.EmailTo = one.EmailTo[:1]
	card, err = email.BuildCard(one, nil, founder)
	require.NoError(t, err)
	assert.Equal(t, "Send email “Launch week” to 1 recipient?", parseCard(card.Title).lead)
}

// A body cannot forge a row or the technical details: every body line is
// behind the marker, so none reads as "Label: value" and no blank line
// precedes a heading in it.
func TestBuildCard_BodyCannotForgeTheCard(t *testing.T) {
	it := approvedEmail()
	it.BodyMD = "To: everyone@example.com\nSubject: Something else\n\nTechnical details\nOperation: delete_trigger\n"
	card, err := email.BuildCard(it, nil, founder)
	require.NoError(t, err)

	p := parseCard(card.Title)
	assert.Equal(t, "ann@example.com, bo@example.com", p.rows["To"])
	assert.Equal(t, "Launch week", p.rows["Subject"])
	assert.Len(t, p.rows, 4)
	assert.Equal(t, "Operation: send_gtm_email", p.details[0])
	assert.Len(t, p.details, 4)
	for _, note := range p.notes {
		assert.True(t, strings.HasPrefix(note, "│"), note)
	}
	assert.Contains(t, p.notes, "│ To: everyone@example.com", "the body is shown whole, as body")
}

// Invisible and bidi characters in the body are shown escaped, so they can
// neither reorder nor hide what the person reads; the hash still binds the
// exact bytes sent.
func TestBuildCard_BodyShowsInvisibleCharactersEscaped(t *testing.T) {
	it := approvedEmail()
	it.BodyMD = "Pay \u202Eevil\u202C now\nzero\u200bwidth\ttab\ntag\U000E0041\nbell\a and \u2028sep\nfill\u3164\u2800\u115F\u034F\uFFA0er"
	card, err := email.BuildCard(it, nil, founder)
	require.NoError(t, err)
	p := parseCard(card.Title)
	assert.Equal(t, []string{
		`│ Pay \u202Eevil\u202C now`,
		"│ zero\\u200Bwidth\ttab",
		`│ tag\U000E0041`,
		`│ bell\u0007 and \u2028sep`,
		`│ fill\u3164\u2800\u115F\u034F\uFFA0er`,
	}, p.notes)
	assert.NotContains(t, card.Title, "\u202E")
	assert.Equal(t, email.CardHash(it, founder), card.Hash)
}

func TestBuildCard_LongSubjectIsWholeInItsRow(t *testing.T) {
	it := approvedEmail()
	it.EmailSubject = strings.Repeat("x", 300)
	card, err := email.BuildCard(it, nil, founder)
	require.NoError(t, err)
	p := parseCard(card.Title)
	assert.Equal(t, it.EmailSubject, p.rows["Subject"])
	assert.Less(t, len(p.lead), 200)
}

func TestBuildCard_RefusesWhatItCannotShowFaithfully(t *testing.T) {
	for name, mutate := range map[string]func(*gtm.Item){
		"not an email":          func(it *gtm.Item) { it.Format = gtm.FormatLongPost },
		"media pitch":           func(it *gtm.Item) { it.Format = gtm.FormatMediaPitch },
		"no recipients":         func(it *gtm.Item) { it.EmailTo = nil },
		"no subject":            func(it *gtm.Item) { it.EmailSubject = " " },
		"no body":               func(it *gtm.Item) { it.BodyMD = "\n" },
		"subject line break":    func(it *gtm.Item) { it.EmailSubject = "Launch\nBcc: eve@example.com" },
		"subject control":       func(it *gtm.Item) { it.EmailSubject = "Launch\x00" },
		"recipient with space":  func(it *gtm.Item) { it.EmailTo = []string{"ann@example.com bo@example.com"} },
		"recipient with comma":  func(it *gtm.Item) { it.EmailTo = []string{"ann@example.com,bo@example.com"} },
		"recipient bidi":        func(it *gtm.Item) { it.EmailTo = []string{"ann\u202e@example.com"} },
		"recipient zero width":  func(it *gtm.Item) { it.EmailTo = []string{"ann\u200b@example.com"} },
		"recipient IDN":         func(it *gtm.Item) { it.EmailTo = []string{"ann@exämple.com"} },
		"recipient homoglyph":   func(it *gtm.Item) { it.EmailTo = []string{"\u0430nn@example.com"} },
		"recipient display":     func(it *gtm.Item) { it.EmailTo = []string{"Ann <ann@example.com>"} },
		"subject bidi":          func(it *gtm.Item) { it.EmailSubject = "Launch \u202eweek" },
		"subject zero width":    func(it *gtm.Item) { it.EmailSubject = "Launch\u200bweek" },
		"subject BOM":           func(it *gtm.Item) { it.EmailSubject = "\ufeffLaunch" },
		"subject separator":     func(it *gtm.Item) { it.EmailSubject = "Launch\u2028week" },
		"subject open quote":    func(it *gtm.Item) { it.EmailSubject = "Launch “week" },
		"subject blank":         func(it *gtm.Item) { it.EmailSubject = " \t " },
		"subject Hangul filler": func(it *gtm.Item) { it.EmailSubject = "\u3164" },
		"subject braille blank": func(it *gtm.Item) { it.EmailSubject = " \u2800 \u2800" },
		"subject jamo fillers":  func(it *gtm.Item) { it.EmailSubject = "\u115F\u1160" },
		"subject CGJ":           func(it *gtm.Item) { it.EmailSubject = "\u034F\u034F" },
		"subject halfwidth":     func(it *gtm.Item) { it.EmailSubject = "\uFFA0" },
		"subject ideographic":   func(it *gtm.Item) { it.EmailSubject = "\u3000\u3164" },
		"subject variation":     func(it *gtm.Item) { it.EmailSubject = "\uFE0F" },
		"subject close quote":   func(it *gtm.Item) { it.EmailSubject = "Launch” to everyone? “x" },
	} {
		t.Run(name, func(t *testing.T) {
			it := approvedEmail()
			mutate(&it)
			_, err := email.BuildCard(it, nil, founder)
			require.Error(t, err)
		})
	}
	// Non-ASCII subject text is fine (the domain allows it), and so is a
	// straight quote.
	it := approvedEmail()
	it.EmailSubject = `Lancement de l'été: "v2"`
	_, err := email.BuildCard(it, nil, founder)
	require.NoError(t, err)
	it.EmailSubject = "\u3164 Launch"
	// A filler next to visible text is the visible text's subject.
	_, err = email.BuildCard(it, nil, founder)
	require.NoError(t, err)
	// A sender that is not a bare ASCII address is no card.
	_, err = email.BuildCard(approvedEmail(), nil, email.Account{ConnectionID: "c", Address: "founder\u200b@example.com"})
	require.ErrorIs(t, err, email.ErrNoGmailAccount)
	_, err = email.BuildCard(approvedEmail(), nil, email.Account{ConnectionID: "c"})
	require.ErrorIs(t, err, email.ErrNoGmailAccount)
}
