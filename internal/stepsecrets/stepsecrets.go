// Package stepsecrets is the pure core of declared secrets: finding the
// {{ secrets.NAME }} placeholders in a step's inputs, the digest a resolve
// ticket is bound to, substituting resolved values, and masking them again in
// everything a step returns.
//
// A placeholder is not CEL. Workflow state and Temporal history carry it as
// literal text; only inside an activity is it replaced, and the value never
// leaves that activity: Mask runs over every output before it is returned.
//
//forge:exclude-contract: pure helpers shared by the step activities and the workflow runtime
package stepsecrets

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/types/known/structpb"
)

// Namespace is the identifier a placeholder reads: {{ secrets.NAME }}.
const Namespace = "secrets"

// Mask replaces a resolved value wherever it, or an encoding of it, appears.
const Mask = "***"

// NamePattern is a secret name, and the environment variable it is exported as.
// It is the control plane's tenantsecret charset, so a name is valid on both
// sides of the wire.
var NamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// plainSecretExpr is the one expression a placeholder may be: secrets.NAME.
var plainSecretExpr = regexp.MustCompile(`^secrets\.([A-Za-z_][A-Za-z0-9_]*)$`)

// PlainName returns NAME when expr is exactly secrets.NAME.
func PlainName(expr string) (string, bool) {
	m := plainSecretExpr.FindStringSubmatch(strings.TrimSpace(expr))
	if m == nil {
		return "", false
	}
	return m[1], true
}

// A placeholder is rendered into a marker by the runtime, before any template
// is evaluated, and the activity recognises only markers. Data that reaches a
// step through an expression (a PR title, an issue body, a model's output) could
// otherwise spell a placeholder and be substituted with a real value, so every
// string an expression produces is passed through Sanitize, which breaks the
// marker introducer. Only text the workflow author wrote can become a marker.
const (
	markerOpen  = "\u27e6secret:" // ⟦secret:
	markerClose = "\u27e7"        // ⟧
	// defanged replaces markerOpen in data: visibly different, never a marker.
	defanged = "\u27e6 secret:"
)

// Marker is the rendered form of a placeholder inside a step's inputs.
func Marker(name string) string { return markerOpen + name + markerClose }

var markerPattern = regexp.MustCompile(markerOpen + `([A-Za-z_][A-Za-z0-9_]*)` + markerClose)

// Sanitize breaks any marker introducer in data so it cannot be mistaken for a
// placeholder the author wrote.
func Sanitize(s string) string {
	if !strings.Contains(s, markerOpen) {
		return s
	}
	return strings.ReplaceAll(s, markerOpen, defanged)
}

// SanitizeAny sanitizes every string in decoded JSON (values and map keys).
func SanitizeAny(in any) any {
	switch x := in.(type) {
	case string:
		return Sanitize(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[Sanitize(k)] = SanitizeAny(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = SanitizeAny(item)
		}
		return out
	case []string:
		out := make([]string, len(x))
		for i, item := range x {
			out[i] = Sanitize(item)
		}
		return out
	default:
		return in
	}
}

// Render evaluates a template whose authored {{ secrets.NAME }} holes become
// markers and whose every other hole is data. eval is the template engine (it
// knows no `secrets` namespace); Render hides the authored holes from it behind
// a one-use random token, sanitizes whatever it produced, and only then turns
// the tokens into markers. A string that data could write never becomes one: the
// token is unknown to anyone who writes data, and Sanitize breaks the marker
// introducer in everything the engine returned.
func Render(s string, eval func(string) (interface{}, error)) (interface{}, error) {
	if !strings.Contains(s, "{{") {
		return eval(s)
	}
	matches, err := wfcel.ScanTemplate(s)
	if err != nil {
		return eval(s)
	}
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return nil, errors.New("cannot render a secret placeholder safely")
	}
	tok := "\x00" + hex.EncodeToString(buf[:]) + ":"
	var out strings.Builder
	last, found := 0, false
	for _, m := range matches {
		if name, ok := PlainName(m.Expr); ok {
			out.WriteString(s[last:m.Start])
			out.WriteString(tok + name + "\x00")
			last, found = m.End, true
		}
	}
	if !found {
		res, err := eval(s)
		if err != nil {
			return nil, err
		}
		return SanitizeAny(res), nil
	}
	out.WriteString(s[last:])
	res, err := eval(out.String())
	if err != nil {
		return nil, err
	}
	return restoreTokens(SanitizeAny(res), tok), nil
}

