// Copyright (c) 2025 Reliant Labs

package alerting

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"text/template/parse"
	"unicode/utf8"
)

// ANNOTATION TEMPLATES.
//
// An annotation is a Go text/template in the Prometheus alerting convention:
// `$labels` / `.Labels` is the instance's label set, `$value` / `.Value` its
// sample, and `{{ printf "%.0f" $value }}` formats it. A rule's annotations are
// written by whoever owns the rule, which on a hosted control plane is a
// customer, so the language is the useful subset with every unbounded construct
// taken out:
//
//   - No loops and no template calls: `range`, `break`, `continue`, `define`,
//     `block` and `template` are refused at parse time, so execution is linear in
//     the template's length.
//   - printf refuses a width or precision over maxFormatWidth, `*` and argument
//     indexes, so one verb cannot allocate gigabytes.
//   - Output stops at the annotation limit (maxLabelValueBytes).
//
// The functions are AnnotationFuncs plus text/template's builtins (and, or,
// not, eq, ne, lt, le, gt, ge, len, index, slice, print, println, html, js,
// urlquery). A template that does not parse or fails to execute is sent as its
// raw text: a broken summary must never stop an alert from firing.

// maxFormatWidth bounds a printf verb's width and precision.
const maxFormatWidth = 64

// annotationPrelude binds the Prometheus variables. It is on the template's
// first line, so a parse error's line number is the author's.
const annotationPrelude = "{{$labels := .Labels}}{{$value := .Value}}"

