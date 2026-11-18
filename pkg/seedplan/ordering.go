package seedplan

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Column-ordering CHECK constraints — the shape a per-column model cannot see.
//
// Every constraint model in this package keys on ONE column (EnumPools,
// CheckBounds, LengthBounds), and the introspection feeding them drops
// anything whose conkey names two columns (singleCheckColumn returns "").
// A `CHECK (expires_at > issued_at)` is therefore structurally invisible:
// the seeder draws each column independently, timestampLiteral hands every
// non-managed time column in a row the SAME instant, and the INSERT dies on
// a constraint the applied schema states plainly.
//
// That is the same rule the rest of this package already lives by
// (constraints.go): values FORGE synthesizes are satisfied BY CONSTRUCTION,
// because forge is the one that put the constraint there. Skipping the
// table is not an option — a plan that seeds nothing and reports success is
// the false green this whole mechanism exists to prevent.
//
// # What is satisfied, and how
//
// The recognized shape is a binary comparison between two columns of one
// table — `a > b`, `a >= b`, `a < b`, `a <= b`. It is the overwhelmingly
// common multi-column CHECK: validity windows (expires_at > issued_at),
// date ranges (ends_on >= starts_on), bounded pairs (max_qty >= min_qty).
//
// The comparison rarely stands alone. Optional timestamps are ordinary, so
// the constraint an author actually writes over a pair of them is
// NULL-GUARDED:
//
//	CHECK (issued_at IS NULL OR expires_at IS NULL OR expires_at > issued_at)
//
// which is the SAME ordering requirement — the guard only exempts rows where
// one side is absent. forge reads it structurally rather than by spelling: an
// OR is TRUE as soon as ONE branch is, and an AND is TRUE when EVERY branch
// is, so the question "does forge's placement satisfy this expression" has a
// three-line answer that does not care how many guards precede the
// comparison, in which order they appear, or whether the comparison is a
// conjunction of several. Everything else — COALESCE, CASE, NOT, an
// arithmetic offset (`b > a + interval '1 day'`) — is left to the warning
// below, because forge's placement does not prove those.
//
// Each recognized comparison is an EDGE in a per-table DAG: lo -> hi, "hi must
// sit above lo". Columns are ranked by longest path from a root, and the
// component is then placed as a LADDER, lowest rank first, each rank strictly
// above the running floor the ranks below it established (see orderLadder).
// The ordering holds by construction rather than by luck, and non-strict (>=)
// constraints are satisfied by the strict assignment too, so strictness is not
// tracked. Root columns keep the value they would have had anyway, so a schema
// with no ordering constraint seeds exactly as before.
//
// TIME chains are the one exception: they still take base + rank * 30 days
// directly, because a timestamp column has no value pool to honor.
//
// The ladder exists because the placement must respect what a column is
// ALLOWED to hold, not merely what the comparison requires. Its predecessor
// took the root's value plus the rank number, which assumed any column can
// hold any value — false for a column drawing from a closed pool, whether the
// author's `{min, max, step}` in db/seeds/vocab.yaml or a numeric `IN (...)`
// CHECK. Measured on two consecutive dogfood runs of one schema: a declared
// [500000, 2900000] on `invoices.amount_cents` seeded a minimum of 1, because
// the pairing CHECK pinned it to `amount_paid_cents + 1` and discarded the
// declaration outright. A pooled column now keeps its own draw when that
// already clears the floor and otherwise takes the lowest pool member that
// does, which honors the CHECK and the declaration together whenever the two
// are jointly satisfiable — and says so by name (orderVocabWarnings) when they
// are not.
//
// # What is NOT satisfied, and why that is stated rather than hidden
//
// A constraint forge cannot place — a cycle, a comparison between columns of
// different types, one involving a key or UNIQUE column another mechanism
// already owns, or an expression whose truth its placement does not prove
// (num_nonnulls, CASE, COALESCE, NOT, an offset comparison) — is left alone
// and recorded as a plan warning naming the constraint. The seed then either
// happens to satisfy it or fails at the INSERT with the full explanation
// Apply now attaches. Guessing at an arbitrary predicate would write rows
// that contradict a rule the schema states, which is worse than not writing
// them.
//
// # The OTHER multi-column shape
//
// A DISCRIMINATED UNION — a top-level OR of AND-groups, each pinning a
// discriminator column to a literal and constraining its siblings from there —
// is not an ordering and is placed by union.go instead. That pass runs after
// this one (so it never claims a column an ordering CHECK already places) and
// speaks for every constraint whose expression parses as a union, placed or
// refused, which is why the loop below steps over them: one line of SQL
// collects one warning.

