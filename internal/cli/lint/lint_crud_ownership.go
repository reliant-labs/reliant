// File: internal/cli/lint/lint_crud_ownership.go
//
// crud-ownership — `forge lint --crud-ownership`.
//
// A forge-generated CRUD op reads and writes whatever row id the caller
// names. On an AUTHENTICATED rpc that means every signed-in caller can read,
// update and delete every other caller's rows, unless something scopes the
// op. Since v1.10.0 the table can say whose rows it holds, and the generated
// ops then scope themselves:
//
//	COMMENT ON TABLE plans IS 'forge:owner=thread_id>chat_id>user_id';
//
// (schemadef.TableMarkerOwner; the runtime is forge/pkg/crud's Owned*
// helpers). Nothing makes a project write it, so the default is still the
// unscoped op, and the `unscoped_auth` audit only escalates once SOME table
// declared an owner. This rule asks the question of every table instead:
// a table served by generated CRUD on an authenticated rpc must say whose
// rows it holds, or say why nobody owns them.
//
// ── What answers it ──────────────────────────────────────────────────────
//
//   - `forge:owner=<spec>` on the table — the enforced form.
//   - `forge:owner` on any of its columns — the older, advisory form: it
//     arms the audit's gate and scaffolds a scoping wrapper into the owned
//     CRUD shim, so the project has already decided.
//   - `forge:public: <reason>` or `forge:unscoped: <reason>` on the table —
//     rows every caller may read, or rows scoped some other way (an admin
//     service, an op override, a row-level-security policy). They change no
//     codegen; the reason is the point, so a declaration without one is a
//     finding of its own, on any table.
//
// A `forge:owner=` that does not parse or resolve still answers the rule:
// `forge generate` refuses it with the precise error, and a second finding
// here would only restate it.
//
// ── What it reads, and why as text ───────────────────────────────────────
//
//   - The tables and their catalog comments come from the migrations' TEXT
//     (CREATE / DROP / ALTER … RENAME TO / COMMENT ON TABLE and COLUMN,
//     replayed in file order; COMMENT ON replaces the whole comment, so the
//     last one wins). Every fact the rule needs is a comment, which is why
//     `--column-markers` and the `unscoped_auth` audit read the same text,
//     and why this lane needs no shadow postgres and runs in every sweep.
//   - The AUTHENTICATED rpcs come from the forge descriptor
//     (codegen.ParseServicesFromProtos), the same projection of
//     auth_required the interceptor's fail-closed set is built from.
//   - "forge generates the op" is read from the generated ops file itself:
//     each service's handlers_crud_ops_gen.go declares `crud<Method>Op()`
//     for exactly the rpcs forge matched to a table, validated, and found no
//     hand-written handler pre-empting — the provenance record
//     --create-nullability reads too. A hand-written handler owns its own
//     scoping and is the audit's business, not this rule's.
//   - The rpc→table join is BuildSchemaEntities' (codegen.CRUDShape, the
//     table's own entity name or `forge:entity-name`, then the conventional
//     table names; `forge:no-entity` tables are never joined).
//
// ── Severity ─────────────────────────────────────────────────────────────
//
// An error, under the warn-first rollout (lint_rollout_table.go): a project
// pinned below the release after the one that ships it sees a warning
// carrying the gate date.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/sqlident"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// ruleCRUDTableUnowned is the stable rule ID.
const ruleCRUDTableUnowned = "crud-table-unowned"

// crudOwnershipFinding is one table the rule reports. Marker is set when the
// finding is a scope exemption declared without a reason; otherwise the table
// declares no ownership at all and RPCs names the authenticated rpcs whose
// generated ops serve it.
type crudOwnershipFinding struct {
	File    string
	Line    int
	Table   string
	Comment string   // the table's current comment, so the fix can keep it
	RPCs    []string // "Service.Method", sorted
	Marker  string   // schemadef.TableMarkerPublic / TableMarkerUnscoped
}

