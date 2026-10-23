package naming

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jinzhu/inflection"
)

// ToPascalCase converts a hyphenated, underscored, or camelCase name to PascalCase.
// It handles both '-' and '_' as word separators and capitalizes Go initialisms.
// e.g. "api-gateway" -> "APIGateway", "user_service" -> "UserService", "http_client" -> "HTTPClient".
func ToPascalCase(s string) string {
	var b strings.Builder
	upNext := true
	for _, r := range s {
		if r == '-' || r == '_' {
			upNext = true
			continue
		}
		if upNext {
			b.WriteRune(unicode.ToUpper(r))
			upNext = false
		} else {
			b.WriteRune(r)
		}
	}
	return applyGoInitialisms(b.String())
}

// GoInitialisms are common Go initialisms that should be all-caps.
// This is the single source of truth — all packages must import from here.
var GoInitialisms = []string{
	"ACL", "API", "ASCII", "CPU", "CSS", "DB", "DNS", "EOF", "GUID",
	"HTML", "HTTP", "HTTPS", "ID", "IO", "IP", "JSON", "JWT", "LHS",
	"LLM", "MCP", "QPS",
	"RAM", "RHS", "RPC", "SLA", "SMTP", "SQL", "SSH", "TCP",
	"TLS", "TTL", "UDP", "UI", "UID", "UUID", "URI", "URL",
	"UTF8", "VM", "XML", "XMPP", "XSRF", "XSS",
}

// GoInitialismsMap provides O(1) lookup for initialism detection (lowercase keys).
var GoInitialismsMap = func() map[string]bool {
	m := make(map[string]bool, len(GoInitialisms))
	for _, v := range GoInitialisms {
		m[strings.ToLower(v)] = true
	}
	return m
}()

// goInitialismsSorted is GoInitialisms sorted by length descending,
// so that longer initialisms (e.g. "UUID") are replaced before shorter
// ones (e.g. "ID") to avoid partial replacements like "UuID".
var goInitialismsSorted = func() []string {
	sorted := make([]string, len(GoInitialisms))
	copy(sorted, GoInitialisms)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})
	return sorted
}()

// applyGoInitialisms replaces known initialisms with their all-caps form,
// but only where the title-cased initialism is a whole PascalCase word: the
// match must be followed by an upper-case letter, a digit, or the end of the
// string. A capital letter always starts a word, so no leading check is needed.
//
//	"HttpClient" -> "HTTPClient", "ApiId" -> "APIID", "Uuid" -> "UUID"
//	"Identity" -> "Identity", "Slack" -> "Slack", "Ramp" -> "Ramp"
//
// Plurals are deliberately not initialisms: "Ids" stays "Ids", as in
// staticcheck ST1003 (which tests whole words) and protoc-gen-go (which
// applies no initialisms at all), so forge's names match buf's stubs.
// Longer initialisms are processed first so "Uuid" never becomes "UuID".
func applyGoInitialisms(s string) string {
	for _, initialism := range goInitialismsSorted {
		titleCase := strings.ToUpper(initialism[:1]) + strings.ToLower(initialism[1:])
		var b strings.Builder
		for {
			i := strings.Index(s, titleCase)
			if i < 0 {
				break
			}
			end := i + len(titleCase)
			b.WriteString(s[:i])
			if endsWord(s, end) {
				b.WriteString(initialism)
			} else {
				b.WriteString(titleCase)
			}
			s = s[end:]
		}
		b.WriteString(s)
		s = b.String()
	}
	return s
}

// TripsReviveVarNaming reports whether revive's var-naming rule would want a
// different spelling of goIdent (e.g. CreateApiKey → CreateAPIKey). Proto /
// connect-generated handler method names must match the Handler interface
// exactly, so scaffolded stubs keep the generator spelling and suppress the
// lint instead of renaming.
func TripsReviveVarNaming(goIdent string) bool {
	return applyGoInitialisms(goIdent) != goIdent
}

// endsWord reports whether a PascalCase word ends at s[end]: end of string,
// or the next rune is upper-case or a digit.
func endsWord(s string, end int) bool {
	if end >= len(s) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(s[end:])
	return unicode.IsUpper(r) || unicode.IsDigit(r)
}