// orderRel is one recognized ordering constraint, normalized so hi must sit
// above lo. It carries the constraint's name so a refusal can point at the
// exact line of SQL.
type orderRel struct {
	lo, hi     string
	constraint string
}

// orderSlot is one column's place in its table's ordering DAG.
type orderSlot struct {
	// rank is the longest path from a root; 0 means "a root", which keeps
	// its natural synthesized value.
	rank int
	// roots are the rank-0 columns of this column's connected component,
	// sorted. The base value is the MAX of their natural values, which is
	// what makes the arithmetic above a proof rather than a hope.
	roots []string
	// members is every column of this component, sorted. The placement is a
	// LADDER over the whole component (see orderLadder) rather than a
	// per-column function of rank, because a column drawing from a closed
	// pool cannot be placed at an arbitrary arithmetic offset.
	members []string
	// cons names the ordering constraints governing this column, sorted, so
	// a refusal can point at the exact line of SQL.
	cons []string
	// kind is the canonical type shared by every column of the component.
	kind schemadef.CanonicalType
}

// orderCompareRE matches ONE column-to-column comparison in postgres's
// canonical spelling, e.g. `expires_at > issued_at`. Both operands must be
// bare column references: a cast, a function call, or an arithmetic offset
// (`issued_at + '1 day'::interval`) deliberately does not match, because a
// placement forge cannot prove satisfies the expression is a placement it
// must not make.
var orderCompareRE = regexp.MustCompile(
	`^"?([a-z_][a-z0-9_]*)"?\s*(>=|>|<=|<)\s*"?([a-z_][a-z0-9_]*)"?$`)

// derivationRE matches a CHECK that states one column EQUALS an arithmetic
// expression over others in the same row — `total = subtotal + tax`,
// `balance = amount - amount_paid`. Postgres renders these with the operands
// parenthesized, so the right-hand side is matched loosely and validated by
// the caller against the columns postgres says the constraint spans.
//
// This is deliberately NOT fed into the ordering pass. An equality is not an
// ordering edge and forge's placement cannot satisfy it: assigning each of
// three columns an independent value leaves `a = b + c` true only by
// coincidence. Recognizing the shape buys a better REFUSAL, not a placement.
var derivationRE = regexp.MustCompile(
	`^"?([a-z_][a-z0-9_]*)"?\s*=\s*(.+[-+*/].+)$`)

// derivedColumn returns the column a CHECK derives, when the constraint has
// the `<col> = <expression over sibling columns>` shape. ok is false for
// every other expression, including a plain column-to-column equality, which
// states an invariant rather than a derivation.
func derivedColumn(ck schemadef.CheckConstraint) (string, bool) {
	body, ok := checkBody(ck.Def)
	if !ok {
		return "", false
	}
	m := derivationRE.FindStringSubmatch(unwrapParens(body))
	if m == nil {
		return "", false
	}
	target := m[1]
	// The derived column must be one postgres says the constraint spans, and
	// the expression must reference at least one OTHER spanned column — that
	// is what makes it a derivation rather than a bound on a literal.
	spans := make(map[string]bool, len(ck.Columns))
	for _, c := range ck.Columns {
		spans[c] = true
	}
	if !spans[target] {
		return "", false
	}
	for _, c := range ck.Columns {
		if c != target && strings.Contains(m[2], c) {
			return target, true
		}
	}
	return "", false
}

