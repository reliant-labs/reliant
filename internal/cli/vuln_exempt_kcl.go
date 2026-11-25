package cli

// The KCL edit behind `forge vuln exempt`: state one forge.VulnExemption in
// an env's main.k.
//
// KCL'S OWN PARSER DECIDES WHAT IS CODE. The file is parsed by the KCL
// runtime forge already embeds (kcl-go's native ParseFile), and every
// decision below is made on its AST: which literals are VulnExemption,
// VulnPolicy, DeliveryPolicy and Bundle schema expressions, which fields they
// set, and where each one starts and ends. Nothing is matched by regex
// against source text, so a `vuln_gate = …` in a comment, an `id = "…"`
// inside a string (escapes included) or a computed expression is never
// mistaken for a declaration.
//
// WHY NOT KCL's OVERRIDE API (kcl.OverrideFile, `kcl -O`). It re-prints the
// whole module from the AST, so the edit would reformat the file and could
// drop the author's comments: a one-line acceptance would arrive as a diff of
// the whole file. And its specs address a top-level path
// (`bundle.delivery_policy.vuln_gate.exemptions`): they cannot say "the one
// element of this list whose id is X", nor see a policy that is assigned to
// a variable and passed along. So the AST locates, and the edit is a splice
// of new text at an offset the AST gave — the original bytes around it are
// kept as they were.
//
// WHAT IT EDITS, and only when nothing else could be the env's policy:
//
//  1. a literal forge.VulnExemption with the same id: its span is replaced;
//  2. else the vuln_gate's `exemptions = [...]` list literal gains an
//     element, or the VulnPolicy literal gains `exemptions = [...]`;
//  3. else the DeliveryPolicy literal gains a `vuln_gate`;
//  4. else the one Bundle literal gains a `delivery_policy`.
//
// FAIL CLOSED. It refuses to write — and prints the exact lines to add by
// hand — whenever it cannot be sure which declaration is effective, or that
// the id is not already accepted somewhere it cannot see:
//
//   - a VulnExemption whose id, or (for this id) any field, is not a plain
//     string literal;
//   - the id appearing in any other string (a reason, a computed list, an
//     f-string), or only in comments;
//   - more than one delivery_policy / DeliveryPolicy / vuln_gate /
//     VulnPolicy / exemptions declaration, or any of them computed (a
//     variable, a function call, a union with a value, an unpacked dict);
//   - a Bundle beside a DeliveryPolicy literal that is not its
//     delivery_policy field (an "independent" policy);
//   - a file the parser reports any error in.
//
// After the splice the result is parsed again and checked: it differs from
// the original only inside the inserted or updated text, it holds exactly
// one literal exemption of the id with the requested values, every other
// exemption literal is unchanged, and no declaration was added beyond the
// ones the edit meant to add. The caller then renders the env and restores
// the file unless the rendered policy carries the exemption.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"kcl-lang.io/kcl-go/pkg/kcl"
	"kcl-lang.io/kcl-go/pkg/spec/gpyrpc"
)

// vulnExemptionLiteral is the KCL for one exemption, on one line.
func vulnExemptionLiteral(alias, id, reason, expires string) string {
	return fmt.Sprintf("%s.VulnExemption {id = %s, reason = %s, expires = %s}", alias, kclQuote(id), kclQuote(reason), kclQuote(expires))
}

// kclQuote is a KCL double-quoted string literal. The caller refuses what
// it cannot spell safely (a newline, `${`, which KCL would interpolate).
func kclQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\t", `\t`)
	return `"` + r.Replace(s) + `"`
}

// errVulnExemptNoAnchor is the refusal when the file has no single place to
// state the exemption, or the edit cannot be proven safe.
type errVulnExemptNoAnchor struct {
	why     string
	snippet string
}

func (e *errVulnExemptNoAnchor) Error() string {
	return e.why + "\n  Nothing was written. Add it by hand where the env declares its delivery policy:\n\n" + e.snippet
}

// ─── The AST, as kcl-go's native parser returns it ─────────────────────────

// kclAST is one parsed file: its source, where each line starts, and the
// JSON AST (each node a wrapper {node, line, column, end_line, end_column};
// lines 1-based, columns 0-based in characters).
type kclAST struct {
	src        string
	lineStarts []int
	body       any
	comments   []string
}

