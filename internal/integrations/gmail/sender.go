// Copyright (c) 2025 Reliant Labs

package gmail

import (
	"net/mail"
	"strings"

	"golang.org/x/net/publicsuffix"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// A new message's trigger.sender is its From address, and From alone proves
// nothing: anyone can write any From. What Gmail does attest is the
// Authentication-Results header its own MX (authserv-id mx.google.com) adds
// on receipt, so the sender is verified ONLY when that header shows
//
//   - dmarc=pass for the From domain (header.from), or
//   - dkim=pass with a signing domain (header.d, or header.i's domain)
//     aligned with the From domain: the same organizational domain, which
//     is DMARC's relaxed alignment (RFC 7489 §3.1.1).
//
// SPF is not enough: it authenticates the envelope sender, which need not be
// the From the recipient sees.
//
// Only the FIRST Authentication-Results header is read, and only when it is
// Gmail's. Gmail prepends its own above the message's original headers, so
// one further down was written by the sender or a relay, and a sender can
// write "dmarc=pass" as easily as a From (RFC 8601 §5).

// gmailAuthServID is the authserv-id Gmail's receiving MX stamps.
const gmailAuthServID = "mx.google.com"

// authResultsHeader is requested with the poller's metadata headers.
const authResultsHeader = "Authentication-Results"

// emailSender is the trigger.sender of a message with these headers, in the
// order Gmail returned them.
func emailSender(headers []apiHeader) *core.TriggerSender {
	var from, authResults string
	haveFrom, haveAuth := false, false
	for _, h := range headers {
		switch {
		case !haveFrom && strings.EqualFold(h.Name, "From"):
			from, haveFrom = h.Value, true
		case !haveAuth && strings.EqualFold(h.Name, authResultsHeader):
			authResults, haveAuth = h.Value, true
		}
	}
	sender := &core.TriggerSender{Kind: core.TriggerSenderKindEmail}
	addrs, err := mail.ParseAddressList(from)
	if err != nil || len(addrs) != 1 {
		// Unparsable, or several From addresses (which DMARC itself
		// refuses): record what was written, never as verified.
		sender.ID = strings.ToLower(strings.TrimSpace(decodeHeader(from)))
		return sender
	}
	sender.ID = strings.ToLower(addrs[0].Address)
	sender.DisplayName = addrs[0].Name
	_, domain, ok := strings.Cut(sender.ID, "@")
	if !ok || domain == "" || !haveAuth {
		return sender
	}
	sender.Verified = authenticatesFrom(authResults, domain)
	return sender
}

// authenticatesFrom reports whether an Authentication-Results value is
// Gmail's and shows DMARC pass for fromDomain, or a DKIM pass aligned with it.
func authenticatesFrom(value, fromDomain string) bool {
	segments := strings.Split(stripComments(value), ";")
	// The first segment is the authserv-id, optionally followed by a version.
	if id := strings.Fields(segments[0]); len(id) == 0 || !strings.EqualFold(id[0], gmailAuthServID) {
		return false
	}
	for _, segment := range segments[1:] {
		method, result, props := parseResult(segment)
		if result != "pass" {
			continue
		}
		switch method {
		case "dmarc":
			if strings.EqualFold(props["header.from"], fromDomain) {
				return true
			}
		case "dkim":
			signer := props["header.d"]
			if signer == "" {
				_, signer, _ = strings.Cut(props["header.i"], "@")
			}
			if aligned(signer, fromDomain) {
				return true
			}
		}
	}
	return false
}

// parseResult reads one resinfo: "method=result ptype.property=value ...".
// The method and result are lowercased; property values keep their case.
func parseResult(segment string) (method, result string, props map[string]string) {
	fields := strings.Fields(segment)
	if len(fields) == 0 {
		return "", "", nil
	}
	method, result, _ = strings.Cut(fields[0], "=")
	props = make(map[string]string, len(fields)-1)
	for _, field := range fields[1:] {
		if key, value, ok := strings.Cut(field, "="); ok {
			props[strings.ToLower(key)] = strings.Trim(value, `"`)
		}
	}
	return strings.ToLower(method), strings.ToLower(result), props
}

// aligned is DMARC's relaxed identifier alignment: the two domains share an
// organizational domain (the registrable domain under the public suffix
// list). A domain that is itself a public suffix aligns with nothing.
func aligned(signer, fromDomain string) bool {
	signer = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(signer), "."))
	fromDomain = strings.ToLower(strings.TrimSuffix(fromDomain, "."))
	if signer == "" || fromDomain == "" {
		return false
	}
	a, err := publicsuffix.EffectiveTLDPlusOne(signer)
	if err != nil {
		return false
	}
	b, err := publicsuffix.EffectiveTLDPlusOne(fromDomain)
	if err != nil {
		return false
	}
	return a == b
}

// stripComments removes RFC 5322 comments — parenthesized, possibly nested,
// with backslash escapes — and unfolds the header onto one line. A comment
// can hold any text, a ";" or an "x=pass" included, so it is gone before
// anything is split.
func stripComments(value string) string {
	var b strings.Builder
	depth, quoted, escaped := 0, false, false
	for _, r := range value {
		switch {
		case escaped:
			escaped = false
			if depth == 0 {
				b.WriteRune(r)
			}
			continue
		case r == '\\':
			escaped = true
			if depth == 0 {
				b.WriteRune(r)
			}
			continue
		case quoted:
			if r == '"' {
				quoted = false
			}
		case r == '"' && depth == 0:
			quoted = true
		case r == '(':
			depth++
			continue
		case r == ')' && depth > 0:
			depth--
			continue
		}
		if depth > 0 {
			continue
		}
		if r == '\r' || r == '\n' || r == '\t' {
			r = ' '
		}
		b.WriteRune(r)
	}
	return b.String()
}
