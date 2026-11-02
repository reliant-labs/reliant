// Package codegen — tier_kcl_gen.go generates forge's KCL deploy-tier schemas
// (kcl/tiers/tiers_gen.k) from the Go spec types in pkg/deploy/v1alpha1.
//
// # GO IS THE SOURCE, KCL IS A PROJECTION
//
// The tier specs are declared ONCE, in Go, because they have two consumers
// that must agree: forge's KCL (what an app author writes) and the control
// plane's CRD (what the API server stores). Before this generator, the
// hosted backend was declared four times with three unit systems. Now the KCL
// schema is derived from the same structural schema controller-tools derives
// the CRD from, so the author-facing field set and the API server's pruning
// schema cannot disagree. A field present in Go but missing from the
// installed schema is SILENTLY PRUNED by the API server, and it reads back as
// a zero value forever.
//
// # HOW
//
// controller-tools' crd.Parser produces an OpenAPI JSONSchemaProps per Go
// type, with every +kubebuilder marker applied: enum, pattern, min/max,
// default. This file lowers each one to a KCL schema. Every property becomes
// a field, required-ness comes from the schema, and a $ref to another
// package type becomes a reference to that type's generated schema. The
// validation keywords become `check:` lines. The KCL checks are an AUTHOR-TIME
// convenience that fails at `kcl run` with a message naming the field. Go's
// Validate() remains the check every path runs.
//
// Cross-field rules (at most one env channel, which kinds may set which
// fields, the profile mask, and so on) are not JSONSchema and are not
// generated. They live in
// Validate() and run on every deploy. Restating them by hand in KCL would
// recreate the second declaration this generator exists to delete.
package codegen

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-tools/pkg/crd"
	"sigs.k8s.io/controller-tools/pkg/loader"
	"sigs.k8s.io/controller-tools/pkg/markers"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/deploy/v1alpha1"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// TierSpecPackage is the Go package the tier specs live in.
const TierSpecPackage = "./pkg/deploy/v1alpha1"

// TierKCLPath is where the generated module lives, relative to forge's repo
// root. It is embedded with the rest of the KCL module and imported by
// projects as `forge.tiers`.
const TierKCLPath = "kcl/tiers/tiers_gen.k"

// TierRoots are the spec types whose KCL schemas are generated. Every type
// they reference is generated too, transitively. The KCL schema name is the Go
// type name minus the "Spec" suffix, which is what an author writes
// (tiers.Workload { ... }).
var TierRoots = []string{"WorkloadSpec", "StaticSiteSpec", "ManagedDatabaseSpec"}

// TierSchemas is the lowered input for GenerateTierKCL: the schema of every
// type reachable from TierRoots, and each type's Go doc summary.
type TierSchemas struct {
	Schemas map[string]apiext.JSONSchemaProps // Go type name -> schema
	Docs    map[string]string                 // Go type name -> first doc paragraph
	// Pointers records, per Go type, the JSON fields declared as a Go
	// POINTER. A pointer says that ABSENCE is meaningful (Workload.probes:
	// no probes), which the JSONSchema cannot say, since a pointer and a
	// value lower to the same object schema. The generator reads it so it
	// never defaults such a field to an empty instance.
	Pointers map[string]map[string]bool
}