// AnnotationFuncs names the functions an annotation template may call beyond
// text/template's builtins, for docs and error messages.
func AnnotationFuncs() []string {
	out := make([]string, 0, len(annotationFuncs))
	for name := range annotationFuncs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

var annotationFuncs = template.FuncMap{
	"printf":             annotationPrintf,
	"humanize":           humanize,
	"humanize1024":       humanize1024,
	"humanizeDuration":   humanizeDuration,
	"humanizePercentage": humanizePercentage,
	"toUpper":            strings.ToUpper,
	"toLower":            strings.ToLower,
	// The sprig float arithmetic Prometheus-style rules reach for, e.g.
	// {{ printf "%.0f" (mulf $value 100.0) }} for a ratio as a percentage.
	"addf": func(a any, v ...any) (float64, error) {
		return foldf(a, v, func(x, y float64) float64 { return x + y })
	},
	"subf": func(a any, v ...any) (float64, error) {
		return foldf(a, v, func(x, y float64) float64 { return x - y })
	},
	"mulf": func(a any, v ...any) (float64, error) {
		return foldf(a, v, func(x, y float64) float64 { return x * y })
	},
	"divf": func(a any, v ...any) (float64, error) {
		return foldf(a, v, func(x, y float64) float64 { return x / y })
	},
}

// sampleValue is `$value`. It prints as it always has (six significant digits)
// and formats as the float it is under printf's numeric verbs.
type sampleValue float64

func (v sampleValue) String() string { return strconv.FormatFloat(float64(v), 'g', 6, 64) }

// annotationData is the template's dot.
type annotationData struct {
	Labels map[string]string
	Value  sampleValue
}

// ValidateAnnotations reports the first annotation whose template cannot
// render: it does not parse, uses a refused construct, calls an unknown
// function, or hands printf a constant format it refuses. It is the
// render-time check; evaluation itself never fails on an annotation.
func ValidateAnnotations(annotations map[string]string) error {
	for _, k := range sortedKeys(annotations) {
		if _, err := parseAnnotation(annotations[k]); err != nil {
			return fmt.Errorf("annotation %q: %w", k, err)
		}
	}
	return nil
}

// annotationTemplates is a rule's annotations, each parsed once per evaluation
// rather than once per row.
type annotationTemplates struct {
	raw  map[string]string
	tmpl map[string]*template.Template
	errs map[string]error
}

func compileAnnotations(in map[string]string) *annotationTemplates {
	a := &annotationTemplates{raw: in, tmpl: map[string]*template.Template{}, errs: map[string]error{}}
	for k, v := range in {
		t, err := parseAnnotation(v)
		if err != nil {
			a.errs[k] = err
			continue
		}
		if t != nil {
			a.tmpl[k] = t
		}
	}
	return a
}

// expand renders every annotation for one instance. The error names every
// annotation that fell back to its raw text, for the caller to log.
func (a *annotationTemplates) expand(labels map[string]string, value float64) (map[string]string, error) {
	if len(a.raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(a.raw))
	var errs error
	for _, k := range sortedKeys(a.raw) {
		v := a.raw[k]
		if err := a.errs[k]; err != nil {
			errs = errors.Join(errs, fmt.Errorf("annotation %q: %w", k, err))
		} else if t := a.tmpl[k]; t != nil {
			s, err := execAnnotation(t, labels, value)
			if err != nil {
				errs = errors.Join(errs, fmt.Errorf("annotation %q: %w", k, err))
			} else {
				v = s
			}
		}
		out[k] = truncate(v)
	}
	return out, errs
}

// parseAnnotation parses one template. Plain text is no template (nil, nil).
func parseAnnotation(text string) (*template.Template, error) {
	if !strings.Contains(text, "{{") {
		return nil, nil
	}
	t, err := template.New("annotation").Option("missingkey=zero").Funcs(annotationFuncs).Parse(annotationPrelude + text)
	if err != nil {
		return nil, err
	}
	if len(t.Templates()) > 1 {
		return nil, errors.New("define and block are not allowed in an annotation")
	}
	if err := checkNodes(t.Root); err != nil {
		return nil, err
	}
	return t, nil
}

// checkNodes refuses the unbounded constructs and checks constant printf
// formats.
func checkNodes(n parse.Node) error {
	switch n := n.(type) {
	case *parse.ListNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Nodes {
			if err := checkNodes(c); err != nil {
				return err
			}
		}
	case *parse.RangeNode, *parse.BreakNode, *parse.ContinueNode:
		return errors.New("range is not allowed in an annotation")
	case *parse.TemplateNode:
		return errors.New("template is not allowed in an annotation")
	case *parse.IfNode:
		return checkBranch(&n.BranchNode)
	case *parse.WithNode:
		return checkBranch(&n.BranchNode)
	case *parse.ActionNode:
		return checkNodes(n.Pipe)
	case *parse.PipeNode:
		if n == nil {
			return nil
		}
		for _, c := range n.Cmds {
			if err := checkNodes(c); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		if len(n.Args) >= 2 {
			if id, ok := n.Args[0].(*parse.IdentifierNode); ok && id.Ident == "printf" {
				if s, ok := n.Args[1].(*parse.StringNode); ok {
					if _, err := scanFormat(s.Text); err != nil {
						return fmt.Errorf("printf %q: %w", s.Text, err)
					}
				}
			}
		}
		for _, a := range n.Args {
			if err := checkNodes(a); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkBranch(b *parse.BranchNode) error {
	for _, n := range []parse.Node{b.Pipe, b.List, b.ElseList} {
		if err := checkNodes(n); err != nil {
			return err
		}
	}
	return nil
}

// errAnnotationFull stops execution once the output is past what is kept.
var errAnnotationFull = errors.New("annotation output limit reached")

type boundedWriter struct {
	b     strings.Builder
	limit int
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if room := w.limit - w.b.Len(); len(p) > room {
		w.b.Write(p[:max(room, 0)])
		return max(room, 0), errAnnotationFull
	}
	return w.b.Write(p)
}

func execAnnotation(t *template.Template, labels map[string]string, value float64) (string, error) {
	// A few bytes past the limit, so truncate cuts on a rune boundary.
	w := &boundedWriter{limit: maxLabelValueBytes + 4}
	if err := t.Execute(w, annotationData{Labels: labels, Value: sampleValue(value)}); err != nil && !errors.Is(err, errAnnotationFull) {
		return "", err
	}
	return w.b.String(), nil
}

// annotationPrintf is fmt.Sprintf with bounded verbs. A numeric verb (%d, %e,
// %f, %g) formats any number, and text that parses as one: a label is text, so
// {{ printf "%.0f" $labels.count }} rounds the number the label holds.
func annotationPrintf(format string, args ...any) (string, error) {
	verbs, err := scanFormat(format)
	if err != nil {
		return "", fmt.Errorf("printf %q: %w", format, err)
	}
	for i := range args {
		if i >= len(verbs) {
			break
		}
		args[i] = coerceForVerb(verbs[i], args[i])
	}
	return fmt.Sprintf(format, args...), nil
}

// scanFormat returns the verb of every argument a format consumes, refusing a
// width or precision over maxFormatWidth, `*` and explicit argument indexes.
func scanFormat(format string) ([]rune, error) {
	var verbs []rune
	for i := 0; i < len(format); i++ {
		if format[i] != '%' {
			continue
		}
		i++
		if i < len(format) && format[i] == '%' {
			continue
		}
		for i < len(format) && strings.IndexByte("+-# 0", format[i]) >= 0 {
			i++
		}
		var err error
		if i, err = boundedNumber(format, i, "width"); err != nil {
			return nil, err
		}
		if i < len(format) && format[i] == '.' {
			if i, err = boundedNumber(format, i+1, "precision"); err != nil {
				return nil, err
			}
		}
		if i >= len(format) {
			break
		}
		if format[i] == '[' {
			return nil, errors.New("argument indexes are not supported")
		}
		r, size := utf8.DecodeRuneInString(format[i:])
		i += size - 1
		verbs = append(verbs, r)
	}
	return verbs, nil
}

func boundedNumber(format string, i int, what string) (int, error) {
	if i < len(format) && format[i] == '*' {
		return i, fmt.Errorf("a %s from an argument (*) is not supported", what)
	}
	if i < len(format) && format[i] == '[' {
		return i, errors.New("argument indexes are not supported")
	}
	start := i
	for i < len(format) && format[i] >= '0' && format[i] <= '9' {
		i++
	}
	if i > start {
		if n, err := strconv.Atoi(format[start:i]); err != nil || n > maxFormatWidth {
			return i, fmt.Errorf("%s %s is over the limit of %d", what, format[start:i], maxFormatWidth)
		}
	}
	return i, nil
}

func coerceForVerb(verb rune, arg any) any {
	switch verb {
	case 'e', 'E', 'f', 'F', 'g', 'G':
		if f, ok := numberOf(arg); ok {
			return f
		}
	case 'd':
		if f, ok := numberOf(arg); ok && !math.IsNaN(f) && math.Abs(f) < 1<<63 {
			return int64(math.Round(f))
		}
	}
	return arg
}

// numberOf reads a number from a number or from text holding one.
func numberOf(v any) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.String:
		f, err := strconv.ParseFloat(strings.TrimSpace(rv.String()), 64)
		return f, err == nil
	}
	return 0, false
}

func toNumber(v any) (float64, error) {
	f, ok := numberOf(v)
	if !ok {
		return 0, fmt.Errorf("%q is not a number", fmt.Sprint(v))
	}
	return f, nil
}

func foldf(a any, rest []any, op func(x, y float64) float64) (float64, error) {
	acc, err := toNumber(a)
	if err != nil {
		return 0, err
	}
	for _, v := range rest {
		f, err := toNumber(v)
		if err != nil {
			return 0, err
		}
		acc = op(acc, f)
	}
	return acc, nil
}

// The humanize family is Prometheus's, so a summary written for Prometheus
// reads the same here.

func humanize(v any) (string, error) {
	f, err := toNumber(v)
	if err != nil {
		return "", err
	}
	if f == 0 || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%.4g", f), nil
	}
	prefix := ""
	if math.Abs(f) >= 1 {
		for _, p := range []string{"k", "M", "G", "T", "P", "E", "Z", "Y"} {
			if math.Abs(f) < 1000 {
				break
			}
			prefix, f = p, f/1000
		}
		return fmt.Sprintf("%.4g%s", f, prefix), nil
	}
	for _, p := range []string{"m", "u", "n", "p", "f", "a", "z", "y"} {
		if math.Abs(f) >= 1 {
			break
		}
		prefix, f = p, f*1000
	}
	return fmt.Sprintf("%.4g%s", f, prefix), nil
}

func humanize1024(v any) (string, error) {
	f, err := toNumber(v)
	if err != nil {
		return "", err
	}
	if math.Abs(f) <= 1 || math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%.4g", f), nil
	}
	prefix := ""
	for _, p := range []string{"ki", "Mi", "Gi", "Ti", "Pi", "Ei", "Zi", "Yi"} {
		if math.Abs(f) < 1024 {
			break
		}
		prefix, f = p, f/1024
	}
	return fmt.Sprintf("%.4g%s", f, prefix), nil
}

func humanizeDuration(v any) (string, error) {
	f, err := toNumber(v)
	if err != nil {
		return "", err
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Sprintf("%.4g", f), nil
	}
	if f == 0 {
		return fmt.Sprintf("%.4gs", f), nil
	}
	if math.Abs(f) >= 1 {
		sign := ""
		if f < 0 {
			sign, f = "-", -f
		}
		d := int64(f)
		seconds, minutes, hours, days := d%60, (d/60)%60, (d/3600)%24, d/86400
		switch {
		case days != 0:
			return fmt.Sprintf("%s%dd %dh %dm %ds", sign, days, hours, minutes, seconds), nil
		case hours != 0:
			return fmt.Sprintf("%s%dh %dm %ds", sign, hours, minutes, seconds), nil
		case minutes != 0:
			return fmt.Sprintf("%s%dm %ds", sign, minutes, seconds), nil
		}
		return fmt.Sprintf("%s%.4gs", sign, f), nil
	}
	prefix := ""
	for _, p := range []string{"m", "u", "n", "p", "f", "a", "z", "y"} {
		if math.Abs(f) >= 1 {
			break
		}
		prefix, f = p, f*1000
	}
	return fmt.Sprintf("%.4g%ss", f, prefix), nil
}

func humanizePercentage(v any) (string, error) {
	f, err := toNumber(v)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%.4g%%", f*100), nil
}
