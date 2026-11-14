// File: internal/cli/lint_column_markers.go
//
// column-markers — `forge lint --column-markers`.
//
// A `forge:*` catalog-comment marker (COMMENT ON TABLE / VIEW / COLUMN /
// CONSTRAINT) that is misspelled, or declared on an object nothing reads it
// on, does NOTHING — postgres stores the comment verbatim, forge reads it
// looking for an exact known marker, and a typo like `forge:immutible`
// silently fails to opt the column into anything. That is the same
// silent-failure shape `forge:ref` shipped with before the registry existed:
// undiscoverable until the seeder (or, for forge:immutable, an unwanted
// UPDATE clobber) surprised someone.
//
// This check reads every COMMENT ON statement in the migrations and
// classifies each `forge:` marker in it through
// schemadef.CommentMarkerProblems — the SAME classifier `forge generate`
// runs over the applied catalog (schemadef.CheckMarkers), over the same
// per-object vocabularies `forge project annotations --kind column|table`
// dumps. So the lint, the generator and the docs cannot disagree about what
// a marker is.
//
// It GATES, like the Go-source twin (lint_go_markers.go) and unlike the
// proto one: a `forge:` token in a catalog comment has one reader, and the
// generate that reads the schema already refuses the same comment — this
// lane is the file:line half of that verdict, found without a shadow
// database. Prose in the comment is fine; only `forge:<name>` tokens are
// read, and `forge: ` followed by a space is not one.

package lint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/sqlident"
	"github.com/reliant-labs/forge/pkg/dbgrants"
	"github.com/reliant-labs/forge/pkg/schemadef"
)

// commentOnColumnRe matches `COMMENT ON COLUMN <table>.<column> IS '...'`.
// The captured object is the dotted table.column form as authored; the
// captured text is the comment body (quotes doubled per postgres escaping
// are left as-is — marker names never contain a quote, so this is fine for
// marker detection).
var commentOnColumnRe = regexp.MustCompile(`(?is)\bcomment\s+on\s+column\s+(` + sqlident.Qualified + `)\s+is\s+'((?:[^']|'')*)'`)

// commentOnConstraintRe matches
// `COMMENT ON CONSTRAINT <name> ON <table> IS '...'`.
var commentOnConstraintRe = regexp.MustCompile(`(?is)\bcomment\s+on\s+constraint\s+(` + sqlident.Part + `)\s+on\s+(` + sqlident.Qualified + `)\s+is\s+'((?:[^']|'')*)'`)

// forgeTokenRe extracts the whole `forge:...` token (up to the next
// whitespace) out of a comment body, for reporting the exact offending
// text rather than the whole comment. The proto-markers lane reads it;
// catalog comments are tokenized by schemadef.MarkerTokens.
var forgeTokenRe = regexp.MustCompile(`forge:\S*`)

// commentOnRelationRe matches `COMMENT ON TABLE|VIEW|MATERIALIZED VIEW
// <name> IS '...'`; the first capture is the object kind.
var commentOnRelationRe = regexp.MustCompile(`(?is)\bcomment\s+on\s+(table|view|materialized\s+view)\s+(` + sqlident.Qualified + `)\s+is\s+'((?:[^']|'')*)'`)

// columnMarkerFinding is one COMMENT ON COLUMN/CONSTRAINT whose text
// carries an unrecognized `forge:` marker. File is projectDir-relative.
type columnMarkerFinding struct {
	File   string
	Line   int
	Object string // "orders.patient_id", "table orders" or "constraint orders_patient_id_fkey on orders"
	Marker string // the offending forge:* marker name, verbatim
	// Problem is schemadef's sentence: why the marker does nothing there,
	// and the vocabulary that object does read.
	Problem string
	// Rule is the finding's rule ID; empty is ruleColumnMarkers.
	Rule string
}

// ruleColumnMarkers is the lane's own rule: a marker the object does not read.
const ruleColumnMarkers = "forge-column-markers"

// ruleIdentitySubjectUnreadable is a forge:identity-subject column the
// read-only role cannot read. The console reads the database as that role
// (forge_ro), so the column never reaches it, and the link the marker asks for
// is never drawn. Declaring both is a contradiction: one of them is wrong.
const ruleIdentitySubjectUnreadable = "forge-identity-subject-unreadable"

func (f columnMarkerFinding) rule() string {
	if f.Rule != "" {
		return f.Rule
	}
	return ruleColumnMarkers
}

// columnMarkerFixHint renders the remediation: schemadef's own sentence, so
// the hint names exactly the vocabulary the generator enforces.
func columnMarkerFixHint(f columnMarkerFinding) string {
	return fmt.Sprintf("on %s: %s (see `forge project annotations --kind column` / `--kind table`)", f.Object, f.Problem)
}

// migrationsDirFor resolves the migrations directory every lint lane reads:
// the primary database's (forge.yaml's database.migrations_dir, or the env
// declaration's primary database), or the db/migrations default. Only
// --migration-safety reads every declared database.
func migrationsDirFor(cfg *config.ProjectConfig) string {
	if cfg == nil {
		return filepath.FromSlash(config.DefaultMigrationsDir)
	}
	return filepath.FromSlash(cfg.PrimaryDatabase().MigrationsPath())
}

