// Package stepsecrets is the pure core of declared secrets: finding the
// {{ secrets.NAME }} placeholders in a step's inputs, the markers they render
// to, and the digest a resolve ticket is bound to.
//
// A placeholder is not CEL. Workflow state and Temporal history carry it as a
// marker; only the action activity resolves it, and then only into a request's
// credential slot (a header), never into text a step returns.
//
//forge:exclude-contract: pure helpers shared by the step activities and the workflow runtime
package stepsecrets

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"google.golang.org/protobuf/types/known/structpb"
)

// Namespace is the identifier a placeholder reads: {{ secrets.NAME }}.
const Namespace = "secrets"

// NamePattern is a secret name. It is the control plane's tenantsecret charset,
// so a name is valid on both sides of the wire.
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

// HasMarker reports whether s carries a marker, or anything that starts one.
func HasMarker(s string) bool { return strings.Contains(s, markerOpen) }

// CredentialSlot splits a rendered value that is one credential slot: a literal
// prefix (possibly empty, e.g. "Bearer ") followed by exactly one marker, which
// ends the value. Anything else (no marker, two, text after it) is not a slot.
// The prefix is what the author wrote around the placeholder, so it is sent as
// is, ahead of the resolved value.
func CredentialSlot(s string) (prefix, name string, ok bool) {
	if strings.Count(s, markerOpen) != 1 || !strings.HasSuffix(s, markerClose) {
		return "", "", false
	}
	at := strings.Index(s, markerOpen)
	m := markerPattern.FindStringSubmatch(s[at:])
	if m == nil || len(m[0]) != len(s)-at {
		return "", "", false
	}
	return s[:at], m[1], true
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
