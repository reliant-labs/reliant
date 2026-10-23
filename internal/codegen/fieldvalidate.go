// File: internal/codegen/fieldvalidate.go
//
// FieldConstraints is forge's projection-ready view of a field's
// protovalidate (buf.validate.field) rules — the ONE declaration that
// projects to THREE enforcement points:
//
//   1. a DB CHECK in the born migration (SQLChecks),
//   2. a zod validator chain in the generated form (ZodChain), and
//   3. the wire, enforced by the protovalidate interceptor off the
//      options carried by the message it is handed.
//
// The wire point needs no projection for a request that NESTS the entity
// (Update<Entity>Request wraps it, and protovalidate recurses into the
// nested message's annotated fields) — but a request that FLATTENS entity
// fields (Create<Entity>Request) is a different message, and carries no
// rules unless they are repeated on it. ValidateFieldOptions below is that
// carrier: it lifts a field's `(buf.validate.field)` options out of its
// authored `[...]` block so the flattening request can re-declare them
// verbatim. Without it a Create with an over-length name passes the wire,
// reaches the DB, trips the CHECK, and surfaces as Internal — a 500-class
// error for a client mistake.
//
// Two extractors populate it: the compiled-descriptor path
// (forge_descriptor.go, via the buf.build/gen/go extension types) and the
// lightweight raw proto scan (proto_rawscan.go, regex-grade — a brand-new
// `// forge:entity` message need not be in the descriptor yet). Both feed
// the same struct, so the projections below are the single source of truth
// for the SQL/zod spellings regardless of which extractor ran.
//
// Scope: the high-value, unambiguous subset — numeric bounds, string
// length, pattern, email, required. Everything else protovalidate supports
// (CEL, uuid/hostname/ip/uri and other string formats, repeated/map/
// timestamp/duration rules, in/not_in, const, cross-field CEL) is STILL
// enforced at the wire by the interceptor; it is simply not projected onto
// the DB/zod layers, where the mechanical mapping would be lossy or
// brittle.

package codegen

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// FieldConstraints holds the subset of buf.validate.field rules forge
// projects. Numeric bounds are kept as the raw literal token ("0", "100",
// "9.99") so an integer bound never picks up a spurious ".0" in the
// emitted SQL or zod. All fields are additive/omitempty so old descriptors
// (which carry none) round-trip unchanged.
type FieldConstraints struct {
	// Numeric bounds (apply to int*/uint*/sint*/fixed*/float/double).
	Gte string `json:"gte,omitempty"`
	Gt  string `json:"gt,omitempty"`
	Lte string `json:"lte,omitempty"`
	Lt  string `json:"lt,omitempty"`
	// String length in characters (runes).
	MinLen *uint64 `json:"min_len,omitempty"`
	MaxLen *uint64 `json:"max_len,omitempty"`
	// Pattern is an RE2 regular expression the whole string must match.
	Pattern string `json:"pattern,omitempty"`
	// Email requires a syntactically valid email address.
	Email bool `json:"email,omitempty"`
	// Required marks the field must-be-set (protovalidate `required`).
	Required bool `json:"required,omitempty"`
}

// HasAny reports whether any constraint is set — the guard every consumer
// uses before projecting.
func (c *FieldConstraints) HasAny() bool {
	if c == nil {
		return false
	}
	return c.Gte != "" || c.Gt != "" || c.Lte != "" || c.Lt != "" ||
		c.MinLen != nil || c.MaxLen != nil || c.Pattern != "" || c.Email || c.Required
}

// isNumericValidateKind reports whether the proto scalar kind takes
// numeric (gte/gt/lte/lt) constraints.
//
// Those are exactly the numeric scalar kinds, and that is not a
// coincidence worth restating a twelve-name list over: protovalidate's
// numeric rules apply to a kind because the kind is a number, which is the
// same fact that puts it on JavaScript's number tower. Two lists of the
// same twelve names are two chances for a kind to be numeric in the SQL
// CHECK half and non-numeric in the zod half — where the disagreement
// surfaces as a missing constraint, never as an error.
func isNumericValidateKind(kind string) bool {
	return isNumericProtoScalar(kind)
}

// emailSQLRegex is a deliberately PERMISSIVE email shape for the DB CHECK.
// The strict enforcement lives at the wire (protovalidate) and in the form
// (zod .email()); the DB check is defense-in-depth and must never be the
// stricter gate — a permissive pattern can only reject things the wire
// already rejected. Postgres ARE: \s is whitespace, \. a literal dot;
// standard_conforming_strings leaves the backslashes intact.
const emailSQLRegex = `^[^@\s]+@[^@\s]+\.[^@\s]+$`