// LoadTierSchemas runs controller-tools' parser over the tier package in
// forgeRoot and returns every reachable type's schema.
func LoadTierSchemas(forgeRoot string) (*TierSchemas, error) {
	restore, err := chdir(forgeRoot)
	if err != nil {
		return nil, err
	}
	defer restore()

	pkgs, err := loader.LoadRootsWithConfig(&packages.Config{Env: goexec.Env()}, TierSpecPackage)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", TierSpecPackage, err)
	}
	if len(pkgs) != 1 {
		return nil, fmt.Errorf("load %s: want 1 package, got %d", TierSpecPackage, len(pkgs))
	}
	pkg := pkgs[0]

	reg := &markers.Registry{}
	if err := (crd.Generator{}).RegisterMarkers(reg); err != nil {
		return nil, err
	}
	parser := &crd.Parser{Collector: &markers.Collector{Registry: reg}, Checker: &loader.TypeChecker{}}
	crd.AddKnownTypes(parser)
	parser.NeedPackage(pkg)

	out := &TierSchemas{Schemas: map[string]apiext.JSONSchemaProps{}, Docs: map[string]string{}, Pointers: map[string]map[string]bool{}}
	var need func(name string) error
	need = func(name string) error {
		if _, done := out.Schemas[name]; done {
			return nil
		}
		ident := crd.TypeIdent{Package: pkg, Name: name}
		info := parser.LookupType(pkg, name)
		if info == nil {
			return fmt.Errorf("type %s not found in %s", name, TierSpecPackage)
		}
		parser.NeedSchemaFor(ident)
		schema := parser.Schemata[ident]
		out.Schemas[name] = schema
		out.Docs[name] = firstParagraph(info.Doc)
		for _, f := range info.Fields {
			if _, isPtr := f.RawField.Type.(*ast.StarExpr); !isPtr {
				continue
			}
			jsonName, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if out.Pointers[name] == nil {
				out.Pointers[name] = map[string]bool{}
			}
			out.Pointers[name][jsonName] = true
		}
		for _, ref := range localRefs(schema, pkg.PkgPath) {
			if err := need(ref); err != nil {
				return err
			}
		}
		return nil
	}
	for _, root := range TierRoots {
		if err := need(root); err != nil {
			return nil, err
		}
	}
	if errs := pkg.Errors; len(errs) > 0 {
		return nil, fmt.Errorf("controller-tools reported errors loading %s: %v", TierSpecPackage, errs)
	}
	out.inlineScalarRefs(pkg.PkgPath)
	return out, nil
}

// inlineScalarRefs replaces every $ref to a NON-object type (a named string
// such as Network or Engine) with that type's own schema: its type plus its
// enum/pattern constraints. The referenced scalar types are then dropped from
// the set. A KCL schema is a struct, and a named string lowered to one would
// be an empty schema that no string value satisfies. Worse, its enum would be
// lost, and the field would accept any string.
func (ts *TierSchemas) inlineScalarRefs(pkgPath string) {
	scalar := map[string]apiext.JSONSchemaProps{}
	for name, s := range ts.Schemas {
		if s.Type != "object" {
			scalar[name] = s
		}
	}
	var fix func(s *apiext.JSONSchemaProps)
	fix = func(s *apiext.JSONSchemaProps) {
		if s.Ref != nil {
			if name, ok := localRefName(*s.Ref, pkgPath); ok {
				if target, isScalar := scalar[name]; isScalar {
					// Field-level keywords (a default, a description) win over
					// the type's, the same precedence the API server applies.
					merged := target
					merged.Description = s.Description
					if s.Default != nil {
						merged.Default = s.Default
					}
					*s = merged
				}
			}
		}
		for k, p := range s.Properties {
			fix(&p)
			s.Properties[k] = p
		}
		if s.Items != nil && s.Items.Schema != nil {
			fix(s.Items.Schema)
		}
	}
	for name := range scalar {
		delete(ts.Schemas, name)
		delete(ts.Docs, name)
	}
	for name, s := range ts.Schemas {
		fix(&s)
		ts.Schemas[name] = s
	}
}

// localRefs lists the same-package type names a schema references.
func localRefs(s apiext.JSONSchemaProps, pkgPath string) []string {
	var refs []string
	var walk func(apiext.JSONSchemaProps)
	walk = func(s apiext.JSONSchemaProps) {
		if s.Ref != nil {
			if name, ok := localRefName(*s.Ref, pkgPath); ok {
				refs = append(refs, name)
			}
		}
		for _, p := range s.Properties {
			walk(p)
		}
		if s.Items != nil && s.Items.Schema != nil {
			walk(*s.Items.Schema)
		}
		if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
			walk(*s.AdditionalProperties.Schema)
		}
	}
	walk(s)
	return refs
}

