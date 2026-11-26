// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Redacted is what a returned group value shaped like an identifier is
// replaced with. The property-name guard (identifierProperty) keeps
// identifier properties out of a breakdown; this is the value-side guard for
// what a harmless-sounding property, or an event name, carries anyway.
const Redacted = "(redacted)"

var (
	emailShape = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
	ipv4Shape  = regexp.MustCompile(`(?:^|[^0-9.])(?:\d{1,3}\.){3}\d{1,3}(?:$|[^0-9.])`)
	// ipv6Candidate is a run that may be an IPv6 address; net.ParseIP
	// decides.
	ipv6Candidate = regexp.MustCompile(`[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*`)
	// macShape is a MAC address: six colon- or dash-separated pairs, or
	// three dotted quads.
	macShape  = regexp.MustCompile(`(?i)(?:^|[^0-9a-f:-])(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}(?:$|[^0-9a-f:-])|(?:^|[^0-9a-f.])[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}(?:$|[^0-9a-f.])`)
	uuidShape = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	longHex   = regexp.MustCompile(`(?i)[0-9a-f]{24,}`)
	// alnumToken is a run of 16 or more letters and digits with a digit in
	// it: a ULID, cuid, nanoid or API key body. A word, however long, has no
	// digit.
	alnumToken = regexp.MustCompile(`[A-Za-z0-9]{16,}`)
	// mixedToken is a run of base64 / base64url characters at least 16 long
	// (a JWT's segments, a nanoid, whose - and _ split it into short alnum
	// runs); it is a token when it mixes upper case, lower case and digits.
	// / and . are left out, so a path or a version is never one run.
	mixedToken = regexp.MustCompile(`[A-Za-z0-9+_=-]{16,}`)
	// longNumber is 12 or more digits, not the fraction of a decimal: a
	// numeric id.
	longNumber = regexp.MustCompile(`(?:^|[^0-9.])\d{12,}`)
	// phoneCandidate is a run of digits with phone punctuation; its digit
	// count decides.
	phoneCandidate = regexp.MustCompile(`\+?\(?\d[\d\s().-]{5,}\d`)
	// dateOrTime is a calendar date (2026-11-26, 2026.11.26, 20261126T103000Z)
	// or a clock time, dropped before the digit checks so it is kept.
	dateOrTime = regexp.MustCompile(`\d{8}T\d{6}Z?|\d{4}[-./]\d{1,2}[-./]\d{1,2}|\d{1,2}:\d{2}(?::\d{2})?`)
	// dottedPhone is the one phone shape written with dots only, 415.555.2671;
	// any other run of dotted digits is a version (131.0.6778.86).
	dottedPhone = regexp.MustCompile(`^\(?\d{3}\)?\.\d{3}\.\d{4}$`)
	yearSegment = regexp.MustCompile(`^(?:19|20)\d{2}$`)
)

// redact is v, or Redacted when v, or v URL-decoded, carries something shaped
// like an email, an IPv4, IPv6 or MAC address, a UUID, a token (long hex,
// base64, ULID, cuid, nanoid, an API key), a numeric id or a phone number.
func redact(v string) string {
	for _, s := range decodings(v) {
		if identifierValue(s) {
			return Redacted
		}
	}
	return v
}

// decodings are v and its URL-decodings (up to three rounds, for a value
// encoded twice), each once.
func decodings(v string) []string {
	out := []string{v}
	for range 3 {
		d, err := url.QueryUnescape(out[len(out)-1])
		if err != nil || d == out[len(out)-1] {
			break
		}
		out = append(out, d)
	}
	return out
}

// identifierValue reports whether v carries an identifier-shaped run.
func identifierValue(v string) bool {
	if emailShape.MatchString(v) || ipv4Shape.MatchString(v) || macShape.MatchString(v) ||
		uuidShape.MatchString(v) || longHex.MatchString(v) {
		return true
	}
	for _, c := range ipv6Candidate.FindAllString(v, -1) {
		if ip := net.ParseIP(strings.Trim(c, ".")); ip != nil && ip.To4() == nil {
			return true
		}
	}
	if numericPathSegment(v) {
		return true
	}
	// Dates and clock times are digits with punctuation too: drop them
	// before the digit-shaped checks, so they are kept.
	rest := dateOrTime.ReplaceAllString(v, " ")
	if longNumber.MatchString(rest) {
		return true
	}
	for _, c := range alnumToken.FindAllString(rest, -1) {
		if strings.ContainsAny(c, "0123456789") {
			return true
		}
	}
	for _, c := range mixedToken.FindAllString(rest, -1) {
		if mixedCaseAndDigits(c) {
			return true
		}
	}
	for _, c := range phoneCandidate.FindAllString(rest, -1) {
		if phoneShaped(c) {
			return true
		}
	}
	return false
}

// numericPathSegment reports whether v's path (before any ? or #) has a
// segment of 4 or more digits that is not a year (/orders/48213, /u/1234;
// not /blog/2026/11).
func numericPathSegment(v string) bool {
	if !strings.Contains(v, "/") {
		return false
	}
	if i := strings.IndexAny(v, "?#"); i >= 0 {
		v = v[:i]
	}
	for _, seg := range strings.Split(v, "/") {
		if len(seg) >= 4 && allDigits(seg) && !yearSegment.MatchString(seg) {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// phoneShaped reports whether a phoneCandidate run is a phone number: a
// leading + and 8 to 15 digits, the dotted 3.3.4 shape, or 10 to 15 digits
// with other separators.
func phoneShaped(c string) bool {
	n := 0
	for _, r := range c {
		if unicode.IsDigit(r) {
			n++
		}
	}
	switch {
	case strings.HasPrefix(c, "+"):
		return n >= 8 && n <= 15
	case !strings.ContainsAny(c, " ()-"):
		return dottedPhone.MatchString(c) || (n >= 10 && n <= 15 && !strings.Contains(c, "."))
	}
	return n >= 10 && n <= 15
}

func mixedCaseAndDigits(s string) bool {
	var upper, lower, digit bool
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return upper && lower && digit
}
