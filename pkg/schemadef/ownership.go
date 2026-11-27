package schemadef

import (
	"fmt"
	"regexp"
	"strings"
)

// TableMarkerOwner declares, on a TABLE, whose rows these are — and, unlike
// the column form of the same word (ColumnMarkerOwner), it is ENFORCED: the
// generated CRUD ops over the table scope every read and write to the
// caller's rows themselves.
//
//	COMMENT ON TABLE plans    IS 'forge:owner=thread_id>chat_id>user_id';
//	COMMENT ON TABLE accounts IS 'forge:owner=user:user_id,org:org_id';
//
// The value is a comma-separated list of alternative owners (a row is the
// caller's when ANY of them matches). Each is `[<kind>:]<path>`:
//
//   - kind names a kind of principal the application resolves for a caller
//     ("user", "org", "team", …) through crud.OwnerResolver. It defaults to
//     "user". Forge attaches no meaning to it beyond handing it to the
//     resolver: only the application knows its org model.
//   - path is one or more columns joined by `>`. The first is a column of
//     this table. Each column but the last must be a single-column FOREIGN
//     KEY; the next column is read on the table it references. The last
//     column is compared with the caller's values for kind. So
//     `thread_id>chat_id>user_id` reads "plans.thread_id references a thread,
//     whose chat_id references a chat, whose user_id is the caller".
//
// The spec holds no spaces, so other markers can share the comment. A spec
// that does not resolve against the applied schema (an unknown column, a hop
// through a column that is not a foreign key) fails generate: a mis-declared
// owner must never degrade silently into an unscoped table.
//
// Why a table marker rather than a column marker: ownership through a join
// is a fact about the table (its path to an owner), not about any one of its
// columns, and an owner reached by alternatives (user OR org) names several
// columns at once. The column form keeps its meaning (it arms the
// `unscoped_auth` audit and scaffolds a scoping wrapper into the owned CRUD
// shim) and is unaffected.
const TableMarkerOwner = "forge:owner"

// TableMarkerPublic declares that a table's rows are deliberately readable
// by every caller (a catalog, a price list): `COMMENT ON TABLE plans_catalog
// IS 'forge:public: the pricing page lists every plan'`. It changes no
// codegen. It answers the `crud-table-unowned` lint, which asks every table
// with authenticated CRUD to say whose rows it holds. The text after the
// marker is the reason, and the lint asks for one.
const TableMarkerPublic = "forge:public"

// TableMarkerUnscoped declares that a table's rows belong to no principal
// the generated CRUD could scope by, or that the scoping is written by hand
// (an admin-only service, an op override, a row-level-security policy):
// `COMMENT ON TABLE feature_flags IS 'forge:unscoped: admin service only'`.
// Like TableMarkerPublic it changes no codegen and answers the
// `crud-table-unowned` lint; the text after it is the reason.
const TableMarkerUnscoped = "forge:unscoped"

// OwnerTerm is one parsed alternative of a TableMarkerOwner spec.
type OwnerTerm struct {
	// Kind is the principal kind the caller's values are resolved for.
	Kind string
	// Path is the column chain, first column on the declaring table.
	Path []string
}

// DefaultOwnerKind is the kind a term without a `<kind>:` prefix names.
const DefaultOwnerKind = "user"

