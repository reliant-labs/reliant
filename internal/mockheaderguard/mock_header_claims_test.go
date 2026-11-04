package mockheaderguard_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/generator"
)

// The generated mock-data header used to open with:
//
//	Deterministic mock data for <Entity>. Mirrors the values
//	produced by db/fixtures so the mock transport and the seeded test
//	database return identical objects for the same identifiers.
//
// forge scaffolds no `db/fixtures` directory — the seed model is
// `db/seeds/` (vocab.yaml + a user-owned custom/ overlay), and the mock
// values come from codegen.SeedProjection reading the project's own
// seedplan.Plan. So the header named a directory that does not exist, and
// pointed a reader looking for "where does this data come from?" at
// nothing. `pkg/testkit`'s LoadFixture still says db/fixtures, but that is
// a path the CALLER passes and forge never creates; the mock header was
// claiming forge itself produces one.
//
// This guard derives the forbidden set from the filesystem of a REAL
// scaffolded project rather than from a hand-written list of bad strings:
// a directory reference in the mock header is legitimate exactly when
// `forge project new` creates that directory. Add a new
// `db/<something>` reference to the template and this test either finds
// the directory in a freshly generated project or names the claim.

// dbDirClaimRE captures every `db/<name>` path the mock-data header cites.
var dbDirClaimRE = regexp.MustCompile(`\bdb/([a-z_][a-z0-9_-]*)`)

// TestMockHeaderCitesOnlyDirectoriesForgeScaffolds reads the mock-data
// template's header comment, extracts every db/<dir> it names, and requires
// each to exist in a project forge actually generates.
func TestMockHeaderCitesOnlyDirectoriesForgeScaffolds(t *testing.T) {
	tmplPath := filepath.Join("..", "templates", "frontend", "mocks", "mock-data.ts.tmpl")
	//nolint:gocritic // path is repo-relative by design: the guard reads the SHIPPED template.
	raw, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatalf("read mock-data template: %v", err)
	}

	header := leadingCommentBlock(string(raw))
	if strings.TrimSpace(header) == "" {
		t.Fatal("the mock-data template has no leading comment block — this guard reads that header, " +
			"and with nothing to read every assertion below would pass vacuously")
	}

	// A project with migrations enabled — the shape that gets db/ at all.
	root := filepath.Join(t.TempDir(), "mockhdr")
	gen := generator.NewProjectGenerator("mockhdr", root, "example.com/mockhdr")
	if err := gen.Generate(); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	// The oracle must be real: if this scaffold grew no db/ tree at all,
	// every claim below would "pass" by having nothing to contradict it.
	if _, err := os.Stat(filepath.Join(root, "db")); err != nil {
		t.Fatalf("the generated project has no db/ directory (%v) — the comparison has no oracle", err)
	}
	if _, err := os.Stat(filepath.Join(root, "db", "seeds")); err != nil {
		t.Fatalf("the generated project has no db/seeds/ (%v) — the oracle cannot distinguish "+
			"a real directory from a fictional one", err)
	}

	claims := map[string]bool{}
	for _, m := range dbDirClaimRE.FindAllStringSubmatch(header, -1) {
		claims[m[1]] = true
	}
	if len(claims) == 0 {
		// Not a failure: the header may legitimately cite no db/ path. But
		// say so out loud, so a reader knows this run proved nothing about
		// directory claims rather than assuming it proved them all.
		t.Log("the mock-data header cites no db/<dir> path — nothing to verify")
		return
	}

	for dir := range claims {
		if _, err := os.Stat(filepath.Join(root, "db", dir)); err != nil {
			t.Errorf("the generated mock header cites db/%s, but `forge project new` creates no such "+
				"directory (%v).\nThe mocks are rendered from the project's own seedplan.Plan (see "+
				"codegen.SeedProjection); the seed model on disk is db/seeds/.\nA header naming a "+
				"directory that does not exist sends a reader asking \"where does this data come "+
				"from?\" to nothing.", dir, err)
		}
	}
}

// leadingCommentBlock returns the run of `//` comment lines the template
// opens with — the header a reader of the generated file sees first, and
// the only part of the template this guard makes claims about.
func leadingCommentBlock(src string) string {
	var b strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// A blank line inside the banner keeps the block going only if
			// the banner has already started; a leading blank is skipped.
			if b.Len() == 0 {
				continue
			}
			b.WriteString("\n")
			continue
		}
		if !strings.HasPrefix(trimmed, "//") {
			break
		}
		b.WriteString(trimmed)
		b.WriteString("\n")
	}
	return b.String()
}
