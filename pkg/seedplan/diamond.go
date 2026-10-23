package seedplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/schemadef"
)

// Diamonds: two paths to one parent, and the invariant that lives only at the
// join.
//
// Every value this package synthesizes satisfies its own constraint by
// construction, and a foreign key is no exception — the seeder picks a real
// parent row for every edge. But it picks EACH EDGE INDEPENDENTLY, and some
// schemas carry a constraint that no single edge expresses:
//
//	orders.patient_id                                  -> patients.id
//	orders.prescription_id -> prescriptions.patient_id -> patients.id
//
// Both columns hold valid references. Nothing in either constraint says they
// have to name the SAME patient, and independent picks do not: measured on a
// real generated app, 18 of 20 seeded orders named one patient directly and a
// different one through their prescription. The run then handed four fan-out
// units those order ids as "verification data" — rows a CORRECT implementation
// of the rule they were building must reject.
//
// The seeder was never BROKEN here. It was unaware: it had no concept of a
// transitive constraint, so it never asked whether two routes to one node
// agree.
//
// This is general, not one domain's quirk: Order->Customer + Order->Address->
// Customer, Invoice->Account + Invoice->Subscription->Account. Anywhere a
// table references two parents and one parent also reaches the other, the two
// routes are an unstated invariant.
//
// # Derivable → derive. Decision → declare.
//
// That a diamond EXISTS is derivable from the foreign-key graph alone, so
// forge finds it with no help. WHICH parent is authoritative is a fact about
// the DOMAIN — forge guessing it would be forge making an architecture choice
// on the user's behalf, and it would be wrong for real schemas (an order can
// ship to an address belonging to someone else). So forge does not guess, and
// it does not shrug either: it REFUSES to seed, and the refusal is a runbook
// naming the relationship, the options, and the literal statement to paste.
//
// # Where the declaration lives, and why
//
// On the foreign-key constraint, as a postgres COMMENT:
//
//	COMMENT ON CONSTRAINT orders_patient_id_fkey ON orders IS
//	    'forge:ref derived-from=prescription_id';
//
// Three candidates were weighed:
//
//   - db/seeds/vocab.yaml — closest to the seeder, but it is the wrong SCOPE
//     twice over. That overlay says in its own doc that it supplies vocabulary
//     only and that referential machinery is never overridable. And "an
//     order's patient is determined by its prescription" is not a seeding
//     fact: validation and the ApproveOrder rule need the same sentence.
//   - A forge: annotation in the migration text — next to the FK, but
//     migrations are append-only history, so the annotation would have to be
//     located across the whole set, and forge would have to parse migration
//     SQL. It reads the applied CATALOG today, so this would introduce a
//     second source of truth for the same fact.
//   - COMMENT ON CONSTRAINT — plain SQL, applied by the migration that
//     creates the reference, and landing in pg_description where forge's
//     EXISTING introspection can read it (schemadef.ForeignKey.Comment). No
//     forge-specific file, no forge-specific parser, no second source of
//     truth; psql \d+ and every other tool see it too.
//
// The third wins on every axis that matters: the fact sits where a reader
// looking at the reference would find it, and forge reads it from the one
// place it already reads the schema.
//
// # The vocabulary
//
//	forge:ref derived-from=<column>   the route leaving by <column> is
//	                                  authoritative; seed THIS column from it
//	forge:ref authoritative           this column is the truth; the other edge
//	                                  is constrained to agree with it
//	forge:ref independent             the two are unrelated facts; seed each
//	                                  on its own and say nothing further
//
// # When forge cannot honour a declaration
//
// A UNIQUE foreign-key column is a 1-1 relationship: the seeder hands it a
// DISTINCT parent per row on purpose, so neither resolution can touch it —
// deriving it, or narrowing it into a bucket, would put two rows on one parent
// and abort the INSERT. Such a declaration is refused with its own reason
// rather than honoured into a failed transaction.
//
// `authoritative` also narrows the authoritative column itself, to parents
// that HAVE a partner on the other edge: a parent with none cannot satisfy the
// declaration on any row. That narrowing is real (the column ends up ranging
// over "patients who have a prescription") and is stated here rather than
// discovered in the data.
//
// # Why the derivation cannot loop
//
// A derived column reads other columns, so termination is a real question.
// BuildPlan force-nulls edges until a full topological order exists, so the
// foreign-key graph MINUS the force-null edges — the only edges a route may
// use — is a DAG. A loop would need A to reach B and B to reach A in that DAG.
// maxDiamondPath is carried through the walk anyway as a structural backstop:
// past it, derivation degrades to the independent pick rather than recursing.