func restoreTokens(in any, tok string) any {
	switch x := in.(type) {
	case string:
		if !strings.Contains(x, tok) {
			return x
		}
		var out strings.Builder
		rest := x
		for {
			i := strings.Index(rest, tok)
			if i < 0 {
				out.WriteString(rest)
				return out.String()
			}
			out.WriteString(rest[:i])
			rest = rest[i+len(tok):]
			j := strings.IndexByte(rest, 0)
			if j < 0 {
				out.WriteString(tok + rest)
				return out.String()
			}
			out.WriteString(Marker(rest[:j]))
			rest = rest[j+1:]
		}
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, v := range x {
			m[k] = restoreTokens(v, tok)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, v := range x {
			l[i] = restoreTokens(v, tok)
		}
		return l
	default:
		return in
	}
}

// AuthoredNames returns the distinct names an UNRENDERED template reads as
// placeholders, in first-seen order. Validation and disclosure use it.
func AuthoredNames(s string) []string {
	matches, err := wfcel.ScanTemplate(s)
	if err != nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range matches {
		if name, ok := PlainName(m.Expr); ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// HasAuthored reports whether a template contains an authored placeholder.
func HasAuthored(s string) bool { return len(AuthoredNames(s)) > 0 }

// Names returns the distinct names s carries as markers, in first-seen order.
func Names(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range markerPattern.FindAllStringSubmatch(s, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return out
}

// Unprotect turns the markers back into the {{ secrets.NAME }} text the author
// wrote, for anything shown to a person (the command in a run's output, a log).
func Unprotect(s string) string {
	if !strings.Contains(s, markerOpen) {
		return s
	}
	return markerPattern.ReplaceAllStringFunc(s, func(found string) string {
		return "{{ " + Namespace + "." + markerPattern.FindStringSubmatch(found)[1] + " }}"
	})
}

// ShellRefs rewrites the markers in a shell command to ${NAME}, so the value
// reaches the command through its environment and never through its text: it is
// not in the command line, a process listing or any stored copy of the command.
func ShellRefs(s string) string {
	if !strings.Contains(s, markerOpen) {
		return s
	}
	return markerPattern.ReplaceAllStringFunc(s, func(found string) string {
		return "${" + markerPattern.FindStringSubmatch(found)[1] + "}"
	})
}

// AuthoredNamesInValue returns the distinct authored placeholder names anywhere
// in a structured value, sorted.
func AuthoredNamesInValue(v *structpb.Value) []string {
	set := map[string]bool{}
	walkStrings(v, func(s string) {
		for _, n := range AuthoredNames(s) {
			set[n] = true
		}
	})
	return sortedKeys(set)
}

// ValueNames returns the distinct marker names anywhere in a structured value,
// sorted.
func ValueNames(v *structpb.Value) []string {
	set := map[string]bool{}
	walkStrings(v, func(s string) {
		for _, n := range Names(s) {
			set[n] = true
		}
	})
	return sortedKeys(set)
}

func walkStrings(v *structpb.Value, fn func(string)) {
	switch k := v.GetKind().(type) {
	case *structpb.Value_StringValue:
		fn(k.StringValue)
	case *structpb.Value_StructValue:
		for _, f := range k.StructValue.GetFields() {
			walkStrings(f, fn)
		}
	case *structpb.Value_ListValue:
		for _, item := range k.ListValue.GetValues() {
			walkStrings(item, fn)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DeniedError is the controller's refusal to resolve: one printable line that
// names the fix and discloses nothing foreign, safe to show the workflow's
// author. Any other resolve failure is reported generically.
type DeniedError struct{ Reason string }

func (e *DeniedError) Error() string { return e.Reason }

// Ref names one secret by environment and name. It is data, not a secret.
type Ref struct {
	Environment string `json:"environment"`
	Name        string `json:"name"`
}

// CanonicalRefs collapses duplicates and sorts by environment, then name,
// bytewise. The control plane computes the same form.
func CanonicalRefs(refs []Ref) []Ref {
	seen := make(map[Ref]bool, len(refs))
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Environment != out[j].Environment {
			return out[i].Environment < out[j].Environment
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// RefsDigest is the lowercase hex sha256 of the compact JSON of CanonicalRefs
// (no HTML escaping): the request_sha256 a resolve ticket is bound to.
func RefsDigest(refs []Ref) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(CanonicalRefs(refs))
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:])
}

// Values holds resolved secret values for the life of one activity attempt,
// keyed by name. It never prints its contents.
type Values map[string]string

func (Values) String() string   { return "stepsecrets.Values{redacted}" }
func (Values) GoString() string { return "stepsecrets.Values{redacted}" }

// Substitute replaces every marker in s with its value. A marker
// whose name has no value is left in place. Substitution is one pass: a value
// that itself looks like a marker is data, not a second lookup.
func (v Values) Substitute(s string) string {
	if !strings.Contains(s, markerOpen) {
		return s
	}
	return markerPattern.ReplaceAllStringFunc(s, func(found string) string {
		if val, ok := v[markerPattern.FindStringSubmatch(found)[1]]; ok {
			return val
		}
		return found
	})
}

// SubstituteValue returns a copy of a structured value with every string
// substituted, so a secret in a header or body reaches the request intact.
func (v Values) SubstituteValue(in *structpb.Value) *structpb.Value {
	switch k := in.GetKind().(type) {
	case *structpb.Value_StringValue:
		return structpb.NewStringValue(v.Substitute(k.StringValue))
	case *structpb.Value_StructValue:
		fields := make(map[string]*structpb.Value, len(k.StructValue.GetFields()))
		for name, f := range k.StructValue.GetFields() {
			fields[name] = v.SubstituteValue(f)
		}
		return structpb.NewStructValue(&structpb.Struct{Fields: fields})
	case *structpb.Value_ListValue:
		items := make([]*structpb.Value, 0, len(k.ListValue.GetValues()))
		for _, item := range k.ListValue.GetValues() {
			items = append(items, v.SubstituteValue(item))
		}
		return structpb.NewListValue(&structpb.ListValue{Values: items})
	default:
		return in
	}
}

// SubstituteAny is SubstituteValue for decoded JSON (map[string]any, []any, string).
func (v Values) SubstituteAny(in any) any {
	switch x := in.(type) {
	case string:
		return v.Substitute(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = v.SubstituteAny(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = v.SubstituteAny(item)
		}
		return out
	default:
		return in
	}
}

// Env returns the values as environment variables named after the secrets.
func (v Values) Env() map[string]string {
	out := make(map[string]string, len(v))
	for name, val := range v {
		out[name] = val
	}
	return out
}

// Masker replaces resolved values, and their encodings, with Mask.
type Masker struct{ forms []string }

// NewMasker builds a masker for the given values. For each value it masks the
// raw text and the forms a value takes when something echoes it: standard and
// URL-safe base64 (padded and not), URL query and path escapes, and the JSON
// string escape. Longer forms are replaced first so a form that contains a
// shorter one is not left half-masked.
func NewMasker(values ...string) *Masker {
	set := map[string]bool{}
	add := func(s string) {
		// A one- or two-byte value would shred ordinary text. The control plane
		// refuses short values; this is the matching floor on this side.
		if len(s) >= 4 {
			set[s] = true
		}
	}
	for _, val := range values {
		if val == "" {
			continue
		}
		add(val)
		raw := []byte(val)
		add(base64.StdEncoding.EncodeToString(raw))
		add(base64.RawStdEncoding.EncodeToString(raw))
		add(base64.URLEncoding.EncodeToString(raw))
		add(base64.RawURLEncoding.EncodeToString(raw))
		add(url.QueryEscape(val))
		add(url.PathEscape(val))
		if j, err := json.Marshal(val); err == nil && len(j) > 2 {
			add(string(j[1 : len(j)-1]))
		}
		// A line-by-line view: a multi-line value is masked per line too, so a
		// tool that prints it indented or prefixed does not leave a line behind.
		if strings.ContainsAny(val, "\r\n") {
			for _, line := range strings.FieldsFunc(val, func(r rune) bool { return r == '\n' || r == '\r' }) {
				add(strings.TrimSpace(line))
			}
		}
	}
	forms := make([]string, 0, len(set))
	for f := range set {
		forms = append(forms, f)
	}
	sort.Slice(forms, func(i, j int) bool {
		if len(forms[i]) != len(forms[j]) {
			return len(forms[i]) > len(forms[j])
		}
		return forms[i] < forms[j]
	})
	return &Masker{forms: forms}
}

// Empty reports whether the masker has nothing to mask.
func (m *Masker) Empty() bool { return m == nil || len(m.forms) == 0 }

// Mask returns s with every registered form replaced.
func (m *Masker) Mask(s string) string {
	if m.Empty() || s == "" {
		return s
	}
	for _, f := range m.forms {
		if strings.Contains(s, f) {
			s = strings.ReplaceAll(s, f, Mask)
		}
	}
	return s
}

// MaskError returns an error whose text is masked and whose cause chain is
// dropped when it could carry a value.
func (m *Masker) MaskError(err error) error {
	if err == nil || m.Empty() {
		return err
	}
	if masked := m.Mask(err.Error()); masked != err.Error() {
		return maskedError(masked)
	}
	return err
}

type maskedError string

func (e maskedError) Error() string { return string(e) }

// MaskAny masks every string in decoded JSON.
func (m *Masker) MaskAny(in any) any {
	switch x := in.(type) {
	case string:
		return m.Mask(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[m.Mask(k)] = m.MaskAny(item)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = m.MaskAny(item)
		}
		return out
	default:
		return in
	}
}

// MaskValue returns a copy of a structured value with every string masked.
func (m *Masker) MaskValue(in *structpb.Value) *structpb.Value {
	if m.Empty() || in == nil {
		return in
	}
	switch k := in.GetKind().(type) {
	case *structpb.Value_StringValue:
		return structpb.NewStringValue(m.Mask(k.StringValue))
	case *structpb.Value_StructValue:
		return structpb.NewStructValue(m.MaskStruct(k.StructValue))
	case *structpb.Value_ListValue:
		items := make([]*structpb.Value, 0, len(k.ListValue.GetValues()))
		for _, item := range k.ListValue.GetValues() {
			items = append(items, m.MaskValue(item))
		}
		return structpb.NewListValue(&structpb.ListValue{Values: items})
	default:
		return in
	}
}

// MaskStruct returns a copy of a struct with every key and string masked.
func (m *Masker) MaskStruct(s *structpb.Struct) *structpb.Struct {
	if s == nil || m.Empty() {
		return s
	}
	fields := make(map[string]*structpb.Value, len(s.GetFields()))
	for name, f := range s.GetFields() {
		fields[m.Mask(name)] = m.MaskValue(f)
	}
	return &structpb.Struct{Fields: fields}
}
