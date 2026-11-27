// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"regexp"
	"strings"
)

// Token detection: whether a value carries a key, a secret or a random id.
// The specific detectors run first (known secret prefixes, prefixed ids,
// base32, long base64); then every run of token characters is cut into
// pieces and each piece is judged by its alphabet (pieceIsToken). The rule
// that keeps campaign and product names (Q4EnterpriseLaunch2026,
// spring2026newsletter) is that they are made of words; it never outranks a
// detector, and where the two conflict the detector wins (a name that reads
// Redacted is acceptable, a leaked key is not). TestTokenRecall measures the
// misses per token shape; acceptedFalsePositives lists what that costs.

var (
	// secretPrefix is the known secret and key formats.
	secretPrefix = regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}` +
		`|gh[opsu]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}` +
		`|\b[srp]k_(?:live|test)_[A-Za-z0-9]{8,}` +
		`|xox[abpr]-[A-Za-z0-9-]{8,}` +
		`|AIza[0-9A-Za-z_-]{20,}` +
		`|glpat-[A-Za-z0-9_-]{16,}` +
		`|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}` +
		`|-----BEGIN`)
	// prefixedID is a typed id, a short lowercase type and an id body:
	// cus_…, acct_…, org_…. The body must have a digit or mixed case.
	prefixedID = regexp.MustCompile(`\b[a-z]{2,8}_([A-Za-z0-9]{10,})\b`)
	// base32Run is RFC 4648 base32; a run with a digit, or that switches
	// between letters and digits 3 or more times, is a token.
	base32Run = regexp.MustCompile(`[A-Z2-7]{16,}`)
	// base64Run is a long run of base64 (an AWS secret key, 40): with mixed
	// case and a digit it is a token.
	base64Run = regexp.MustCompile(`[A-Za-z0-9+/]{32,}`)
	// tokenRun is a run of token characters; pieceIsToken judges its pieces.
	tokenRun = regexp.MustCompile(`[A-Za-z0-9+=_-]{16,}`)
)

const (
	// minTokenLen is the shortest piece judged as a possible token.
	minTokenLen = 16
	// wholeTokenLen is the run length from which a run's pieces are also
	// judged together: a nanoid (21) has - and _ in its alphabet.
	wholeTokenLen = 21
	// minLetterDigitSwitches is how often a piece switches between letters
	// and digits to be a token; a name with a quarter, a version or a year
	// (Q4EnterpriseLaunch2026) switches three times at most.
	minLetterDigitSwitches = 4
	// minShortLetterDigitSwitches is the same for a piece shorter than
	// wholeTokenLen, where a token has fewer characters to switch in. It
	// takes MacBookPro16inch2023 (an accepted false positive).
	minShortLetterDigitSwitches = 3
	// minDigitRuns is how many separate runs of digits a lowercase piece has
	// to be a token (k3j9xq2m…); a name has one, its year.
	minDigitRuns = 3
	// minClassSwitchRate is the share of neighbouring characters that change
	// class (upper, lower, digit) in a random mixed piece: about 0.6 in a
	// random token, 0.2 to 0.4 in CamelCase.
	minClassSwitchRate = 0.5
	// minCaseSwitchRate is the share of neighbouring letters that change case
	// in a mixed-case piece without digits for it to be a token.
	minCaseSwitchRate = 0.45
	// minEnglishScore is the mean letter-pair score (bigramScore) under which
	// a piece's letters are random. About 1% of names built of English words
	// score under it, and about 1% of random runs of 10 letters over it.
	minEnglishScore = 0.25
	// minJudgedBigrams is how many letter pairs a piece needs before
	// randomLetters reads it.
	minJudgedBigrams = 6
)

// hasSecretOrPrefixedID reports whether v carries a known secret format or a
// prefixed id.
func hasSecretOrPrefixedID(v string) bool {
	if secretPrefix.MatchString(v) {
		return true
	}
	for _, m := range prefixedID.FindAllStringSubmatch(v, -1) {
		if body := m[1]; strings.ContainsAny(body, "0123456789") || hasUpper(body) && hasLower(body) {
			return true
		}
	}
	return false
}

// hasToken reports whether v carries a base32, base64 or other random token.
func hasToken(v string) bool {
	for _, r := range base32Run.FindAllString(v, -1) {
		if strings.ContainsAny(r, "234567") || letterDigitSwitches(r) >= 3 {
			return true
		}
	}
	for _, r := range base64Run.FindAllString(v, -1) {
		if mixedCaseAndDigits(r) {
			return true
		}
	}
	for _, run := range tokenRun.FindAllString(v, -1) {
		pieces := strings.FieldsFunc(run, func(r rune) bool { return strings.ContainsRune("-_+=", r) })
		for _, p := range pieces {
			if len(p) >= minTokenLen && pieceIsToken(p, false) {
				return true
			}
		}
		if len(run) >= wholeTokenLen && len(pieces) > 1 && pieceIsToken(strings.Join(pieces, ""), true) {
			return true
		}
	}
	return false
}

// pieceIsToken judges a run of letters and digits by its alphabet. joined
// is a run's pieces put together (a nanoid split by - and _), whose digit
// groups the separators already set apart, so the digit-pattern rules do not
// apply to it.
//
//   - Upper case and digits (ULID, Crockford, base32): a token with a digit.
//   - Letters of one case only: a token when it does not read as English
//     (randomLetters).
//   - Lower case and digits (cuid, hex): a token when it switches between
//     letters and digits often, has several digit runs, or its letters do
//     not read as English.
//   - Mixed case: a token when its classes change at random density, it
//     switches between letters and digits often, (without digits) its case
//     changes often, or its letters do not read as English.
func pieceIsToken(p string, joined bool) bool {
	upper, lower, digit := hasUpper(p), hasLower(p), strings.ContainsAny(p, "0123456789")
	switches := minLetterDigitSwitches
	if len(p) < wholeTokenLen {
		switches = minShortLetterDigitSwitches
	}
	digitPattern := !joined && letterDigitSwitches(p) >= switches
	switch {
	case !upper && !lower:
		return false // digits only: the numeric-id rules decide
	case !lower:
		return digit || randomLetters(p)
	case !upper && !digit:
		return randomLetters(p)
	case !upper:
		return digitPattern || !joined && digitRuns(p) >= minDigitRuns || randomLetters(p)
	}
	return digitPattern || classSwitchRate(p) >= minClassSwitchRate || randomLetters(p) ||
		!digit && caseSwitchRate(p) >= minCaseSwitchRate
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

// digitRuns counts the separate runs of digits in s.
func digitRuns(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) && (i == 0 || !isDigit(s[i-1])) {
			n++
		}
	}
	return n
}

// classSwitchRate is the share of neighbouring characters of s that change
// class (upper, lower, digit).
func classSwitchRate(s string) float64 {
	if len(s) < 2 {
		return 0
	}
	n := 0
	for i := 1; i < len(s); i++ {
		if charClass(s[i]) != charClass(s[i-1]) {
			n++
		}
	}
	return float64(n) / float64(len(s)-1)
}

// caseSwitchRate is the share of neighbouring letters of s that change case.
func caseSwitchRate(s string) float64 {
	pairs, n := 0, 0
	for i := 1; i < len(s); i++ {
		if isLetter(s[i]) && isLetter(s[i-1]) {
			pairs++
			if isUpperByte(s[i]) != isUpperByte(s[i-1]) {
				n++
			}
		}
	}
	if pairs == 0 {
		return 0
	}
	return float64(n) / float64(pairs)
}

// randomLetters reports whether s's letters do not read as English: the
// mean log-likelihood ratio of its neighbouring letter pairs (case folded,
// not across a digit), English over uniformly random letters, is under
// minEnglishScore. Words and names built of them score about 0.5 to 1.8;
// random letters about -1.2. A piece with fewer than minJudgedBigrams pairs
// is not judged.
func randomLetters(s string) bool {
	pairs, sum := 0, 0.0
	for i := 1; i < len(s); i++ {
		a, b := s[i-1], s[i]
		if !isLetter(a) || !isLetter(b) {
			continue
		}
		pairs++
		sum += bigramScore(a|0x20, b|0x20)
	}
	return pairs >= minJudgedBigrams && sum/float64(pairs) < minEnglishScore
}

// bigramScore is ln(P(ab) * 676) for the English letter pair ab, quantized
// to steps of 0.5 in [-6, 3.5]: positive for pairs English uses more than
// chance (th, in, er), negative for pairs it avoids (qx, jz).
func bigramScore(a, b byte) float64 {
	return float64(englishBigrams[int(a-'a')*26+int(b-'a')]-'a')*0.5 - 6
}

// englishBigrams holds bigramScore's levels, 'a' (-6) to 't' (3.5), one row
// per first letter. It is derived from Peter Norvig's letter-pair counts over
// the Google Books corpus (norvig.com/ngrams/count_2l.txt).
const englishBigrams = "" +
	"jnooilnjnhlqorhnhqpqmnkjnj" + // a
	"nihgoeefmicnifngalkhmeecmc" + // b
	"pglipgfondnmhhphfmkomffcke" + // c
	"olklpkjjohgkkjnkemnmmkkdkd" + // d
	"pmpqonmlnijpoqnnkrrplnnnmg" + // e
	"mhjhnmhhneekigoicnjnlfhdjb" + // f
	"nijioijmmefkilmicmlllficjd" + // g
	"phiirhghoeeijjoieljmkfibje" + // h
	"nlpnomnhigkonrpmhnpqinhidk" + // i
	"kcdckbabgbdbcbkeaddbkbbaaa" + // j
	"khhgnhgimeeihjjhbhljhdgbhb" + // k
	"pkkmpkiipfipkioldjnmmjidnd" + // l
	"pmihphghneeimhonbhmjlfhdkb" + // m
	"okoqpmpjoillklokfjoqlkkflg" + // n
	"mmnnkpmkliloprnndqnopmnikg" + // o
	"ogiiohglmcenkgomdolllegdjb" + // p
	"dacaabaadaaebaabacddmabaaa" + // q
	"pknnrkljpgllnmplfmoomlkenf" + // r
	"olnkqljophkllkonhkoqnilfle" + // s
	"pklkqkirqgglljqkeoonnimgng" + // t
	"mlmlmilgleinmohmcpooeghgif" + // u
	"mefhpcedoaadddkeaegeecdbea" + // v
	"nghgngemofehhkmhajljfejdhd" + // w
	"jfieifdfjabegdgkaegkgeegfa" + // x
	"ljkjljiikgfjkjnldkmlhgjcge" + // y
	"icddkbbdiabeccicaddcfacaeg" // z

func isLetter(c byte) bool    { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isUpperByte(c byte) bool { return c >= 'A' && c <= 'Z' }
func isDigit(c byte) bool     { return c >= '0' && c <= '9' }

func hasUpper(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 'A' && r <= 'Z' })
}
func hasLower(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r >= 'a' && r <= 'z' })
}

func charClass(c byte) int {
	switch {
	case isDigit(c):
		return 0
	case isUpperByte(c):
		return 1
	}
	return 2
}

func mixedCaseAndDigits(s string) bool {
	return hasUpper(s) && hasLower(s) && strings.ContainsAny(s, "0123456789")
}