// maxDiamondPath bounds the indirect-route search and the derivation walk. A
// diamond three or four hops deep is still a diamond, but the search runs over
// the foreign-key graph, which can be dense; the bound keeps the scan cheap
// and the report readable. Shares maxFKDepth's rationale and value.
const maxDiamondPath = maxFKDepth

// diamondExamples is how many rows a message names — enough to paste into a
// query, few enough to stay readable.
const diamondExamples = 3

// refMarker is the prefix that makes a constraint comment a forge declaration.
// Anything else in the comment is a human note and is ignored.
const refMarker = "forge:ref "

// Declaration verdicts.
const (
	refDerivedFrom   = "derived-from="
	refAuthoritative = "authoritative"
	refIndependent   = "independent"
)

// fkHop is one edge of a route: the table the walk stands on, the column it
// leaves by, and the table that column references. A route's FIRST hop leaves
// the child table itself.
type fkHop struct {
	from     string
	column   string
	refTable string
}

// diamond is one detected two-routes-to-one-parent shape.
type diamond struct {
	child string
	// direct is the child's own column that references the shared parent.
	direct columnPlan
	// route is the indirect way to the same parent, starting with a DIFFERENT
	// column of the child. Its last hop's refTable is the shared parent.
	route []fkHop
	// verdict is what the constraint comment declared: "", refDerivedFrom
	// (with the via column appended), refAuthoritative, or refIndependent.
	verdict string
	// issue is set when a declaration EXISTS but forge cannot honour it, and
	// carries the reason for the refusal. "" means "simply undeclared".
	issue string
}

func (d diamond) parent() string { return d.direct.fk.RefTable }
func (d diamond) viaCol() string { return d.route[0].column }
func (d diamond) refCol() string { return d.direct.fk.RefColumn }
func (d diamond) declared() bool { return d.verdict != "" }
func (d diamond) constraint() string {
	if n := d.direct.fk.Name; n != "" {
		return n
	}
	return d.child + "_" + d.direct.col.Name + "_fkey"
}

// authPair is the resolved form of a `forge:ref authoritative` declaration:
// the direct column is the truth, so the OTHER edge of the diamond is narrowed
// to rows that agree with it.
type authPair struct {
	// directCol is the authoritative column on the child table. Its value set
	// is narrowed to parents that some via row actually reaches, because a
	// parent with no partner cannot satisfy the declaration on any row.
	directCol string
	// viaCol is the child column that must agree; viaTable is what it
	// references and tail is the route from THERE to the shared parent (the
	// full route minus its first hop, which is viaCol itself).
	viaCol   string
	viaTable string
	tail     []fkHop
}

