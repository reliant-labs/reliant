package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/vulnpatch"
	"github.com/reliant-labs/forge/internal/vulnscan"
)

func covBFinding(id string, fixed bool, exempt bool, aliases ...string) vulnscan.DocumentFinding {
	f := vulnscan.DocumentFinding{Finding: vulnscan.Finding{ID: id, Aliases: aliases, Package: vulnscan.Package{Type: "go-module", Name: "example.com/" + id, Version: "1.0.0"}}}
	if fixed {
		f.FixState = vulnscan.FixStateFixed
		f.FixedIn = []string{"1.1.0"}
	}
	if exempt {
		f.Exemption = &vulnscan.Exemption{}
	}
	return f
}

func TestSelectPatchFindings(t *testing.T) {
	in := patchFindings{Findings: []vulnscan.DocumentFinding{
		covBFinding("CVE-1", true, false, "GHSA-aaa"),
		covBFinding("CVE-2", false, false),
		covBFinding("CVE-3", true, true),
	}}

	// No ids: every fixable, non-exempt finding; the left-out ones are counted.
	got, missing, notes := selectPatchFindings(in, nil)
	if len(got) != 1 || got[0].ID != "CVE-1" || missing != nil {
		t.Fatalf("default selection = %+v missing %v", got, missing)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "1 finding(s) with no fixed version left out") || !strings.Contains(joined, "1 exempt finding(s) left out") {
		t.Errorf("notes = %q", joined)
	}

	// Named ids match ids and aliases case-insensitively, name exempt and
	// unfixable findings anyway, dedupe, and report what matched nothing.
	got, missing, _ = selectPatchFindings(in, []string{"ghsa-AAA", "CVE-1", "CVE-3", "CVE-9"})
	if len(got) != 2 || got[0].ID != "CVE-1" || got[1].ID != "CVE-3" {
		t.Errorf("named selection = %+v", got)
	}
	if len(missing) != 1 || missing[0] != "CVE-9" {
		t.Errorf("missing = %v, want [CVE-9]", missing)
	}
}

func TestIsPatchDirAndSubject(t *testing.T) {
	for arg, want := range map[string]bool{".": true, "..": true, "./svc": true, "a/b": true, `a\b`: true, "prod": false, "staging": false} {
		if got := isPatchDir(arg); got != want {
			t.Errorf("isPatchDir(%q) = %v, want %v", arg, got, want)
		}
	}
	for _, tc := range []struct{ env, rel, dir, want string }{
		{"prod", "v1", "x", "prod release v1"},
		{"prod", "", "x", "prod"},
		{"", "", "./svc", "the scan of ./svc"},
	} {
		if got := subjectOf(tc.env, tc.rel, tc.dir); got != tc.want {
			t.Errorf("subjectOf(%q,%q,%q) = %q, want %q", tc.env, tc.rel, tc.dir, got, tc.want)
		}
	}
	var opt optionalString
	if err := opt.Set("go test ./..."); err != nil || opt.String() != "go test ./..." || opt.Type() != "" {
		t.Errorf("optionalString = %q, %v", opt.String(), err)
	}
}

func TestPatchExitMapsTheDocumentToTheCommandsExit(t *testing.T) {
	counts := "1 fixed, 2 bumped, 3 still present, 4 failed, 5 skipped"
	sum := vulnpatch.Summary{Fixed: 1, Bumped: 2, StillPresent: 3, Failed: 4, Skipped: 5}
	for _, tc := range []struct {
		name     string
		doc      vulnpatch.Document
		wantCode int
		wantMsg  string
	}{
		{"ok", vulnpatch.Document{ExitCode: vulnpatch.ExitOK, Summary: sum}, 0, ""},
		{"a bump failed", vulnpatch.Document{ExitCode: vulnpatch.ExitVerify, Summary: vulnpatch.Summary{Failed: 1}}, exitWrong, "patched, but a bump failed: 0 fixed, 0 bumped, 0 still present, 1 failed, 0 skipped"},
		{"verify command failed", vulnpatch.Document{ExitCode: vulnpatch.ExitVerify, Summary: sum, Verify: &vulnpatch.VerifyResult{Command: "go test ./..."}}, exitWrong, "patched, but verification failed (go test ./...): " + counts},
		{"rescan could not run", vulnpatch.Document{ExitCode: vulnpatch.ExitVerify, Summary: sum, Verify: &vulnpatch.VerifyResult{Passed: true, RescanError: "no db"}}, exitWrong, "patched, but the rescan could not confirm the fix: " + counts},
		{"still present", vulnpatch.Document{ExitCode: vulnpatch.ExitVerify, Summary: sum, Verify: &vulnpatch.VerifyResult{Passed: true}}, exitWrong, "patched, but the rescan still reports a finding: " + counts},
		{"nothing to do", vulnpatch.Document{ExitCode: vulnpatch.ExitNothingToDo, Summary: sum}, exitUndetermined, "nothing deterministic to do for some findings: " + counts},
		{"refused", vulnpatch.Document{ExitCode: vulnpatch.ExitRefused, Refusals: []string{"dirty tree", "major bump"}}, exitRefused, "refused, nothing changed: dirty tree; major bump"},
		{"unknown code passes through", vulnpatch.Document{ExitCode: 9, Summary: sum}, 9, counts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := patchExit(tc.doc)
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			var exit *exitCodeError
			if !errors.As(err, &exit) || exit.code != tc.wantCode || exit.msg != tc.wantMsg {
				t.Fatalf("err = %v (%+v); want exit %d %q", err, exit, tc.wantCode, tc.wantMsg)
			}
		})
	}
}

