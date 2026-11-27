// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Redacted is what a returned group value shaped like an identifier is
// replaced with. The property-name guard (identifierProperty) keeps
// identifier properties out of a breakdown; this is the value-side guard for
// what a harmless-sounding property, or an event name, carries anyway.
// decode folds every redacted group of a result into one Redacted row.
//
// When catching an identifier and keeping an ordinary value conflict,
// catching wins: a campaign name that sometimes reads Redacted is
// acceptable, a leaked tenant id or key is not.
const Redacted = "(redacted)"

var (
	emailShape = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
	// ipv4Candidate is four dotted numbers; hasIPv4 decides.
	ipv4Candidate = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
	// ipv4Dashed is the dashed form cloud host names carry, ip-10-1-2-3.
	ipv4Dashed = regexp.MustCompile(`(?i)\bip-(\d{1,3})-(\d{1,3})-(\d{1,3})-(\d{1,3})\b`)
	// ipv6Candidate is a run that may be an IPv6 address; net.ParseIP
	// decides.
	ipv6Candidate = regexp.MustCompile(`[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*`)
	// macShape is a MAC address: six colon- or dash-separated pairs, or
	// three dotted quads.
	macShape  = regexp.MustCompile(`(?i)(?:^|[^0-9a-f:-])(?:[0-9a-f]{2}[:-]){5}[0-9a-f]{2}(?:$|[^0-9a-f:-])|(?:^|[^0-9a-f.])[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}(?:$|[^0-9a-f.])`)
	uuidShape = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	longHex   = regexp.MustCompile(`(?i)[0-9a-f]{24,}`)
	// gaClientID is a Google Analytics client id, GA1.2.<random>.<time>.
	gaClientID = regexp.MustCompile(`(?i)\bGA\d\.\d+\.\d+\.\d+`)
	// longNumber is 12 or more digits, not the fraction of a decimal: a
	// numeric id.
	longNumber = regexp.MustCompile(`(?:^|[^0-9.])\d{12,}`)
	// phoneCandidate is a run of digits with phone punctuation; its digit
	// count decides.
	phoneCandidate = regexp.MustCompile(`\+?\(?\d[\d\s().-]{5,}\d`)
	// dateOrTime is a calendar date (2026-11-26, 2026.11.26, 20261126T103000Z)
	// or a clock time, dropped before the digit checks so it is kept.
	dateOrTime = regexp.MustCompile(`\d{8}T\d{6}Z?|\d{4}[-./]\d{1,2}[-./]\d{1,2}|\d{1,2}:\d{2}(?::\d{2})?`)
	// compactDate is a bare YYYYMMDD date: 8 digits, but not an id.
	compactDate = regexp.MustCompile(`^(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])$`)
	// dottedPhone is the one phone shape written with dots only, 415.555.2671;
	// any other run of dotted digits is a version (131.0.6778.86).
	dottedPhone = regexp.MustCompile(`^\(?\d{3}\)?\.\d{3}\.\d{4}$`)
)

const (
	// minBareIDDigits is the shortest bare number taken for an id.
	minBareIDDigits = 8
	// minPathIDDigits is the shortest numeric path segment taken for an id
	// (/orders/4821309), above page numbers, years and resolutions
	// (/pricing/5000, /archive/2100, /1080).
	minPathIDDigits = 6
)

// redact is v, or Redacted when v, or v URL-decoded, carries something shaped
// like a secret or a token (hasToken), an email, an IPv4, IPv6 or MAC
// address, a UUID, an analytics client id, a numeric id or a phone number.
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
	if hasSecretOrPrefixedID(v) {
		return true
	}
	if emailShape.MatchString(v) || macShape.MatchString(v) || uuidShape.MatchString(v) ||
		longHex.MatchString(v) || gaClientID.MatchString(v) || hasIPv4(v) {
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
	if t := strings.TrimSpace(v); len(t) >= minBareIDDigits && allDigits(t) && !compactDate.MatchString(t) {
		return true
	}
	// Dates and clock times are digits with punctuation too: drop them
	// before the digit-shaped checks, so they are kept.
	rest := dateOrTime.ReplaceAllString(v, " ")
	if longNumber.MatchString(rest) || hasToken(rest) {
		return true
	}
	for _, c := range phoneCandidate.FindAllString(rest, -1) {
		if phoneShaped(c) {
			return true
		}
	}
	return false
}

// hasIPv4 reports whether v carries an IPv4 host address: four dotted
// numbers each at most 255, the first not 0, and not x.y.0.0 (a network, or
// a version like 4.0.0.0). It must not be part of a version: after a v or a
// digit, or followed by more dotted parts. After a dot it counts only when
// the label before the dot is a name (srv.10.1.2.3, not 1.10.1.2.3). The
// dashed host-name form, ip-10-1-2-3, counts too.
func hasIPv4(v string) bool {
	for _, m := range ipv4Candidate.FindAllStringIndex(v, -1) {
		i, j := m[0], m[1]
		if i > 0 {
			switch p := v[i-1]; {
			case isDigit(p), p == 'v', p == 'V':
				continue
			case p == '.' && (i < 2 || !isLetter(v[i-2])):
				continue
			}
		}
		if j < len(v) && (isDigit(v[j]) || v[j] == '.' && j+1 < len(v) && isDigit(v[j+1])) {
			continue
		}
		if hostOctets(strings.Split(v[i:j], ".")) {
			return true
		}
	}
	for _, m := range ipv4Dashed.FindAllStringSubmatch(v, -1) {
		if hostOctets(m[1:]) {
			return true
		}
	}
	return false
}

// hostOctets reports whether four octets are a host address: each at most
// 255, the first not 0, and not x.y.0.0.
func hostOctets(octets []string) bool {
	n := make([]int, 4)
	for k, o := range octets {
		x, err := strconv.Atoi(o)
		if err != nil || x > 255 {
			return false
		}
		n[k] = x
	}
	return n[0] != 0 && (n[2] != 0 || n[3] != 0)
}

// numericPathSegment reports whether v's path (before any ? or #) has a
// segment of minPathIDDigits or more digits.
func numericPathSegment(v string) bool {
	if !strings.Contains(v, "/") {
		return false
	}
	if i := strings.IndexAny(v, "?#"); i >= 0 {
		v = v[:i]
	}
	for _, seg := range strings.Split(v, "/") {
		if len(seg) >= minPathIDDigits && allDigits(seg) {
			return true
		}
	}
	return false
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
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