// orderRelsFromCheck reads the ordering edges off an introspected CHECK that
// forge's placement is PROVEN to satisfy. ok is false for every expression
// whose truth the placement does not establish — those are named by the
// caller's warning rather than guessed at.
func orderRelsFromCheck(ck schemadef.CheckConstraint) ([]orderRel, bool) {
	body, ok := checkBody(ck.Def)
	if !ok {
		return nil, false
	}
	rels, ok := provableOrdering(body)
	if !ok || len(rels) == 0 {
		return nil, false
	}
	// Every parsed column must be one postgres says the constraint spans, or
	// the expression was not what the matcher thought it was.
	spans := make(map[string]bool, len(ck.Columns))
	for _, c := range ck.Columns {
		spans[c] = true
	}
	for i := range rels {
		if !spans[rels[i].lo] || !spans[rels[i].hi] {
			return nil, false
		}
		rels[i].constraint = ck.Name
	}
	return rels, true
}

// checkBody strips the `CHECK (...)` wrapper — and the `NOT VALID` suffix a
// constraint added over existing rows carries — off an introspected
// definition, leaving the bare expression. A NOT VALID constraint is still
// enforced on every INSERT, so it is satisfied exactly like a validated one.
func checkBody(def string) (string, bool) {
	s := strings.TrimSpace(def)
	s = strings.TrimSpace(strings.TrimSuffix(s, "NOT VALID"))
	if !strings.HasPrefix(s, "CHECK (") || !strings.HasSuffix(s, ")") {
		return "", false
	}
	return strings.TrimSpace(s[len("CHECK (") : len(s)-1]), true
}

// provableOrdering returns the ordering edges that make expr TRUE under
// forge's placement, which assigns each ranked column a value strictly above
// its lower bound. ok is false when the placement does not PROVE the
// expression — never when it merely might hold.
//
// The whole reading is three rules, and the soundness of each is the
// definition of the operator:
//
//   - OR: true as soon as ONE branch is, so the first placeable branch is
//     enough and the others (`issued_at IS NULL`, `expires_at IS NULL`, an
//     unrelated escape hatch) need no attention at all.
//   - AND: true only when EVERY branch is, so every branch must be placeable
//     and every edge it names is required.
//   - a comparison of two columns: the edge itself.
//
// A NULL guard is therefore not stripped, normalized, or special-cased — it
// is just a disjunct the OR rule steps over, which is why guard order,
// guard count, and one-sided guards all read the same.
func provableOrdering(expr string) ([]orderRel, bool) {
	e := unwrapParens(expr)
	if parts := splitTopLevel(e, "OR"); len(parts) > 1 {
		for _, part := range parts {
			if rels, ok := provableOrdering(part); ok {
				return rels, true
			}
		}
		return nil, false
	}
	if parts := splitTopLevel(e, "AND"); len(parts) > 1 {
		var all []orderRel
		for _, part := range parts {
			rels, ok := provableOrdering(part)
			if !ok {
				return nil, false
			}
			all = append(all, rels...)
		}
		return all, true
	}
	m := orderCompareRE.FindStringSubmatch(e)
	if m == nil {
		return nil, false
	}
	left, op, right := m[1], m[2], m[3]
	if left == right {
		return nil, false
	}
	if strings.HasPrefix(op, ">") {
		return []orderRel{{lo: right, hi: left}}, true
	}
	return []orderRel{{lo: left, hi: right}}, true
}

// sqlScan walks a canonical constraint expression byte by byte, tracking
// parenthesis depth and single-quoted literals. Both readers below need
// exactly this and nothing more: postgres parenthesizes every boolean
// operand, and a quoted literal may contain either a parenthesis or the
// word OR.
type sqlScan struct {
	depth int
	quote bool
}

// step consumes expr[i] and returns the next index to read — i+2 across the
// doubled quote that escapes one inside a literal, i+1 otherwise.
func (s *sqlScan) step(expr string, i int) int {
	switch c := expr[i]; {
	case s.quote:
		if c == '\'' {
			if i+1 < len(expr) && expr[i+1] == '\'' {
				return i + 2
			}
			s.quote = false
		}
	case c == '\'':
		s.quote = true
	case c == '(':
		s.depth++
	case c == ')':
		s.depth--
	}
	return i + 1
}

// atTop reports whether the current position is ordinary expression text —
// not nested in parentheses and not inside a string literal.
func (s *sqlScan) atTop() bool { return s.depth == 0 && !s.quote }