// localRefName decodes a controller-tools definition link back to a type name,
// if it points into pkgPath. Same-package references are unqualified
// ("#/definitions/EnvVar"). Cross-package ones carry the escaped path
// ("#/definitions/<pkg~1path>~0<Type>"), and only this package's qualified
// form is accepted. Anything else leaves the closed tier package.
func localRefName(ref, pkgPath string) (string, bool) {
	if qualified := crd.TypeRefLink(pkgPath, ""); strings.HasPrefix(ref, qualified) {
		return strings.TrimPrefix(ref, qualified), true
	}
	bare := strings.TrimPrefix(ref, crd.TypeRefLink("", ""))
	if bare == ref || strings.Contains(bare, "~") {
		return "", false
	}
	return bare, true
}

// kclSchemaName maps a Go type name to its KCL schema name.
func kclSchemaName(goName string) string { return strings.TrimSuffix(goName, "Spec") }

// GenerateTierKCL renders the KCL module text. It is pure: the same schemas
// always produce byte-identical output.
func GenerateTierKCL(in *TierSchemas, pkgPath string) (string, error) {
	names := make([]string, 0, len(in.Schemas))
	for n := range in.Schemas {
		names = append(names, n)
	}
	// Tier roots first, in declared order, then their dependencies sorted.
	// The file then reads top-down from what an author writes.
	rootIdx := map[string]int{}
	for i, r := range TierRoots {
		rootIdx[r] = i
	}
	sort.Slice(names, func(i, j int) bool {
		ri, iok := rootIdx[names[i]]
		rj, jok := rootIdx[names[j]]
		switch {
		case iok && jok:
			return ri < rj
		case iok != jok:
			return iok
		default:
			return names[i] < names[j]
		}
	})

	var b strings.Builder
	b.WriteString(`"""
GENERATED by forge from pkg/deploy/v1alpha1 — DO NOT EDIT.

Regenerate: FORGE_UPDATE_GENERATED=1 go test ./internal/codegen -run TestTierKCLIsCurrent

The forge.dev deploy-tier SPECS as KCL schemas: what an app author declares
for Workload / StaticSite / ManagedDatabase. The Go types are the source
of truth, and the control plane's CRDs are the same types. Field names are the
CRD's JSON names, so a declaration here serializes directly to a CR spec.

The schemas are CLOSED: a field that is not declared is refused, which is how
a tier rejects configuration it must not honour. The checks below are the
per-field JSONSchema constraints, failing at author time. Cross-field rules
(one env channel, kind capabilities, profiles, ...) are enforced by the Go
Validate() that every deploy path runs.

Each schema also has a <name>_json lambda that drops unset optional fields,
so the output is exactly the JSON a CR spec carries.
"""
import regex

`)
	for _, goName := range names {
		if err := writeTierSchema(&b, goName, in.Schemas[goName], in.Docs[goName], pkgPath, in.Schemas, in.Pointers[goName]); err != nil {
			return "", fmt.Errorf("%s: %w", goName, err)
		}
	}
	writeRestrictedMask(&b)
	// Each schema ends with a blank separator line, so the last one would
	// leave the file ending in "\n\n". End it with exactly one newline — what
	// end-of-file-fixer (and every editor) writes — so the committed file is
	// never "fixed" into something the generator does not produce.
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

// writeRestrictedMask emits the hosted runtime's author-time mask: every
// WorkloadSpec field path and kind that v1alpha1.FieldProfiles /
// KindProfiles allow ONLY under ProfileFull, each with the reason admission
// gives. forge.render refuses a hosted-bound workload that sets one, naming
// the field, the workload and the reason (kcl/render.k _mask_violations).
//
// Generated from the same table the control plane admits with, so the two
// cannot disagree, and a spec field Go has not classified (default-deny) is
// never in the table as allowed: it is absent from the authoring schema
// until someone adds it there too, and the reflection test in
// pkg/deploy/v1alpha1 already refuses an unclassified field.
func writeRestrictedMask(b *strings.Builder) {
	var fields []string
	for path, p := range v1alpha1.FieldProfiles {
		if p == v1alpha1.ProfileFull {
			fields = append(fields, path)
		}
	}
	sort.Strings(fields)
	var kinds []string
	for k, p := range v1alpha1.KindProfiles {
		if p == v1alpha1.ProfileFull {
			kinds = append(kinds, string(k))
		}
	}
	sort.Strings(kinds)

	b.WriteString("# The hosted (Restricted-profile) mask: every WorkloadSpec field path and\n")
	b.WriteString("# kind v1alpha1.FieldProfiles / KindProfiles allow ONLY under the Full\n")
	b.WriteString("# profile, with the reason admission gives. Paths are JSON names joined\n")
	b.WriteString(`# with "."; a list element shares its list's path ("env.secretRef").` + "\n")
	b.WriteString("RESTRICTED_FIELD_REASONS: {str:str} = {\n")
	for _, f := range fields {
		fmt.Fprintf(b, "    %s = %s\n", strconv.Quote(f), strconv.Quote(v1alpha1.FullOnlyReason(f)))
	}
	b.WriteString("}\n\n")
	b.WriteString("RESTRICTED_KIND_REASONS: {str:str} = {\n")
	for _, k := range kinds {
		fmt.Fprintf(b, "    %s = %s\n", strconv.Quote(k), strconv.Quote(v1alpha1.FullOnlyKindReason(v1alpha1.WorkloadKind(k))))
	}
	b.WriteString("}\n")
}

func writeTierSchema(b *strings.Builder, goName string, s apiext.JSONSchemaProps, doc, pkgPath string, all map[string]apiext.JSONSchemaProps, pointers map[string]bool) error {
	name := kclSchemaName(goName)
	required := map[string]bool{}
	for _, r := range s.Required {
		required[r] = true
	}
	props := make([]string, 0, len(s.Properties))
	for p := range s.Properties {
		props = append(props, p)
	}
	sort.Strings(props)

	fmt.Fprintf(b, "schema %s:\n", name)
	if doc != "" {
		fmt.Fprintf(b, "    %s\n", quoteKCLDoc(doc))
	}
	var checks []string
	for _, p := range props {
		ps := s.Properties[p]
		typ, err := kclType(ps, pkgPath)
		if err != nil {
			return fmt.Errorf("field %s: %w", p, err)
		}
		opt := "?"
		if required[p] {
			opt = ""
		}
		line := fmt.Sprintf("    %s%s: %s", p, opt, typ)
		if ref, ok := structRef(ps, pkgPath); ok && !required[p] && ps.Default == nil && !pointers[p] && defaultsWhenEmpty(all[ref]) {
			// An optional struct that an empty instance completes, and that
			// carries at least one default (Resources), defaults to that
			// empty instance. Omitting it then yields the defaulted shape
			// instead of an absent block, which is the same working default
			// Go's WithDefaults applies.
			//
			// NOT for a Go pointer field: there absence is the declaration
			// (Workload.probes unset means the kind's probe policy), and an
			// empty default instance would give every job a probe.
			line = fmt.Sprintf("    %s: %s = %s {}", p, typ, typ)
		} else if ps.Default != nil {
			def, err := kclDefault(ps.Default.Raw)
			if err != nil {
				return fmt.Errorf("field %s default: %w", p, err)
			}
			// A defaulted field is not optional in KCL: it always has a value.
			line = fmt.Sprintf("    %s: %s = %s", p, typ, def)
		}
		b.WriteString(line + "\n")
		checks = append(checks, fieldChecks(name, p, ps, !required[p] && ps.Default == nil)...)
	}
	typeChecks, err := typeLevelChecks(name, s)
	if err != nil {
		return err
	}
	checks = append(checks, typeChecks...)
	if len(checks) > 0 {
		b.WriteString("\n    check:\n")
		for _, c := range checks {
			b.WriteString("        " + c + "\n")
		}
	}
	b.WriteString("\n")
	writeJSONLambda(b, name, props, s, pkgPath)
	b.WriteString("\n")
	return nil
}

// writeJSONLambda emits <snake>_json: the schema instance as a dict with unset
// optionals DROPPED and nested tier schemas recursed, i.e. the exact JSON a CR
// spec carries. A projection that emitted `null` for unset fields would make
// the API server see an explicit null, which clears a field rather than
// leaving it unset.
func writeJSONLambda(b *strings.Builder, name string, props []string, s apiext.JSONSchemaProps, pkgPath string) {
	fmt.Fprintf(b, "%s_json = lambda v: %s -> {str:} {\n", toSnake(name), name)
	b.WriteString("    _d = {\n")
	for _, p := range props {
		fmt.Fprintf(b, "        %q: %s\n", p, jsonExpr("v."+p, s.Properties[p], pkgPath))
	}
	b.WriteString("    }\n")
	b.WriteString("    {k: _v for k, _v in _d if _v != Undefined and _v != None}\n")
	b.WriteString("}\n")
}

func jsonExpr(access string, s apiext.JSONSchemaProps, pkgPath string) string {
	if s.Ref != nil {
		if ref, ok := localRefName(*s.Ref, pkgPath); ok {
			return fmt.Sprintf("%s_json(%s) if %s else Undefined", toSnake(kclSchemaName(ref)), access, access)
		}
	}
	if s.Type == "array" && s.Items != nil && s.Items.Schema != nil && s.Items.Schema.Ref != nil {
		if ref, ok := localRefName(*s.Items.Schema.Ref, pkgPath); ok {
			return fmt.Sprintf("[%s_json(_i) for _i in %s] if %s else Undefined", toSnake(kclSchemaName(ref)), access, access)
		}
	}
	// A map of tier structs recurses per value, exactly like a list: a raw
	// schema instance would carry its unset optionals into the CR.
	if s.Type == "object" && s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil && s.AdditionalProperties.Schema.Ref != nil {
		if ref, ok := localRefName(*s.AdditionalProperties.Schema.Ref, pkgPath); ok {
			return fmt.Sprintf("{_k: %s_json(_i) for _k, _i in %s} if %s else Undefined", toSnake(kclSchemaName(ref)), access, access)
		}
	}
	return access
}

// oneOfRuleRE matches the CEL rule controller-tools emits for the
// ExactlyOneOf / AtMostOneOf markers: a sum of `(has(self.f)?1:0)` terms
// compared against 1.
var (
	oneOfRuleRE     = regexp.MustCompile(`^((\(has\(self\.[A-Za-z_][A-Za-z0-9_]*\)\?1:0\)\+?)+) (==|<=) 1$`)
	oneOfRuleTermRE = regexp.MustCompile(`has\(self\.([A-Za-z_][A-Za-z0-9_]*)\)`)
)

// typeLevelChecks lowers the TYPE-level validations: x-kubernetes-validations
// (CEL) rules. Only the forms controller-tools' one-of markers emit are
// understood. Any other CEL rule is an ERROR, not a skipped line: a rule the
// API server enforces but the KCL does not would make the author-time schema
// silently looser than the wire, which is the drift this generator exists to
// rule out. Teach this function the new form instead.
func typeLevelChecks(schemaName string, s apiext.JSONSchemaProps) ([]string, error) {
	var out []string
	for _, v := range s.XValidations {
		m := oneOfRuleRE.FindStringSubmatch(v.Rule)
		if m == nil {
			return nil, fmt.Errorf("x-kubernetes-validations rule %q has no KCL lowering; extend typeLevelChecks", v.Rule)
		}
		var terms []string
		for _, f := range oneOfRuleTermRE.FindAllStringSubmatch(m[1], -1) {
			terms = append(terms, fmt.Sprintf("(1 if %s != Undefined and %s != None else 0)", f[1], f[1]))
		}
		msg := v.Message
		if msg == "" {
			msg = "violates " + v.Rule
		}
		out = append(out, fmt.Sprintf("%s %s 1, %s", strings.Join(terms, " + "), m[3], quoteKCLString(schemaName+": "+msg)))
	}
	return out, nil
}

// structRef reports the type a property references, if it is a struct in the
// tier package.
func structRef(s apiext.JSONSchemaProps, pkgPath string) (string, bool) {
	if s.Ref == nil {
		return "", false
	}
	return localRefName(*s.Ref, pkgPath)
}

// defaultsWhenEmpty reports whether an empty instance of a struct schema is
// complete AND meaningful. That means no field is required, and at least one
// field has a default that an empty instance picks up.
//
// It does not require EVERY field to be defaulted. A field may be optional
// with no static default because its default depends on a sibling field:
// Resources' limits default to their requests. That is a rule JSONSchema
// cannot express, so the field stays unset here, and Go's WithDefaults
// resolves it. Requiring a static default on every field would force a WRONG
// one onto such a field (a 250m limit under a 500m request) just to keep
// the enclosing block defaulted.
func defaultsWhenEmpty(s apiext.JSONSchemaProps) bool {
	if len(s.Required) > 0 {
		return false
	}
	for _, p := range s.Properties {
		if p.Default != nil {
			return true
		}
	}
	return false
}

// kclType lowers a property schema to a KCL type expression.
func kclType(s apiext.JSONSchemaProps, pkgPath string) (string, error) {
	if s.Ref != nil {
		ref, ok := localRefName(*s.Ref, pkgPath)
		if !ok {
			return "", fmt.Errorf("reference %s leaves the tier package; tier specs are closed and self-contained", *s.Ref)
		}
		return kclSchemaName(ref), nil
	}
	switch s.Type {
	case "string":
		return "str", nil
	case "integer":
		return "int", nil
	case "number":
		return "float", nil
	case "boolean":
		return "bool", nil
	case "array":
		if s.Items == nil || s.Items.Schema == nil {
			return "", fmt.Errorf("array with no item schema")
		}
		inner, err := kclType(*s.Items.Schema, pkgPath)
		if err != nil {
			return "", err
		}
		return "[" + inner + "]", nil
	case "object":
		if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
			inner, err := kclType(*s.AdditionalProperties.Schema, pkgPath)
			if err != nil {
				return "", err
			}
			return "{str:" + inner + "}", nil
		}
	}
	return "", fmt.Errorf("unsupported schema type %q: a tier spec is scalars, closed enums, lists and tier structs only", s.Type)
}