func (f crudOwnershipFinding) message() string {
	if f.Marker != "" {
		return fmt.Sprintf("table %s declares %s with no reason: the marker exists to record why the table needs no owner, and an empty one records nothing", f.Table, f.Marker)
	}
	return fmt.Sprintf("table %s is read and written by forge-generated CRUD on authenticated rpc(s) %s and declares no owner: every signed-in caller can read, update and delete every row",
		f.Table, strings.Join(f.RPCs, ", "))
}

func (f crudOwnershipFinding) fixHint() string {
	keep := ""
	if strings.TrimSpace(f.Comment) != "" {
		keep = fmt.Sprintf(" COMMENT ON replaces the whole comment, so keep its text: it is %q.", f.Comment)
	}
	if f.Marker != "" {
		return fmt.Sprintf("in a new migration, say why: COMMENT ON TABLE %s IS '%s: <why>';, or declare the owner instead: COMMENT ON TABLE %s IS '%s=user_id';.%s",
			f.Table, f.Marker, f.Table, schemadef.TableMarkerOwner, keep)
	}
	return fmt.Sprintf("in a new migration, declare whose rows these are: COMMENT ON TABLE %[1]s IS '%[2]s=user_id'; "+
		"(the generated ops then scope every read and write to the caller; through a join: '%[2]s=<fk>><column>', "+
		"by user or org: '%[2]s=user:user_id,org:org_id'). If no caller owns them, say why: "+
		"COMMENT ON TABLE %[1]s IS '%[3]s: <why>'; (scoped some other way) or '%[4]s: <why>'; (every caller may read them).%[5]s "+
		"See `forge project annotations --kind table`.",
		f.Table, schemadef.TableMarkerOwner, schemadef.TableMarkerUnscoped, schemadef.TableMarkerPublic, keep)
}

// crudOwnershipShouldRun runs the lane in a forge project with migrations:
// the ops it judges are forge's.
func crudOwnershipShouldRun(rc *lintRunCtx) (bool, string) {
	if rc.cwd == "" || rc.cfg == nil || !dirExists(migrationsDirFor(rc.cfg)) {
		return false, ""
	}
	return true, ""
}

// collectCRUDOwnershipJSON maps the findings onto the JSON contract.
func collectCRUDOwnershipJSON(cwd string, cfg *config.ProjectConfig) ([]lintJSONFinding, error) {
	fs, err := collectCRUDOwnershipFindings(cwd, migrationsDirFor(cfg))
	if err != nil {
		return nil, fmt.Errorf("crud-ownership lint failed: %w", err)
	}
	out := make([]lintJSONFinding, 0, len(fs))
	for _, f := range fs {
		out = append(out, lintJSONFinding{
			File: f.File, Line: f.Line, Severity: lintSevError,
			Rule: ruleCRUDTableUnowned, Message: f.message(), FixHint: f.fixHint(),
		})
	}
	return out, nil
}

// collectCRUDOwnershipFindings is the shared engine. A project with no
// migrations, no services or no generated ops yields nothing.
func collectCRUDOwnershipFindings(root, migrationsDir string) ([]crudOwnershipFinding, error) {
	migDir := migrationsDir
	if !filepath.IsAbs(migDir) {
		migDir = filepath.Join(root, migDir)
	}
	if !dirHasUpSQL(migDir) {
		return nil, nil
	}
	schema, err := ownershipSchemaFromMigrations(root, migDir)
	if err != nil {
		return nil, err
	}
	services, err := codegen.ParseServicesFromProtos("", root)
	if err != nil {
		return nil, fmt.Errorf("read the forge descriptor: %w", err)
	}
	generated := generatedCRUDOps(root, services)
	return crudOwnershipFindingsFor(schema, services, func(svc codegen.ServiceDef, m codegen.Method) bool {
		ops := generated[svc.Name]
		return ops[m.GoName()] || ops[m.Name]
	}), nil
}