// splitTopLevel splits expr on a boolean operator appearing at parenthesis
// depth 0 outside any literal, which is the only place postgres's canonical
// spelling puts one. A single-element result means the operator is absent.
func splitTopLevel(expr, op string) []string {
	pad := " " + op + " "
	var (
		parts []string
		sc    sqlScan
		start int
	)
	for i := 0; i < len(expr); {
		if sc.atTop() && strings.HasPrefix(expr[i:], pad) {
			parts = append(parts, expr[start:i])
			i += len(pad)
			start = i
			continue
		}
		i = sc.step(expr, i)
	}
	return append(parts, expr[start:])
}

// unwrapParens removes parentheses that wrap the WHOLE expression. A leading
// paren that closes before the end is a different pair — `(a) OR (b)` is not
// a wrapped expression — so depth reaching 0 early ends the unwrapping.
func unwrapParens(expr string) string {
	for {
		expr = strings.TrimSpace(expr)
		if len(expr) < 2 || expr[0] != '(' || expr[len(expr)-1] != ')' {
			return expr
		}
		var sc sqlScan
		for i := 0; i < len(expr); {
			next := sc.step(expr, i)
			if sc.depth == 0 && next < len(expr) {
				return expr
			}
			i = next
		}
		expr = expr[1 : len(expr)-1]
	}
}

// buildOrderChains resolves every table's ordering DAG into per-column
// slots, and returns the warnings for the constraints it could not place.
func buildOrderChains(tables []schemadef.Table) (map[string]map[string]orderSlot, []string) {
	chains := map[string]map[string]orderSlot{}
	var warns []string
	for _, t := range tables {
		slots, w := tableOrderChains(t)
		warns = append(warns, w...)
		if len(slots) > 0 {
			chains[t.Name] = slots
		}
	}
	sort.Strings(warns)
	return chains, warns
}

// orderEligible reports whether a column may be PLACED by the ordering pass:
// it must be a plain synthesized data column of a comparable type. Keys,
// foreign keys and single-column-UNIQUE columns are owned by other
// mechanisms (referential derivation, distinct-value assignment) whose
// values this pass must not overwrite.
func orderEligible(t schemadef.Table, name string) (schemadef.Column, bool) {
	var col schemadef.Column
	found := false
	for _, c := range t.Columns {
		if c.Name == name {
			col, found = c, true
			break
		}
	}
	if !found || col.IsPK || col.IsGenerated || col.IsArray {
		return col, false
	}
	switch col.Type {
	case schemadef.TypeTime, schemadef.TypeInt, schemadef.TypeFloat:
	default:
		return col, false
	}
	for _, fk := range t.ForeignKeys {
		if fk.Column == name {
			return col, false
		}
	}
	if uniqueSingleColumn(t, name) {
		return col, false
	}
	return col, true
}

