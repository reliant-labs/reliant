package cli

// Owned CRUD pages that miss a field the entity now has.
//
// The list/detail/create/edit pages are scaffold-once: written at birth and
// never rewritten (renderPageScaffoldIfMissing). Everything else follows a
// new column — the ORM, CRUD, search, grants, mocks — so a field added after
// the pages were born silently never reaches them, and generate said nothing.
//
// This names the gap without touching the page. The page is rendered as it
// would be written today, and every entity field that render shows but the
// owned page never mentions is reported in generate's closing "left as they
// are" list (checksums.NoteLeftAloneDetail), with the lines forge would write
// for it. "Mentions" is a whole
// word anywhere in the file, comments included, so a field left out on
// purpose is acknowledged by naming it in a comment, and nothing else needs
// to be declared.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
)

// notePageMissingFields reports the fields the page at relPath would show if
// forge wrote it today but does not mention. Best-effort: a page that is
// absent, unreadable or cannot be rendered is not reported.
func notePageMissingFields(tmpl *template.Template, data codegen.PageTemplateData, projectDir, relPath string) {
	owned, err := os.ReadFile(filepath.Join(projectDir, relPath))
	if err != nil {
		return
	}
	var fresh bytes.Buffer
	if err := tmpl.Execute(&fresh, data); err != nil {
		return
	}
	missing := pageMissingFields(pageFieldNames(data), fresh.String(), string(owned))
	if len(missing) == 0 {
		return
	}
	checksums.NoteLeftAloneDetail(filepath.ToSlash(relPath), fmt.Sprintf(
		"this page is yours, so forge does not rewrite it, and it does not show %s, which %s now has: add %s by hand "+
			"(the lines below are what forge writes for %s today), or name %s in a comment to say it is left out on purpose",
		quotedList(missing), data.EntityName, pronounFor(missing), pronounFor(missing), pronounFor(missing)),
		freshLinesFor(missing, fresh.String()))
}

// maxFreshLinesPerField bounds the example lines printed for one field.
const maxFreshLinesPerField = 3

// freshLinesFor is, for each missing field, the lines of the fresh render
// that mention it (trimmed, at most maxFreshLinesPerField each).
func freshLinesFor(missing []string, fresh string) []string {
	lines := strings.Split(fresh, "\n")
	var out []string
	for _, n := range missing {
		re := wordRE(n)
		shown := 0
		for _, l := range lines {
			if shown == maxFreshLinesPerField {
				break
			}
			if t := strings.TrimSpace(l); t != "" && re.MatchString(t) {
				out = append(out, n+": "+t)
				shown++
			}
		}
	}
	return out
}

func wordRE(n string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(n) + `\b`)
}

// pageFieldNames is every field name (camelCase) the entity's pages can show.
func pageFieldNames(d codegen.PageTemplateData) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, f := range d.Columns {
		add(f.Name)
	}
	for _, f := range d.ListColumns {
		add(f.Name)
	}
	for _, f := range d.CreateFields {
		add(f.Name)
	}
	for _, f := range d.UpdateFields {
		add(f.Name)
	}
	return out
}

// pageMissingFields is the fields of names that fresh (the page as forge
// would write it now) mentions and owned (the page on disk) does not, sorted.
func pageMissingFields(names []string, fresh, owned string) []string {
	var out []string
	for _, n := range names {
		re := wordRE(n)
		if re.MatchString(fresh) && !re.MatchString(owned) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func quotedList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "`" + n + "`"
	}
	if len(q) == 1 {
		return "field " + q[0]
	}
	return "fields " + strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

func pronounFor(names []string) string {
	if len(names) == 1 {
		return "it"
	}
	return "them"
}
