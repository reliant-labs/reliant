package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// guideFor renders a stand-in for reliant.md.tmpl: the header, a guide
// naming cli, then the Project Notes placeholder.
func guideFor(cli, body string) string {
	return "<!-- forge:version=1 -->\n# demo\n\nThis project is driven by the `" + cli + "` CLI.\n\n" + body + "\n\n" +
		projectNotesHeading + "\n\n<!-- Add project-specific context, conventions, and open questions here. -->"
}

// fakeRenders is this forge's guide: "current" for the project as it is,
// "scaffold" for what `forge project new` writes. It records the CLI asked for.
func fakeRenders(gotCLI *string) AgentsGuideRenders {
	return func(cli string) ([]byte, []byte, error) {
		*gotCLI = cli
		return []byte(guideFor(cli, "the v2 guide")), []byte(guideFor(cli, "the v2 scaffold guide")), nil
	}
}

// userNotes is a Project Notes section a user wrote: the heading, odd
// whitespace, no trailing newline — kept byte for byte.
const userNotes = projectNotesHeading + "\n\n  - deploys go through `task ship`  \n\n## Our own section\n\ntrailing"

func writeGuide(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, AgentsGuidePath), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readGuide(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, AgentsGuidePath))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func forgePart(t *testing.T, content string) []byte {
	t.Helper()
	f, _, ok := SplitAgentsGuide([]byte(content))
	if !ok {
		t.Fatalf("not a splittable guide:\n%s", content)
	}
	return f
}

// A guide forge wrote (its hash is in the ledger) that an older forge
// rendered is replaced by this forge's, and the user's notes survive exactly.
func TestRefreshAgentsGuide_StaleRecordedGuideIsRefreshedNotesKept(t *testing.T) {
	root := t.TempDir()
	stale := guideFor("forge", "the v1 guide")
	writeGuide(t, root, string(forgePart(t, stale))+userNotes)
	checksums.RecordScaffoldForgePart(root, AgentsGuidePath, forgePart(t, stale))

	var cli string
	res, err := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli))
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != GuideRefreshed {
		t.Fatalf("outcome = %v (%s), want refreshed", res.Outcome, res.Reason)
	}
	want := string(forgePart(t, guideFor("forge", "the v2 guide"))) + userNotes
	if got := readGuide(t, root); got != want {
		t.Fatalf("AGENTS.md:\n%q\nwant:\n%q", got, want)
	}
	// The record follows the new guide, so the NEXT forge may replace it.
	if !checksums.ScaffoldForgePartIsPristine(root, AgentsGuidePath, forgePart(t, want)) {
		t.Error("the refreshed guide's hash was not recorded")
	}
	// And it converges: a second run changes nothing.
	if res, _ := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli)); res.Outcome != GuideLeftAlone {
		t.Errorf("second run: outcome = %v, want left alone", res.Outcome)
	}
}

// A project this forge scaffolded has no record yet, but its guide is
// byte-for-byte this forge's scaffold render: that is proof enough.
func TestRefreshAgentsGuide_ThisForgesScaffoldIsRefreshed(t *testing.T) {
	root := t.TempDir()
	writeGuide(t, root, string(forgePart(t, guideFor("forge", "the v2 scaffold guide")))+userNotes)
	var cli string
	res, err := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli))
	if err != nil || res.Outcome != GuideRefreshed {
		t.Fatalf("outcome = %v, err = %v, want refreshed", res.Outcome, err)
	}
	if got, want := readGuide(t, root), string(forgePart(t, guideFor("forge", "the v2 guide")))+userNotes; got != want {
		t.Fatalf("AGENTS.md:\n%q\nwant:\n%q", got, want)
	}
}

// The guide keeps the CLI it names, whichever binary runs generate.
func TestRefreshAgentsGuide_KeepsTheCLIItNames(t *testing.T) {
	root := t.TempDir()
	stale := guideFor("reliant forge", "the v1 guide")
	writeGuide(t, root, stale)
	checksums.RecordScaffoldForgePart(root, AgentsGuidePath, forgePart(t, stale))
	var cli string
	if _, err := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli)); err != nil {
		t.Fatal(err)
	}
	if cli != "reliant forge" {
		t.Errorf("rendered with CLI %q, want the file's own %q", cli, "reliant forge")
	}
}