// sqlQuote single-quotes a SQL string literal, doubling embedded quotes.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// SQLChecks returns the CHECK clause(s) to append to a column definition
// for a field of the given proto scalar kind. Each entry is a complete
// `CHECK (...)` expression. NULL rows pass every check by SQL three-valued
// logic, so nullable columns need no special form.
func (c *FieldConstraints) SQLChecks(col, kind string) []string {
	if !c.HasAny() {
		return nil
	}
	var checks []string
	if isNumericValidateKind(kind) {
		if c.Gte != "" {
			checks = append(checks, fmt.Sprintf("CHECK (%s >= %s)", col, c.Gte))
		}
		if c.Gt != "" {
			checks = append(checks, fmt.Sprintf("CHECK (%s > %s)", col, c.Gt))
		}
		if c.Lte != "" {
			checks = append(checks, fmt.Sprintf("CHECK (%s <= %s)", col, c.Lte))
		}
		if c.Lt != "" {
			checks = append(checks, fmt.Sprintf("CHECK (%s < %s)", col, c.Lt))
		}
		return checks
	}
	if kind == "string" {
		// A required / min_len>=1 string forbids the empty string; fold
		// `required` into an effective min length of 1 when no explicit
		// min_len is present, so both spell the same non-empty CHECK.
		minLen := c.MinLen
		if minLen == nil && c.Required {
			one := uint64(1)
			minLen = &one
		}
		switch {
		case minLen != nil && c.MaxLen != nil && *minLen == *c.MaxLen:
			// Fixed-length code (ISO currency = 3, ISO country = 2, NPI = 10,
			// …): a single exact-length CHECK, never `BETWEEN N AND N`. This
			// mirrors the `string.len = N` spelling on the wire (buf lint
			// rejects `min_len == max_len` in favor of string.len/string.const)
			// and the `= N` idiom the seed-data length parser already reads.
			// Semantically `= N` ≡ `BETWEEN N AND N`; the drift detector folds
			// the two forms to the same bound atoms so neither spuriously drifts.
			checks = append(checks, fmt.Sprintf("CHECK (char_length(%s) = %d)", col, *minLen))
		case minLen != nil && c.MaxLen != nil:
			checks = append(checks, fmt.Sprintf("CHECK (char_length(%s) BETWEEN %d AND %d)", col, *minLen, *c.MaxLen))
		case minLen != nil:
			checks = append(checks, fmt.Sprintf("CHECK (char_length(%s) >= %d)", col, *minLen))
		case c.MaxLen != nil:
			checks = append(checks, fmt.Sprintf("CHECK (char_length(%s) <= %d)", col, *c.MaxLen))
		}
		if c.Pattern != "" {
			checks = append(checks, fmt.Sprintf("CHECK (%s ~ %s)", col, sqlQuote(c.Pattern)))
		}
		if c.Email {
			checks = append(checks, fmt.Sprintf("CHECK (%s ~ %s)", col, sqlQuote(emailSQLRegex)))
		}
	}
	return checks
}

// SuppressesZeroDefault reports whether a NOT NULL column carrying these
// constraints should OMIT its zero default. A constrained value is one the
// caller must supply — and its type's zero (empty string / 0) would often
// violate the very CHECK just emitted (min_len>=1, gte>0, a non-empty
// pattern, email). Dropping the default keeps the migration internally
// consistent and mirrors the existing *_id reference-column treatment
// (NOT NULL, no default — an empty reference is a bug, not a value).
func (c *FieldConstraints) SuppressesZeroDefault(kind string) bool {
	if !c.HasAny() {
		return false
	}
	if kind == "string" {
		return c.Required || c.MinLen != nil || c.Pattern != "" || c.Email
	}
	if isNumericValidateKind(kind) {
		// A lower bound may exclude 0; an upper-only bound keeps 0 valid.
		return c.Gt != "" || c.Gte != "" && c.Gte != "0"
	}
	return false
}

