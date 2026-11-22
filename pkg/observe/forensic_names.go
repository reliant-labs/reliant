// File: forensic_names.go — the name rules forensics redacts by: which
// parameter and proto field names hold an identifier (recorded by value) and
// which name a secret (never recorded by value, whatever the type).
//
// The browser runtime applies the same rules to the same names
// (web-runtime/src/rpc-forensics.ts, isIdShaped / isSecretNamed). Both sides
// test against one table, testdata/forensic_secret_names.json, so they cannot
// drift apart.
package observe

import (
	"regexp"
	"strings"
)

// forensicSensitive are name fragments that mark a secret wherever they
// appear in the name, matched after lowercasing and removing `_` and `-`: a
// session or token "ID" is often the credential itself.
var forensicSensitive = []string{
	"token", "secret", "passw", "credential", "session", "cookie",
	"apikey", "privatekey", "signingkey", "otp",
}

// forensicSecretWords are secrets too short to match as fragments — `pin`
// would match `spinner` and `shipping`, `tan` would match `tank` — so they
// match only a whole word of the name (forensicNameWords), or two adjacent
// words joined (`security_code`, `passCode`), optionally plural (`pins`).
var forensicSecretWords = map[string]bool{
	"pin": true, "otp": true, "cvv": true, "cvc": true, "ssn": true, "tan": true,
	"passcode": true, "securitycode": true, "mfa": true, "totp": true,
}

// ForensicSecretName reports whether a parameter or proto field named name
// holds a secret: its value is never recorded. A string or bytes value
// records its length, a number its digit count ("[len=4]") unless its name
// says it is a quantity (ForensicSecretNumberName); a bool and an enum keep
// their value, being one of a few the schema lists.
//
// A name is secret when, lowercased with `_` and `-` removed, it contains
// token, secret, passw, credential, session, cookie, apikey, privatekey,
// signingkey or otp; or when one of its words (split on `_`, `-`, camelCase
// and letter/digit boundaries), or two adjacent words joined, is pin, otp,
// cvv, cvc, ssn, tan, passcode, securitycode, mfa or totp, or one of those
// plus "s". So `pin`, `userPIN`, `cvv2`, `security_code` and `SSNs` are
// secret; `spinner`, `pinned`, `shipping` and `tank` are not.
func ForensicSecretName(name string) bool {
	if sensitiveName(name) {
		return true
	}
	words := forensicNameWords(name)
	for i, w := range words {
		if secretWord(w) || i+1 < len(words) && secretWord(w+words[i+1]) {
			return true
		}
	}
	return false
}

// forensicQuantityWords name a quantity, not a secret: a number whose first
// or last word is one of them (`max_tokens`, `session_count`) records its
// value even though its name is secret.
var forensicQuantityWords = map[string]bool{
	"max": true, "min": true, "limit": true, "count": true,
	"total": true, "size": true, "len": true, "length": true,
}

// forensicLeadingQuantityWord is a quantity only as the FIRST word
// (`num_sessions`): last, it reads as "number" — `pin_num`, `ssn_num` and
// `otpNum` are the secret itself.
const forensicLeadingQuantityWord = "num"

// ForensicSecretNumberName reports whether a NUMBER named name hides its
// value: a secret name (ForensicSecretName) that is ID-shaped, or whose first
// and last words are not quantity words (max, min, limit, count, total, size,
// len, length, and num as the first word only). So `pin`, `pin_num`,
// `otp_counter_id` and `max_session_id` record their digit count, and
// `max_tokens`, `token_limit`, `session_count` and `num_sessions` their
// value. An ID-shaped secret name is never a quantity: whatever word leads
// it, a session or OTP ID is the credential itself. ID-shaped here also
// means the shape ForensicIDName has always matched (`max_SESSIONID`), so no
// name that was hidden before starts recording its value. The quantity exemption
// is for numbers only: a string or bytes value under any secret name records
// its length.
func ForensicSecretNumberName(name string) bool {
	if !ForensicSecretName(name) {
		return false
	}
	// legacyIDShapedName too: `max_SESSIONID` and `maxTOKENId` were
	// ID-shaped secrets before idShapedName narrowed the shape, and a
	// credential that was hidden must stay hidden.
	if idShapedName(name) || legacyIDShapedName(name) {
		return true
	}
	words := forensicNameWords(name)
	if len(words) == 0 {
		return true
	}
	first, last := words[0], words[len(words)-1]
	return !forensicQuantityWords[first] && first != forensicLeadingQuantityWord && !forensicQuantityWords[last]
}

// ForensicIDValueName reports whether a parameter or proto field named name
// holds an identifier whose value forensics may record: id / ids / uuid /
// uuids, a `_id` / `_ids` / `_uuid` / `_uuids` suffix (any case), or an `Id`
// / `ID` / `Ids` / `IDs` / `Uuid` / `UUID` / `Uuids` / `UUIDs` suffix after a
// lower-case letter or digit — unless the name is also secret
// (ForensicSecretName: `session_id`, `pin_id`), which records the length
// only. The generator, the proto walk and the browser runtime share this one
// rule.
func ForensicIDValueName(name string) bool {
	return idShapedName(name) && !ForensicSecretName(name)
}

