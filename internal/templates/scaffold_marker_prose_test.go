package templates

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// A user-owned scaffold's prose must not spell a forge directive the way the
// directive itself is spelled.
//
// The failure this pins was real: the contract package's service.go explained
// "The //forge:contract marker on `service` below …" a few lines above the
// real `//forge:contract` line, so an agent's string-based edit aimed at the
// directive matched the comment instead, and a grep for packages carrying a
// marker listed every package scaffolded with the explanation. A directive
// line is a comment whose whole text is the marker; prose names a marker
// without the comment slashes (`the forge:contract marker`), which no exact
// search for the directive line can confuse with the real one.

// embeddedDirectiveRe matches a comment line that carries a directive
// spelling AFTER other text: `// The //forge:contract marker`, or a
// backquoted `// forge:no-observe` inside an explanation.
var embeddedDirectiveRe = regexp.MustCompile(`^\s*//.*\S.*//\s?forge:[a-z]`)

// TestOwnedScaffoldProseSpellsNoDirective walks every user-owned Go scaffold
// (its banner says it is scaffolded once and never overwritten) and fails a
// comment line that embeds a directive spelling in prose. The scanned set is
// derived from the template tree, so a scaffold added later is covered the
// day it lands; an empty scan is a hard failure.
func TestOwnedScaffoldProseSpellsNoDirective(t *testing.T) {
	t.Parallel()

	scanned := 0
	err := fs.WalkDir(templateFS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go.tmpl") {
			return nil
		}
		body := readTemplate(t, p)
		if !strings.HasPrefix(body, "// yours: scaffolded once") {
			return nil
		}
		scanned++
		for i, line := range strings.Split(body, "\n") {
			if embeddedDirectiveRe.MatchString(line) {
				t.Errorf("%s:%d: prose spells a forge directive as the directive itself — name it without the comment slashes (\"the forge:<name> marker\"), so a search for the real directive line cannot match this comment:\n\t%s",
					p, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if scanned == 0 {
		t.Fatal("no user-owned Go scaffold found (banner \"// yours: scaffolded once\"); the guard inspected nothing")
	}
}
