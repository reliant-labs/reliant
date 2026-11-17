// File: internal/cli/audit/audit_unscoped_auth_rawsql.go
//
// The part of `unscoped_auth` that looks at hand-written SQL.
//
// # Why the caller-resolution predicate is not enough here
//
// The neighbouring file classifies an RPC by one question: does the
// handler body reach the auth seam? That question is a sound proxy for
// "is this scoped" in the case it was designed against — a delegating
// CRUD handler, where the scaffold has already written the query and
// resolving the caller is the only step left undone. Forge knows the op
// seam in that case, which is why it can hand back a wrapper that
// finishes the job.
//
// It is a proxy for nothing at all when the query is a Go string. The
// generated ORM cannot express GROUP BY or SUM, so forge itself routes
// every reporting or rollup screen to hand-written SQL — and on the
// other side of that string literal the AST pass is blind. Resolving the
// caller and then omitting the owner predicate is exactly as easy as
// doing it right, and produces the identical ✓.
//
// This is not hypothetical. A measured run declared `forge:owner` on a
// column, scoped thirty CRUD handlers from the wrapper this audit
// printed, and went green. Two aggregation RPCs then read across the
// boundary anyway:
//
//	if _, err := middleware.GetUser(ctx); err != nil {   // the audit saw this
//	    return nil, err
//	}
//	...
//	WHERE vehicle_id = $1 AND recorded_at >= $3          // and not this
//
// Both appeared in `scoped_rpcs`, neither in `unscoped_rpcs`, and the
// command exited 0 over an RPC that returned any owner's rows to any
// signed-in caller who could name a row id. The run's own summary: the
// assurance layer stops at the edge of a SQL string, and this shape
// lives on the other side of that edge.
//
// # Three states, not two
//
// The fix is not to analyse SQL — forge has no business claiming to
// understand an arbitrary query, and a checker that pretended to would
// be the same false confidence wearing a parser. It is to stop
// collapsing two different facts onto one word. "Reaches the seam" and
// "is scoped" were the same answer; they are now three:
//
//	raw SQL over an owner-declared table, owner column ABSENT
//	    → ERROR. Forge is not guessing: it knows the table, it knows the
//	      column the migration declared, and that column appears nowhere
//	      in the query. This is the shape that leaked.
//
//	raw SQL over an owner-declared table, owner column PRESENT
//	    → WARN, reported as UNVERIFIABLE. The column is in the text,
//	      which is evidence; forge still cannot see that the value bound
//	      to it came from the caller's claims rather than from the
//	      request, so it says it did not check. It does not fail the
//	      build: there is no evidence of a defect, only an absence of
//	      evidence of correctness, and those deserve different words.
//
//	raw SQL over a table nobody declared owned
//	    → silent, as before.
//
// The middle state is the one that keeps this honest. Converting "I did
// not check that" into "I checked and it is fine" is the defect the
// whole category exists to remove; converting it into "this is broken"
// would be the same dishonesty pointing the other way, and would train
// the reader to skip the check.
//
// # Not firing on everything is the design constraint
//
// A rule that flagged every raw query would be suppressed within a week,
// and the real leak would be invisible again — this time behind a
// suppression rather than behind a ✓. So three conditions must all hold
// before anything is said: the project declared ownership somewhere, the
// string is shaped like a query rather than prose that mentions a table,
// and the table it reads is one of the declared ones. A metrics rollup
// over a catalog stays silent, and an unarmed project stays silent
// entirely, which preserves the arming model the rest of the category
// runs on: forge ships no ownership of its own, and a fresh scaffold is
// not red on day one.
//
// The escape hatch is the EXISTING `forge:auth-unscoped-ok: <reason>`
// directive, reused rather than duplicated. A second spelling would mean
// a reader who had learned one would be refused by the other.
//
// # What it deliberately does not do
//
// It does not parse SQL, resolve a query built by string concatenation
// at runtime, or follow a helper into another package — the same
// package-local limit the seam analysis has, for the same reason: this
// pass resolves names, not types. Each of those is a false NEGATIVE,
// which leaves the reader exactly where they were. The direction that
// was unacceptable was the false positive ✓, and that one is closed.

package audit

import (
	"go/ast"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/sqlident"
)

