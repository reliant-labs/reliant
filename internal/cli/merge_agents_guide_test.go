package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/pkg/testkit"
)

func TestMergeAgentsGuideSelectsCertifiedPrefix(t *testing.T) {
	ours := []byte("our guide")
	theirs := []byte("their guide")
	for _, tc := range []struct {
		name, entry string
		want        []byte
	}{
		{"merged metadata certifies ours", fmt.Sprintf(`{"birth_hash":"other metadata","forge_part_hash":%q}`, checksums.Hash(ours)), ours},
		{"certifies theirs", fmt.Sprintf(`{"forge_part_hash":%q}`, checksums.Hash(theirs)), theirs},
		{"certifies neither", `{"forge_part_hash":"other"}`, nil},
		{"missing proof", `{}`, nil},
		{"unreadable proof", `{`, nil},
		{"declined", fmt.Sprintf(`{"declined":"owned here","forge_part_hash":%q}`, checksums.Hash(ours)), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := guideForMergedCertificate([]byte(tc.entry), ours, theirs)
			if ok != (tc.want != nil) || string(got) != string(tc.want) {
				t.Fatalf("guideForMergedCertificate() = (%q, %t), want %q", got, ok, tc.want)
			}
		})
	}
}

// These exercise the installed driver, including its commit/blob proof, rather
// than stubbing the evidence which decides whether a guide is safe to merge.
func TestMergeAgentsGuideRealGit(t *testing.T) {
	testkit.Slow(t, "runs real git merges with the test binary as merge driver")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	const heading = "## Project Notes\n"
	prefix := func(s string) string { return "<!-- forge:version=1 -->\n# Guide\n" + s + "\n\n" }
	basePrefix, oursPrefix, theirsPrefix := prefix("base"), prefix("ours"), prefix("theirs")
	baseNotes := heading + "first\n\n\n\n\nlast\n"
	oursNotes := heading + "ours first\n\n\n\n\nlast\n"
	theirsNotes := heading + "first\n\n\n\n\ntheirs last\n"

	for _, tc := range []struct {
		name                        string
		baseP, oursP, theirsP       string
		baseN, oursN, theirsN       string
		tamper, missingCert, disown string // base, ours, or theirs
		theirsConfig                string
		want                        string
		conflict                    bool
	}{
		{name: "parallel prefixes and independent notes", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: oursNotes, theirsN: theirsNotes, want: oursPrefix + heading + "ours first\n\n\n\n\ntheirs last\n"},
		{name: "only theirs changed prefix preserves notes bytes", baseP: basePrefix, oursP: basePrefix, theirsP: theirsPrefix, baseN: heading + "base\n", oursN: heading + "  no final newline \t", theirsN: heading + "base\n", want: theirsPrefix + heading + "  no final newline \t"},
		{name: "identical notes preserve CRLF bytes", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: heading + "old\n", oursN: "## Project Notes\r\nnotes\r\n\r\n", theirsN: "## Project Notes\r\nnotes\r\n\r\n", want: oursPrefix + "## Project Notes\r\nnotes\r\n\r\n"},
		{name: "overlapping notes conflict", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: heading + "base\n", oursN: heading + "ours\n", theirsN: heading + "theirs\n", conflict: true},
		{name: "tampered base", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, tamper: "base", conflict: true},
		{name: "tampered ours", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, tamper: "ours", conflict: true},
		{name: "tampered theirs", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, tamper: "theirs", conflict: true},
		{name: "missing certificate", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, missingCert: "theirs", conflict: true},
		{name: "base disowned", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, disown: "base", conflict: true},
		{name: "ours disowned", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, disown: "ours", conflict: true},
		{name: "theirs disowned", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, disown: "theirs", conflict: true},
		{name: "theirs excludes guide", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, theirsConfig: "scaffold:\n  exclude: [AGENTS.md]\n", conflict: true},
		{name: "theirs opts out of harness", baseP: basePrefix, oursP: oursPrefix, theirsP: theirsPrefix, baseN: baseNotes, oursN: baseNotes, theirsN: baseNotes, theirsConfig: "harness: none\n", conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitT(t, dir, "init", "-q", "-b", "main")
			writeTreeFileT(t, filepath.Join(dir, "forge.yaml"), "name: app\n")
			exe, err := filepath.Abs(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			gitT(t, dir, "config", "merge.forge-generated.driver", "'"+exe+"' merge-generated %O %A %B %L %P")
			if _, ok := writeMergeAttributes(dir); !ok {
				t.Fatal("install merge attributes")
			}
			writeSide := func(side, p, n string) {
				t.Helper()
				writeTreeFileT(t, filepath.Join(dir, "AGENTS.md"), p+n)
				if side == "theirs" && tc.theirsConfig != "" {
					writeTreeFileT(t, filepath.Join(dir, "forge.yaml"), "name: app\n"+tc.theirsConfig)
				}
				cert := filepath.Join(dir, ".forge/scaffolded/AGENTS.md.json")
				if side == tc.missingCert {
					if err := os.Remove(cert); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				} else {
					hash := checksums.Hash([]byte(p))
					if side == tc.tamper {
						hash = checksums.Hash([]byte("not the prefix"))
					}
					writeTreeFileT(t, cert, fmt.Sprintf("{\"forge_part_hash\":%q}\n", hash))
				}
				disown := filepath.Join(dir, ".forge/disowned.json")
				if side == tc.disown {
					writeTreeFileT(t, disown, `{"files":{"AGENTS.md":{"reason":"mine"}}}`+"\n")
				} else if err := os.Remove(disown); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				gitT(t, dir, "add", "-A")
				gitT(t, dir, "commit", "-qm", side)
			}
			writeSide("base", tc.baseP, tc.baseN)
			gitT(t, dir, "checkout", "-qb", "incoming")
			writeSide("theirs", tc.theirsP, tc.theirsN)
			gitT(t, dir, "checkout", "-q", "main")
			writeSide("ours", tc.oursP, tc.oursN)
			cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "merge", "--no-edit", "incoming")
			cmd.Dir = dir
			out, mergeErr := cmd.CombinedOutput()
			got, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.conflict {
				if mergeErr == nil || !strings.Contains(string(got), "<<<<<<<") || !strings.Contains(string(got), ">>>>>>>") {
					t.Fatalf("want ordinary conflict; error=%v output=%s guide=%q", mergeErr, out, got)
				}
				if tc.oursN != tc.theirsN && (!strings.Contains(string(got), "ours\n") || !strings.Contains(string(got), "theirs\n")) {
					t.Fatalf("notes conflict lost a side: %s", got)
				}
				return
			}
			if mergeErr != nil {
				t.Fatalf("merge: %v\n%s\nguide: %s", mergeErr, out, got)
			}
			if string(got) != tc.want {
				t.Fatalf("guide bytes differ\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestMergeAgentsGuideAttributesRootOnly(t *testing.T) {
	testkit.Slow(t, "invokes git to inspect installed attributes")
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	if _, ok := writeMergeAttributes(dir); !ok {
		t.Fatal("install merge attributes")
	}
	root := gitT(t, dir, "check-attr", "merge", "--", "AGENTS.md")
	nested := gitT(t, dir, "check-attr", "merge", "--", "nested/AGENTS.md")
	if !strings.Contains(root, "merge: forge-generated") {
		t.Fatalf("root guide not routed: %s", root)
	}
	if strings.Contains(nested, "merge: forge-generated") {
		t.Fatalf("nested guide incorrectly routed: %s", nested)
	}
}