// resolveDiamonds finds every diamond, applies the declarations found on the
// foreign-key constraints, and returns the derivations, the authoritative
// pairs, and the undeclared leftovers. Called at the END of finalize: the walk
// uses real parent assignments, and those hash-pick against the row counts
// finalize settles.
func (p *Plan) resolveDiamonds() (derived map[string]map[string][]fkHop, auth map[string][]authPair, undeclared []diamond) {
	found := p.findDiamonds()
	if len(found) == 0 {
		return nil, nil, nil
	}

	derived = map[string]map[string][]fkHop{}
	auth = map[string][]authPair{}
	claimed := map[string]bool{}

	for _, d := range found {
		key := d.child + "." + d.direct.col.Name
		if claimed[key] {
			// A column reachable by two different routes: the first
			// declaration settles it, and a second one cannot be honoured
			// simultaneously.
			continue
		}
		switch {
		case !d.declared() && p.directEdgeSettlesIt(d):
			// Structurally not a decision — see directEdgeSettlesIt. Resolve
			// it as if the author had written `authoritative`, which is the
			// declaration the shape already implies, and say nothing.
			//
			// A column whose parent is already DEALT — a UNIQUE 1-1 edge, or a
			// member of a composite UNIQUE (tuple.go) — cannot be narrowed
			// into a bucket or drawn from one: both sides of the pair are
			// re-picked here, and re-picking a dealt column puts two rows on
			// one parent. Such a diamond falls through to the ordinary
			// undeclared path and is reported.
			if p.refDealt(d.child, d.direct.col.Name) || p.refDealt(d.child, d.viaCol()) {
				undeclared = append(undeclared, d)
				continue
			}
			claimed[key] = true
			auth[d.child] = append(auth[d.child], authPair{
				directCol: d.direct.col.Name,
				viaCol:    d.viaCol(),
				viaTable:  d.route[0].refTable,
				tail:      d.route[1:],
			})
		case !d.declared():
			undeclared = append(undeclared, d)
		case d.verdict == refIndependent:
			// The author said the two are unrelated facts. Seed each on its
			// own and never mention it again.
			claimed[key] = true
		case d.verdict == refDerivedFrom+d.viaCol():
			// A UNIQUE column is a 1-1 relationship: the seeder gives it a
			// distinct parent per row on purpose, and a derived value would
			// collide and abort the INSERT. Refuse the declaration rather than
			// honour it into a failed transaction.
			if d.direct.uniqueFK || p.tupleOwns(d.child, d.direct.col.Name) {
				d.issue = fmt.Sprintf("%s.%s already takes a dealt parent per row (it carries a UNIQUE index), so it cannot be derived — two rows would end up on one %s",
					d.child, d.direct.col.Name, d.parent())
				undeclared = append(undeclared, d)
				continue
			}
			claimed[key] = true
			if derived[d.child] == nil {
				derived[d.child] = map[string][]fkHop{}
			}
			derived[d.child][d.direct.col.Name] = d.route
		case d.verdict == refAuthoritative:
			// Same constraint, other column. `authoritative` re-picks BOTH
			// ends — the via edge is narrowed into a bucket, and the
			// authoritative column itself is narrowed to the parents some via
			// row can partner with — so a dealt parent on EITHER side cannot
			// survive it. Checking only the via side is how a UNIQUE
			// authoritative column came to be narrowed into a handful of
			// buckets and abort its own INSERT.
			if col, why, dealt := p.refDealtSide(d); dealt {
				d.issue = fmt.Sprintf("%s.%s already takes a dealt parent per row (it carries a UNIQUE index), so it cannot be %s — two rows would end up on one %s",
					d.child, col, why, d.route[0].refTable)
				undeclared = append(undeclared, d)
				continue
			}
			claimed[key] = true
			auth[d.child] = append(auth[d.child], authPair{
				directCol: d.direct.col.Name,
				viaCol:    d.viaCol(),
				viaTable:  d.route[0].refTable,
				tail:      d.route[1:],
			})
		}
	}
	if len(derived) == 0 {
		derived = nil
	}
	if len(auth) == 0 {
		auth = nil
	}
	return derived, auth, undeclared
}

// refDealt reports whether the column's parent row is already DEALT rather
// than picked: a UNIQUE foreign key (the 1-1 bijection) or a member of a
// composite UNIQUE index (tuple.go). Either way the assignment is a proof that
// no two rows collide, and any resolution that re-picks the column destroys it.
func (p *Plan) refDealt(table, column string) bool {
	if _, cp, ok := p.colPlan(table, column); ok && cp.uniqueFK {
		return true
	}
	return p.tupleOwns(table, column)
}

// refDealtSide names which end of an `authoritative` pair is dealt, and how
// that resolution would have moved it.
func (p *Plan) refDealtSide(d diamond) (column, why string, dealt bool) {
	if p.refDealt(d.child, d.direct.col.Name) {
		return d.direct.col.Name, "narrowed to the parents that have a partner", true
	}
	if p.refDealt(d.child, d.viaCol()) {
		return d.viaCol(), "narrowed to agree", true
	}
	return "", "", false
}