// ForensicIDName reports whether a parameter or proto field named name holds
// an identifier whose value forensics may record: id / ids, a `_id` / `_ids`
// suffix, or an `ID` / `Id` / `IDs` / `Ids` suffix — unless the name also
// mentions a credential (token, secret, password, session, cookie, api key,
// …), which records the length only (ForensicSecretIDName).
//
// Deprecated: use ForensicIDValueName, the rule forensics records by. This
// keeps its original matching (`ORDERID` and `VOID` are ID-shaped here, not
// there; `pin_id` is recorded here, not there) for callers that pinned it.
func ForensicIDName(name string) bool {
	return legacyIDShapedName(name) && !legacySensitiveName(name)
}

// ForensicSecretIDName reports whether name is ID-shaped but mentions a
// credential (`otp_counter_id`, `sessionID`): its value is never recorded,
// whatever its type — a string records its length, a number its digit count
// ("[len=9]").
//
// Deprecated: forensics hides every secret name's value, ID-shaped or not:
// use ForensicSecretName (and ForensicSecretNumberName for a number). This
// keeps its original matching for callers that pinned it.
func ForensicSecretIDName(name string) bool {
	return legacyIDShapedName(name) && legacySensitiveName(name)
}

// legacySensitiveName is the credential match ForensicIDName and
// ForensicSecretIDName have always used: the fragments, with only `_`
// removed.
func legacySensitiveName(name string) bool {
	folded := strings.ReplaceAll(strings.ToLower(name), "_", "")
	for _, s := range forensicSensitive {
		if strings.Contains(folded, s) {
			return true
		}
	}
	return false
}

// legacyIDShapedName is the ID shape ForensicIDName and ForensicSecretIDName
// have always matched, kept so neither changes meaning within v1.
func legacyIDShapedName(name string) bool {
	switch name {
	case "id", "ids", "ID", "IDs", "Id", "Ids":
		return true
	}
	for _, suffix := range []string{"_id", "_ids", "ID", "Id", "IDs", "Ids"} {
		if strings.HasSuffix(name, suffix) && len(name) > len(suffix) {
			return true
		}
	}
	return false
}

func sensitiveName(name string) bool {
	folded := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(name))
	for _, s := range forensicSensitive {
		if strings.Contains(folded, s) {
			return true
		}
	}
	return false
}

func secretWord(w string) bool {
	return forensicSecretWords[w] || strings.HasSuffix(w, "s") && forensicSecretWords[w[:len(w)-1]]
}

// forensicNameWords splits name into lowercase words at `_` and `-`, at a
// lower-to-upper change (`userPin` → user, pin), at the end of an upper-case
// run followed by a word (`PINCode` → pin, code, but `SSNs` stays one word),
// and between letters and digits (`cvv2` → cvv, 2). Only ASCII decides a
// boundary; any other character belongs to the word around it.
func forensicNameWords(name string) []string {
	var words []string
	start := -1
	flush := func(end int) {
		if start >= 0 && end > start {
			words = append(words, asciiLower(name[start:end]))
		}
		start = -1
	}
	at := func(i int) byte {
		if i < len(name) {
			return name[i]
		}
		return 0
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c == '-' {
			flush(i)
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		p, next := name[i-1], at(i+1)
		switch {
		case isLowerASCII(p) && isUpperASCII(c),
			isDigitASCII(p) != isDigitASCII(c),
			isUpperASCII(p) && isUpperASCII(c) && isLowerASCII(next) && (next != 's' || isLowerASCII(at(i+2))):
			flush(i)
			start = i
		}
	}
	flush(len(name))
	return words
}

func isLowerASCII(c byte) bool { return c >= 'a' && c <= 'z' }
func isUpperASCII(c byte) bool { return c >= 'A' && c <= 'Z' }
func isDigitASCII(c byte) bool { return c >= '0' && c <= '9' }

func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if isUpperASCII(c) {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// The ID shapes, spelled exactly as the browser runtime's SNAKE_ID_RE and
// CAMEL_ID_RE (no case-folding flag, so both engines read them alike):
// `id`, `ids`, `uuid`, `uuids` alone or after `_`, in any case; and an
// upper-case-led `Id` / `ID` / `Uuid` / `UUID` (plural or not) after a
// lower-case letter or digit (`orderID`, `accountUuid`).
var (
	snakeIDName = regexp.MustCompile(`(^|_)([uU][uU])?[iI][dD][sS]?$`)
	camelIDName = regexp.MustCompile(`[a-z0-9](Id|ID|Ids|IDs|Uuid|UUID|Uuids|UUIDs)$`)
)

func idShapedName(name string) bool {
	return snakeIDName.MatchString(name) || camelIDName.MatchString(name)
}