// ToSnakeCase converts a string (camelCase, PascalCase, UPPER_CASE, etc.) to snake_case.
// e.g. "firstName" -> "first_name", "HTTPStatus" -> "http_status", "UPPER_CASE" -> "upper_case".
func ToSnakeCase(s string) string {
	// Collect runes up front so we can look ahead one rune without reindexing
	// into the byte slice (which would be unsafe for multi-byte code points).
	runes := []rune(s)
	var b strings.Builder
	var prev rune
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				// Insert underscore before an uppercase letter if preceded by a lowercase letter,
				// or if preceded by an uppercase letter followed by a lowercase letter (e.g. "HTTPStatus" -> "http_status").
				if unicode.IsLower(prev) {
					b.WriteByte('_')
				} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
		prev = r
	}
	return b.String()
}

// Humanize splits a PascalCase/camelCase/snake_case identifier into
// space-separated words, preserving each word's case: "RetainedDatabases" ->
// "Retained Databases". For human-facing UI copy only; never for identifiers.
func Humanize(s string) string {
	runes := []rune(strings.NewReplacer("_", " ", "-", " ").Replace(s))
	var b strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			if unicode.IsLower(prev) || unicode.IsDigit(prev) ||
				(unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// HumanizeLower is Humanize lowercased as a whole: "retained databases".
func HumanizeLower(s string) string {
	return strings.ToLower(Humanize(s))
}

// Pluralize returns the English plural form of a word using the inflection library.
func Pluralize(s string) string {
	if len(s) == 0 {
		return s
	}
	return inflection.Plural(s)
}

// EntityFieldName returns the snake_case proto FIELD name that a CRUD
// create/get/update response uses to carry a single entity — the snake_case
// of the entity's (PascalCase) message name. For a "ModuleConfig" entity this
// is "module_config"; protoc-gen-go then generates the Go field "ModuleConfig".
//
// This is the SINGLE source of truth for the entity-carrying field name,
// shared by three sites that MUST agree or multi-word CRUD silently breaks:
//   - the entity scaffolder (`forge scaffold entity`) emits `<Entity> <field> = 1;`,
//   - the CRUD ops emitter references the Go form (ToProtoPascalCase of this),
//   - the CRUD shape detector (validateCRUDShape) matches the response's
//     observed snake_case field names against this.
//
// The historical bug: the detector compared against strings.ToLower(Name)
// ("moduleconfig"), which matches neither the scaffolder's "module_config" nor
// the emitter's "ModuleConfig" — so every multi-word entity fell to the
// custom-read-shape stub and produced empty CRUD. Routing all three through
// this helper makes the concatenated-lowercase form impossible to reintroduce.
func EntityFieldName(entityName string) string {
	return ToSnakeCase(entityName)
}

// EntityListFieldName returns the snake_case proto FIELD name that a CRUD list
// response uses to carry the repeated entity — the pluralized snake_case of
// the entity's message name. For "ModuleConfig" this is "module_configs".
// It is the plural companion to EntityFieldName; see that doc for why the
// derivation is centralized here.
func EntityListFieldName(entityName string) string {
	return Pluralize(EntityFieldName(entityName))
}

// ColumnGoName is the Go field name forge's generated ORM gives a column
// on a db.<Entity> struct: title-case each '_'/'-'-separated segment, then
// spell Go initialisms in capitals the way gofmt-era Go code does ("id" →
// "ID", "org_id" → "OrgID", "api_key" → "APIKey", "address_line_2" →
// "AddressLine2", "base64item" → "Base64item", "user_ids" → "UserIds" — a
// plural is not an initialism, as in staticcheck ST1003).
//
// This is FORGE's rule for FORGE's types, and it is deliberately not
// protoc-gen-go's: the generated pb field over the same name can differ
// ("id" → Id, "base64item" → Base64Item, "address_line_2" →
// AddressLine_2). A conversion between the two structs must spell each side
// with its own rule — GoFieldName for the pb message, this for the db
// struct. It once claimed to match protoc-gen-go, and every pb field
// reference built from it broke on the first name with a digit in it.
//
// It used to apply no initialisms ("Id", "UserId"), for no reason but that
// changing it renames ORM struct fields in every existing project; the
// store is Go, and Go spells these ID and UserID (reliant P5b, F12.6).
func ColumnGoName(s string) string {
	var b strings.Builder
	upNext := true
	for _, r := range s {
		if r == '-' || r == '_' {
			upNext = true
			continue
		}
		if upNext {
			b.WriteRune(unicode.ToUpper(r))
			upNext = false
		} else {
			b.WriteRune(r)
		}
	}
	return applyGoInitialisms(b.String())
}

// ToKebabCase converts a name to kebab-case, treating known initialisms
// (LLM, API, URL, JSON, etc. — see GoInitialisms) as a single segment so
// that "LLMGateway" → "llm-gateway" rather than "l-l-m-gateway".
//
// Accepts input in PascalCase, camelCase, snake_case, or already-kebab-
// case form; the output is always lowercase, hyphen-separated, with no
// runs of multiple hyphens. This is the canonical kebab function for
// every site that emits filenames, slugs, route paths, or import paths
// that need to round-trip with proto Go names — keep it the single
// source of truth so frontend hooks files, navigation slugs, and re-
// export indexers can never disagree on whether "LLMGateway" splits as
// "l-l-m-gateway" or "llm-gateway".
func ToKebabCase(s string) string {
	if s == "" {
		return ""
	}

	// Two-stage pipeline:
	//   1. Insert separators at every word boundary, recognising known
	//      initialisms as a single word.
	//   2. Lowercase + collapse runs of separators into a single '-'.
	runes := []rune(s)
	var segments []string
	cur := strings.Builder{}
	flush := func() {
		if cur.Len() > 0 {
			segments = append(segments, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}

	i := 0
	for i < len(runes) {
		r := runes[i]
		if r == '_' || r == '-' || r == ' ' {
			flush()
			i++
			continue
		}

		// At an uppercase rune: see if a known initialism starts here.
		if unicode.IsUpper(r) {
			// Length of the contiguous uppercase run starting at i.
			runEnd := i
			for runEnd < len(runes) && unicode.IsUpper(runes[runEnd]) {
				runEnd++
			}
			runLen := runEnd - i

			matchedLen := 0
			for _, init := range goInitialismsSorted {
				il := len(init)
				if il > runLen {
					continue
				}
				// Compare runes[i:i+il] to init case-insensitively.
				ok := true
				for k := range il {
					if !unicode.IsUpper(runes[i+k]) {
						ok = false
						break
					}
					if toLowerByte(byte(runes[i+k])) != toLowerByte(init[k]) {
						ok = false
						break
					}
				}
				if !ok {
					continue
				}
				// When the initialism is shorter than the uppercase run,
				// only treat it as a boundary if the next rune begins a
				// lowercase word ("HTTPSConnection" → HTTPS + Connection).
				// Otherwise we'd split "AAPI" into "a"+"api"; better to
				// fall through to the standard PascalCase rule and let
				// the run remain glued.
				if il < runLen && i+il < len(runes) && !unicode.IsLower(runes[i+il]) {
					continue
				}
				matchedLen = il
				break
			}

			if matchedLen > 0 {
				flush()
				for k := 0; k < matchedLen; k++ {
					cur.WriteRune(runes[i+k])
				}
				flush()
				i += matchedLen
				continue
			}

			// Standard PascalCase split: any uppercase rune begins a
			// new segment unless it's part of an existing uppercase run
			// of length > 1 (e.g. "HTTPClient" → "HTTP" then "Client",
			// where "P" is the last uppercase before "C" lowercase'd).
			// The "look ahead one rune for lowercase" rule lets the
			// run of caps end exactly where a CamelCase word begins.
			flush()
			cur.WriteRune(r)
			i++
			for i < len(runes) && unicode.IsUpper(runes[i]) {
				// If the NEXT-NEXT rune is lowercase, THIS uppercase
				// is the start of the next CamelCase word — flush.
				if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					break
				}
				cur.WriteRune(runes[i])
				i++
			}
			continue
		}

		// Lowercase / digit / other — accumulate into the current segment.
		cur.WriteRune(r)
		i++
	}
	flush()

	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return strings.Join(out, "-")
}

func toLowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// ToExportedFieldName converts a lowercase package/field name to an exported
// Go identifier, respecting Go initialisms.
// Examples: "api" -> "API", "db" -> "DB", "orders" -> "Orders"
func ToExportedFieldName(pkg string) string {
	if len(pkg) == 0 {
		return pkg
	}
	if GoInitialismsMap[strings.ToLower(pkg)] {
		return strings.ToUpper(pkg)
	}
	return strings.ToUpper(pkg[:1]) + pkg[1:]
}

// kebabToSnake converts hyphens to underscores. Used as a single-pass
// transform on inputs known to be ASCII; declared at package scope so
// the strings.NewReplacer table is allocated once.
var kebabToSnake = strings.NewReplacer("-", "_")

// normalizeRepeatedUnderscores collapses runs of `__` into a single `_`
// and trims leading/trailing `_`. This shows up when the input had
// adjacent separators ("a--b__c") or when ToSnakeCase met an unusual
// boundary; the on-disk dir name and the Go package name must both be
// well-formed identifiers, so a stable normalization keeps codegen safe.
func normalizeRepeatedUnderscores(s string) string {
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	return strings.Trim(s, "_")
}

// GoPackage normalises a CLI/forge.yaml-style name into a Go package
// identifier in snake_case form. Hyphens convert to underscores; existing
// underscores are preserved; PascalCase / camelCase boundaries are split
// (so "AdminServer" → "admin_server", "calibrator_refit" stays
// "calibrator_refit", "admin-server" → "admin_server"). Distinct from
// ServicePackage in that it does NOT strip a trailing "Service" —
// callers using it for workers, operators, and arbitrary package leaves
// want the raw form.
//
// Snake_case is a valid Go package identifier (Go's spec allows it,
// `golint` only warns), and matches the on-disk convention proto buf
// emits for multi-word proto packages (e.g. proto package
// `services.admin_server.v1` → directory `services/admin_server/v1`).
// Keeping the package name aligned with the proto path means handler
// dirs, generated mock files, and wire_gen function names all stay in
// lockstep without forcing the user to choose between snake on disk and
// compact in code.
func GoPackage(name string) string {
	// camelCase / PascalCase → snake_case (handles HTTPStatus → http_status)
	snake := ToSnakeCase(name)
	// kebab → snake
	snake = kebabToSnake.Replace(snake)
	// fold to lowercase + collapse repeated separators
	return normalizeRepeatedUnderscores(strings.ToLower(snake))
}

// KCLIdentifier returns the KCL variable name a workload is declared
// under in `deploy/kcl/workloads.k`.
//
// The declaration is NAMED — `billing = fw.Workload {...}` rather than an
// anonymous list entry — because a name is what an environment refines:
// `wl.billing | {replicas = 3}`. An anonymous entry can only be
// reconstructed, which is the shape this vocabulary replaced.
//
// KCL identifiers follow the same rules as Go's, so this is GoPackage:
// "billing-admin" -> billing_admin, "BillingService" -> billing_service.
// Kept as its own function rather than a call to GoPackage at each site
// so the INTENT is legible (this is a KCL binding, not a Go package) and
// so the two can diverge if KCL's identifier rules ever do.
func KCLIdentifier(name string) string {
	return GoPackage(name)
}

// ServicePackage is the single canonical Go-package form for a service,
// binary, frontend, worker, or operator name.
//
// Inputs accepted:
//
//   - Forge / CLI names: kebab-case ("admin-server"), snake_case
//     ("admin_server"), or already-compact ("api").
//   - Proto service names: PascalCase ending in "Service"
//     ("EchoService", "AdminServerService").
//
// Output is always a single lowercase snake_case Go-style identifier.
// The PascalCase-with-"Service"-suffix branch first trims the suffix so
// "EchoService" -> "echo" and "AdminServerService" -> "admin_server".
// The pure-CLI branch normalises hyphens to underscores and PascalCase
// boundaries to underscores, so "admin-server" / "admin_server" /
// "AdminServer" all collapse to "admin_server".
//
// One canonical function for every site that emits a handler dir, a
// generated mock file, a bootstrap import, or a wire alias — keep it
// the single source of truth so the on-disk dir layout, the codegen
// keys, and the cleanup sweeper can never disagree.
//
// History: pre-2026-06-08 this function (and GoPackage) emitted compact
// form (separators stripped — "admin_server" → "adminserver"). The
// compact convention collided with the universal snake_case dir layout
// projects actually use (protoc-gen-go emits snake for multi-word proto
// packages, KCL package names use snake, every existing forge project
// on disk had snake handler dirs). The bug surface was a duplicate-dir
// failure where `forge generate` created `handlers/adminserver/`
// alongside the existing `handlers/admin_server/`, then regenerated
// wire_gen.go to reference the compact form while user-owned
// bootstrap.go still referenced the snake form — broken build.
func ServicePackage(name string) string {
	trimmed := strings.TrimSuffix(name, "Service")
	if trimmed == "" {
		trimmed = name
	}
	return GoPackage(trimmed)
}

// ServiceHookFile returns the canonical frontend hook filename for a
// service. Encodes the rule that the hook file is the service name in
// kebab-case (with initialisms kept glued) suffixed with `-hooks_gen.ts`.
//
// The `_gen` is the convention, not decoration: this file is Tier-1 — it is
// hash-guarded and rewritten on every `forge generate` — and forge's rule is
// now mechanical, so that a reader can tell what forge owns from the
// filename alone rather than by opening the file and reading its banner.
//
// All file-emitter sites AND the re-export indexer must go through this
// function. If a future caller needs a different suffix or extension,
// extract a parameterised helper rather than duplicating the kebab
// transform — the re-export index only stays in lockstep with on-disk
// filenames because both go through the same canonical splitter
// (`ToKebabCase`).
func ServiceHookFile(name string) string {
	return ToKebabCase(name) + "-hooks_gen.ts"
}

// ServiceHookFileLegacy returns the PRE-`_gen` hook filename for a service.
//
// It exists for exactly one caller: the retirement sweep that deletes the
// old copy out of a project generated before the rename. Nothing emits this
// name anymore.
func ServiceHookFileLegacy(name string) string {
	return ToKebabCase(name) + "-hooks.ts"
}

// ProtoPackageVersion extracts the proto API version from a fully-qualified
// proto package name — the LAST dotted segment when it has the protobuf
// version shape `v<major>` optionally suffixed with a stability channel
// (`v1`, `v2`, `v1alpha1`, `v2beta3`). Returns "" when the package carries
// no version segment.
//
//	"billing.v1"        -> "v1"
//	"acme.billing.v1"   -> "v1"
//	"shop.v2beta1"      -> "v2beta1"
//	"billing"           -> ""   (unversioned)
//	""                  -> ""
//
// This is the single source of truth for the version metadata the generated
// service inventory records (FORGE_SHAPE_REDESIGN — version-aware registry
// seam). It deliberately reads ONLY the package's own last segment, never
// the service name, so the inventory's Version field is exactly the proto
// API version and additive: a future `billing.v2` records Version "v2" as a
// second version of the same logical service rather than colliding identity.
func ProtoPackageVersion(protoPackage string) string {
	if protoPackage == "" {
		return ""
	}
	last := protoPackage
	if i := strings.LastIndex(protoPackage, "."); i >= 0 {
		last = protoPackage[i+1:]
	}
	if isProtoVersionSegment(last) {
		return last
	}
	return ""
}

// ProtoPackageBase returns the proto package with any trailing version
// segment stripped — the version-INDEPENDENT logical package identity.
//
//	"billing.v1"        -> "billing"
//	"acme.billing.v1"   -> "acme.billing"
//	"billing"           -> "billing"   (already unversioned)
//
// Pairing ProtoPackageBase + ProtoPackageVersion splits a fused
// `billing.v1` identity into ("billing", "v1") so the inventory can record
// the two distinctly. v2 then differs ONLY in Version, sharing the base —
// the data-model precondition for additive multi-version support.
func ProtoPackageBase(protoPackage string) string {
	i := strings.LastIndex(protoPackage, ".")
	if i < 0 {
		return protoPackage
	}
	if isProtoVersionSegment(protoPackage[i+1:]) {
		return protoPackage[:i]
	}
	return protoPackage
}

// isProtoVersionSegment reports whether seg is a protobuf version segment:
// 'v', then one or more digits (the major), optionally followed by a
// stability channel ("alpha"/"beta") and its own digits — matching the
// buf/protobuf convention (v1, v2, v1alpha1, v2beta3). Anything else
// (e.g. a domain segment like "billing" or "v" alone) is not a version.
func isProtoVersionSegment(seg string) bool {
	if len(seg) < 2 || seg[0] != 'v' {
		return false
	}
	r := seg[1:]
	// major digits
	n := 0
	for n < len(r) && r[n] >= '0' && r[n] <= '9' {
		n++
	}
	if n == 0 {
		return false
	}
	r = r[n:]
	if r == "" {
		return true // plain vN
	}
	// optional stability channel + its digits
	for _, ch := range []string{"alpha", "beta"} {
		if rest, ok := strings.CutPrefix(r, ch); ok {
			if rest == "" {
				return false // "v1alpha" with no channel number is malformed
			}
			for i := 0; i < len(rest); i++ {
				if rest[i] < '0' || rest[i] > '9' {
					return false
				}
			}
			return true
		}
	}
	return false
}