// crudOwnershipFindingsFor is the pure rule over an already-read schema and
// service set; generated reports whether forge generates the rpc's op.
func crudOwnershipFindingsFor(schema ownershipSchema, services []codegen.ServiceDef, generated func(codegen.ServiceDef, codegen.Method) bool) []crudOwnershipFinding {
	byName, byTable := map[string]schemadef.Table{}, map[string]schemadef.Table{}
	for _, t := range schema.Tables {
		if t.NoEntity() {
			continue
		}
		byTable[t.Name] = t
		key := strings.ToLower(codegen.TableEntityName(t))
		if _, dup := byName[key]; !dup || t.DeclaredEntityName() != "" {
			byName[key] = t
		}
	}
	lookup := func(entity string) (schemadef.Table, bool) {
		if t, ok := byName[strings.ToLower(entity)]; ok {
			return t, true
		}
		for _, cand := range codegen.EntityTableCandidates(entity) {
			if t, ok := byTable[cand]; ok {
				return t, true
			}
		}
		return schemadef.Table{}, false
	}

	rpcs := map[string][]string{}
	for _, svc := range services {
		for _, m := range svc.Methods {
			if !m.AuthRequired || !generated(svc, m) {
				continue
			}
			op, name := codegen.CRUDShape(m)
			if op == "" {
				continue
			}
			if op == "list" {
				name = inflection.Singular(name)
			}
			t, ok := lookup(name)
			if !ok {
				continue
			}
			rpcs[t.Name] = append(rpcs[t.Name], svc.Name+"."+m.Name)
		}
	}

	var out []crudOwnershipFinding
	for _, t := range schema.Tables {
		site := schema.Sites[t.Name]
		if marker, reason, ok := t.ScopeExemption(); ok {
			if reason == "" {
				out = append(out, crudOwnershipFinding{File: site.commentFile, Line: site.commentLine, Table: t.Name, Comment: t.Comment, Marker: marker})
			}
			continue
		}
		served := rpcs[t.Name]
		if len(served) == 0 || declaresOwnership(t) {
			continue
		}
		sort.Strings(served)
		out = append(out, crudOwnershipFinding{File: site.createFile, Line: site.createLine, Table: t.Name, Comment: t.Comment, RPCs: served})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Table < out[j].Table })
	return out
}

// columnOwnerMarkerRE matches schemadef.ColumnMarkerOwner as a whole token
// (`forge:owner-hint` is not it) — the audit's and codegen's reading.
var columnOwnerMarkerRE = regexp.MustCompile(regexp.QuoteMeta(schemadef.ColumnMarkerOwner) + `(?:[^\w:-]|$)`)

// declaresOwnership reports a table-level `forge:owner=` (parsed or not:
// generate reports a bad one) or a `forge:owner` column.
func declaresOwnership(t schemadef.Table) bool {
	if _, declared, _ := t.OwnerSpec(); declared {
		return true
	}
	for _, c := range t.Columns {
		if columnOwnerMarkerRE.MatchString(c.Comment) {
			return true
		}
	}
	return false
}

// generatedCRUDOpRE matches an op method the ops template emits:
// `func (s *Service) crud<Method>Op() crud.<Kind>Op[...]`.
var generatedCRUDOpRE = regexp.MustCompile(`(?m)^func\s+\(\s*\w+\s+\*Service\s*\)\s+crud(\w+)Op\(\)`)

// generatedCRUDOps maps each service (by proto name) to the Go method names
// whose CRUD op forge generates in its handlers_crud_ops_gen.go.
func generatedCRUDOps(root string, services []codegen.ServiceDef) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, svc := range services {
		dir, err := codegen.ResolveComponentDir(root, "internal/handlers", naming.ServicePackage(svc.Name))
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir.Dir, "handlers_crud_ops_gen.go"))
		if err != nil {
			continue
		}
		ops := map[string]bool{}
		for _, m := range generatedCRUDOpRE.FindAllStringSubmatch(string(data), -1) {
			ops[m[1]] = true
		}
		out[svc.Name] = ops
	}
	return out
}

// ownershipSchema is the slice of the schema the rule reads: each base table
// with its comment and its columns' comments, and where each was declared.
type ownershipSchema struct {
	Tables []schemadef.Table
	Sites  map[string]tableSite
}

// tableSite locates a table's CREATE TABLE and its latest COMMENT ON TABLE
// (project-relative file, 1-based line).
type tableSite struct {
	createFile, commentFile string
	createLine, commentLine int
}

