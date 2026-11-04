// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// recipeActions parses the post-deploy recipe's exit-code bullets out of the
// live capabilities guide, so the scenario below branches on what agents are
// actually told, not on a copy.
func recipeActions(t *testing.T) map[int]string {
	t.Helper()
	bullet := regexp.MustCompile(`(?m)^- \*\*(\d+):\*\*\s*(.*(?:\n  .*)*)`)
	out := map[int]string{}
	for _, m := range bullet.FindAllStringSubmatch(reliantCapabilities, -1) {
		code := 0
		for _, c := range m[1] {
			code = code*10 + int(c-'0')
		}
		out[code] = strings.Join(strings.Fields(m[2]), " ")
	}
	return out
}

// An agent session reads forge's document, takes exit_code from it, and acts on
// the recipe's bullet for that code. Canned documents have forge's real shape
// (schema forge.obs.check/v1).
func TestPostDeployRecipe_BranchesOnExitCode(t *testing.T) {
	actions := recipeActions(t)
	for code, want := range map[int]string{
		0: "done",
		1: "suggested[]",
		2: "not proven good",
		8: "Check again later",
	} {
		got, ok := actions[code]
		if !ok || !strings.Contains(got, want) {
			t.Errorf("exit %d: recipe bullet = %q, want it to say %q", code, got, want)
		}
	}
	if !strings.Contains(actions[2], `never "passed"`) {
		t.Error(`exit 2 must say it is never reported as "passed"`)
	}
	for _, doc := range []string{
		`{"schema":"forge.obs.check/v1","ok":true,"exit_code":0,"findings":[],"suggested":[]}`,
		`{"schema":"forge.obs.check/v1","ok":false,"exit_code":1,"findings":[{"code":"new_issue"}],"suggested":["forge obs issue abc"]}`,
		`{"schema":"forge.obs.check/v1","ok":false,"exit_code":2,"findings":[],"suggested":[]}`,
		`{"schema":"forge.obs.check/v1","ok":false,"exit_code":8,"findings":[],"suggested":[]}`,
	} {
		var parsed struct {
			ExitCode int `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(doc), &parsed); err != nil {
			t.Fatal(err)
		}
		if actions[parsed.ExitCode] == "" {
			t.Errorf("document with exit_code %d has no recipe branch", parsed.ExitCode)
		}
	}
}

// With a real forge on PATH, the document an agent branches on carries the
// fields the recipe reads, even when forge cannot look (no environment here).
func TestPostDeployRecipe_RealForgeDocumentHasTheBranchedFields(t *testing.T) {
	if testing.Short() {
		t.Skip("execs forge; runs in the full lane")
	}
	forge, err := exec.LookPath("forge")
	if err != nil {
		t.Skip("no forge on PATH")
	}
	cmd := exec.Command(forge, "obs", "check", "dev", "--json")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "FORGE_CREDENTIAL_HELPER=")
	out, _ := cmd.Output() // a non-zero exit is the point; the document is on stdout
	var doc struct {
		Schema    string   `json:"schema"`
		ExitCode  *int     `json:"exit_code"`
		Suggested []string `json:"suggested"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("not a JSON document: %v\n%s", err, out)
	}
	if doc.Schema != "forge.obs.check/v1" || doc.ExitCode == nil {
		t.Fatalf("document = %+v, want schema forge.obs.check/v1 and an exit_code", doc)
	}
	if actions := recipeActions(t); actions[*doc.ExitCode] == "" {
		t.Errorf("exit_code %d has no recipe branch", *doc.ExitCode)
	}
}