func parseKCLForEdit(src string) (*kclAST, error) {
	resp, err := kcl.Service().ParseFile(&gpyrpc.ParseFileArgs{Path: "main.k", Source: src})
	if err != nil {
		return nil, err
	}
	for _, e := range resp.GetErrors() {
		// ParseFile also resolves imports, and a lone file cannot: `import
		// forge` resolves from the forge binary, `..workloads` from the
		// project. An unresolved module is not a syntax problem; every other
		// error refuses (fail closed).
		// Suggestions and warnings are advice, not errors.
		if strings.Contains(e.GetCode(), "CannotFindModule") || !strings.EqualFold(e.GetLevel(), "error") {
			continue
		}
		var msgs []string
		for _, m := range e.GetMessages() {
			where := ""
			if p := m.GetPos(); p != nil {
				where = fmt.Sprintf("line %d: ", p.GetLine())
			}
			msgs = append(msgs, where+m.GetMsg())
		}
		return nil, fmt.Errorf("KCL reports %s %s: %s", e.GetLevel(), e.GetCode(), strings.Join(msgs, "; "))
	}
	var root struct {
		Body     json.RawMessage `json:"body"`
		Comments []struct {
			Node struct {
				Text string `json:"text"`
			} `json:"node"`
		} `json:"comments"`
	}
	if err := json.Unmarshal([]byte(resp.GetAstJson()), &root); err != nil {
		return nil, fmt.Errorf("read KCL's AST: %w", err)
	}
	a := &kclAST{src: src, lineStarts: []int{0}}
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			a.lineStarts = append(a.lineStarts, i+1)
		}
	}
	if err := json.Unmarshal(root.Body, &a.body); err != nil {
		return nil, fmt.Errorf("read KCL's AST: %w", err)
	}
	for _, c := range root.Comments {
		a.comments = append(a.comments, c.Node.Text)
	}
	return a, nil
}

// offset converts a (line, character column) position to a byte offset.
func (a *kclAST) offset(line, col int) (int, bool) {
	if line < 1 || line > len(a.lineStarts) || col < 0 {
		return 0, false
	}
	i := a.lineStarts[line-1]
	for c := 0; c < col; c++ {
		if i >= len(a.src) || a.src[i] == '\n' {
			if i == len(a.src) && c == col-1 {
				return i, true
			}
			return 0, false
		}
		_, size := utf8.DecodeRuneInString(a.src[i:])
		i += size
	}
	return i, true
}

// span is a node wrapper's [start, end) in bytes.
func (a *kclAST) span(w map[string]any) (int, int, bool) {
	num := func(k string) int {
		f, _ := w[k].(float64)
		return int(f)
	}
	start, ok1 := a.offset(num("line"), num("column"))
	end, ok2 := a.offset(num("end_line"), num("end_column"))
	if !ok1 || !ok2 || end < start {
		return 0, 0, false
	}
	return start, end, true
}

func kclNode(v any) (wrapper, node map[string]any, ok bool) {
	w, ok := v.(map[string]any)
	if !ok {
		return nil, nil, false
	}
	n, ok := w["node"].(map[string]any)
	return w, n, ok
}

func kclType(n map[string]any) string { s, _ := n["type"].(string); return s }

// kclIdentNames is an Identifier node's dotted names (a schema
// expression's name is an untyped node of the same shape).
func kclIdentNames(n map[string]any) []string {
	if t := kclType(n); t != "Identifier" && t != "" {
		return nil
	}
	list, _ := n["names"].([]any)
	var out []string
	for _, v := range list {
		w, _ := v.(map[string]any)
		s, _ := w["node"].(string)
		out = append(out, s)
	}
	return out
}

// ─── What the file declares ─────────────────────────────────────────────────

// kclItem is one `key op value` entry of a config literal.
type kclItem struct {
	key, op string
	vw, vn  map[string]any // the value's wrapper and node
	w       map[string]any // the entry's wrapper
}

// kclSchemaLit is one `<alias>.<Schema> {…}` expression.
type kclSchemaLit struct {
	schema, alias string
	w, n          map[string]any
	config        map[string]any // the Config wrapper; nil for no config
	items         []kclItem
	// opaque: an entry with no plain-identifier key (an unpacked dict, an
	// if-entry), or positional arguments: it may set anything.
	opaque bool
	// field is the key or assignment target the literal is the value of,
	// "" when it is nested in anything else.
	field string
	// literal and the three values: for a VulnExemption, whether it is
	// id/reason/expires, each a plain string literal, and nothing else.
	literal             bool
	idLiteral           bool
	id, reason, expires string
}