// tableOrderChains ranks one table's ordering constraints.
func tableOrderChains(t schemadef.Table) (map[string]orderSlot, []string) {
	var (
		rels  []orderRel
		warns []string
	)
	refuse := func(name, why string) {
		warns = append(warns, fmt.Sprintf(
			"seed plan: %s constraint %q spans two columns and forge cannot place its values (%s) — seeded rows satisfy it only by chance",
			t.Name, name, why))
	}
	// A discriminated union (union.go) is a multi-column CHECK this pass can
	// never read — its comparisons are against LITERALS, not sibling columns
	// — and that pass speaks for it, placed or refused. Stepping over those
	// here is what keeps one line of SQL from collecting two warnings that
	// contradict each other.
	claimed := unionClaimedChecks(t)
	for _, ck := range t.Checks {
		if len(ck.Columns) < 2 || claimed[ck.Name] {
			continue
		}
		found, ok := orderRelsFromCheck(ck)
		if !ok {
			// A derivation is a DIFFERENT failure from an unreadable
			// comparison, and it has a fix the author can act on. Saying only
			// "not a two-column ordering comparison" sends them looking for a
			// seeding workaround for something the schema should state
			// outright — measured twice, in two runs that both hand-rolled
			// the derivation instead.
			if col, isDerived := derivedColumn(ck); isDerived {
				warns = append(warns, fmt.Sprintf(
					"seed plan: %s constraint %q derives %s from other columns, which forge cannot place — "+
						"declare the derivation instead of asserting it: "+
						"`%s <type> GENERATED ALWAYS AS (<expression>) STORED`. "+
						"The database then maintains it, the CHECK becomes unnecessary, and the seeder skips the column. "+
						"For a value derived from OTHER ROWS (a sum over children), no GENERATED column can express it — "+
						"keep the column plain, mark the proto field `// forge:computed`, and write it from the RPC that owns the change.",
					t.Name, ck.Name, col, col))
				continue
			}
			// A biconditional lifecycle rule is the other failure with a
			// known fix, and it is the one that reaches here most often:
			// it parses as neither a guard nor a union, so without this
			// the author is told their status constraint is "not a
			// two-column ordering comparison" — a sentence about a
			// subsystem their constraint has nothing to do with.
			if rewrite, isBiconditional := biconditionalRewrite(ck.Def); isBiconditional {
				warns = append(warns, fmt.Sprintf(
					"seed plan: %s constraint %q %s — until then seeded rows satisfy it only by chance",
					t.Name, ck.Name, biconditionalAdvice(rewrite, biconditionalDiscriminator(rewrite))))
				continue
			}
			refuse(ck.Name, "not a two-column ordering comparison")
			continue
		}
		// One constraint can name several edges (`b > a AND c > b`), and an
		// AND needs all of them: an edge forge may not place leaves the
		// WHOLE constraint unsatisfied, so the refusal is all-or-nothing.
		why := ""
		for _, rel := range found {
			lo, okLo := orderEligible(t, rel.lo)
			hi, okHi := orderEligible(t, rel.hi)
			switch {
			case !okLo || !okHi:
				why = "one side is a key, a reference, a UNIQUE column or a non-comparable type"
			case lo.Type != hi.Type:
				why = fmt.Sprintf("%s is %s but %s is %s", rel.lo, lo.Type, rel.hi, hi.Type)
			}
			if why != "" {
				break
			}
		}
		if why != "" {
			refuse(ck.Name, why)
			continue
		}
		rels = append(rels, found...)
	}
	if len(rels) == 0 {
		return nil, warns
	}

	ranks, ok := rankColumns(rels)
	if !ok {
		names := map[string]bool{}
		for _, r := range rels {
			names[r.constraint] = true
		}
		for name := range names {
			refuse(name, "the ordering constraints form a cycle, which no assignment satisfies")
		}
		return nil, warns
	}

	comps := components(rels)
	kindOf := map[string]schemadef.CanonicalType{}
	consOf := map[string][]string{}
	for _, r := range rels {
		c, _ := orderEligible(t, r.lo)
		kindOf[r.lo] = c.Type
		c, _ = orderEligible(t, r.hi)
		kindOf[r.hi] = c.Type
		consOf[r.lo] = appendDistinct(consOf[r.lo], r.constraint)
		consOf[r.hi] = appendDistinct(consOf[r.hi], r.constraint)
	}

	slots := map[string]orderSlot{}
	for _, comp := range comps {
		var roots []string
		for _, c := range comp {
			if ranks[c] == 0 {
				roots = append(roots, c)
			}
		}
		sort.Strings(roots)
		for _, c := range comp {
			cons := append([]string(nil), consOf[c]...)
			sort.Strings(cons)
			slots[c] = orderSlot{
				rank: ranks[c], roots: roots, members: comp, cons: cons, kind: kindOf[c],
			}
		}
	}
	return slots, warns
}

// appendDistinct appends s unless it is already present. The lists it builds
// are a handful of constraint names, so a linear scan is the whole cost.
func appendDistinct(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

// rankColumns assigns each column the length of the longest path reaching
// it. ok is false when the edges contain a cycle.
func rankColumns(rels []orderRel) (map[string]int, bool) {
	indeg := map[string]int{}
	out := map[string][]string{}
	for _, r := range rels {
		if _, ok := indeg[r.lo]; !ok {
			indeg[r.lo] = 0
		}
		indeg[r.hi]++
		out[r.lo] = append(out[r.lo], r.hi)
	}
	rank := map[string]int{}
	var queue []string
	for c, d := range indeg {
		if d == 0 {
			queue = append(queue, c)
		}
	}
	sort.Strings(queue)
	seen := 0
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		seen++
		for _, next := range out[c] {
			if rank[c]+1 > rank[next] {
				rank[next] = rank[c] + 1
			}
			if indeg[next]--; indeg[next] == 0 {
				queue = append(queue, next)
				sort.Strings(queue)
			}
		}
	}
	if seen != len(indeg) {
		return nil, false
	}
	return rank, true
}