// collectColumnMarkerFindings is the shared engine behind text mode and
// `forge lint --json`. It walks migrationsDir for *.up.sql files and flags
// every COMMENT ON COLUMN / COMMENT ON CONSTRAINT whose text contains
// `forge:` but matches none of schemadef.KnownColumnMarkers. Findings come
// back sorted by (file, line) so output is deterministic.
//
// A missing or empty migrations directory is not an error — many linter
// invocations run from a repo root before any migration exists — it just
// yields no findings.
func collectColumnMarkerFindings(migrationsDir string) ([]columnMarkerFinding, error) {
	if _, err := os.Stat(migrationsDir); os.IsNotExist(err) {
		return nil, nil
	}

	var files []string
	if err := filepath.WalkDir(migrationsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".up.sql") {
			return nil
		}
		files = append(files, path)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk %s: %w", migrationsDir, err)
	}
	sort.Strings(files)

	var findings []columnMarkerFinding
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", file, err)
		}
		findings = append(findings, findUnknownColumnMarkers(file, string(data))...)
	}
	return findings, nil
}

// findUnknownColumnMarkers scans one migration file's content for
// COMMENT ON statements carrying a forge:* marker the object does not read.
func findUnknownColumnMarkers(file, content string) []columnMarkerFinding {
	var findings []columnMarkerFinding
	add := func(start int, object string, site schemadef.MarkerSite, column, text string) {
		for _, p := range schemadef.CommentMarkerProblems(site, column, text) {
			// A retired marker in migration TEXT may be one a later
			// migration already cleared, and an applied migration is never
			// edited; whether the schema still carries it is the catalog's
			// question, which generate and --db-grants ask (CheckMarkers).
			if schemadef.RetiredMarker(p.Marker) {
				continue
			}
			findings = append(findings, columnMarkerFinding{
				File:    file,
				Line:    strings.Count(content[:start], "\n") + 1,
				Object:  object,
				Marker:  p.Marker,
				Problem: p.Problem,
			})
		}
	}

	for _, m := range commentOnColumnRe.FindAllStringSubmatchIndex(content, -1) {
		object := content[m[2]:m[3]]
		column, text := sqlident.Last(object), content[m[4]:m[5]]
		add(m[0], object, schemadef.SiteColumn, column, text)
		if problem := unreadableIdentitySubject(column, text); problem != "" {
			findings = append(findings, columnMarkerFinding{
				File:    file,
				Line:    strings.Count(content[:m[0]], "\n") + 1,
				Object:  object,
				Marker:  schemadef.ColumnMarkerIdentitySubject,
				Problem: problem,
				Rule:    ruleIdentitySubjectUnreadable,
			})
		}
	}
	for _, m := range commentOnConstraintRe.FindAllStringSubmatchIndex(content, -1) {
		name := content[m[2]:m[3]]
		table := content[m[4]:m[5]]
		add(m[0], fmt.Sprintf("constraint %s on %s", name, table), schemadef.SiteConstraint, "", content[m[6]:m[7]])
	}
	for _, m := range commentOnRelationRe.FindAllStringSubmatchIndex(content, -1) {
		site := schemadef.SiteTable
		if !strings.EqualFold(content[m[2]:m[3]], "table") {
			site = schemadef.SiteView
		}
		add(m[0], fmt.Sprintf("%s %s", site, content[m[4]:m[5]]), site, "", content[m[6]:m[7]])
	}

	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Line < findings[j].Line })
	return findings
}

// unreadableIdentitySubject is the sentence for a forge:identity-subject
// column the read-only role is not granted, "" when the column carries no
// valid declaration or the role may read it. The role's decision is
// dbgrants.Column.Disposition over what the comment says: forge:secret
// withholds the column, and a credential-shaped name withholds it until
// forge:readable grants it. A forge:secret on the proto field is not in the
// migration's text; `forge generate` and the grants see that one.
func unreadableIdentitySubject(column, comment string) string {
	c := schemadef.Column{Name: column, Comment: comment}
	if _, ok := c.IdentitySubject(); !ok {
		return ""
	}
	_, readable := c.ReadableReason()
	switch (dbgrants.Column{Name: column, Secret: c.DeclaresSecret(), Readable: readable}).Disposition() {
	case dbgrants.WithheldSecret:
		return fmt.Sprintf("%s is also declared %s, so the read-only role the console reads through never sees it and no identity link is drawn — "+
			"a subject id or email is not a credential: drop %s, or drop %s if the column really is secret",
			schemadef.ColumnMarkerIdentitySubject, schemadef.ColumnMarkerSecret, schemadef.ColumnMarkerSecret, schemadef.ColumnMarkerIdentitySubject)
	case dbgrants.WithheldUndecided:
		return fmt.Sprintf("%s's name looks like a credential, so the read-only role the console reads through is not granted it and no identity link is drawn — "+
			"add `%s <why it is not a credential>` to the comment",
			column, schemadef.ColumnMarkerReadable)
	}
	return ""
}
