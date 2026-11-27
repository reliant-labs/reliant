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
//
// ── The one exception: a table still as forge scaffolded it (WARNING) ────
//
// `forge scaffold` births a `// forge:entity` table with its authenticated
// CRUD and deliberately declares no owner — only the author knows whose rows
// they are (see the birth in internal/scaffold/entityproto.go). Failing the
// gate on that output makes the scaffold phase's expected state illegal: an
// agent told to gate on lint invents an ownership decision nobody asked it
// to make. So while the table is still exactly what forge scaffolded, the
// finding is a warning, "pending: choose an owner for <Entity>", carrying
// the same fix. Once a person edits the table, it gates again.
//
// "Exactly what forge scaffolded" is two facts read from the migrations:
//
//   - the file that creates the table is a forge birth whose birth checksum
//     (checksums.StampSQLBirth) still matches every byte — an in-place edit
//     to the birth, a column added or a comment reworded, ends it;
//   - no other migration alters it (ALTER TABLE; any COMMENT ON it, a
//     column, or a constraint/trigger/policy/rule of it, IS NULL included;
//     CREATE/ALTER/DROP of an index, trigger, policy or rule on it — see
//     alterTableRe), except another pristine birth:
//     a later birth back-fills a foreign key onto an earlier table, and that
//     is forge's write, not a person's.
//
// No evidence reads as edited: a table born before births carried the
// checksum, a hand-written CREATE TABLE, or a squashed baseline gates as it
// always did. An exemption declared without a reason is never held: it is a
// declaration, and wrong whatever state the table is in.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/checksums"
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
	// Entity names the table's entity ("Task") in a pending finding.
	Entity string
	// Pending is set when the table is still exactly as forge scaffolded
	// it, which holds the finding at a warning (see the file header).
	Pending bool
}

// severity is the finding's JSON severity: an error, unless the table is
// still forge's fresh scaffold.
func (f crudOwnershipFinding) severity() string {
	if f.Pending {
		return lintSevWarning
	}
	return lintSevError
}

