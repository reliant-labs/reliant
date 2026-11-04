package templates

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

// A scaffold-once artifact must not be written against identifiers the same
// scaffold instructs the author to delete.
//
// The failure this pins is specific and was real: an internal-package
// scaffold shipped a contract.go with a PLACEHOLDER surface, told the author
// (in the file and in the charter) to replace it with the real domain
// signatures, and shipped a born _test.go naming those same placeholder
// identifiers. Replacing the contract as instructed left the born test naming
// three identifiers that no longer existed. It surfaced as a `typecheck`
// error inside golangci-lint, in a file the author was never pointed at, and
// it took the contract linter and `forge generate` down with it — a gate red
// produced by following the instructions exactly.
//
// The rule generalizes to every scaffold: whatever the scaffold marks as a
// placeholder, the born test must not name. contract_test.go already gets
// this right — it ships commented example rows and names nothing.

// placeholderDeclRe matches a top-level type declaration or an interface
// method signature in a Go template, capturing the identifier.
var placeholderDeclRe = regexp.MustCompile(`^\s*(?:type\s+([A-Z]\w*)|([A-Z]\w*)\s*\()`)

// placeholderHintRe matches the comment phrasings a scaffold uses to tell the
// author "this is not the real thing; swap it out".
var placeholderHintRe = regexp.MustCompile(`(?i)\bplaceholder\b|\breplace (?:it |them |this )?with\b|TODO:\s*replace`)

// TestInternalPackageScaffoldTestsNameNoPlaceholder walks every
// internal-package scaffold that ships a `_test.go.tmpl` and asserts the born
// test names no identifier its sibling templates declare under a placeholder
// comment.
//
// The scanned set is derived from the template tree, not from a hard-coded
// list of filenames, so a scaffold added later is covered the day it lands.
// An empty scan is a hard failure: a guard that inspects nothing reports green
// forever.
func TestInternalPackageScaffoldTestsNameNoPlaceholder(t *testing.T) {
	t.Parallel()

	const root = "internal-package"
	dirs := map[string]bool{}
	err := fs.WalkDir(templateFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go.tmpl") {
			return nil
		}
		dirs[path.Dir(p)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	scanned := 0
	for dir := range dirs {
		entries, err := fs.ReadDir(templateFS, dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}

		var testTmpls []string
		placeholders := map[string]string{} // ident → template that declared it
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go.tmpl") {
				continue
			}
			body := readTemplate(t, path.Join(dir, name))
			if strings.HasSuffix(name, "_test.go.tmpl") {
				testTmpls = append(testTmpls, name)
				continue
			}
			for ident := range placeholderIdents(body) {
				placeholders[ident] = name
			}
		}

		for _, tt := range testTmpls {
			scanned++
			body := readTemplate(t, path.Join(dir, tt))
			if !strings.Contains(body, "func Test") {
				t.Errorf("%s/%s contains no test; the fix for a placeholder coupling is to change the test's SUBJECT, never to delete the test", dir, tt)
			}
			for ident, from := range placeholders {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(ident) + `\b`).MatchString(body) {
					t.Errorf("%s/%s references %q, which %s declares as a placeholder the author is told to replace. "+
						"Replacing it as instructed leaves this born test naming an identifier that no longer exists, "+
						"which surfaces as a typecheck failure in a file nobody was pointed at.", dir, tt, ident, from)
				}
			}
		}
	}

	if scanned == 0 {
		t.Fatalf("scanned 0 scaffold test templates under %s/ — the guard is inspecting nothing. "+
			"Retarget it at wherever the scaffold-once tests moved rather than leaving it green.", root)
	}
}

// TestPlaceholderIdentsDetectsTheCouplingShape proves the detector actually
// fires. Without it the walk above would pass on any tree, including one that
// reintroduced the exact coupling.
func TestPlaceholderIdentsDetectsTheCouplingShape(t *testing.T) {
	t.Parallel()

	const contract = `package thing

// Service is the thing's use-case surface.
type Service interface {
	// Run is a placeholder for the primary use case.
	// Replace with your workflow's signature.
	Run(ctx context.Context, in RunInput) (RunResult, error)
}

// RunInput is the input shape for the placeholder Run method.
type RunInput struct {
	ID string
}

// Store is this package's persistence boundary.
type Store interface {
	Get(ctx context.Context, id string) error
}
`

	got := placeholderIdents(contract)
	for _, want := range []string{"Run", "RunInput"} {
		if !got[want] {
			t.Errorf("placeholderIdents missed %q; got %v", want, keys(got))
		}
	}
	for _, notWant := range []string{"Service", "Store"} {
		if got[notWant] {
			t.Errorf("placeholderIdents wrongly flagged %q as a placeholder; got %v", notWant, keys(got))
		}
	}
}

// placeholderIdents returns the identifiers a template declares directly
// under a comment that marks them as placeholders. The comment block runs
// until the first non-comment line, so only the declaration a hint actually
// introduces is captured.
func placeholderIdents(body string) map[string]bool {
	out := map[string]bool{}
	hinted := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			if placeholderHintRe.MatchString(trimmed) {
				hinted = true
			}
			continue
		}
		if trimmed == "" {
			hinted = false
			continue
		}
		if hinted {
			if m := placeholderDeclRe.FindStringSubmatch(line); m != nil {
				ident := m[1]
				if ident == "" {
					ident = m[2]
				}
				out[ident] = true
			}
		}
		hinted = false
	}
	return out
}

func readTemplate(t *testing.T, p string) string {
	t.Helper()
	b, err := templateFS.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