// renameTableRe matches `ALTER TABLE <t> RENAME TO <new>`. CREATE and DROP
// reuse the fixture lane's createTableRe / dropTableRe.
var renameTableRe = regexp.MustCompile(`(?is)\balter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?(` + sqlident.Qualified + `)\s+rename\s+to\s+(` + sqlident.Part + `)`)

// ownershipSchemaFromMigrations replays the migrations' text in file order.
// Statements this does not recognise are ignored: the rule can then miss a
// table, never invent one.
func ownershipSchemaFromMigrations(root, migDir string) (ownershipSchema, error) {
	type tableState struct {
		t    schemadef.Table
		cols map[string]string
		site tableSite
	}
	tables := map[string]*tableState{}
	var order []string

	type event struct {
		at   int
		kind string
		m    []string
	}
	err := eachMigration(root, migDir, func(rel, text string) {
		lineAt := func(off int) int { return strings.Count(text[:off], "\n") + 1 }
		var evs []event
		add := func(kind string, re *regexp.Regexp) {
			for _, loc := range re.FindAllStringSubmatchIndex(text, -1) {
				m := make([]string, len(loc)/2)
				for i := range m {
					if loc[2*i] >= 0 {
						m[i] = text[loc[2*i]:loc[2*i+1]]
					}
				}
				evs = append(evs, event{at: loc[0], kind: kind, m: m})
			}
		}
		add("create", createTableRe)
		add("drop", dropTableRe)
		add("rename", renameTableRe)
		add("comment-table", commentOnRelationRe)
		add("comment-column", commentOnColumnRe)
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].at < evs[j].at })

		for _, ev := range evs {
			switch ev.kind {
			case "create":
				name := sqlident.Last(ev.m[1])
				if _, ok := tables[name]; !ok {
					order = append(order, name)
				}
				tables[name] = &tableState{t: schemadef.Table{Name: name}, cols: map[string]string{},
					site: tableSite{createFile: rel, createLine: lineAt(ev.at)}}
			case "drop":
				for _, n := range strings.Split(ev.m[1], ",") {
					delete(tables, sqlident.Last(n))
				}
			case "rename":
				from, to := sqlident.Last(ev.m[1]), sqlident.Last(ev.m[2])
				if st, ok := tables[from]; ok {
					delete(tables, from)
					st.t.Name = to
					tables[to] = st
					order = append(order, to)
				}
			case "comment-table":
				if !strings.EqualFold(ev.m[1], "table") {
					continue // a view's comment
				}
				if st, ok := tables[sqlident.Last(ev.m[2])]; ok {
					st.t.Comment = unescapeSQLString(ev.m[3])
					st.site.commentFile, st.site.commentLine = rel, lineAt(ev.at)
				}
			case "comment-column":
				parts := sqlident.Parts(ev.m[1])
				if len(parts) < 2 {
					continue
				}
				if st, ok := tables[parts[len(parts)-2]]; ok {
					st.cols[parts[len(parts)-1]] = unescapeSQLString(ev.m[2])
				}
			}
		}
	})
	if err != nil {
		return ownershipSchema{}, err
	}

	out := ownershipSchema{Sites: map[string]tableSite{}}
	seen := map[string]bool{}
	for _, name := range order {
		st, ok := tables[name]
		if !ok || seen[name] || st.t.Name != name {
			continue
		}
		seen[name] = true
		colNames := make([]string, 0, len(st.cols))
		for c := range st.cols {
			colNames = append(colNames, c)
		}
		sort.Strings(colNames)
		for _, c := range colNames {
			st.t.Columns = append(st.t.Columns, schemadef.Column{Name: c, Comment: st.cols[c]})
		}
		if st.site.commentFile == "" {
			st.site.commentFile, st.site.commentLine = st.site.createFile, st.site.createLine
		}
		out.Tables = append(out.Tables, st.t)
		out.Sites[name] = st.site
	}
	sort.Slice(out.Tables, func(i, j int) bool { return out.Tables[i].Name < out.Tables[j].Name })
	return out, nil
}

// unescapeSQLString undoes postgres's doubled-quote escaping in a string
// literal's body.
func unescapeSQLString(s string) string { return strings.ReplaceAll(s, "''", "'") }