// kclField is one `<key> op <value>` occurrence of a field this edit cares
// about, anywhere in the file.
type kclField struct {
	key, op string
	vw, vn  map[string]any
	owner   *kclSchemaLit // nil for a plain dict or an assignment
}

type kclString struct {
	text  string         // decoded value, or raw source for an f-string
	owner *kclSchemaLit  // the VulnExemption it is a field of, if any
	w     map[string]any // its wrapper
}

type kclFacts struct {
	schemas  map[string][]*kclSchemaLit
	fields   map[string][]kclField
	strings  []kclString
	comments []string
}

var vulnExemptFields = map[string]bool{"delivery_policy": true, "vuln_gate": true, "exemptions": true}

func collectKCLFacts(a *kclAST) *kclFacts {
	f := &kclFacts{schemas: map[string][]*kclSchemaLit{}, fields: map[string][]kclField{}, comments: a.comments}
	var walk func(v any, field string, owner *kclSchemaLit)
	walk = func(v any, field string, owner *kclSchemaLit) {
		switch t := v.(type) {
		case []any:
			for _, x := range t {
				walk(x, "", owner)
			}
			return
		case map[string]any:
		default:
			return
		}
		w, n, isNode := kclNode(v)
		if !isNode || kclType(n) == "" {
			for _, x := range v.(map[string]any) {
				walk(x, "", owner)
			}
			return
		}
		switch kclType(n) {
		case "Schema":
			_, nameN, _ := kclNode(n["name"])
			names := kclIdentNames(nameN)
			if len(names) == 0 {
				break
			}
			lit := &kclSchemaLit{schema: names[len(names)-1], alias: strings.Join(names[:len(names)-1], "."), w: w, n: n, field: field}
			if args, _ := n["args"].([]any); len(args) > 0 {
				lit.opaque = true
			}
			if kw, _ := n["kwargs"].([]any); len(kw) > 0 {
				lit.opaque = true
			}
			f.schemas[lit.schema] = append(f.schemas[lit.schema], lit)
			walk(n["args"], "", owner)
			walk(n["kwargs"], "", owner)
			if cw, cn, ok := kclNode(n["config"]); ok && kclType(cn) == "Config" {
				lit.config = cw
				walkConfig(cn, lit, f, walk)
			} else {
				lit.opaque = lit.opaque || n["config"] != nil
				walk(n["config"], "", lit)
			}
			if lit.schema == "VulnExemption" {
				lit.judgeExemption()
			}
			return
		case "Config":
			walkConfig(n, nil, f, walk)
			return
		case "Assign":
			targets, _ := n["targets"].([]any)
			name := ""
			if len(targets) == 1 {
				_, tn, _ := kclNode(targets[0])
				nw, _ := tn["name"].(map[string]any)
				s, _ := nw["node"].(string)
				if paths, _ := tn["paths"].([]any); len(paths) == 0 {
					name = s
				}
			}
			if vulnExemptFields[name] {
				vw, vn, _ := kclNode(n["value"])
				f.fields[name] = append(f.fields[name], kclField{key: name, op: "Override", vw: vw, vn: vn})
			}
			walk(n["value"], name, owner)
			return
		case "StringLit":
			s, _ := n["value"].(string)
			f.strings = append(f.strings, kclString{text: s, owner: owner, w: w})
			return
		case "JoinedString":
			raw, _ := n["raw_value"].(string)
			f.strings = append(f.strings, kclString{text: raw, w: w})
		}
		for k, x := range n {
			if k != "type" {
				walk(x, "", owner)
			}
		}
	}
	walk(a.body, "", nil)
	return f
}

// walkConfig records a config literal's entries (on owner, when it is a
// schema's) and walks their values.
func walkConfig(n map[string]any, owner *kclSchemaLit, f *kclFacts, walk func(any, string, *kclSchemaLit)) {
	items, _ := n["items"].([]any)
	for _, iv := range items {
		iw, in, ok := kclNode(iv)
		if !ok {
			continue
		}
		key := ""
		if _, kn, ok := kclNode(in["key"]); ok {
			if names := kclIdentNames(kn); len(names) == 1 {
				key = names[0]
			}
		}
		op, _ := in["operation"].(string)
		vw, vn, _ := kclNode(in["value"])
		if owner != nil {
			if key == "" {
				owner.opaque = true
			}
			owner.items = append(owner.items, kclItem{key: key, op: op, vw: vw, vn: vn, w: iw})
		}
		if vulnExemptFields[key] {
			f.fields[key] = append(f.fields[key], kclField{key: key, op: op, vw: vw, vn: vn, owner: owner})
		}
		// A VulnExemption's own field strings are attributed to it.
		strOwner := (*kclSchemaLit)(nil)
		if owner != nil && owner.schema == "VulnExemption" {
			strOwner = owner
		}
		walk(in["key"], "", strOwner)
		walk(in["value"], key, strOwner)
	}
}

