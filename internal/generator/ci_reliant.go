package generator

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// `ci.provider: reliant`: the project's CI as Reliant workflows on GitHub
// events instead of GitHub Actions. Each workflow runs on a Reliant machine
// (or, for the review, none) and posts one GitHub check run as the Reliant
// App through the engine's `check:` block, so the merge path needs no Actions
// minutes, runners or runner registration.
//
// The files are scaffold-once and the user's, like ci.yml. What they hold is
// deliberately thin: the gate itself is `forge ci gate --tier ... --json`, so
// a fix to what CI checks ships with a forge bump, not as an edit to a file
// the user owns.

// reliantWorkflowDir is where Reliant reads a project's workflows.
const reliantWorkflowDir = ".reliant/workflows/"

// The Reliant workflows forge scaffolds for ci.provider reliant.
const (
	// ReliantCIWorkflowPath runs `forge ci gate` on pull requests, merge
	// groups and pushes to the default branch, as the check <project>/ci.
	ReliantCIWorkflowPath = reliantWorkflowDir + "ci.yaml"
	// ReliantReviewWorkflowPath is the opt-in agent code review, as the check
	// <project>/review.
	ReliantReviewWorkflowPath = reliantWorkflowDir + "review.yaml"
)

// ReliantCheckName is the GitHub check run a ci.provider reliant workflow
// posts: "<project>/<workflow>", the name a branch ruleset requires.
func ReliantCheckName(cfg *config.ProjectConfig, workflow string) string {
	return cfg.Name + "/" + workflow
}

// reliantCIWorkflows is the ci.provider reliant half of CIWorkflowsFor.
// Unlike the GitHub set it does not vary with the project's shape: the gate
// decides what runs (`forge ci gate` plans its checks from the same
// forge.yaml), so the workflow only says when and where.
func reliantCIWorkflows(cfg *config.ProjectConfig) []CIWorkflowFile {
	var out []CIWorkflowFile
	for _, dest := range []string{ReliantCIWorkflowPath, ReliantReviewWorkflowPath} {
		name := CIWorkflowName(dest)
		out = append(out, CIWorkflowFile{
			Template: name + ".yaml.tmpl",
			Dest:     dest,
			Data:     templates.ReliantCIWorkflowData{Check: ReliantCheckName(cfg, name)},
		})
	}
	return out
}

// isReliantCIPath reports whether relPath is a workflow ci.provider reliant
// scaffolds.
func isReliantCIPath(relPath string) bool {
	rel := filepath.ToSlash(relPath)
	return rel == ReliantCIWorkflowPath || rel == ReliantReviewWorkflowPath
}

// CIProviderOfPath is the ci.provider whose CI files include relPath:
// "reliant" for the .reliant/workflows/ files forge scaffolds, "github" for
// any other CI-mapper path, "" for a path the mapper does not own.
func CIProviderOfPath(relPath string) string {
	switch {
	case isReliantCIPath(relPath):
		return config.CIProviderReliant
	case IsCIMapperPath(relPath):
		return config.CIProviderGitHub
	}
	return ""
}

// ReliantCIGuidance is what to do after forge writes the ci.provider reliant
// workflows: activate them, require the check, and retire the GitHub Actions
// workflows the project already has (which forge leaves alone: they are the
// user's). written are the paths just written.
func ReliantCIGuidance(cfg *config.ProjectConfig, cli string, written []string) string {
	check := ReliantCheckName(cfg, "ci")
	var b strings.Builder
	fmt.Fprintf(&b, "\nℹ️  ci.provider: reliant — %s run as Reliant workflows on GitHub events, posting GitHub check runs as the Reliant App.\n",
		strings.Join(written, " and "))
	fmt.Fprintf(&b, "   1. Commit .reliant/workflows/ and push it to the default branch.\n")
	fmt.Fprintf(&b, "   2. Enable ci on this project's machine: Workflows → Library → ci → Enable (pick the repository),\n")
	fmt.Fprintf(&b, "      or `reliant trigger activate --workflow ci --machine <machine> --param repository=<owner/name>`.\n")
	fmt.Fprintf(&b, "      review is opt-in: enable it the same way, with No machine, if you want an agent review check.\n")
	fmt.Fprintf(&b, "   3. Once %s reports on a pull request, require it in the branch ruleset (Settings → Rules → Rulesets →\n", check)
	fmt.Fprintf(&b, "      Require status checks to pass → add %s, source: the Reliant App), then drop the GitHub Actions checks it replaces.\n", check)
	fmt.Fprintf(&b, "   4. .github/workflows/ is yours and was left as it is: delete ci.yml once %s gates merges.\n", check)
	fmt.Fprintf(&b, "   Details: `%s skill load ci/reliant`.\n", cli)
	return b.String()
}
