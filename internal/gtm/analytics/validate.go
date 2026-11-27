// Copyright (c) 2026 Reliant Labs

package analytics

import (
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/reliant-labs/reliant/internal/gtm"
)

// MaxSQLWindow is the longest window a sql template reads: the control
// plane's range cap for sql panels (forge obsquery.MaxRangeDashboards).
// A funnel reads up to gtm.MaxAnalyticsWindow.
const MaxSQLWindow = 30 * 24 * time.Hour

// name is what an event or property name may be. It repeats
// gtm.ValidAnalyticsName's pattern on purpose: what reaches a statement is
// decided here, so loosening the domain's rule cannot loosen this one.
var name = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// validName reports whether s may be emitted as a name literal.
func validName(s string) bool { return name.MatchString(s) }

// identifierWords are whole words of a property name that make it an
// identifier: a breakdown by one would list ids, people, devices or secrets.
// They are short or common, so they match only as a whole word.
var identifierWords = map[string]bool{
	"id": true, "ids": true, "uid": true, "uids": true, "sid": true, "pid": true,
	"ip": true, "ips": true, "mac": true, "org": true, "orgs": true, "users": true,
	"anon": true, "anonymous": true, "mail": true, "tel": true, "msisdn": true, "key": true, "keys": true, "pwd": true, "auth": true, "bearer": true,
	"name": true, "names": true, "login": true, "handle": true, "addr": true, "ssn": true, "dob": true,
	"birthday": true, "birthdate": true, "card": true, "pan": true, "cvv": true, "iban": true, "bic": true,
	"jwt": true, "xff": true, "adid": true, "aaid": true, "gaid": true, "idfa": true, "idfv": true,
	"imei": true, "imsi": true, "iccid": true, "ua": true,
	"cell": true, "otp": true, "pin": true, "location": true, "lat": true, "lng": true, "lon": true,
}

// identifierStems match anywhere in a word or in the whole name run
// together ("useremail", "ipaddress", "x-forwarded-for"): each is long or
// distinctive enough that a harmless name rarely contains it.
var identifierStems = []string{
	"user", "email", "phone", "session", "visitor", "uuid", "guid", "token", "secret", "password", "passwd",
	"apikey", "accesskey", "secretkey", "privatekey", "cookie", "fingerprint", "forwarded", "address",
	"username", "fullname", "firstname", "lastname", "surname", "givenname", "familyname", "nickname",
	"creditcard", "cardnumber", "mobilenumber", "birth", "passport", "gclid", "fbclid", "msclkid", "dclid",
	"ttclid", "wbraid", "gbraid", "idfa", "imei", "jwt", "anonymous", "deviceid", "clientid", "customerid", "accountid",
	"ipv4", "ipv6", "contact", "recipient", "sender", "credential", "signature", "serial", "accountnumber",
	"referralcode", "invitecode", "latitude", "longitude", "geolocation",
}

// identifierNames are whole property names that name an identifier though
// their words, in other names, do not: "mobile" alone is a number, while
// is_mobile, mobile_os and mobile_app are categories.
// mobile_no, mobileNo and mobile_num are the number by another name.
var identifierNames = map[string]bool{"mobile": true, "mobile_no": true, "mobile_num": true, "mobile_nr": true}

// categoryProperties are names the rules above would refuse that are
// categories, not identifiers: a plan's name, a token count, an auth method.
// The list is explicit and small, and it never loosens the rules for
// anything else. A name matches with its words run together, lowercased
// (plan_name, planName and PlanName are planname; iPhone is iphone), and a
// trailing s is a plural of the same name (plan_names).
var categoryProperties = compactSet(
	"plan_name", "os_name", "browser_name", "feature_name", "experiment_name",
	"flag_key", "feature_key", "input_tokens", "output_tokens", "token_count",
	"auth_method", "auth_provider", "cookie_consent", "user_role", "user_type",
	"session_count", "card_type", "org_plan", "iphone",
	"contact_form_variant", "sender_domain", "location_type",
)