// judgeExemption decides whether a VulnExemption is a plain literal.
func (l *kclSchemaLit) judgeExemption() {
	vals := map[string]string{}
	plain := !l.opaque && len(l.items) == 3
	for _, it := range l.items {
		isStr := kclType(it.vn) == "StringLit"
		if it.key == "id" && isStr && (it.op == "Override" || it.op == "Union") {
			l.idLiteral = true
			l.id, _ = it.vn["value"].(string)
		}
		if !isStr || it.op != "Override" || (it.key != "id" && it.key != "reason" && it.key != "expires") {
			plain = false
			continue
		}
		if _, dup := vals[it.key]; dup {
			plain = false
		}
		vals[it.key], _ = it.vn["value"].(string)
	}
	if plain && len(vals) == 3 {
		l.literal = true
		l.reason, l.expires = vals["reason"], vals["expires"]
	}
	if l.opaque {
		l.idLiteral = false
	}
}

// ─── The edit ───────────────────────────────────────────────────────────────

// kclExemptEdit is one exemption being stated: what, and the schema alias
// (`forge` in `import forge`) its literals are spelled with.
type kclExemptEdit struct {
	id, reason, expires, alias string
}

func (e *kclExemptEdit) literal(alias string) string {
	return vulnExemptionLiteral(alias, e.id, e.reason, e.expires)
}

// refuse is the fail-closed answer: why, and the lines to add by hand.
func (e *kclExemptEdit) refuse(format string, a ...any) error {
	lines := fmt.Sprintf("    delivery_policy = %[1]s.DeliveryPolicy {\n        vuln_gate = %[1]s.VulnPolicy {\n            exemptions = [\n                %[2]s\n            ]\n        }\n    }\n",
		e.alias, e.literal(e.alias))
	return &errVulnExemptNoAnchor{why: fmt.Sprintf(format, a...), snippet: lines}
}

func kclLine(w map[string]any) int { l, _ := w["line"].(float64); return int(l) }

// exemptInKCL returns src with the exemption stated, and whether it changed
// anything (false when the same exemption is already there, byte for byte).
// Any doubt is an *errVulnExemptNoAnchor, and nothing is to be written.
func exemptInKCL(src, id, reason, expires string) (string, bool, error) {
	e := &kclExemptEdit{id: id, reason: reason, expires: expires, alias: "forge"}
	a, err := parseKCLForEdit(src)
	if err != nil {
		return "", false, e.refuse("forge cannot parse this file safely (%v)", err)
	}
	f := collectKCLFacts(a)
	for _, k := range []string{"Bundle", "DeliveryPolicy", "VulnPolicy", "VulnExemption"} {
		if len(f.schemas[k]) > 0 && f.schemas[k][0].alias != "" {
			e.alias = f.schemas[k][0].alias
			break
		}
	}
	same, err := e.declarationsOfID(f)
	if err != nil {
		return "", false, err
	}
	exList, err := e.effectivePolicy(f)
	if err != nil {
		return "", false, err
	}
	ed, err := e.splice(a, f, same, exList)
	if err != nil || ed.next == src {
		return src, false, err
	}
	if err := verifyKCLExemptEdit(e, src, ed, f); err != nil {
		return "", false, e.refuse("the edit could not be verified (%v)", err)
	}
	return ed.next, true, nil
}

