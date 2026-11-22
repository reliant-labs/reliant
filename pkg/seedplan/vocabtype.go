package seedplan

import (
	"fmt"
	"sort"
	"strings"

	gofakeit "github.com/brianvoe/gofakeit/v7"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Typed vocabulary: the declaration surface for what parsing cannot derive.
//
// Everything else in this package derives a value from a constraint the schema
// STATES. That works whenever the constraint can be inverted — a length bound
// is a width, an IN-list is a set, a tractable regex is a shape to build. It
// stops working at exactly two places, and both are real:
//
//   - A regex postgres accepts that Go's RE2 cannot compile. Any lookahead
//     (`^(?=.*[A-Z]).{8,}$`) is a POSIX pattern with no RE2 equivalent, so
//     there is nothing to parse and nothing to invert.
//   - A constraint that is satisfiable but whose satisfying values are not
//     DERIVABLE from its text. `char_length(shipping_country) = 2` is honestly
//     satisfied by "00"; that it should be an ISO country code is a fact about
//     the domain, and the schema does not state it.
//
// Regex reversal is not attempted for the first, and inference is not
// attempted for the second — a guessed fixture that happens to pass is how a
// wrong value ships silently. Instead the author DECLARES a type:
//
//	columns:
//	  orders.customer_email:   {type: email}
//	  orders.shipping_country: {type: country_code}
//	  products.sku:            {type: uuid}
//
// and the value comes from gofakeit, which knows what an email or a country
// code is. This composes with the existing value-pool form: `{type: ...}` says
// "generate values of this kind", a list says "use exactly these".

// vocabTypePoolSize is how many values a declared `{type: ...}` expands to.
// Large enough that a UNIQUE column of realistic seed size draws distinct
// values without probing, small enough that the pool stays readable in a diff.
const vocabTypePoolSize = 24

// vocabTypeGen is one supported semantic type: the name authors write in
// vocab.yaml and the gofakeit lookup that produces its values.
type vocabTypeGen struct {
	// name is the vocab.yaml spelling (snake_case, forge's convention).
	name string
	// lookup is gofakeit's own registry key.
	lookup string
}

// vocabTypeGens is the supported set. Each entry is a NAME MAPPING only — the
// generator behind it, and therefore the knowledge of what an email or a
// postal code looks like, lives in gofakeit. The list is deliberately a
// curated subset rather than every gofakeit function: a type here is a promise
// that forge will keep generating that kind of value, and exposing the whole
// registry would make every upstream rename a breaking change to a project's
// vocab.yaml.
//
// Membership is VERIFIED against gofakeit at init (see init below) rather than
// trusted, so an entry naming a lookup that upstream removed or renamed fails
// immediately and loudly instead of silently producing nothing.
var vocabTypeGens = []vocabTypeGen{
	{"email", "email"},
	{"uuid", "uuid"},
	{"url", "url"},
	{"phone", "phone"},
	{"country_code", "countryabr"},
	{"country", "country"},
	{"name", "name"},
	{"first_name", "firstname"},
	{"last_name", "lastname"},
	{"username", "username"},
	{"company", "company"},
	{"street_address", "street"},
	{"city", "city"},
	{"state", "state"},
	{"postal_code", "zip"},
	{"currency_code", "currencyshort"},
	{"language", "language"},
	{"timezone", "timezone"},
	{"domain", "domainname"},
	{"ipv4", "ipv4address"},
	{"ipv6", "ipv6address"},
	{"mac_address", "macaddress"},
	{"credit_card_number", "creditcardnumber"},
	{"color", "color"},
}

// vocabTypeIndex is the resolved name → lookup map, built and validated once.
var vocabTypeIndex = map[string]*gofakeit.Info{}

func init() {
	for _, g := range vocabTypeGens {
		info := gofakeit.GetFuncLookup(g.lookup)
		if info == nil {
			// A declared type with no generator behind it would silently
			// produce nothing at seed time. Fail at program start instead:
			// this is forge's own table being wrong, not the user's input.
			panic(fmt.Sprintf(
				"seeddata: vocab type %q names gofakeit lookup %q, which does not exist "+
					"(gofakeit upgrade removed or renamed it)", g.name, g.lookup))
		}
		if info.Output != "string" {
			panic(fmt.Sprintf(
				"seeddata: vocab type %q (gofakeit %q) produces %q, not a string — "+
					"typed vocabulary supplies text column values",
				g.name, g.lookup, info.Output))
		}
		vocabTypeIndex[g.name] = info
	}
}

// VocabTypeNames returns every supported type name, sorted. Derived from the
// same table the resolver uses, so documentation, error messages and the
// validator cannot drift from what is actually supported.
func VocabTypeNames() []string {
	out := make([]string, 0, len(vocabTypeIndex))
	for name := range vocabTypeIndex {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// IsVocabType reports whether name is a supported semantic type.
func IsVocabType(name string) bool {
	_, ok := vocabTypeIndex[name]
	return ok
}

// VocabTypeValues generates n deterministic values of the named type.
//
// Determinism is forge's standing guarantee for seed data — the same (schema,
// config, vocab) must render byte-identically — so the faker is seeded from
// the same stateless per-cell hash every other draw uses, keyed by
// (salt, table, column). A column's values therefore depend only on its own
// identity: adding a typed column never reshuffles another one.
//
// The values are de-duplicated, because a caller asking for n values wants n
// USABLE values and a repeated one is unusable for a UNIQUE column. Fewer than
// n may be returned when the generator cannot produce that many distinct
// values (a country code has ~250; a boolean-ish type far fewer), which the
// caller handles the same way it handles a short CHECK vocabulary.
func VocabTypeValues(typeName string, salt int, table, column string, n int) ([]string, error) {
	info, ok := vocabTypeIndex[typeName]
	if !ok {
		return nil, fmt.Errorf("unknown vocab type %q (supported: %s)",
			typeName, strings.Join(VocabTypeNames(), ", "))
	}
	if n <= 0 {
		return nil, nil
	}
	faker := gofakeit.New(cellHash(salt, table, column+"#type", 0))
	seen := make(map[string]bool, n)
	out := make([]string, 0, n)
	// Bounded attempts: a small pool (country_code) would otherwise spin
	// forever trying to reach a large n.
	for attempts := 0; len(out) < n && attempts < n*32+64; attempts++ {
		v, err := info.Generate(faker, &gofakeit.MapParams{}, info)
		if err != nil {
			return nil, fmt.Errorf("generate %s value: %w", typeName, err)
		}
		s, ok := v.(string)
		if !ok || s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("vocab type %q produced no values", typeName)
	}
	return out, nil
}

// InferVocabType returns the semantic type a column UNAMBIGUOUSLY has, given
// its schema constraints and its name. ok is false whenever the answer would
// be a guess — and that is the point of the function.
//
// The rule is narrow on purpose: a name alone is never enough. `country` on a
// free-text column could hold "United States"; only the combination of the
// name and a constraint the type would SATISFY (a 2-char length for an ISO
// code, a regex mentioning `@` for an email) makes the reading forced rather
// than plausible. Everything else returns false and the author is asked to
// declare, because a fixture that is wrong-but-passing is worse than one that
// fails loudly: it ships.
func InferVocabType(t schemadef.Table, col schemadef.Column) (string, bool) {
	if col.Type != schemadef.TypeString || col.IsArray {
		return "", false
	}
	name := strings.ToLower(col.Name)
	minLen, maxLen := LengthBounds(t, col)
	pats := patternsOf(t, col)

	// An email column whose CHECK is an email-shaped pattern. The pattern must
	// be present: without it, `contact_email TEXT` is satisfied by anything
	// and forge has no reason to prefer a real-looking address.
	if hasNameToken(name, "email") && anyPatternMentions(pats, "@") {
		return "email", true
	}

	// A country column pinned to exactly 2 characters is an ISO 3166-1
	// alpha-2 code — the only 2-character country notation in use.
	if hasNameToken(name, "country") && minLen == 2 && maxLen == 2 {
		return "country_code", true
	}

	// A currency column pinned to exactly 3 characters is an ISO 4217 code.
	if hasNameToken(name, "currency") && minLen == 3 && maxLen == 3 {
		return "currency_code", true
	}

	return "", false
}

// hasNameToken reports whether the column name contains token as a whole
// `_`-separated segment. Segment matching, not substring: `country_code`
// contains "country", but `discountry` (or the far likelier `discount_rate`)
// must not match "count".
func hasNameToken(name, token string) bool {
	for _, seg := range strings.Split(name, "_") {
		if seg == token {
			return true
		}
	}
	return false
}

// anyPatternMentions reports whether any of the column's regex CHECKs contains
// the given literal marker. Deliberately a TEXTUAL test on the pattern source,
// not a parse: the patterns this disambiguates include ones RE2 cannot compile,
// which is precisely the case typed vocabulary exists to serve.
func anyPatternMentions(pats []string, marker string) bool {
	for _, p := range pats {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}
