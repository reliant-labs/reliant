// Copyright (c) 2025 Reliant Labs

package gmail

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// gmailAuthResults is the shape Gmail's MX writes, folded across lines and
// with comments, as it arrives in the API's payload.headers.
func gmailAuthResults(results string) string {
	return "mx.google.com;\r\n       " + results
}

const (
	dmarcPass = "dkim=pass header.i=@example.com header.s=sel1 header.b=AbCdEf;\r\n       " +
		"spf=pass (google.com: domain of boss@example.com designates 203.0.113.5 as permitted sender) smtp.mailfrom=boss@example.com;\r\n       " +
		"dmarc=pass (p=REJECT sp=REJECT dis=NONE) header.from=example.com"
	// The spoof: From says example.com, but the mail was sent and signed by
	// evil.test, so DMARC for example.com fails.
	spoofed = "dkim=pass header.i=@evil.test header.s=s1 header.b=x;\r\n       " +
		"spf=pass (google.com: domain of attacker@evil.test designates 198.51.100.7 as permitted sender) smtp.mailfrom=attacker@evil.test;\r\n       " +
		"dmarc=fail (p=NONE sp=NONE dis=NONE) header.from=example.com"
)

func headers(pairs ...string) []apiHeader {
	out := make([]apiHeader, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, apiHeader{Name: pairs[i], Value: pairs[i+1]})
	}
	return out
}

func TestEmailSenderIsVerifiedOnlyByGmailsAuthenticationResults(t *testing.T) {
	cases := []struct {
		name     string
		headers  []apiHeader
		want     core.TriggerSender
		verified bool
	}{
		{
			name:    "DMARC pass for the From domain",
			headers: headers("From", "The Boss <Boss@Example.com>", "Authentication-Results", gmailAuthResults(dmarcPass)),
			want:    core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", DisplayName: "The Boss", Verified: true},
		},
		{
			name:    "From matches but DMARC fails: the spoof",
			headers: headers("From", "The Boss <boss@example.com>", "Authentication-Results", gmailAuthResults(spoofed)),
			want:    core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", DisplayName: "The Boss", Verified: false},
		},
		{
			name: "DKIM pass from a subdomain of the From's organization, no DMARC record",
			headers: headers("From", "boss@example.com", "Authentication-Results",
				gmailAuthResults("dkim=pass header.i=@mail.example.com header.s=s header.b=x; spf=neutral smtp.mailfrom=bounce@other.test")),
			want: core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", Verified: true},
		},
		{
			name: "DKIM pass from an unaligned domain",
			headers: headers("From", "boss@example.com", "Authentication-Results",
				gmailAuthResults("dkim=pass header.i=@example.org header.s=s header.b=x; spf=pass smtp.mailfrom=boss@example.org")),
			want: core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", Verified: false},
		},
		{
			name:    "SPF alone authenticates the envelope, not the From",
			headers: headers("From", "boss@example.com", "Authentication-Results", gmailAuthResults("spf=pass smtp.mailfrom=boss@example.com")),
			want:    core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", Verified: false},
		},
		{
			name: "a pass that is only inside a comment",
			headers: headers("From", "boss@example.com", "Authentication-Results",
				gmailAuthResults("dmarc=fail (dmarc=pass header.from=example.com) header.from=example.com")),
			want: core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", Verified: false},
		},
		{
			name:    "results stamped by someone other than Gmail",
			headers: headers("From", "boss@example.com", "Authentication-Results", "mx.evil.test; "+dmarcPass),
			want:    core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", Verified: false},
		},
		{
			// Gmail's own header is first; the sender's forged one below it
			// is never read.
			name: "a forged pass below Gmail's real fail",
			headers: headers("Authentication-Results", gmailAuthResults(spoofed),
				"From", "boss@example.com",
				"Authentication-Results", gmailAuthResults(dmarcPass)),
			want: core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", Verified: false},
		},
		{
			name:    "no Authentication-Results at all",
			headers: headers("From", "boss@example.com"),
			want:    core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com", Verified: false},
		},
		{
			name:    "two From addresses",
			headers: headers("From", "boss@example.com, other@example.com", "Authentication-Results", gmailAuthResults(dmarcPass)),
			want:    core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.com, other@example.com", Verified: false},
		},
		{
			name: "a public suffix aligns with nothing",
			headers: headers("From", "boss@example.co.uk", "Authentication-Results",
				gmailAuthResults("dkim=pass header.d=co.uk header.s=s header.b=x")),
			want: core.TriggerSender{Kind: core.TriggerSenderKindEmail, ID: "boss@example.co.uk", Verified: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, *emailSender(tc.headers))
		})
	}
}