// directEdgeSettlesIt reports whether a diamond's shape already answers the
// question, so that refusing to guess would be refusing to read.
//
// The rule is one comparison: the DIRECT column is NOT NULL and the first hop
// of the indirect route is NULLABLE. That asymmetry is the schema's own
// statement, not an inference about the domain. A row must always name its
// parent directly; it may exist with no value at all on the other route, and a
// fact that is permitted to be absent cannot be the one that determines a
// column which never is. There is no reading of such a schema under which the
// optional edge is authoritative — `derived-from` would have to invent a
// parent for every row whose route is NULL.
//
// This stays inside "derivable → derive. decision → declare." (see the file
// header): it fires only where the two routes are not symmetric, and a genuine
// decision needs symmetry — two required routes, both always present, either
// of which a domain could reasonably call the truth. Those still refuse. So do
// two optional ones: absent on both sides is no asymmetry to read.
//
// It is deliberately STRUCTURAL. It does not look at column names and knows
// nothing about tenancy, ownership or scope; a schema with no such column
// benefits identically. That matters because the alternative — recognizing
// `company_id`/`tenant_id`/`org_id` — would put a domain concept back into a
// package that removed one on purpose.
//
// An explicit declaration is checked first by the caller, so the author always
// outranks this.
//
// The practical effect: on a wide schema where most tables carry a required
// scoping column and reach the same parent again through optional references,
// this removes the bulk of the refusals — leaving the handful where two
// required routes really do state the same fact twice.
func (p *Plan) directEdgeSettlesIt(d diamond) bool {
	if !d.direct.col.NotNull {
		return false
	}
	_, viaPlan, ok := p.colPlan(d.child, d.viaCol())
	return ok && !viaPlan.col.NotNull
}

// findDiamonds returns every diamond in the plan, in deterministic order:
// tables in plan (topological) order, then by the direct column's position,
// then by the via column's. Each carries whatever its constraint comment
// declared.
func (p *Plan) findDiamonds() []diamond {
	var out []diamond
	for i := range p.tables {
		tp := &p.tables[i]

		// The child's own cross-table foreign keys. A self-reference resolves
		// per-row against the same table and is not a second route to a third
		// one; a force-null edge holds no value to reconcile.
		var edges []columnPlan
		for _, cp := range tp.cols {
			if cp.fk != nil && !cp.selfRef && !cp.forceNull {
				edges = append(edges, cp)
			}
		}
		if len(edges) < 2 {
			continue
		}

		for _, direct := range edges {
			for _, via := range edges {
				if via.col.Name == direct.col.Name {
					continue
				}
				route, ok := p.routeToParent(tp.table.Name, via, direct.fk.RefTable, direct.fk.RefColumn)
				if !ok {
					continue
				}
				out = append(out, diamond{
					child:   tp.table.Name,
					direct:  direct,
					route:   route,
					verdict: refVerdict(direct.fk.Comment),
				})
			}
		}
	}
	return out
}

// refVerdict extracts the declaration from a constraint comment, or "" when
// the comment carries none. A comment may hold prose as well; only the
// `forge:ref ` clause is read, and only the first one.
func refVerdict(comment string) string {
	i := strings.Index(comment, refMarker)
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(comment[i+len(refMarker):])
	if j := strings.IndexAny(v, " \t\r\n;"); j >= 0 {
		v = v[:j]
	}
	switch {
	case v == refAuthoritative, v == refIndependent, strings.HasPrefix(v, refDerivedFrom) && len(v) > len(refDerivedFrom):
		return v
	}
	return ""
}