// rawSQLRPC is one authenticated RPC whose query forge could not verify,
// in either of the two senses above. Which list it lands in carries the
// severity; the fields are the same either way, because the reader's
// next move is the same: open the named file, find the named table, and
// check the predicate forge could not.
type rawSQLRPC struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	File    string `json:"file"`
	// Tables are the owner-declared tables this RPC's SQL reads, sorted.
	Tables []string `json:"tables"`
	// OwnerColumns are the columns those tables declared as their owner,
	// in the same order. They are what make the finding falsifiable: the
	// reader can open the migration, see the declaration, and either add
	// the predicate or withdraw the marker.
	OwnerColumns []string `json:"owner_columns"`
	// MentionsOwnerColumn distinguishes the two states. False is the
	// leak shape; true is "present in the text, not verified".
	MentionsOwnerColumn bool `json:"mentions_owner_column"`
}

// looksLikeSQLRE decides whether a string constant is a query or merely
// prose that happens to name a table. Requiring the verb AND its
// companion keyword is what keeps a log line ("summarising vehicles for
// the caller") out: a sentence rarely contains SELECT … FROM.
var looksLikeSQLRE = regexp.MustCompile(`(?is)\bselect\b[\s\S]*\bfrom\b|\binsert\s+into\b|\bdelete\s+from\b|\bupdate\b[\s\S]*\bset\b`)

// sqlTableRefRE pulls the table names out of a query: whatever follows
// FROM, JOIN, INTO or UPDATE. It is deliberately shallow — it will miss
// a table named only inside a CTE reference and will happily return a
// subquery alias — because the result is INTERSECTED with the declared
// owner set before anything is reported. A spurious name that nobody
// declared owned falls out; a real one that is declared does not.
// The name may be schema-qualified and quoted (`public.vehicles`); the
// table is its last part, resolved by sqlident the way postgres would.
var sqlTableRefRE = regexp.MustCompile(`(?is)\b(?:from|join|into|update)\s+(` + sqlident.Qualified + `)`)

// sqlLiteralsByDecl maps each function declaration in a handler package
// to the SQL-shaped strings its body can reach: literals written inline,
// plus package-level string constants it names.
//
// Constants are followed because that is where a real rollup lives.
// Nobody inlines a forty-line aggregate at the call site, and a check
// that only read inline literals would report the project that actually
// leaked as clean — the same silent pass relocated.
func sqlLiteralsByDecl(decls []*ast.FuncDecl, consts map[string]string) [][]string {
	out := make([][]string, len(decls))
	for i, fn := range decls {
		var found []string
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.BasicLit:
				if node.Kind == token.STRING {
					if s, err := strconv.Unquote(node.Value); err == nil && looksLikeSQLRE.MatchString(s) {
						found = append(found, s)
					}
				}
			case *ast.Ident:
				if s, ok := consts[node.Name]; ok {
					found = append(found, s)
				}
			}
			return true
		})
		out[i] = found
	}
	return out
}

// packageSQLConsts collects package-level string constants and vars
// whose value is SQL-shaped, keyed by name.
func packageSQLConsts(files []*ast.File) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					lit, ok := vs.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil || !looksLikeSQLRE.MatchString(s) {
						continue
					}
					out[name.Name] = s
				}
			}
		}
	}
	return out
}

