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
const Redacted = "(redacted)"

var (
	emailShape = regexp.MustCompile(`[^\s@]+@[^\s@]+\.[^\s@]+`)
	// ipv4Candidate is four dotted numbers; hasIPv4 decides.
	ipv4Candidate = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}`)
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
	// tokenRun is a run of token characters at least 16 long; tokenShaped
	// decides whether it is a token or a name like Q4EnterpriseLaunch2026.
	tokenRun = regexp.MustCompile(`[A-Za-z0-9+=_-]{16,}`)
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

// Shape thresholds.
const (
	// minTokenLen is the shortest piece taken for a token.
	minTokenLen = 16
	// wholeTokenLen is the run length from which a run's pieces are also
	// judged together: a nanoid (21) has - and _ in its alphabet.
	wholeTokenLen = 21
	// minLetterDigitSwitches is how often a token switches between letters
	// and digits (01ARZ3NDEKTSV4RR..., cjld2cjxh0000qzrmn...). A name with a
	// quarter, a version or a year in it (Q4EnterpriseLaunch2026,
	// MacBookPro16inch2023) switches three times at most.
	minLetterDigitSwitches = 4
	// minClassSwitchRate is the share of neighbouring characters that change
	// class (upper, lower, digit) for a mixed-case piece to be random: about
	// 0.6 in a random token, about 0.2 in CamelCase (Windows10Enterprise).
	minClassSwitchRate = 0.5
	// minBareIDDigits is the shortest bare number taken for an id.
	minBareIDDigits = 8
	// minPathIDDigits is the shortest numeric path segment taken for an id
	// (/orders/4821309), above page numbers, years and resolutions
	// (/pricing/5000, /archive/2100, /1080).
	minPathIDDigits = 6
)

// redact is v, or Redacted when v, or v URL-decoded, carries something shaped
// like an email, an IPv4, IPv6 or MAC address, a UUID, a token (long hex,
// base64, ULID, cuid, nanoid, an API key), an analytics client id, a numeric
// id or a phone number.
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
	if longNumber.MatchString(rest) {
		return true
	}
	for _, run := range tokenRun.FindAllString(rest, -1) {
		if tokenShaped(run) {
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

// hasIPv4 reports whether v carries an IPv4 host address: four dotted
// numbers each at most 255, not part of a version (after a v, or followed by
// more dotted parts), and not ending in .0 (a network, or a version like
// 4.0.0.0, not a host).
func hasIPv4(v string) bool {
	for _, m := range ipv4Candidate.FindAllStringIndex(v, -1) {
		i, j := m[0], m[1]
		if i > 0 && strings.IndexByte("0123456789.vV", v[i-1]) >= 0 {
			continue
		}
		if j < len(v) && (isDigit(v[j]) || v[j] == '.' && j+1 < len(v) && isDigit(v[j+1])) {
			continue
		}
		octets := strings.Split(v[i:j], ".")
		host := octets[3] != "0"
		for _, o := range octets {
			if n, err := strconv.Atoi(o); err != nil || n > 255 {
				host = false
			}
		}
		if host {
			return true
		}
	}
	return false
}

// tokenShaped reports whether a tokenRun is a token. Each piece between
// separators (- _ + =) of at least minTokenLen is judged on its own, and a
// run of wholeTokenLen or more is also judged whole (a nanoid). A piece is a
// token when it switches between letters and digits minLetterDigitSwitches
// times, or is mixedRandom. Base64 without digits is never one: k-anonymity
// covers it.
func tokenShaped(run string) bool {
	pieces := strings.FieldsFunc(run, func(r rune) bool { return strings.ContainsRune("-_+=", r) })
	for _, p := range pieces {
		if len(p) >= minTokenLen && (letterDigitSwitches(p) >= minLetterDigitSwitches || mixedRandom(p)) {
			return true
		}
	}
	if len(run) >= wholeTokenLen && len(pieces) > 1 {
		return mixedRandom(strings.Join(pieces, ""))
	}
	return false
}

// letterDigitSwitches counts the neighbouring characters of s of which one
// is a digit and the other is not.
func letterDigitSwitches(s string) int {
	n := 0
	for i := 1; i < len(s); i++ {
		if isDigit(s[i]) != isDigit(s[i-1]) {
			n++
		}
	}
	return n
}

// mixedRandom reports whether s has upper case, lower case and digits and
// changes class at minClassSwitchRate of its neighbouring characters.
func mixedRandom(s string) bool {
	if len(s) < 2 || !mixedCaseAndDigits(s) {
		return false
	}
	switches := 0
	for i := 1; i < len(s); i++ {
		if charClass(s[i]) != charClass(s[i-1]) {
			switches++
		}
	}
	return float64(switches)/float64(len(s)-1) >= minClassSwitchRate
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func charClass(c byte) int {
	switch {
	case isDigit(c):
		return 0
	case c >= 'A' && c <= 'Z':
		return 1
	}
	return 2
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