// fieldChecks lowers the JSONSchema validation keywords to KCL check lines.
// Optional fields are guarded so an unset value passes.
func fieldChecks(schemaName, field string, s apiext.JSONSchemaProps, optional bool) []string {
	var out []string
	for _, c := range fieldCheckPairs(schemaName, field, field, s) {
		cond := c.cond
		if optional {
			cond = fmt.Sprintf("%s == Undefined or %s", field, cond)
		}
		out = append(out, cond+", "+c.msg)
	}
	return out
}

// kclCheck is one check line, kept as a (condition, message) PAIR until it is
// rendered. The per-item lowering wraps a condition in `all _x in f { ... }`,
// and it used to recover the condition by splitting the rendered line on its
// first ", ". A regex.match(x, "p") or an enum list carries ", " inside the
// condition itself, so an items:Pattern produced `{ regex.match(_x }`, which
// is not KCL. Structure is not re-parsed out of text.
type kclCheck struct{ cond, msg string }

// fieldCheckPairs lowers one schema's keywords with subject as the KCL
// expression under test and label as the field name in messages.
func fieldCheckPairs(schemaName, subject, label string, s apiext.JSONSchemaProps) []kclCheck {
	msg := func(format string, args ...any) string {
		return quoteKCLString(fmt.Sprintf("%s.%s ", schemaName, label) + fmt.Sprintf(format, args...))
	}
	var out []kclCheck
	add := func(cond, m string) { out = append(out, kclCheck{cond, m}) }
	if len(s.Enum) > 0 {
		vals := make([]string, 0, len(s.Enum))
		for _, e := range s.Enum {
			vals = append(vals, string(e.Raw))
		}
		add(fmt.Sprintf("%s in [%s]", subject, strings.Join(vals, ", ")), msg("must be one of %s", strings.Join(vals, ", ")))
	}
	if s.Pattern != "" {
		add(fmt.Sprintf("regex.match(%s, %s)", subject, quoteKCLString(s.Pattern)), msg("must match %s", s.Pattern))
	}
	if s.MinLength != nil && *s.MinLength > 0 {
		add(fmt.Sprintf("len(%s) >= %d", subject, *s.MinLength), msg("must be at least %d characters", *s.MinLength))
	}
	if s.MaxLength != nil {
		add(fmt.Sprintf("len(%s) <= %d", subject, *s.MaxLength), msg("must be at most %d characters", *s.MaxLength))
	}
	if s.MinItems != nil && *s.MinItems > 0 {
		add(fmt.Sprintf("len(%s) >= %d", subject, *s.MinItems), msg("needs at least %d entries", *s.MinItems))
	}
	if s.MaxItems != nil {
		add(fmt.Sprintf("len(%s) <= %d", subject, *s.MaxItems), msg("allows at most %d entries", *s.MaxItems))
	}
	if s.MaxProperties != nil {
		add(fmt.Sprintf("len(%s) <= %d", subject, *s.MaxProperties), msg("allows at most %d entries", *s.MaxProperties))
	}
	if s.Minimum != nil {
		add(fmt.Sprintf("%s >= %s", subject, fmtNum(*s.Minimum)), msg("must be at least %s", fmtNum(*s.Minimum)))
	}
	if s.Maximum != nil {
		add(fmt.Sprintf("%s <= %s", subject, fmtNum(*s.Maximum)), msg("must be at most %s", fmtNum(*s.Maximum)))
	}
	// Per-item constraints on a list of scalars (e.g. crds).
	if s.Type == "array" && s.Items != nil && s.Items.Schema != nil && s.Items.Schema.Ref == nil {
		for _, c := range fieldCheckPairs(schemaName, "_x", label+"[]", *s.Items.Schema) {
			add(fmt.Sprintf("all _x in %s { %s }", subject, c.cond), c.msg)
		}
	}
	return out
}