// ZodChain returns the zod validator suffix (e.g. ".gte(0).lte(100)" or
// ".min(1).max(64).email()") for a form field of the given form type
// ("number" takes numeric bounds; everything else is treated as a string
// input). The base builder (z.coerce.number() / z.string()) is emitted by
// the template; this is only the trailing refinement chain.
func (c *FieldConstraints) ZodChain(formType string) string {
	if !c.HasAny() {
		return ""
	}
	var b strings.Builder
	if formType == "number" {
		if c.Gte != "" {
			fmt.Fprintf(&b, ".gte(%s)", c.Gte)
		}
		if c.Gt != "" {
			fmt.Fprintf(&b, ".gt(%s)", c.Gt)
		}
		if c.Lte != "" {
			fmt.Fprintf(&b, ".lte(%s)", c.Lte)
		}
		if c.Lt != "" {
			fmt.Fprintf(&b, ".lt(%s)", c.Lt)
		}
		return b.String()
	}
	// String-input chain.
	minLen := c.MinLen
	if minLen == nil && c.Required {
		one := uint64(1)
		minLen = &one
	}
	if minLen != nil {
		fmt.Fprintf(&b, ".min(%d)", *minLen)
	}
	if c.MaxLen != nil {
		fmt.Fprintf(&b, ".max(%d)", *c.MaxLen)
	}
	if c.Email {
		b.WriteString(".email()")
	}
	if c.Pattern != "" {
		// new RegExp("...") sidesteps the / delimiter-escaping the /re/
		// literal would need; the pattern is JS-string-escaped.
		fmt.Fprintf(&b, ".regex(new RegExp(%s))", jsStringLiteral(c.Pattern))
	}
	return b.String()
}

// rawValidateOptionRE matches one dotted-form protovalidate option inside
// a field's `[...]` block: `(buf.validate.field).<path> = <value>`, where
// <value> is a quoted string, a bare token, or a number. The braced
// aggregate spellings — per-type (`(buf.validate.field).int64 = {gte: 1}`)
// and full (`(buf.validate.field) = {int64: {gte: 1}, required: true}`) —
// are handled separately by aggregateBraceStartRE + applyAggregateBody so
// the raw scan projects the SAME CHECKs the compiled-descriptor extractor
// (and therefore the drift detector) reads off those forms. This scanner
// exists so a brand-new `// forge:entity` message — not yet in the
// descriptor — still births CHECK constraints regardless of how the author
// spelled the rules.
var rawValidateOptionRE = regexp.MustCompile(
	`\(buf\.validate\.field\)\.([a-zA-Z0-9_.]+)\s*=\s*("(?:[^"\\]|\\.)*"|[^,\]\s]+)`)

// aggregateBraceStartRE matches the START of a braced protovalidate
// aggregate value, either the per-type partial form
// (`(buf.validate.field).int64 = {`) or the full form
// (`(buf.validate.field) = {`). The balanced, quote-aware brace body that
// follows is parsed by applyAggregateBody.
var aggregateBraceStartRE = regexp.MustCompile(
	`\(buf\.validate\.field\)(?:\.[a-zA-Z0-9_]+)?\s*=\s*\{`)

// ParseRawValidateOptions parses the protovalidate rules out of a field's
// inline options text (the `[...]` block, brackets optional) into a
// FieldConstraints, or nil when none of the supported rules appear. Both the
// dotted form (`.int64.gte = 1`) and the braced aggregate form
// (`.int64 = {gte: 1}` / `= {int64: {gte: 1}}`) are recognized, so a rule
// projects to the same CHECK no matter which spelling the author used. Rules
// outside the projected subset are ignored — they are still enforced at the
// wire by the interceptor.
func ParseRawValidateOptions(optsText string) *FieldConstraints {
	if !strings.Contains(optsText, "buf.validate.field") {
		return nil
	}
	c := &FieldConstraints{}
	// Dotted form: (buf.validate.field).<path> = <scalar>.
	for _, m := range rawValidateOptionRE.FindAllStringSubmatch(optsText, -1) {
		path, val := m[1], strings.TrimSpace(m[2])
		rule := path
		if i := strings.LastIndexByte(path, '.'); i >= 0 {
			rule = path[i+1:]
		}
		c.applyValidateRule(rule, val)
	}
	// Braced aggregate form(s): parse each balanced `{ ... }` body. A partial
	// aggregate (`.int64 = {gte: 1}`) matches with its rules at the top of the
	// body; a full aggregate (`= {int64: {gte: 1}, required: true}`) nests the
	// per-type rules a level deeper — applyAggregateBody recurses to reach
	// them. The dotted regex above matches a partial aggregate's start too but
	// captures a `{` token for a type-named rule, which applyValidateRule
	// ignores, so the two passes never double-apply a rule.
	for _, loc := range aggregateBraceStartRE.FindAllStringIndex(optsText, -1) {
		open := loc[1] - 1 // index of the '{' the regex ends on
		end := matchBrace(optsText, open)
		if end < 0 {
			continue
		}
		c.applyAggregateBody(optsText[open+1 : end])
	}
	if !c.HasAny() {
		return nil
	}
	return c
}