// routeToParent finds the shortest foreign-key route from the child table,
// leaving by the `via` column, to `parent` — whose FINAL edge references the
// same column the direct edge does. Without that last condition the two routes
// land on different columns of the parent and comparing what they reach says
// nothing.
//
// Breadth-first over non-self, non-force-null edges; no table visited twice;
// bounded by maxDiamondPath. Each table's edges are walked in column order so
// the route reported for a schema is the same on every run.
func (p *Plan) routeToParent(child string, via columnPlan, parent, refColumn string) ([]fkHop, bool) {
	start := via.fk.RefTable
	if start == parent {
		return nil, false // `via` IS a second direct edge, not a route through one
	}
	first := fkHop{from: child, column: via.col.Name, refTable: start}

	type state struct {
		table string
		hops  []fkHop
	}
	queue := []state{{table: start, hops: []fkHop{first}}}
	seen := map[string]bool{child: true, start: true}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if len(cur.hops) >= maxDiamondPath {
			continue
		}
		t, ok := p.byName[cur.table]
		if !ok {
			continue // a table forge does not seed cannot be walked
		}
		fks := append([]schemadef.ForeignKey(nil), t.ForeignKeys...)
		sort.Slice(fks, func(i, j int) bool { return fks[i].Column < fks[j].Column })

		for _, fk := range fks {
			if fk.RefTable == cur.table {
				continue // self-reference: not a route to a third table
			}
			_, hcp, found := p.colPlan(cur.table, fk.Column)
			if !found || hcp.forceNull {
				continue // an edge that is always NULL is not a route
			}
			hops := append(append([]fkHop(nil), cur.hops...), fkHop{from: cur.table, column: fk.Column, refTable: fk.RefTable})
			if fk.RefTable == parent {
				if fk.RefColumn != refColumn {
					continue
				}
				return hops, true
			}
			if seen[fk.RefTable] {
				continue
			}
			seen[fk.RefTable] = true
			queue = append(queue, state{table: fk.RefTable, hops: hops})
		}
	}
	return nil, false
}

// walkRoute follows a route from child row i and returns the parent row index
// it lands on. ok is false as soon as any hop is NULL, or past the derivation
// depth bound.
func (p *Plan) walkRoute(table string, route []fkHop, i, depth int) (int, bool) {
	if depth >= maxDiamondPath {
		return 0, false
	}
	row := i
	for _, hop := range route {
		htp, hcp, found := p.colPlan(table, hop.column)
		if !found || hcp.fk == nil {
			return 0, false
		}
		var ok bool
		row, ok = p.fkParentRowAt(htp, hcp, row, depth+1)
		if !ok {
			return 0, false
		}
		table = hop.refTable
	}
	return row, true
}

// authPairFor returns the `forge:ref authoritative` pair this column takes
// part in, and which side of it the column is on.
func (p *Plan) authPairFor(table, column string) (pair authPair, isDirect, ok bool) {
	for _, ap := range p.authRefs[table] {
		switch column {
		case ap.directCol:
			return ap, true, true
		case ap.viaCol:
			return ap, false, true
		}
	}
	return authPair{}, false, false
}

// authoritativeRow answers a `forge:ref authoritative` declaration.
//
// Both columns are pinned off ONE grouping: every row of the via table,
// bucketed by the parent its tail route reaches. The authoritative column
// draws from the buckets' KEYS — narrowing it to parents some via row can
// actually partner with, because a parent with no partner cannot satisfy the
// declaration on any row — and the via column then draws from that bucket.
// Both draws are the same deterministic hash-pick every other column uses.
//
// The narrowing is real and is stated in the declaration's own documentation:
// the authoritative column ends up ranging over "patients who have a
// prescription" rather than all patients.
func (p *Plan) authoritativeRow(tp tablePlan, cp columnPlan, ap authPair, isDirect bool, i, depth int) (int, bool) {
	buckets, targets := p.routeBuckets(ap.viaTable, ap.tail, depth)
	if len(targets) == 0 {
		return 0, false
	}
	salt := p.cfg.EffectiveSalt()
	// The authoritative column's own draw — computed identically on both
	// branches so the two columns agree without either reading the other.
	want := targets[pick(salt, tp.table.Name, ap.directCol, i, len(targets))]
	if isDirect {
		return want, true
	}
	partners := buckets[want]
	if len(partners) == 0 {
		return 0, false
	}
	return partners[pick(salt, tp.table.Name, cp.col.Name, i, len(partners))], true
}

// routeBuckets groups a table's rows by the parent row their route reaches:
// parent row index -> the rows that reach it, plus the sorted list of reachable
// parents. Memoized per (table, route) — it is read once per cell and the walk
// is O(rows).
func (p *Plan) routeBuckets(table string, route []fkHop, depth int) (map[int][]int, []int) {
	key := table + "|" + renderRoute(route)
	if got, ok := p.bucketMemo[key]; ok {
		return got.buckets, got.targets
	}
	tp, ok := p.tablePlanFor(table)
	if !ok {
		return nil, nil
	}
	buckets := map[int][]int{}
	for j := 0; j < tp.n; j++ {
		if target, ok := p.walkRoute(table, route, j, depth); ok {
			buckets[target] = append(buckets[target], j)
		}
	}
	targets := make([]int, 0, len(buckets))
	for t := range buckets {
		targets = append(targets, t)
	}
	sort.Ints(targets)
	if p.bucketMemo == nil {
		p.bucketMemo = map[string]routeBucketSet{}
	}
	p.bucketMemo[key] = routeBucketSet{buckets: buckets, targets: targets}
	return buckets, targets
}