// reachableSQL closes the per-declaration literal sets over the
// package's own call graph, so a handler that factors its query into a
// helper is judged on the query, not on the delegation.
//
// This mirrors seamReachingNames exactly — same fixed point, same
// package-local limit, same monotone bound — because it answers the
// mirror-image question, and the two must agree about how far "this
// handler does X" reaches. If following one hop is right for finding the
// caller resolution, it is right for finding the query.
func reachableSQL(decls []*ast.FuncDecl, direct [][]string) map[string][]string {
	// Name-keyed, like the seam closure. A name carrying several
	// declarations accumulates all of their SQL: over-collecting here is
	// the safe direction, since every table is intersected with the
	// declared owner set before it can produce a finding.
	byName := map[string][]string{}
	for i, fn := range decls {
		byName[fn.Name.Name] = append(byName[fn.Name.Name], direct[i]...)
	}

	// calls[i] is the set of package-local names declaration i invokes.
	calls := make([]map[string]bool, len(decls))
	for i, fn := range decls {
		calls[i] = map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				calls[i][f.Name] = true
			case *ast.SelectorExpr:
				calls[i][f.Sel.Name] = true
			}
			return true
		})
	}

	// Every round that continues has added at least one string to some
	// declaration's set, and sets only grow, so the loop is bounded by
	// declarations × the total literal count. Written as an explicit
	// bound for the same reason the seam closure is: a fixed point that
	// silently relied on its own monotonicity would be one edit away
	// from hanging the audit.
	for round := 0; round <= len(decls); round++ {
		changed := false
		for i, fn := range decls {
			self := fn.Name.Name
			for callee := range calls[i] {
				if callee == self {
					continue
				}
				for _, sql := range byName[callee] {
					if !containsString(byName[self], sql) {
						byName[self] = append(byName[self], sql)
						changed = true
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return byName
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// sqlOwnerEvidence is what the SQL half learned about one handler.
type sqlOwnerEvidence struct {
	Tables              []string
	OwnerColumns        []string
	MentionsOwnerColumn bool
}

// classifyHandlerSQL reports which owner-declared tables a handler's
// reachable SQL reads, and whether every one of those tables' owner
// columns appears somewhere in that SQL.
//
// found is false when the handler reaches no SQL over a declared table
// at all — the silent case, and by far the common one.
//
// The owner-column test requires EVERY owned table's column to be
// present, not any. A query joining two owned tables and filtering on
// only one of them is the shape the same run shipped in its cost
// rollup — the predicate repeated in some arms and missing from others —
// and "at least one predicate appeared" would have cleared it.
func classifyHandlerSQL(queries []string, ownerTables map[string]string) (sqlOwnerEvidence, bool) {
	seen := map[string]bool{}
	// Per-table: did the owner column appear in a query that reads it?
	filtered := map[string]bool{}
	for _, q := range queries {
		lower := strings.ToLower(q)
		for _, m := range sqlTableRefRE.FindAllStringSubmatch(q, -1) {
			table := sqlident.Last(m[1])
			column, owned := ownerTables[table]
			if !owned {
				continue
			}
			seen[table] = true
			if !filtered[table] {
				filtered[table] = regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.ToLower(column)) + `\b`).MatchString(lower)
			}
		}
	}
	if len(seen) == 0 {
		return sqlOwnerEvidence{}, false
	}

	ev := sqlOwnerEvidence{MentionsOwnerColumn: true}
	for table := range seen {
		ev.Tables = append(ev.Tables, table)
	}
	sort.Strings(ev.Tables)
	for _, table := range ev.Tables {
		ev.OwnerColumns = append(ev.OwnerColumns, ownerTables[table])
		if !filtered[table] {
			ev.MentionsOwnerColumn = false
		}
	}
	return ev, true
}

// rawSQLScopingHint says what forge did and did not check, and what the
// reader has to do that forge cannot do for them.
//
// It carries no pasteable wrapper, unlike the CRUD gate's remediation,
// and that absence is deliberate: forge would have to guess where in an
// arbitrary query the predicate belongs — which join arm, which
// subquery, which CTE — and a plausible-looking snippet pasted into the
// wrong arm of a rollup is worse than none, because it looks like the
// fix was applied. The CRUD gate can offer code because it owns the op
// seam; here it can only say precisely what is missing and where.
func rawSQLScopingHint() string {
	return "forge reads handler code, not SQL: it can see that the query names an owner-declared table and whether the declared " +
		"owner column appears in the text, and nothing beyond that. Add the owner predicate to EVERY arm of the query that reads " +
		"an owned table — each join, subquery and UNION branch — and bind it to the value you resolved from the caller, never to a " +
		"field off the request. Pin it with a test that a caller from one owner reads zero rows belonging to another; if an RPC is " +
		"intentionally global, say so in code above it with `// " + AuthUnscopedOKDirective + " <reason>`."
}

// describeRawSQL renders the raw-SQL findings for the summary line,
// capped the same way the CRUD gating summary is. The full set is always
// in details.
func describeRawSQL(findings []rawSQLRPC) string {
	const max = 5
	parts := make([]string, 0, max)
	for i, f := range findings {
		if i == max {
			parts = append(parts, "... ("+strconv.Itoa(len(findings))+" total)")
			break
		}
		parts = append(parts, f.Service+"."+f.Method+" ("+strings.Join(f.Tables, ", ")+")")
	}
	return strings.Join(parts, ", ")
}

func sortRawSQL(v []rawSQLRPC) {
	sort.SliceStable(v, func(i, j int) bool {
		if v[i].Service != v[j].Service {
			return v[i].Service < v[j].Service
		}
		return v[i].Method < v[j].Method
	})
}
