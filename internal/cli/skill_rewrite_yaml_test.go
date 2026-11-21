package cli

import (
	"strings"
	"testing"
)

// A skill loaded under `reliant forge` rewrites the commands a reader types,
// but not a ```yaml file: a CI workflow's runner has the standalone forge
// (npm i -g @reliantlabs/forge), never reliant.
func TestRewriteForgeCommandsLeavesYAMLFences(t *testing.T) {
	in := "Run `forge env up dev`.\n\n```yaml\n      - run: npm i -g @reliantlabs/forge\n      - run: forge env deploy prod --yes\n```\n\n```bash\nforge env status prod\n```\n"
	got := string(rewriteForgeCommands([]byte(in), "reliant forge"))
	for _, want := range []string{"Run `reliant forge env up dev`.", "      - run: forge env deploy prod --yes\n", "\nreliant forge env status prod\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("rewritten skill lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "run: reliant forge") {
		t.Errorf("a yaml fence was rewritten:\n%s", got)
	}
}

// The preview skill's GitHub Actions recipe, as `reliant forge skill load`
// prints it, runs the forge its runner installed.
func TestPreviewSkillCIRecipeRunsStandaloneForge(t *testing.T) {
	body, err := RenderSkillContentAt("", "deploy/preview", SkillAudienceAll, "reliant forge")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "run: reliant forge") || strings.Contains(string(body), "\n          reliant forge env preview create") {
		t.Errorf("deploy/preview's CI recipe names `reliant forge`, which the runner does not have")
	}
}
