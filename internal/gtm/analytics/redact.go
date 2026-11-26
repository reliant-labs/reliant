// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"net"
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
	uuidShape     = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	longHex       = regexp.MustCompile(`(?i)[0-9a-f]{24,}`)
	// tokenCandidate is a run of base64 / base64url characters (and the dots
	// of a JWT) at least 24 long; it is a token when it mixes upper case,
	// lower case and digits.
	tokenCandidate = regexp.MustCompile(`[A-Za-z0-9+/_=.-]{24,}`)
	// phoneCandidate is a run of digits with phone punctuation; its digit
	// count decides.
	phoneCandidate = regexp.MustCompile(`\+?\(?\d[\d\s().-]{5,}\d`)
)

// redact is v, or Redacted when v carries something shaped like an email, an
// IPv4 or IPv6 address, a UUID, a long hex or base64 token, or a phone
// number.
func redact(v string) string {
	if identifierValue(v) {
		return Redacted
	}
	return v
}

// identifierValue reports whether v carries an identifier-shaped run.
func identifierValue(v string) bool {
	if emailShape.MatchString(v) || ipv4Shape.MatchString(v) || uuidShape.MatchString(v) || longHex.MatchString(v) {
		return true
	}
	for _, c := range ipv6Candidate.FindAllString(v, -1) {
		if ip := net.ParseIP(strings.Trim(c, ".")); ip != nil && ip.To4() == nil {
			return true
		}
	}
	for _, c := range tokenCandidate.FindAllString(v, -1) {
		if mixedCaseAndDigits(c) {
			return true
		}
	}
	// Dates and clock times are digits with phone punctuation too: drop them
	// before looking for a number.
	for _, c := range phoneCandidate.FindAllString(dateOrTime.ReplaceAllString(v, " "), -1) {
		if phoneShaped(c) {
			return true
		}
	}
	return false
}

// dateOrTime is a calendar date (2026-11-26, 2026.11.26) or a clock time.
var dateOrTime = regexp.MustCompile(`\d{4}[-./]\d{1,2}[-./]\d{1,2}|\d{1,2}:\d{2}(?::\d{2})?`)

// dottedPhone is the one phone shape written with dots only, 415.555.2671;
// any other run of dotted digits is a version (131.0.6778.86).
var dottedPhone = regexp.MustCompile(`^\(?\d{3}\)?\.\d{3}\.\d{4}$`)

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
