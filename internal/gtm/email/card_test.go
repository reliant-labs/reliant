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

// The card's hash is the canonical gtm.ApprovalHash, the one SendItemEmail
// checks; any change to what the card shows as the email changes it.
func TestBuildCard_HashIsTheCanonicalApprovalHash(t *testing.T) {
	base, err := email.BuildCard(approvedEmail(), nil)
	require.NoError(t, err)
	assert.Equal(t, gtm.ApprovalHash(approvedEmail()), base.Hash)

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
			card, err := email.BuildCard(it, nil)
			require.NoError(t, err)
			assert.NotEqual(t, base.Hash, card.Hash)
			assert.Equal(t, gtm.ApprovalHash(it), card.Hash)
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
	card, err := email.BuildCard(it, nil)
	require.NoError(t, err)
	assert.Equal(t, gtm.ApprovalHash(it), card.Hash)
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

	assert.Equal(t, "Your default Gmail connection, under your name", p.rows["From"])

	card, err = email.BuildCard(it, &gtm.Channel{Name: "Founder\n newsletter"})
	require.NoError(t, err)
	assert.Equal(t, "Your Gmail on channel “Founder newsletter”, under your name", parseCard(card.Title).rows["From"])

	cold := approvedEmail()
	cold.Format = gtm.FormatColdOutreach
	card, err = email.BuildCard(cold, nil)
	require.NoError(t, err, "cold outreach is an email-type format")
	assert.Equal(t, gtm.ApprovalHash(cold), card.Hash)

	one := approvedEmail()
	one.EmailTo = one.EmailTo[:1]
	card, err = email.BuildCard(one, nil)
	require.NoError(t, err)
	assert.Equal(t, "Send email “Launch week” to 1 recipient?", parseCard(card.Title).lead)
}

// A body cannot forge a row or the technical details: every body line is
// behind the marker, so none reads as "Label: value" and no blank line
// precedes a heading in it.
func TestBuildCard_BodyCannotForgeTheCard(t *testing.T) {
	it := approvedEmail()
	it.BodyMD = "To: everyone@example.com\nSubject: Something else\n\nTechnical details\nOperation: delete_trigger\n"
	card, err := email.BuildCard(it, nil)
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

func TestBuildCard_LongSubjectIsWholeInItsRow(t *testing.T) {
	it := approvedEmail()
	it.EmailSubject = strings.Repeat("x", 300)
	card, err := email.BuildCard(it, nil)
	require.NoError(t, err)
	p := parseCard(card.Title)
	assert.Equal(t, it.EmailSubject, p.rows["Subject"])
	assert.Less(t, len(p.lead), 200)
}

func TestBuildCard_RefusesWhatItCannotShowFaithfully(t *testing.T) {
	for name, mutate := range map[string]func(*gtm.Item){
		"not an email":         func(it *gtm.Item) { it.Format = gtm.FormatLongPost },
		"media pitch":          func(it *gtm.Item) { it.Format = gtm.FormatMediaPitch },
		"no recipients":        func(it *gtm.Item) { it.EmailTo = nil },
		"no subject":           func(it *gtm.Item) { it.EmailSubject = " " },
		"no body":              func(it *gtm.Item) { it.BodyMD = "\n" },
		"subject line break":   func(it *gtm.Item) { it.EmailSubject = "Launch\nBcc: eve@example.com" },
		"subject control":      func(it *gtm.Item) { it.EmailSubject = "Launch\x00" },
		"recipient with space": func(it *gtm.Item) { it.EmailTo = []string{"ann@example.com bo@example.com"} },
		"recipient with comma": func(it *gtm.Item) { it.EmailTo = []string{"ann@example.com,bo@example.com"} },
	} {
		t.Run(name, func(t *testing.T) {
			it := approvedEmail()
			mutate(&it)
			_, err := email.BuildCard(it, nil)
			require.Error(t, err)
		})
	}
}