// routeBucketSet is one memoized grouping.
type routeBucketSet struct {
	buckets map[int][]int
	targets []int
}

// tablePlanFor returns the plan for a table by name.
func (p *Plan) tablePlanFor(name string) (*tablePlan, bool) {
	for i := range p.tables {
		if p.tables[i].table.Name == name {
			return &p.tables[i], true
		}
	}
	return nil, false
}

// UndeclaredDiamondError is the refusal. It is returned instead of writing
// rows whose two routes to one parent disagree, because such rows are worse
// than no rows: they are fixtures a correct implementation of the very rule
// they describe must reject, and a run already built four units against them.
//
// Error() is the runbook: what was expected, what was found, and the literal
// statement to paste.
type UndeclaredDiamondError struct {
	// Stanzas is one paste-ready block per undeclared reference.
	Stanzas []string
	// Quiet names the undeclared references whose two routes happen to
	// AGREE in the rows this run planned, so they produced no stanza.
	// They are the same decision waiting to be made: a later run with
	// different random values will refuse on them too.
	Quiet []string
}

func (e *UndeclaredDiamondError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "refusing to seed: %d foreign-key reference(s) are reachable by two paths, and which path "+
		"is authoritative is a fact about your domain that the schema does not state. forge will not guess it — "+
		"a wrong guess writes fixtures that a correct implementation must reject.\n\n%s\n\nApply the chosen "+
		"statement in a migration (it is plain SQL and lands in the postgres catalog, where forge, psql \\d+ and "+
		"anything else can read it), then seed again.",
		len(e.Stanzas), strings.Join(e.Stanzas, "\n\n"))

	// Naming these costs one paragraph and saves a boot cycle each. A
	// refusal reports only the pairs that ACTUALLY disagree in the rows it
	// just planned, which is the right thing to refuse on — but it means a
	// schema with several diamonds surfaces them a few at a time, over
	// successive runs, as different random values happen to collide.
	// Declaring the ones named above and re-running can simply reveal
	// these next.
	if len(e.Quiet) > 0 {
		fmt.Fprintf(&b, "\n\nAlso undeclared, though this run's values happened to agree on them — "+
			"the same decision, and a later run WILL refuse on it:\n")
		for _, q := range e.Quiet {
			fmt.Fprintf(&b, "  %s\n", q)
		}
		b.WriteString("Declaring every one of them in the same migration takes one cycle instead of several.")
	}
	return b.String()
}

// diamondRefusal builds the refusal for the undeclared diamonds, or nil when
// there are none. Diamonds whose two routes CANNOT disagree — a single-row
// parent, a 1-1 UNIQUE key that forces the identity mapping — are left out:
// there is no decision for the author to make and demanding one is nagging.
func (p *Plan) diamondRefusal() error {
	if len(p.undeclared) == 0 {
		return nil
	}
	var stanzas []string
	var quiet []string
	for _, d := range p.undeclared {
		if s := p.diamondStanza(d); s != "" {
			stanzas = append(stanzas, s)
			continue
		}
		// No stanza means the two routes agreed on every row this run
		// planned — not that the reference is declared. Record it so one
		// refusal can name the whole decision set (see
		// UndeclaredDiamondError.Error).
		quiet = append(quiet, fmt.Sprintf("%s.%s -> %s.%s (also reachable as: %s)",
			d.child, d.direct.col.Name, d.parent(), d.refCol(), renderRoute(d.route)))
	}
	if len(stanzas) == 0 {
		// Nothing disagrees anywhere: seeding these rows is safe, and
		// demanding a decision nobody's data needs is nagging.
		return nil
	}
	return &UndeclaredDiamondError{Stanzas: stanzas, Quiet: quiet}
}