// kclDefault renders a JSON default value as a KCL literal.
func kclDefault(raw []byte) (string, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", err
	}
	return renderKCLValue(v, 0), nil
}

func fmtNum(f float64) string {
	if f == float64(int64(f)) {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// quoteKCLDoc renders a docstring. Triple quotes are safe unless the text
// itself contains them.
func quoteKCLDoc(s string) string {
	return `"""` + strings.ReplaceAll(s, `"""`, `\"\"\"`) + `"""`
}

// firstParagraph returns a type's doc up to its first blank line, with the
// line breaks collapsed. The rest of the reasoning stays in the Go source,
// which the generated header points at.
func firstParagraph(doc string) string {
	para, _, _ := strings.Cut(strings.TrimSpace(doc), "\n\n")
	return strings.Join(strings.Fields(para), " ")
}

func toSnake(s string) string {
	isUpper := func(i int) bool { return i < len(s) && s[i] >= 'A' && s[i] <= 'Z' }
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUpper(i) {
			// A word boundary is lower->Upper, or the last capital of an
			// acronym run that is followed by a lowercase letter
			// ("CDNConfig" -> "cdn_config").
			if i > 0 && (!isUpper(i-1) || (i+1 < len(s) && !isUpper(i+1))) {
				b.WriteByte('_')
			}
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// WriteTierKCL regenerates kcl/tiers/tiers_gen.k in forgeRoot.
func WriteTierKCL(forgeRoot, pkgPath string) (string, error) {
	in, err := LoadTierSchemas(forgeRoot)
	if err != nil {
		return "", err
	}
	body, err := GenerateTierKCL(in, pkgPath)
	if err != nil {
		return "", err
	}
	path := filepath.Join(forgeRoot, TierKCLPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return body, atomicfile.WriteFile(path, []byte(body), 0o644)
}