// ValidateFieldOptions returns an inline-options block carrying ONLY the
// `(buf.validate.field)` options found in optsText (a field's raw `[...]`
// block, brackets optional), spelled as the author wrote them and collapsed
// to one line:
//
//	[(buf.validate.field).string.min_len = 1, (buf.validate.field).string.max_len = 120]
//
// It returns "" when the text declares no protovalidate rule. Non-validate
// options in the same block (`deprecated = true`, a custom extension) are
// dropped — the caller is copying VALIDATION onto another message, not the
// field's whole option set.
//
// Verbatim, rather than re-rendering FieldConstraints: FieldConstraints is
// deliberately the lossy SQL/zod-projectable SUBSET, but the wire's target
// IS proto, so nothing needs to be dropped. Copying the text carries the
// rules forge does not project either — CEL, uuid/hostname/ip and the other
// string formats, in/not_in, const, repeated/map/duration rules — so a rule
// declared once is enforced on the wire wherever the field travels.
//
// Only FIELD options are carried. Message-level rules
// (`option (buf.validate.message).cel`) are the one shape that can name
// sibling fields, and are never lifted onto a request that carries a
// different field set.
func ValidateFieldOptions(optsText string) string {
	if !strings.Contains(optsText, "buf.validate.field") {
		return ""
	}
	body := strings.TrimSpace(optsText)
	body = strings.TrimSuffix(strings.TrimPrefix(body, "["), "]")
	var kept []string
	for _, opt := range splitInlineOptions(body) {
		if strings.HasPrefix(opt, "(buf.validate.field)") {
			kept = append(kept, opt)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return "[" + strings.Join(kept, ", ") + "]"
}

// splitInlineOptions splits the INSIDE of a field's `[...]` block into its
// top-level options, on commas that sit outside any quoted string and any
// (), {} or [] group — a braced aggregate value
// (`.int64 = {gte: 1, lte: 12}`) is therefore one option, not two. Each
// option comes back trimmed with its runs of whitespace collapsed to a
// single space OUTSIDE quotes (the raw scan folds a multi-line braced value
// onto one line, and a `pattern` literal may legitimately contain runs of
// spaces).
func splitInlineOptions(body string) []string {
	var (
		out     []string
		cur     strings.Builder
		depth   int
		inQuote bool
		lastWS  bool
	)
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
		lastWS = false
	}
	for i := 0; i < len(body); i++ {
		ch := body[i]
		if inQuote {
			cur.WriteByte(ch)
			if ch == '\\' && i+1 < len(body) {
				cur.WriteByte(body[i+1])
				i++
				continue
			}
			if ch == '"' {
				inQuote = false
			}
			continue
		}
		switch {
		case ch == '"':
			inQuote = true
			cur.WriteByte(ch)
			lastWS = false
		case ch == '(' || ch == '{' || ch == '[':
			depth++
			cur.WriteByte(ch)
			lastWS = false
		case ch == ')' || ch == '}' || ch == ']':
			depth--
			cur.WriteByte(ch)
			lastWS = false
		case ch == ',' && depth == 0:
			flush()
		case isProtoSpace(ch):
			if !lastWS {
				cur.WriteByte(' ')
				lastWS = true
			}
		default:
			cur.WriteByte(ch)
			lastWS = false
		}
	}
	flush()
	return out
}

// applyValidateRule folds one recognized protovalidate rule (by leaf name)
// and its raw value token into c. Unknown rule names are ignored. Shared by
// the dotted-form and braced-aggregate scanners so both spellings project
// identically.
func (c *FieldConstraints) applyValidateRule(rule, val string) {
	switch rule {
	case "gte":
		c.Gte = val
	case "gt":
		c.Gt = val
	case "lte":
		c.Lte = val
	case "lt":
		c.Lt = val
	case "min_len":
		if n, err := strconv.ParseUint(val, 10, 64); err == nil {
			c.MinLen = &n
		}
	case "max_len":
		if n, err := strconv.ParseUint(val, 10, 64); err == nil {
			c.MaxLen = &n
		}
	case "len": // exact length → min == max
		if n, err := strconv.ParseUint(val, 10, 64); err == nil {
			n := n
			c.MinLen, c.MaxLen = &n, &n
		}
	case "pattern":
		c.Pattern = unquoteProtoString(val)
	case "email":
		c.Email = val == "true"
	case "required":
		c.Required = val == "true"
	}
}

// applyAggregateBody parses the inside of a protovalidate aggregate `{ ... }`
// value, applying each recognized `<rule>: <value>` pair to c. A value that
// is itself a `{ ... }` block — the per-type rules submessage in the full
// aggregate form (`int64: {gte: 1}`, or the proto-text colon-less
// `int64 {gte: 1}`) — is recursed into, so the partial (`.int64 = {gte: 1}`)
// and full (`= {int64: {gte: 1}, required: true}`) spellings project
// identically. Rule names never collide with the type submessage names, so a
// flat recursive walk over every `key value` pair suffices.
func (c *FieldConstraints) applyAggregateBody(body string) {
	for i, n := 0, len(body); i < n; {
		// Skip separators (whitespace, commas, and the rare semicolon).
		for i < n && (isProtoSpace(body[i]) || body[i] == ',' || body[i] == ';') {
			i++
		}
		if i >= n {
			break
		}
		// Read the key identifier.
		start := i
		for i < n && isProtoIdentChar(body[i]) {
			i++
		}
		if i == start { // stray char — advance so the loop can't stall
			i++
			continue
		}
		key := body[start:i]
		// Skip whitespace and an optional ':' between key and value.
		for i < n && isProtoSpace(body[i]) {
			i++
		}
		if i < n && body[i] == ':' {
			i++
			for i < n && isProtoSpace(body[i]) {
				i++
			}
		}
		if i >= n {
			break
		}
		if body[i] == '{' { // nested per-type submessage — recurse
			end := matchBrace(body, i)
			if end < 0 {
				break
			}
			c.applyAggregateBody(body[i+1 : end])
			i = end + 1
			continue
		}
		val, next := readAggregateValue(body, i)
		i = next
		c.applyValidateRule(key, val)
	}
}

// readAggregateValue reads one scalar value token starting at i: a
// double-quoted string (returned WITH its quotes, so a `pattern` value round-
// trips through unquoteProtoString exactly as the dotted form does) or a bare
// token ending at the next separator (whitespace, comma, semicolon, or the
// closing brace). It returns the token and the index just past it.
func readAggregateValue(s string, i int) (string, int) {
	n := len(s)
	if i < n && s[i] == '"' {
		j := i + 1
		for j < n {
			if s[j] == '\\' && j+1 < n {
				j += 2
				continue
			}
			if s[j] == '"' {
				j++
				break
			}
			j++
		}
		return s[i:j], j
	}
	start := i
	for i < n && !isProtoSpace(s[i]) && s[i] != ',' && s[i] != ';' && s[i] != '}' {
		i++
	}
	return s[start:i], i
}

// matchBrace returns the index of the '}' matching the '{' at open, or -1.
// Content inside double-quoted string literals (a `pattern` may embed
// braces) is skipped, honoring backslash escapes.
func matchBrace(s string, open int) int {
	depth := 0
	inQuote := false
	for i := open; i < len(s); i++ {
		ch := s[i]
		if inQuote {
			if ch == '\\' {
				i++ // skip the escaped char
				continue
			}
			if ch == '"' {
				inQuote = false
			}
			continue
		}
		switch ch {
		case '"':
			inQuote = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isProtoSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func isProtoIdentChar(b byte) bool {
	return b == '_' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// unquoteProtoString strips surrounding double quotes and unescapes the
// common proto string escapes (\\ \" \n \t) — enough for the regex
// patterns users write in a `pattern` rule.
func unquoteProtoString(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	inner := s[1 : len(s)-1]
	var b strings.Builder
	for i := 0; i < len(inner); i++ {
		if inner[i] == '\\' && i+1 < len(inner) {
			switch inner[i+1] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(inner[i+1])
			}
			i++
			continue
		}
		b.WriteByte(inner[i])
	}
	return b.String()
}

// jsStringLiteral renders s as a double-quoted JS/TS string literal,
// escaping backslashes and double quotes (RE2 patterns routinely carry
// both).
func jsStringLiteral(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
