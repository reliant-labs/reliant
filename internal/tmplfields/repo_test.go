package tmplfields

import (
	"bufio"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/repowalk"
	"github.com/reliant-labs/forge/internal/templates"
)

// minChecked is a floor a little under the number of call sites the scan
// resolves and checks today (see the t.Log line). It keeps the guard from
// passing vacuously: a resolver change, or a renamed accessor, that quietly
// leaves every site OPEN fails here instead of checking nothing. Raise it as
// coverage grows; lower it only with the reason in the commit.
const minChecked = 220

// TestEveryRenderCallSiteSuppliesTheFieldsItsTemplateReads is the guard: no
// `<X>Templates().Render("<name>.tmpl", data)` in this module, production
// code or test, may hand its template data that lacks a field the template
// references. See the package doc for why executing the templates cannot
// catch this, and for what is left unchecked.
//
// It reads every Go file in-process (repowalk + os.ReadFile) and the
// templates from the embedded FS, so the -short tier's cached result is
// invalidated by exactly the inputs it judged.
func TestEveryRenderCallSiteSuppliesTheFieldsItsTemplateReads(t *testing.T) {
	t.Parallel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	modPath := modulePath(t, root)
	files := moduleGoFiles(t, root)

	rep, err := Scan(files, Config{
		ModulePath:   modPath,
		TemplatesPkg: modPath + "/internal/templates",
		Accessors:    templateAccessors(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var reasons []string
	for r, n := range rep.Skipped {
		reasons = append(reasons, r+": "+strconv.Itoa(n))
	}
	sort.Strings(reasons)
	t.Logf("Render calls (call site × template it renders): %d checked, %d open (data type not resolvable), %d skipped [%s]",
		rep.Checked, rep.Open, rep.SkippedTotal(), strings.Join(reasons, "; "))

	for _, f := range rep.Findings {
		t.Error(f.String())
	}
	if rep.Checked < minChecked {
		t.Errorf("only %d Render call sites were checked, under the floor of %d: the resolver has stopped resolving call sites it used to (or the accessors moved), so this guard is near-vacuous", rep.Checked, minChecked)
	}
}

// templateAccessors maps each category accessor of internal/templates to its
// embedded templates.
func templateAccessors() map[string]Accessor {
	noArg := func(c func() templates.TemplateCategory) Accessor {
		return Accessor{Get: func(_, name string) ([]byte, bool) {
			b, err := c().Get(name)
			return b, err == nil
		}}
	}
	withArg := func(c func(string) templates.TemplateCategory) Accessor {
		return Accessor{TakesArg: true, Get: func(arg, name string) ([]byte, bool) {
			b, err := c(arg).Get(name)
			return b, err == nil
		}}
	}
	return map[string]Accessor{
		"ProjectTemplates":         noArg(templates.ProjectTemplates),
		"FrontendTemplates":        noArg(templates.FrontendTemplates),
		"DeployTemplates":          noArg(templates.DeployTemplates),
		"IngressTemplates":         noArg(templates.IngressTemplates),
		"TestTemplates":            noArg(templates.TestTemplates),
		"InternalPkgTemplates":     noArg(templates.InternalPkgTemplates),
		"ServiceTemplates":         noArg(templates.ServiceTemplates),
		"WebhookTemplates":         noArg(templates.WebhookTemplates),
		"WorkerTemplates":          noArg(templates.WorkerTemplates),
		"WorkerCronTemplates":      noArg(templates.WorkerCronTemplates),
		"OperatorTemplates":        noArg(templates.OperatorTemplates),
		"CITemplates":              withArg(templates.CITemplates),
		"InternalPkgKindTemplates": withArg(templates.InternalPkgKindTemplates),
	}
}

func modulePath(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("expected forge's go.mod at %s: %v", root, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod has no module line")
	return ""
}

// moduleGoFiles reads every .go file of the module, keyed by its slash path
// relative to root. Directories no build of this module sees are skipped.
func moduleGoFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	skip := map[string]bool{".git": true, ".forge": true, "node_modules": true, "testdata": true, "vendor": true, "tmp": true, "bin": true}
	files := map[string][]byte{}
	err := repowalk.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".") || strings.HasPrefix(d.Name(), "_")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