// components groups the columns into weakly-connected sets, each returned
// in sorted order.
func components(rels []orderRel) [][]string {
	parent := map[string]string{}
	var find func(string) string
	find = func(c string) string {
		p, ok := parent[c]
		if !ok || p == c {
			parent[c] = c
			return c
		}
		root := find(p)
		parent[c] = root
		return root
	}
	for _, r := range rels {
		a, b := find(r.lo), find(r.hi)
		if a != b {
			parent[a] = b
		}
	}
	byRoot := map[string][]string{}
	for c := range parent {
		root := find(c)
		byRoot[root] = append(byRoot[root], c)
	}
	roots := make([]string, 0, len(byRoot))
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	out := make([][]string, 0, len(roots))
	for _, r := range roots {
		cols := byRoot[r]
		sort.Strings(cols)
		out = append(out, cols)
	}
	return out
}

// OrderStepDays is how far apart consecutive ranks of a TIME chain sit — a
// month, which reads as a plausible validity window rather than an
// artifact. Exported because the scaffolded lifecycle test's create fixtures
// place their timestamps the same way (see codegen's crudTestFixtures):
// there is ONE ordering model, so a born fixture and the seeded row it
// mirrors can never disagree.
const OrderStepDays = 30

// OrderRanks returns the ordering rank of every column of t that a PLACEABLE
// ordering CHECK governs: 0 for a chain's root (which keeps its natural
// value) and r for a column that must sit r steps above it. A column absent
// from the map is governed by no ordering constraint forge can place, and
// takes its independent value.
//
// This is the same resolution the seed plan performs, exposed for the OTHER
// half of forge's generated data — the create-request fixtures in the born
// lifecycle test, which mint rows against the very same schema.
func OrderRanks(t schemadef.Table) map[string]int {
	slots, _ := tableOrderChains(t)
	if len(slots) == 0 {
		return nil
	}
	ranks := make(map[string]int, len(slots))
	for col, slot := range slots {
		ranks[col] = slot.rank
	}
	return ranks
}

// orderedLiteral returns the literal for a column placed by an ordering
// constraint. ok is false for root columns (which keep their natural value)
// and for any column whose placement would violate a bound the schema
// states elsewhere — the caller then falls back to independent synthesis and
// the INSERT reports the conflict rather than forge silently choosing which
// constraint to honor.
func (p *Plan) orderedLiteral(tp tablePlan, col schemadef.Column, i int) (string, bool) {
	slot, ok := p.orderChains[tp.table.Name][col.Name]
	if !ok || slot.rank == 0 {
		return "", false
	}
	if slot.kind == schemadef.TypeTime {
		base, ok := p.chainBase(tp, slot, i)
		if !ok {
			return "", false
		}
		at, perr := time.Parse(time.RFC3339, base)
		if perr != nil {
			return "", false
		}
		return sqlString(at.AddDate(0, 0, OrderStepDays*slot.rank).Format("2006-01-02T15:04:05Z")), true
	}
	placed, _ := p.orderLadder(tp, slot, i)
	got, ok := placed[col.Name]
	if !ok {
		return "", false
	}
	if b, has := p.bounds.get(tp.table.Name, col.Name); has {
		if (b.Min != nil && got.val < float64(*b.Min)) || (b.Max != nil && got.val > float64(*b.Max)) {
			return "", false // the range CHECK cannot hold the ordered value
		}
	}
	return got.lit, true
}

// orderPlacement is one column's resolved position on the ladder: the literal
// to emit, and the numeric value the NEXT rank is measured against.
type orderPlacement struct {
	lit string
	val float64
}