// diamondStanza renders one undeclared reference: the two paths, the rows that
// actually disagree, and the three statements to choose between. Returns ""
// when nothing disagrees.
func (p *Plan) diamondStanza(d diamond) string {
	tp, ok := p.tablePlanFor(d.child)
	if !ok {
		return ""
	}

	disagreed := 0
	var examples []string
	for i := 0; i < tp.n; i++ {
		direct, ok := p.fkParentRow(*tp, d.direct, i)
		if !ok {
			continue // this row declines the direct edge; nothing to reconcile
		}
		viaRow, ok := p.walkRoute(d.child, d.route, i, 0)
		if !ok {
			continue // the indirect route is NULL somewhere on this row
		}
		if direct == viaRow {
			continue
		}
		disagreed++
		if len(examples) < diamondExamples {
			examples = append(examples, fmt.Sprintf("%s %s vs %s",
				p.rowRef(tp, i),
				rawValue(p.referencedValue(d.parent(), d.refCol(), direct, 0)),
				rawValue(p.referencedValue(d.parent(), d.refCol(), viaRow, 0))))
		}
	}
	if disagreed == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  %s.%s -> %s.%s\n", d.child, d.direct.col.Name, d.parent(), d.refCol())
	fmt.Fprintf(&b, "    also reachable as: %s\n", renderRoute(d.route))
	if d.issue != "" {
		fmt.Fprintf(&b, "    the declaration on %s cannot be honoured: %s\n", d.constraint(), d.issue)
	}
	fmt.Fprintf(&b, "    seeded independently, the two disagree in %d of %d row(s): %s\n",
		disagreed, tp.n, strings.Join(examples, "; "))
	b.WriteString("    Declare which is authoritative (pick ONE):\n")
	fmt.Fprintf(&b, "      COMMENT ON CONSTRAINT %s ON %s IS 'forge:ref derived-from=%s';\n",
		d.constraint(), d.child, d.viaCol())
	fmt.Fprintf(&b, "        -- the second path decides: seed %s.%s from %s\n",
		d.child, d.direct.col.Name, renderRoute(d.route))
	fmt.Fprintf(&b, "      COMMENT ON CONSTRAINT %s ON %s IS 'forge:ref authoritative';\n",
		d.constraint(), d.child)
	fmt.Fprintf(&b, "        -- %s.%s is the truth: %s.%s is narrowed to agree with it\n",
		d.child, d.direct.col.Name, d.child, d.viaCol())
	fmt.Fprintf(&b, "      COMMENT ON CONSTRAINT %s ON %s IS 'forge:ref independent';\n",
		d.constraint(), d.child)
	fmt.Fprintf(&b, "        -- genuinely unrelated facts (a gift shipped to someone else's address)\n")
	fmt.Fprintf(&b, "      Usually `authoritative`: the row states its own %s, and the route\n", d.parent())
	fmt.Fprintf(&b, "      through %s is a back-reference. Deriving the other way can disagree\n", d.viaCol())
	fmt.Fprintf(&b, "      with a column that already scopes this row, producing rows that\n")
	fmt.Fprintf(&b, "      reference across that boundary.")
	return b.String()
}

// rowRef names one child row the way a user can look it up: by primary-key
// value when the table has a single-column PK, else by ordinal.
func (p *Plan) rowRef(tp *tablePlan, i int) string {
	if len(tp.table.PKCols) == 1 {
		if v, ok := p.SeedValue(tp.table.Name, tp.table.PKCols[0], i); ok {
			return fmt.Sprintf("%s.%s=%s:", tp.table.Name, tp.table.PKCols[0], v)
		}
	}
	return fmt.Sprintf("%s row %d:", tp.table.Name, i)
}

// renderRoute renders a route as "orders.prescription_id -> prescriptions.
// patient_id", one segment per hop.
func renderRoute(route []fkHop) string {
	segs := make([]string, len(route))
	for i, hop := range route {
		segs[i] = hop.from + "." + hop.column
	}
	return strings.Join(segs, " -> ")
}

// rawValue unquotes a rendered SQL literal for display, so a reported id can
// be pasted into a query. Non-scalar literals pass through unchanged.
func rawValue(lit string) string {
	if v, ok := decodeScalarLiteral(lit); ok {
		return v
	}
	return lit
}