// declarationsOfID finds the literal exemption of the id, if any, and
// refuses when the id could be declared anywhere forge cannot read.
func (e *kclExemptEdit) declarationsOfID(f *kclFacts) ([]*kclSchemaLit, error) {
	var same []*kclSchemaLit
	for _, x := range f.schemas["VulnExemption"] {
		switch {
		case !x.idLiteral:
			return nil, e.refuse("the VulnExemption at line %d has a computed id, so forge cannot tell whether it already accepts %s", kclLine(x.w), e.id)
		case strings.EqualFold(x.id, e.id) && !x.literal:
			return nil, e.refuse("the VulnExemption of %s at line %d is not a plain literal (id, reason and expires as string literals), so forge will not rewrite it", e.id, kclLine(x.w))
		case strings.EqualFold(x.id, e.id):
			same = append(same, x)
		}
	}
	if len(same) > 1 {
		return nil, e.refuse("%s is exempted %d times (lines %d and %d)", e.id, len(same), kclLine(same[0].w), kclLine(same[1].w))
	}
	idRE := regexp.MustCompile(`(?i)(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(e.id) + `($|[^A-Za-z0-9-])`)
	for _, s := range f.strings {
		if len(same) == 1 && s.owner == same[0] {
			continue
		}
		if idRE.MatchString(s.text) {
			return nil, e.refuse("%s appears in a string at line %d outside a literal VulnExemption: it may be accepted by an expression forge cannot read", e.id, kclLine(s.w))
		}
	}
	if len(same) == 0 {
		for _, c := range f.comments {
			if idRE.MatchString(c) {
				return nil, e.refuse("%s is named only in a comment (%s): forge will not guess what it refers to", e.id, strings.TrimSpace(c))
			}
		}
	}
	return same, nil
}

// effectivePolicy refuses unless exactly one place can hold the env's
// policy, and returns the vuln_gate's exemptions entry when it has one.
func (e *kclExemptEdit) effectivePolicy(f *kclFacts) (*kclItem, error) {
	policies, gates, bundles := f.schemas["DeliveryPolicy"], f.schemas["VulnPolicy"], f.schemas["Bundle"]
	for _, key := range []string{"delivery_policy", "vuln_gate", "exemptions"} {
		want := map[string]string{"delivery_policy": "Schema:DeliveryPolicy", "vuln_gate": "Schema:VulnPolicy", "exemptions": "List"}[key]
		occ := f.fields[key]
		if len(occ) > 1 {
			return nil, e.refuse("%s is set %d times (lines %d and %d), so more than one could be effective", key, len(occ), kclLine(occ[0].vw), kclLine(occ[1].vw))
		}
		for _, o := range occ {
			got := kclType(o.vn)
			if got == "Schema" {
				_, nn, _ := kclNode(o.vn["name"])
				if names := kclIdentNames(nn); len(names) > 0 {
					got += ":" + names[len(names)-1]
				}
			}
			if got != want || (o.op != "Override" && !(o.op == "Union" && key != "exemptions")) {
				return nil, e.refuse("%s at line %d is computed (not a %s literal assigned with =), so forge cannot see what it holds", key, kclLine(o.vw), strings.TrimPrefix(want, "Schema:"))
			}
		}
	}
	for name, lits := range map[string][]*kclSchemaLit{"DeliveryPolicy": policies, "VulnPolicy": gates, "Bundle": bundles} {
		for _, l := range lits {
			if l.opaque {
				return nil, e.refuse("the %s at line %d unpacks a value or has conditional entries, so forge cannot see every field it sets", name, kclLine(l.w))
			}
		}
		if len(lits) > 1 && name != "Bundle" {
			return nil, e.refuse("this file declares %d %s literals, so forge cannot tell which one the env uses", len(lits), name)
		}
	}
	if len(policies) == 1 && policies[0].field != "delivery_policy" && len(bundles) > 0 {
		return nil, e.refuse("this file has a Bundle and, at line %d, a DeliveryPolicy that is not its delivery_policy field: forge cannot tell which one is effective", kclLine(policies[0].w))
	}
	if len(gates) == 1 && gates[0].field != "vuln_gate" && (len(policies) > 0 || len(bundles) > 0) {
		return nil, e.refuse("the VulnPolicy at line %d is not a delivery policy's vuln_gate field, so forge cannot tell whether it is the effective one", kclLine(gates[0].w))
	}
	var exList *kclItem
	if len(gates) == 1 {
		for i, it := range gates[0].items {
			if it.key == "exemptions" {
				exList = &gates[0].items[i]
			}
		}
	}
	if len(f.fields["exemptions"]) == 1 && exList == nil {
		return nil, e.refuse("exemptions at line %d is not a field of the vuln_gate's VulnPolicy", kclLine(f.fields["exemptions"][0].vw))
	}
	return exList, nil
}

// kclSplice is one edit: the result, the span [lo, hi) of the original it
// happens at (lo == hi for an insertion), and the schema literals it adds.
type kclSplice struct {
	next   string
	lo, hi int
	adds   map[string]int
}

// splice makes the edit at the one place effectivePolicy left.
func (e *kclExemptEdit) splice(a *kclAST, f *kclFacts, same []*kclSchemaLit, exList *kclItem) (kclSplice, error) {
	src := a.src
	policies, gates, bundles := f.schemas["DeliveryPolicy"], f.schemas["VulnPolicy"], f.schemas["Bundle"]
	out := kclSplice{adds: map[string]int{"VulnExemption": 1}}
	var err error
	switch {
	case len(same) == 1:
		x := same[0]
		if exList == nil || !kclDirectElement(exList.vn, x.w) {
			return out, e.refuse("the VulnExemption of %s at line %d is not an element of the vuln_gate's exemptions list", e.id, kclLine(x.w))
		}
		start, end, ok := a.span(x.w)
		if !ok || !strings.HasSuffix(src[start:end], "}") || !strings.HasPrefix(src[start:end], x.alias) {
			return out, e.refuse("forge could not locate the VulnExemption of %s at line %d exactly", e.id, kclLine(x.w))
		}
		out.next, out.lo, out.hi = src[:start]+e.literal(x.alias)+src[end:], start, end
		out.adds["VulnExemption"] = 0
	case exList != nil:
		out.next, out.lo, out.hi, err = kclAppend(a, exList.vw, exList.vn, "elts", "[", "]", e.literal(gates[0].alias))
	case len(gates) == 1:
		out.next, out.lo, out.hi, err = kclAppend(a, gates[0].config, nodeOf(gates[0].config), "items", "{", "}", "exemptions = ["+e.literal(gates[0].alias)+"]")
	case len(policies) == 1:
		p := policies[0]
		out.next, out.lo, out.hi, err = kclAppend(a, p.config, nodeOf(p.config), "items", "{", "}", fmt.Sprintf("vuln_gate = %s.VulnPolicy {exemptions = [%s]}", p.alias, e.literal(p.alias)))
		out.adds["VulnPolicy"] = 1
	case len(bundles) == 1:
		b := bundles[0]
		out.next, out.lo, out.hi, err = kclAppend(a, b.config, nodeOf(b.config), "items", "{", "}", fmt.Sprintf("delivery_policy = %[1]s.DeliveryPolicy {vuln_gate = %[1]s.VulnPolicy {exemptions = [%[2]s]}}", b.alias, e.literal(b.alias)))
		out.adds["VulnPolicy"], out.adds["DeliveryPolicy"] = 1, 1
	case len(bundles) > 1:
		return out, e.refuse("this file declares %d Bundle literals, so forge cannot tell which one is the env's", len(bundles))
	default:
		return out, e.refuse("this file declares no Bundle, DeliveryPolicy or VulnPolicy literal to add the exemption to (the env's policy may be built in another file)")
	}
	if err != nil {
		return out, e.refuse("%v", err)
	}
	return out, nil
}

func nodeOf(w map[string]any) map[string]any {
	_, n, _ := kclNode(w)
	return n
}

// kclDirectElement reports whether ew is one of the list node's elements.
func kclDirectElement(list map[string]any, ew map[string]any) bool {
	elts, _ := list["elts"].([]any)
	for _, x := range elts {
		if w, ok := x.(map[string]any); ok && fmt.Sprint(w["line"], w["column"]) == fmt.Sprint(ew["line"], ew["column"]) {
			return true
		}
	}
	return false
}

// kclAppend inserts entry as the last element of the list or config literal
// w (n its node, field its element key), matching its layout: one line
// stays one line (`, entry`), a multi-line literal gains a line after its
// last element's line, indented like that element. The returned [lo, hi)
// is the original's span the insertion happens at (lo == hi).
func kclAppend(a *kclAST, w, n map[string]any, field, open, close, entry string) (string, int, int, error) {
	if w == nil || n == nil {
		return "", 0, 0, fmt.Errorf("the literal has no body to add to")
	}
	start, end, ok := a.span(w)
	src := a.src
	if !ok || !strings.HasPrefix(src[start:end], open) || !strings.HasSuffix(src[start:end], close) {
		return "", 0, 0, fmt.Errorf("could not locate the literal at line %v exactly", w["line"])
	}
	closeAt := end - len(close)
	elts, _ := n[field].([]any)
	if len(elts) == 0 {
		if !strings.Contains(src[start:closeAt], "\n") {
			at := start + len(open)
			return src[:at] + entry + src[at:], at, at, nil
		}
		indent := lineIndentAt(src, closeAt) + "    "
		at := start + len(open)
		return src[:at] + "\n" + indent + entry + src[at:], at, at, nil
	}
	lw, _ := elts[len(elts)-1].(map[string]any)
	lstart, lend, ok := a.span(lw)
	if !ok || lend > closeAt {
		return "", 0, 0, fmt.Errorf("could not locate the last element of the literal at line %v", w["line"])
	}
	eol := strings.IndexByte(src[lend:], '\n')
	if eol < 0 || lend+eol >= closeAt {
		// The closing bracket is on the last element's line.
		return src[:lend] + ", " + entry + src[lend:], lend, lend, nil
	}
	rest := strings.TrimSpace(src[lend : lend+eol])
	if rest != "" && rest != "," && !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(rest, ",")), "#") {
		return src[:lend] + ", " + entry + src[lend:], lend, lend, nil
	}
	at := lend + eol
	return src[:at] + "\n" + lineIndentAt(src, lstart) + entry + src[at:], at, at, nil
}

// lineIndentAt is the leading whitespace of the line holding offset i.
func lineIndentAt(src string, i int) string {
	s := strings.LastIndexByte(src[:i], '\n') + 1
	j := s
	for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
		j++
	}
	return src[s:j]
}

// kclChangedWindow is the smallest window [lo, hi) of orig, and its
// counterpart in next, outside which the two are byte-identical.
func kclChangedWindow(orig, next string) (lo, hiOrig, hiNext int) {
	for lo < len(orig) && lo < len(next) && orig[lo] == next[lo] {
		lo++
	}
	hiOrig, hiNext = len(orig), len(next)
	for hiOrig > lo && hiNext > lo && orig[hiOrig-1] == next[hiNext-1] {
		hiOrig--
		hiNext--
	}
	return lo, hiOrig, hiNext
}

// verifyKCLExemptEdit checks an edit: the result parses, differs from orig only inside
// [lo, hi) of orig (the replaced literal, or the insertion point), declares
// exactly the schemas the edit meant to add, holds exactly one literal
// exemption of id with the requested values, and every other exemption is
// unchanged.
func verifyKCLExemptEdit(e *kclExemptEdit, orig string, ed kclSplice, before *kclFacts) error {
	next, lo, hi, adds, id := ed.next, ed.lo, ed.hi, ed.adds, e.id
	wlo, whi, _ := kclChangedWindow(orig, next)
	// A pure insertion's window is empty, and may sit anywhere in a run of
	// equal bytes around the insertion point; a replacement's lies inside
	// the replaced literal.
	if !(wlo == whi && lo == hi) && (wlo < lo || whi > hi) {
		return fmt.Errorf("it would change bytes %d..%d of the file, outside the literal at %d..%d", wlo, whi, lo, hi)
	}
	a, err := parseKCLForEdit(next)
	if err != nil {
		return fmt.Errorf("the edited file does not parse: %v", err)
	}
	after := collectKCLFacts(a)
	for _, k := range []string{"Bundle", "DeliveryPolicy", "VulnPolicy", "VulnExemption"} {
		if got, want := len(after.schemas[k]), len(before.schemas[k])+adds[k]; got != want {
			return fmt.Errorf("it would leave %d %s literals, want %d", got, k, want)
		}
	}
	others := func(f *kclFacts) []string {
		var out []string
		for _, x := range f.schemas["VulnExemption"] {
			if !strings.EqualFold(x.id, id) {
				out = append(out, fmt.Sprintf("%s|%v|%s|%s", x.id, x.literal, x.reason, x.expires))
			}
		}
		sort.Strings(out)
		return out
	}
	if strings.Join(others(before), "\n") != strings.Join(others(after), "\n") {
		return fmt.Errorf("it would change another exemption")
	}
	n := 0
	for _, x := range after.schemas["VulnExemption"] {
		if strings.EqualFold(x.id, id) {
			n++
			if !x.literal || x.id != id || x.reason != e.reason || x.expires != e.expires {
				return fmt.Errorf("the written exemption reads back as id %q, reason %q, expires %q", x.id, x.reason, x.expires)
			}
		}
	}
	if n != 1 {
		return fmt.Errorf("it would leave %d exemptions of %s", n, id)
	}
	return nil
}