func TestWritePatchDocumentRendersEveryPart(t *testing.T) {
	doc := vulnpatch.Document{
		Env: "prod", Release: "v1.2.0", Changed: true,
		Findings: []vulnpatch.Result{
			{ID: "CVE-1", Status: vulnpatch.StatusFixed, Package: vulnscan.Package{Name: "left-pad"}, Installed: "1.0.0", Target: "1.1.0", Manifests: []string{"package.json", "package-lock.json"}},
			{ID: "CVE-2", Status: vulnpatch.StatusSkipped, Package: vulnscan.Package{Name: "zlib"}, Installed: "1.2", Reason: "no deterministic fix"},
		},
		Bumps:    []vulnpatch.Bump{{Manifest: "go.mod", Error: "go get failed"}, {Manifest: "ok.mod"}},
		Verify:   &vulnpatch.VerifyResult{Command: "task test", Source: "taskfile", Passed: false, Output: "FAIL pkg", RescanError: "db missing"},
		PR:       &vulnpatch.PRResult{URL: "https://example.test/pr/1"},
		Notes:    []string{"database is old"},
		Refusals: []string{"a refusal"},
	}
	var b bytes.Buffer
	writePatchDocument(&b, doc)
	out := b.String()
	for _, want := range []string{
		"prod · release v1.2.0 · 2 finding(s)",
		"FIXED", "left-pad 1.0.0 → 1.1.0", "package.json, package-lock.json",
		"SKIPPED", "no deterministic fix",
		"failed in go.mod:\ngo get failed",
		"verify (taskfile): task test FAILED\nFAIL pkg",
		"rescan failed: db missing",
		"pull request: https://example.test/pr/1",
		"note: database is old",
		"refused: a refusal",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("document text missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "failed in ok.mod") || strings.Contains(out, "the tree was not changed") {
		t.Errorf("a clean bump has no failure block and a changed tree says nothing:\n%s", out)
	}

	b.Reset()
	writePatchDocument(&b, vulnpatch.Document{Dir: "./svc", Verify: &vulnpatch.VerifyResult{Command: "t", Source: "flag", Passed: true}})
	if out := b.String(); !strings.Contains(out, "./svc · 0 finding(s)") || !strings.Contains(out, "verify (flag): t passed") || !strings.Contains(out, "the tree was not changed") {
		t.Errorf("directory scan with passing verify and no change:\n%s", out)
	}
}

func TestVerifyCommandPrecedenceAndProjectName(t *testing.T) {
	dir := t.TempDir()
	if cmd, src := verifyCommand("make check", dir, dir); cmd != "make check" || src != vulnpatch.VerifySourceFlag {
		t.Errorf("an explicit command wins: %q (%s)", cmd, src)
	}
	mustWriteFile(t, filepath.Join(dir, "forge.yaml"), "name: shop\nvuln:\n  patch:\n    verify: ' task verify '\n")
	if cmd, src := verifyCommand(verifyGate, dir, dir); cmd != "task verify" || src != vulnpatch.VerifySourceConfig {
		t.Errorf("forge.yaml's vuln.patch.verify is next: %q (%s)", cmd, src)
	}
	if got := patchProjectName(dir); got != "shop" {
		t.Errorf("patchProjectName = %q", got)
	}
	if got := patchProjectName(t.TempDir()); got != "" {
		t.Errorf("no forge.yaml -> empty name, got %q", got)
	}
}

func TestEcosystemListNamesEveryPatcher(t *testing.T) {
	list := ecosystemList()
	for _, e := range vulnpatch.Ecosystems() {
		if !strings.Contains(list, string(e)) {
			t.Errorf("ecosystemList %q misses %s", list, e)
		}
	}
}