func (f crudOwnershipFinding) message() string {
	if f.Marker != "" {
		return fmt.Sprintf("table %s declares %s with no reason: the marker exists to record why the table needs no owner, and an empty one records nothing", f.Table, f.Marker)
	}
	msg := fmt.Sprintf("table %s is read and written by forge-generated CRUD on authenticated rpc(s) %s and declares no owner: every signed-in caller can read, update and delete every row",
		f.Table, strings.Join(f.RPCs, ", "))
	if f.Pending {
		msg = fmt.Sprintf("pending: choose an owner for %s — %s. The table is still exactly as forge scaffolded it "+
			"(its birth migration is unedited and no later migration alters it), so this is a warning for now; "+
			"it fails the build once the table is edited", f.Entity, msg)
	}
	return msg
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
			File: f.File, Line: f.Line, Severity: f.severity(),
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
		out = append(out, crudOwnershipFinding{File: site.createFile, Line: site.createLine, Table: t.Name, Comment: t.Comment, RPCs: served,
			Entity: codegen.TableEntityName(t), Pending: site.scaffolded()})
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
// (project-relative file, 1-based line), and records whether the table is
// still as forge scaffolded it.
type tableSite struct {
	createFile, commentFile string
	createLine, commentLine int
	// bornPristine: the CREATE TABLE is in a forge birth migration whose
	// birth checksum still matches it.
	bornPristine bool
	// editedElsewhere: a migration other than the CREATE's, and not itself
	// a pristine birth, alters the table.
	editedElsewhere bool
}

// scaffolded reports whether the table is still exactly as forge
// scaffolded it: the crud-table-unowned exception (see the file header).
func (s tableSite) scaffolded() bool { return s.bornPristine && !s.editedElsewhere }

// renameTableRe matches `ALTER TABLE <t> RENAME TO <new>`. CREATE reuses the
// fixture lane's createTableRe; dropTablesRe captures DROP TABLE's whole
// name list (`DROP TABLE a, b`), which the fixture lane's dropTableRe does
// not.
var (
	renameTableRe = regexp.MustCompile(`(?is)\balter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?(` + sqlident.Qualified + `)\s+rename\s+to\s+(` + sqlident.Part + `)`)
	dropTablesRe  = regexp.MustCompile(`(?is)\bdrop\s+table\s+(?:if\s+exists\s+)?((?:` + sqlident.Qualified + `)(?:\s*,\s*` + sqlident.Qualified + `)*)`)
	qualifiedRe   = regexp.MustCompile(sqlident.Qualified)
)

// The statements that change an existing table, which end its scaffolded
// state when a person writes them:
//
//   - alterTableRe: any `ALTER TABLE [IF EXISTS] [ONLY] <t>`;
//   - createOnTableRe: `CREATE [UNIQUE] INDEX | TRIGGER | POLICY … ON <t>`;
//   - createRuleRe: `CREATE RULE <r> AS ON <event> TO <t>` (a rule names its
//     table after TO, not ON);
//   - onTableObjectRe: `DROP | ALTER TRIGGER | POLICY | RULE <name> ON <t>`;
//   - commentOnTableObjectRe: any COMMENT ON the table, a column of it, or a
//     constraint, trigger, policy or rule on it, whatever the value — a
//     quoted string, E'…', a dollar quote, or IS NULL, which removes the
//     comment. The value is read separately (sqlCommentValue): Go's regexp
//     cannot match a dollar quote's closing tag.
//
// Names may be schema-qualified or quoted (sqlident). `COMMENT ON INDEX
// <idx>` and `DROP | ALTER INDEX <idx>` name only the index, and resolving
// one to its table would mean tracking every index name, so they are not
// read: the rule can miss that edit, never invent one.
var (
	alterTableRe           = regexp.MustCompile(`(?is)\balter\s+table\s+(?:if\s+exists\s+)?(?:only\s+)?(` + sqlident.Qualified + `)`)
	createOnTableRe        = regexp.MustCompile(`(?is)\bcreate\s+(?:or\s+replace\s+)?(?:unique\s+)?(?:index|(?:constraint\s+)?trigger|policy)\b[^;]*?\bon\s+(?:only\s+)?(` + sqlident.Qualified + `)`)
	createRuleRe           = regexp.MustCompile(`(?is)\bcreate\s+(?:or\s+replace\s+)?rule\s+` + sqlident.Part + `\s+as\s+on\s+(?:select|insert|update|delete)\s+to\s+(?:only\s+)?(` + sqlident.Qualified + `)`)
	onTableObjectRe        = regexp.MustCompile(`(?is)\b(?:drop|alter)\s+(?:trigger|policy|rule)\s+(?:if\s+exists\s+)?` + sqlident.Part + `\s+on\s+(?:only\s+)?(` + sqlident.Qualified + `)`)
	commentOnTableObjectRe = regexp.MustCompile(`(?is)\bcomment\s+on\s+(table|column|(?:constraint|trigger|policy|rule)\s+` + sqlident.Part + `\s+on)\s+(` + sqlident.Qualified + `)\s+is\b`)
)

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
		at, end int
		kind    string
		m       []string
	}
	err := eachMigration(root, migDir, func(rel, text string) {
		lineAt := func(off int) int { return strings.Count(text[:off], "\n") + 1 }
		// text has its comments blanked, and the birth checksum is a
		// comment over the file's own bytes: read those.
		pristineBirth := false
		path := filepath.FromSlash(rel)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if raw, err := os.ReadFile(path); err == nil {
			_, pristineBirth = checksums.SQLBirthIsPristine(raw)
		}
		// edited records a statement in this file that changes table name:
		// a person's edit unless this file is the table's own CREATE or a
		// pristine birth (forge back-filling a foreign key).
		edited := func(name string) {
			if st, ok := tables[name]; ok && !pristineBirth && st.site.createFile != rel {
				st.site.editedElsewhere = true
			}
		}
		var evs []event
		add := func(kind string, re *regexp.Regexp) {
			for _, loc := range re.FindAllStringSubmatchIndex(text, -1) {
				m := make([]string, len(loc)/2)
				for i := range m {
					if loc[2*i] >= 0 {
						m[i] = text[loc[2*i]:loc[2*i+1]]
					}
				}
				evs = append(evs, event{at: loc[0], end: loc[1], kind: kind, m: m})
			}
		}
		add("create", createTableRe)
		add("drop", dropTablesRe)
		add("rename", renameTableRe)
		add("comment", commentOnTableObjectRe)
		add("alter", alterTableRe)
		add("alter", createOnTableRe)
		add("alter", createRuleRe)
		add("alter", onTableObjectRe)
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].at < evs[j].at })

		for _, ev := range evs {
			switch ev.kind {
			case "create":
				name := sqlident.Last(ev.m[1])
				if _, ok := tables[name]; !ok {
					order = append(order, name)
				}
				tables[name] = &tableState{t: schemadef.Table{Name: name}, cols: map[string]string{},
					site: tableSite{createFile: rel, createLine: lineAt(ev.at), bornPristine: pristineBirth}}
			case "drop":
				for _, n := range qualifiedRe.FindAllString(ev.m[1], -1) {
					delete(tables, sqlident.Last(n))
				}
			case "rename":
				from, to := sqlident.Last(ev.m[1]), sqlident.Last(ev.m[2])
				edited(from)
				if st, ok := tables[from]; ok {
					delete(tables, from)
					st.t.Name = to
					tables[to] = st
					order = append(order, to)
				}
			case "comment":
				// COMMENT ON replaces the whole comment and IS NULL removes
				// it; a value this cannot read leaves the last known one.
				value, known := sqlCommentValue(text[ev.end:])
				switch kind := strings.ToLower(ev.m[1]); kind {
				case "table":
					edited(sqlident.Last(ev.m[2]))
					if st, ok := tables[sqlident.Last(ev.m[2])]; ok && known {
						st.t.Comment = value
						st.site.commentFile, st.site.commentLine = rel, lineAt(ev.at)
					}
				case "column":
					parts := sqlident.Parts(ev.m[2])
					if len(parts) < 2 {
						continue
					}
					edited(parts[len(parts)-2])
					if st, ok := tables[parts[len(parts)-2]]; ok && known {
						st.cols[parts[len(parts)-1]] = value
					}
				default: // a constraint, trigger, policy or rule ON the table
					edited(sqlident.Last(ev.m[2]))
				}
			case "alter":
				edited(sqlident.Last(ev.m[1]))
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

// dollarTagRe matches a dollar quote's opening tag: `$$` or `$tag$`.
var dollarTagRe = regexp.MustCompile(`^\$(?:[A-Za-z_][A-Za-z0-9_]*)?\$`)

// sqlCommentValue reads the value a COMMENT ON statement sets from the text
// after its IS: "" for NULL (which removes the comment), else the body of a
// '…' string (doubled quotes undone), an E'…' string (backslash escapes
// too) or a dollar quote. known is false for anything else — a U&'…'
// string, or text that is not a literal at all.
func sqlCommentValue(s string) (value string, known bool) {
	s = strings.TrimLeft(s, " \t\r\n")
	switch {
	case len(s) >= 4 && strings.EqualFold(s[:4], "null") && (len(s) == 4 || !isIdentByte(s[4])):
		return "", true
	case strings.HasPrefix(s, "'"):
		return quotedSQLString(s[1:], false)
	case len(s) >= 2 && (s[0] == 'E' || s[0] == 'e') && s[1] == '\'':
		return quotedSQLString(s[2:], true)
	case strings.HasPrefix(s, "$"):
		tag := dollarTagRe.FindString(s)
		if tag == "" {
			return "", false
		}
		if body, _, found := strings.Cut(s[len(tag):], tag); found {
			return body, true
		}
		return "", false
	}
	return "", false
}

// quotedSQLString reads a string literal's body up to its closing quote,
// s starting just after the opening one. backslash selects E'…' escapes.
func quotedSQLString(s string, backslash bool) (string, bool) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'' && i+1 < len(s) && s[i+1] == '\'':
			b.WriteByte('\'')
			i++
		case c == '\'':
			return b.String(), true
		case c == '\\' && backslash && i+1 < len(s):
			i++
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}