// Every guide forge cannot prove is its own text is left byte-for-byte
// alone and reported: edited after forge recorded it, written by an older
// forge that kept no record, or missing the Project Notes heading.
func TestRefreshAgentsGuide_UnprovableGuideIsUntouchedAndReported(t *testing.T) {
	recorded := guideFor("forge", "the v1 guide")
	cases := map[string]struct {
		content    string
		record     bool
		wantReason string
	}{
		"edited above the notes":   {content: string(forgePart(t, recorded)) + "my own rule\n\n" + userNotes, record: true, wantReason: "edited"},
		"no record (older forge)":  {content: string(forgePart(t, recorded)) + userNotes, wantReason: "no record"},
		"no Project Notes heading": {content: "<!-- forge:version=1 -->\n# demo\n\nall mine, no heading\n", record: true, wantReason: "no `## Project Notes`"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeGuide(t, root, tc.content)
			if tc.record {
				checksums.RecordScaffoldForgePart(root, AgentsGuidePath, forgePart(t, recorded))
			}
			var cli string
			res, err := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli))
			if err != nil {
				t.Fatal(err)
			}
			if res.Outcome != GuideDrifted || !strings.Contains(res.Reason, tc.wantReason) {
				t.Errorf("outcome = %v, reason %q; want drift reported, the reason naming %q", res.Outcome, res.Reason, tc.wantReason)
			}
			if got := readGuide(t, root); got != tc.content {
				t.Errorf("AGENTS.md was rewritten:\n%q", got)
			}
		})
	}
}

// A disowned guide, an AGENTS.md that is not forge's (no header) and an
// absent one are left alone, silently; generate never creates the file.
func TestRefreshAgentsGuide_DisownedForeignAndAbsentAreLeftAlone(t *testing.T) {
	stale := guideFor("forge", "the v1 guide")
	t.Run("disowned", func(t *testing.T) {
		root := t.TempDir()
		writeGuide(t, root, stale)
		checksums.RecordScaffoldForgePart(root, AgentsGuidePath, forgePart(t, stale))
		var cli string
		res, err := RefreshAgentsGuide(root, "forge", true, fakeRenders(&cli))
		if err != nil || res.Outcome != GuideLeftAlone {
			t.Fatalf("outcome = %v, err = %v, want left alone", res.Outcome, err)
		}
		if got := readGuide(t, root); got != stale {
			t.Errorf("disowned AGENTS.md was rewritten:\n%q", got)
		}
	})
	t.Run("not forge's", func(t *testing.T) {
		root := t.TempDir()
		own := "# Our agent guide\n\n" + projectNotesHeading + "\n"
		writeGuide(t, root, own)
		var cli string
		res, err := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli))
		if err != nil || res.Outcome != GuideLeftAlone {
			t.Fatalf("outcome = %v, err = %v, want left alone", res.Outcome, err)
		}
		if got := readGuide(t, root); got != own {
			t.Errorf("the project's own AGENTS.md was rewritten:\n%q", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		root := t.TempDir()
		var cli string
		if res, err := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli)); err != nil || res.Outcome != GuideLeftAlone {
			t.Fatalf("outcome = %v, err = %v, want left alone", res.Outcome, err)
		}
		if _, err := os.Stat(filepath.Join(root, AgentsGuidePath)); !os.IsNotExist(err) {
			t.Error("generate created AGENTS.md")
		}
	})
}

// A guide that already is this forge's, with no record, is adopted: the
// file is untouched, and its hash is recorded so the next forge's guide can
// replace it.
func TestRefreshAgentsGuide_CurrentUnrecordedGuideIsAdopted(t *testing.T) {
	root := t.TempDir()
	current := string(forgePart(t, guideFor("forge", "the v2 guide"))) + userNotes
	writeGuide(t, root, current)
	var cli string
	res, err := RefreshAgentsGuide(root, "forge", false, fakeRenders(&cli))
	if err != nil || res.Outcome != GuideLeftAlone {
		t.Fatalf("outcome = %v, err = %v, want left alone", res.Outcome, err)
	}
	if got := readGuide(t, root); got != current {
		t.Errorf("AGENTS.md was rewritten:\n%q", got)
	}
	if !checksums.ScaffoldForgePartIsPristine(root, AgentsGuidePath, forgePart(t, current)) {
		t.Error("the current guide was not adopted into the ledger")
	}
}