// orderLadder places every numeric column of one ordering component for row
// i, lowest rank first, and returns the columns whose declared value pool it
// could not honor.
//
// It replaces a per-column `base + rank`, which assumed a column can hold ANY
// value. A column drawing from a CLOSED pool cannot — the author's
// `{min, max, step}` in db/seeds/vocab.yaml, or a numeric `IN (...)` CHECK —
// and the arithmetic form pinned the higher column of a pair to `lower + 1`
// and discarded the declaration outright. Measured on two consecutive dogfood
// runs of the same schema: `invoices.amount_cents` declared [500000, 2900000]
// and seeded a minimum of 1, so every invoice in the app carried a one-cent
// balance.
//
// So each rank is placed against a running FLOOR rather than against its own
// rank number. A pooled column keeps its own natural draw when that already
// clears the floor and otherwise takes the smallest pool member that does,
// which satisfies the CHECK and the declaration at once whenever the two are
// jointly satisfiable. A column with no pool takes floor+1 exactly as before,
// so a schema with no vocabulary seeds byte-identically.
//
// Every member of one rank is measured against the floor the ranks BELOW it
// established, not against each other: two columns at the same rank are
// unordered with respect to one another, and stacking them would invent a
// requirement the schema never stated.
func (p *Plan) orderLadder(tp tablePlan, slot orderSlot, i int) (map[string]orderPlacement, []string) {
	table := tp.table.Name
	t := p.byName[table]
	members := append([]string(nil), slot.members...)
	sort.Slice(members, func(a, b int) bool {
		ra, rb := p.orderChains[table][members[a]].rank, p.orderChains[table][members[b]].rank
		if ra != rb {
			return ra < rb
		}
		return members[a] < members[b]
	})

	out := make(map[string]orderPlacement, len(members))
	var unplaceable []string
	// floor is the highest value placed so far; rankFloor freezes it at the
	// start of each rank so same-rank siblings do not stack on each other.
	floor, rankFloor, haveFloor := 0.0, 0.0, false
	curRank := -1
	for _, name := range members {
		col, eligible := orderEligible(t, name)
		if !eligible {
			return nil, nil
		}
		s := p.orderChains[table][name]
		if s.rank != curRank {
			curRank, rankFloor = s.rank, floor
		}
		natural, ok := decodeScalarLiteral(p.valueLiteral(table, col, i))
		if !ok {
			return nil, nil
		}
		naturalVal, perr := strconv.ParseFloat(natural, 64)
		if perr != nil {
			return nil, nil
		}
		placed := orderPlacement{lit: p.orderNaturalLiteral(table, col, natural, naturalVal), val: naturalVal}
		if s.rank > 0 {
			var short bool
			placed, short = p.climb(table, col, rankFloor, naturalVal)
			if short {
				unplaceable = append(unplaceable, name)
			}
		}
		out[name] = placed
		if placed.val > floor || !haveFloor {
			floor, haveFloor = placed.val, true
		}
	}
	return out, unplaceable
}

// climb places one non-root column strictly above floor. short is true when
// the column's whole pool sits at or below the floor: the placement then
// leaves the pool for floor+1, because a value the CHECK rejects aborts the
// transaction and rolls back every table that already seeded, while a value
// outside the declared pool is merely disappointing — and the caller names it
// rather than letting the override pass silently.
func (p *Plan) climb(table string, col schemadef.Column, floor, naturalVal float64) (orderPlacement, bool) {
	step := floor + 1
	pool, pooled := p.closedPool(table, col)
	if !pooled {
		return orderPlacement{lit: p.orderStepLiteral(col, step), val: step}, false
	}
	if naturalVal > floor {
		return orderPlacement{lit: poolLiteral(col, p.orderStepLiteral(col, naturalVal)), val: naturalVal}, false
	}
	best, found := "", false
	var bestVal float64
	for _, m := range pool {
		v, err := strconv.ParseFloat(m, 64)
		if err != nil || v <= floor {
			continue
		}
		if !found || v < bestVal {
			best, bestVal, found = m, v, true
		}
	}
	if found {
		return orderPlacement{lit: poolLiteral(col, best), val: bestVal}, false
	}
	return orderPlacement{lit: p.orderStepLiteral(col, step), val: step}, true
}