// compactSet is names as a set of compact names (compactName).
func compactSet(names ...string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[compactName(n)] = true
	}
	return set
}

// compactName is p's letters and digits, lowercased: the separators and
// case that tell plan_name from planName or iPhone from iphone are dropped.
func compactName(p string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(p) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// categoryProperty reports whether p is one of categoryProperties, or its
// plural.
func categoryProperty(p string) bool {
	c := compactName(p)
	return categoryProperties[c] || categoryProperties[strings.TrimSuffix(c, "s")]
}

// idWords are the words that end in "id" without naming an identifier.
// Any other word (or name run together) ending in "id" or "ids" — orgid,
// traceid, visitorid, planid — is taken for an identifier.
var idWords = []string{
	"paid", "unpaid", "prepaid", "valid", "invalid", "android", "avoid", "devoid", "void", "said", "rapid",
	"fluid", "liquid", "hybrid", "grid", "acid", "solid", "humid", "vivid", "lucid", "druid", "pyramid",
	"stupid", "timid", "tepid", "rigid", "candid", "splendid", "morbid", "cupid", "squid", "plaid",
	"braid", "maid", "raid", "arid",
}

// identifierProperty reports whether a property name looks like it names an
// identifier. Each of its words (split on _ . : - and on camelCase) and the
// whole name run together are tried: a word in identifierWords, a stem of
// identifierStems anywhere, or an "id"/"ids" ending that is not an idWords
// word. A name in categoryProperties is not an identifier.
func identifierProperty(p string) bool {
	if categoryProperty(p) {
		return false
	}
	ws := words(p)
	if identifierNames[strings.Join(ws, "_")] {
		return true
	}
	candidates := append(ws, strings.Join(ws, ""))
	for _, w := range candidates {
		if identifierWords[w] || idEnding(w) {
			return true
		}
		for _, s := range identifierStems {
			if strings.Contains(w, s) {
				return true
			}
		}
	}
	return false
}

// idEnding reports whether w ends in "id" or "ids" other than as one of
// idWords.
func idEnding(w string) bool {
	w = strings.TrimSuffix(w, "s")
	if !strings.HasSuffix(w, "id") {
		return false
	}
	for _, ok := range idWords {
		if strings.HasSuffix(w, ok) {
			return false
		}
	}
	return true
}

// words splits a name on its separators and camelCase humps, lowercased:
// "userId" and "user_id" are both [user id], "HTTPStatus" is [http status].
func words(s string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(s)
	for i, r := range rs {
		switch {
		case r == '_' || r == '.' || r == ':' || r == '-':
			flush()
			continue
		case unicode.IsUpper(r) && len(cur) > 0:
			prevLower := unicode.IsLower(rs[i-1]) || unicode.IsDigit(rs[i-1])
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if prevLower || (unicode.IsUpper(rs[i-1]) && nextLower) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

// validate checks q again, whatever the caller did: this package's own rules
// (names, the sql templates' window, distinct funnel steps, no identifier
// property), then the domain's (gtm.AnalyticsQuery.Validate).
func validate(orgID string, q gtm.AnalyticsQuery) error {
	if strings.TrimSpace(orgID) == "" {
		return errNoOrg
	}
	for _, e := range q.Events {
		if !validName(e) {
			return errInvalidName
		}
	}
	switch q.Template {
	case gtm.AnalyticsPropertyBreakdown:
		if !validName(q.Property) {
			return errInvalidName
		}
		if identifierProperty(q.Property) {
			return errIdentifierProperty
		}
	case gtm.AnalyticsFunnel:
		seen := make(map[string]bool, len(q.Events))
		for _, e := range q.Events {
			if seen[e] {
				return errDuplicateStep
			}
			seen[e] = true
		}
	}
	if q.Template != gtm.AnalyticsFunnel && q.To.Sub(q.From) > MaxSQLWindow {
		return errWindowTooLong
	}
	// This package's own rules come first, so their fixed errors are what a
	// caller sees; the domain's rules then cover the rest.
	return q.Validate()
}