var (
	ownerTableMarkerRE = regexp.MustCompile(`(?:^|[^\w:-])` + regexp.QuoteMeta(TableMarkerOwner) + `=(\S*)`)
	ownerKindRE        = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	ownerColumnRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// ParseOwnerSpec parses the value of a TableMarkerOwner declaration (the
// text after `forge:owner=`).
func ParseOwnerSpec(spec string) ([]OwnerTerm, error) {
	spec = strings.TrimRight(spec, ";.")
	if spec == "" {
		return nil, fmt.Errorf("%s= names no owner: write `%s=<column>` or `%s=<kind>:<fk>>…><column>`", TableMarkerOwner, TableMarkerOwner, TableMarkerOwner)
	}
	var terms []OwnerTerm
	seen := map[string]bool{}
	for _, raw := range strings.Split(spec, ",") {
		kind, path := DefaultOwnerKind, raw
		if i := strings.Index(raw, ":"); i >= 0 {
			kind, path = raw[:i], raw[i+1:]
			if !ownerKindRE.MatchString(kind) {
				return nil, fmt.Errorf("%s=%s: owner kind %q must be lower-case letters, digits and underscores", TableMarkerOwner, spec, kind)
			}
		}
		cols := strings.Split(path, ">")
		for _, c := range cols {
			if !ownerColumnRE.MatchString(c) {
				return nil, fmt.Errorf("%s=%s: %q is not a column name (a path is columns joined by `>`)", TableMarkerOwner, spec, c)
			}
		}
		key := kind + ":" + path
		if seen[key] {
			return nil, fmt.Errorf("%s=%s: owner %q is declared twice", TableMarkerOwner, spec, key)
		}
		seen[key] = true
		terms = append(terms, OwnerTerm{Kind: kind, Path: cols})
	}
	return terms, nil
}

// OwnerSpec returns the table's TableMarkerOwner declaration, parsed.
// declared is false when the comment carries none (the column form,
// ColumnMarkerOwner, lives on a column and is not read here). err reports a
// declaration that does not parse.
func (t Table) OwnerSpec() (terms []OwnerTerm, declared bool, err error) {
	m := ownerTableMarkerRE.FindStringSubmatch(t.Comment)
	if m == nil {
		return nil, false, nil
	}
	terms, err = ParseOwnerSpec(m[1])
	if err != nil {
		return nil, true, fmt.Errorf("table %s: %w", t.Name, err)
	}
	return terms, true, nil
}

// ScopeExemption reports a TableMarkerPublic or TableMarkerUnscoped
// declaration: which marker, and the reason written after it ("" when the
// comment gives none). ok is false when the table declares neither.
func (t Table) ScopeExemption() (marker, reason string, ok bool) {
	for _, mk := range []string{TableMarkerPublic, TableMarkerUnscoped} {
		re := exemptionMarkerRE(mk)
		loc := re.FindStringSubmatchIndex(t.Comment)
		if loc == nil {
			continue
		}
		rest := t.Comment[loc[1]:]
		// The reason runs to the next forge: marker, if another shares the comment.
		if i := strings.Index(rest, "forge:"); i >= 0 {
			rest = rest[:i]
		}
		return mk, strings.Trim(rest, " \t\r\n:=-—–;,."), true
	}
	return "", "", false
}

func exemptionMarkerRE(marker string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\w:-])` + regexp.QuoteMeta(marker) + `(?:[^\w-]|$)`)
}

// OwnerHop is one foreign-key step of a resolved owner path: the table the
// previous column references, the column it references there, and the
// column read on it next (the next hop's foreign key, or the owner column
// itself on the last hop).
type OwnerHop struct {
	Schema string
	Table  string
	Key    string
	Column string
}

// ResolvedOwnerTerm is an OwnerTerm checked against the applied schema.
type ResolvedOwnerTerm struct {
	Kind string
	// Column is the first column of the path, on the owned table. With no
	// Hops it is the owner column itself.
	Column string
	// Constraint is the foreign-key constraint on Column when the path has
	// hops, "" otherwise.
	Constraint string
	Hops       []OwnerHop
}

// ResolveOwnership resolves the table's TableMarkerOwner declaration
// against tables (the applied schema; it must contain every table a path
// passes through). declared is false when the table declares none. Every
// way the spec can fail to describe the schema is an error.
func ResolveOwnership(t Table, tables []Table) (terms []ResolvedOwnerTerm, declared bool, err error) {
	spec, declared, err := t.OwnerSpec()
	if !declared || err != nil {
		return nil, declared, err
	}
	byName := make(map[string]Table, len(tables))
	for _, tb := range tables {
		byName[tableKey(tb.Schema, tb.Name)] = tb
		if _, ok := byName[tb.Name]; !ok {
			byName[tb.Name] = tb
		}
	}
	for _, term := range spec {
		rt, err := resolveOwnerTerm(t, term, byName)
		if err != nil {
			return nil, true, fmt.Errorf("table %s: %s=%s:%s: %w", t.Name, TableMarkerOwner, term.Kind, strings.Join(term.Path, ">"), err)
		}
		terms = append(terms, rt)
	}
	return terms, true, nil
}

func tableKey(schema, name string) string {
	if schema == "" {
		schema = "public"
	}
	return schema + "." + name
}

func resolveOwnerTerm(t Table, term OwnerTerm, byName map[string]Table) (ResolvedOwnerTerm, error) {
	rt := ResolvedOwnerTerm{Kind: term.Kind, Column: term.Path[0]}
	cur := t
	for i, col := range term.Path {
		if !tableHasColumn(cur, col) {
			return rt, fmt.Errorf("table %s has no column %s", cur.Name, col)
		}
		if i == len(term.Path)-1 {
			if i > 0 {
				rt.Hops[len(rt.Hops)-1].Column = col
			}
			break
		}
		fk, ok := singleColumnFK(cur, col)
		if !ok {
			return rt, fmt.Errorf("%s.%s is not a single-column foreign key, so the path cannot continue through it", cur.Name, col)
		}
		next, ok := byName[tableKey(cur.Schema, fk.RefTable)]
		if !ok {
			next, ok = byName[fk.RefTable]
		}
		if !ok {
			return rt, fmt.Errorf("%s.%s references %s, which is not in the applied schema", cur.Name, col, fk.RefTable)
		}
		if i == 0 {
			rt.Constraint = fk.Name
		} else {
			rt.Hops[len(rt.Hops)-1].Column = col
		}
		rt.Hops = append(rt.Hops, OwnerHop{Schema: next.Schema, Table: next.Name, Key: fk.RefColumn})
		cur = next
	}
	return rt, nil
}

func tableHasColumn(t Table, col string) bool {
	for _, c := range t.Columns {
		if c.Name == col {
			return true
		}
	}
	return false
}

func singleColumnFK(t Table, col string) (ForeignKey, bool) {
	for _, fk := range t.ForeignKeys {
		if fk.Column == col && len(fk.KeyColumns) == 0 {
			return fk, true
		}
	}
	return ForeignKey{}, false
}