// orderNaturalLiteral re-renders a column's own natural value. A pooled
// column keeps the pool member's exact spelling (a declared `1500.00` must
// not come back as `1500`); everything else is rendered by the column's type.
func (p *Plan) orderNaturalLiteral(table string, col schemadef.Column, natural string, val float64) string {
	if _, pooled := p.closedPool(table, col); pooled {
		return poolLiteral(col, natural)
	}
	return p.orderStepLiteral(col, val)
}

// orderStepLiteral renders a numeric value in the column's own type — the
// spellings the arithmetic placement always used.
func (p *Plan) orderStepLiteral(col schemadef.Column, v float64) string {
	if col.Type == schemadef.TypeFloat {
		return fmt.Sprintf("%.2f", v)
	}
	return strconv.FormatInt(int64(v), 10)
}

// orderVocabWarnings names every column whose DECLARED value pool an ordering
// placement had to leave.
//
// A CHECK is not negotiable and the overlay is a preference, so the placement
// wins — but silently is the wrong way to win. This is the same rule, and the
// same voice, as unionVocabWarnings: an author who wrote a range in
// db/seeds/vocab.yaml and got values three orders of magnitude below its floor
// has no way to see why without this line, and two measured runs went looking
// for a seeding workaround instead.
func (p *Plan) orderVocabWarnings() []string {
	var out []string
	for _, tp := range p.tables {
		table := tp.table.Name
		slots := p.orderChains[table]
		if len(slots) == 0 {
			continue
		}
		// Every column of a component resolves the SAME ladder, so the walk
		// is once per component, not once per column.
		named, walked := map[string]bool{}, map[string]bool{}
		for _, col := range sortedKeys(slots) {
			slot := slots[col]
			if slot.kind == schemadef.TypeTime || walked[slot.members[0]] {
				continue
			}
			walked[slot.members[0]] = true
			for i := 0; i < tp.n; i++ {
				_, unplaceable := p.orderLadder(tp, slot, i)
				for _, name := range unplaceable {
					if named[name] || len(p.vocab[table][name]) == 0 {
						continue
					}
					named[name] = true
					out = append(out, fmt.Sprintf(
						"seed plan: db/seeds/vocab.yaml declares values for %s.%s, but constraint %s requires "+
							"every one of them to sit above a sibling column that is already higher — "+
							"the CHECK wins and the declared values are not used. "+
							"Raise the range, or lower the range on the column it is compared against",
						table, name, strings.Join(quoteAll(slots[name].cons), " / ")))
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// sortedKeys returns a map's keys in sorted order, so a warning walk is
// deterministic.
func sortedKeys(m map[string]orderSlot) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// quoteAll double-quotes each name, matching how every other constraint
// refusal in this package spells one.
func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strconv.Quote(n)
	}
	return out
}

// chainBase returns the raw value the component's ranks are measured from:
// the LARGEST natural value among its root columns. Taking the max is what
// makes rank arithmetic a proof — every non-root sits above every root, not
// just above the one that happened to be picked.
func (p *Plan) chainBase(tp tablePlan, slot orderSlot, i int) (string, bool) {
	t := p.byName[tp.table.Name]
	best, have := "", false
	for _, root := range slot.roots {
		col, ok := orderEligible(t, root)
		if !ok {
			continue
		}
		raw, ok := decodeScalarLiteral(p.valueLiteral(tp.table.Name, col, i))
		if !ok {
			continue
		}
		if !have || orderGreater(slot.kind, raw, best) {
			best, have = raw, true
		}
	}
	return best, have
}

// orderGreater compares two raw values in the component's type. A value that
// does not parse loses, so an unparseable root can never become the base.
func orderGreater(kind schemadef.CanonicalType, a, b string) bool {
	switch kind {
	case schemadef.TypeTime:
		at, aerr := time.Parse(time.RFC3339, a)
		bt, berr := time.Parse(time.RFC3339, b)
		return aerr == nil && (berr != nil || at.After(bt))
	case schemadef.TypeInt:
		av, aerr := strconv.ParseInt(a, 10, 64)
		bv, berr := strconv.ParseInt(b, 10, 64)
		return aerr == nil && (berr != nil || av > bv)
	case schemadef.TypeFloat:
		av, aerr := strconv.ParseFloat(a, 64)
		bv, berr := strconv.ParseFloat(b, 64)
		return aerr == nil && (berr != nil || av > bv)
	}
	return false
}
