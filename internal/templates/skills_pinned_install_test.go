// skills_pinned_install_test.go — no shipped skill tells an agent to pipe a
// download into a shell, or to install forge at whatever version is newest.
//
// Skills are copied verbatim into CI files by the agents that load them, and
// skill directories grade them: skillsdirectory.com scored the adopt/deploy
// skill "Security: F" for one `curl … install.sh | sh` line. A pipeline that
// runs a script fetched from a moving branch runs whatever that branch holds
// on the day; one that installs an unpinned package changes under its owner
// when forge releases. So every install a skill shows names an exact release,
// from a channel that verifies it: `npm install -g @reliantlabs/forge@<x.y.z>`
// (the package checks the binary against the release's signed checksums),
// forge.yaml's forge_version, or Homebrew.
//
// install.sh itself stays (README and docs/install.md document it for a
// person at a terminal, and removing it would break those who use it); this
// guards only the guidance an agent pastes.
package templates_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

var (
	// pipeToShell is a download piped straight into a shell (curl/wget into
	// sh, bash, …, with or without sudo or VAR=value assignments before the
	// shell; irm into iex). It is matched over text whose line breaks
	// are folded into spaces, so prose that wraps the command still counts,
	// and the span is bounded so two unrelated lines cannot pair up.
	pipeToShell = regexp.MustCompile("\\b(?:curl|wget)\\b[^|`]{0,240}\\|\\s*(?:sudo\\s+)?(?:[A-Za-z_][A-Za-z0-9_]*=\\S*\\s+)*(?:sh|bash|zsh|dash)\\b" +
		"|\\birm\\b[^|`]{0,240}\\|\\s*iex\\b")
	// unpinnedGlobalInstall is a global npm install of forge with no version.
	unpinnedGlobalInstall = regexp.MustCompile(`npm\s+(?:i|install)\s+(?:-g|--global)\s+["']?@reliantlabs/forge(?:[^@\w/-]|$)`)
	foldLines             = regexp.MustCompile(`\s*\n\s*`)
)

// installGuidanceSkills returns every skill body forge ships: the ones embedded in
// the binary (`forge skill load`) and the agent plugin's.
func installGuidanceSkills(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	names, err := templates.ProjectTemplates().List("")
	if err != nil {
		t.Fatalf("list project templates: %v", err)
	}
	for _, name := range names {
		if !strings.HasPrefix(name, "skills/") || !strings.HasSuffix(name, ".md") {
			continue
		}
		body, err := templates.ProjectTemplates().Get(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out["internal/templates/project/"+name] = string(body)
	}
	repoRoot := filepath.Join("..", "..")
	pluginSkills := filepath.Join(repoRoot, "plugins", "reliant-forge")
	err = filepath.WalkDir(pluginSkills, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return walkErr
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(repoRoot, path)
		out[filepath.ToSlash(rel)] = string(body)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", pluginSkills, err)
	}
	return out
}

// installFindings returns what a skill body shows that this file forbids.
func installFindings(body string) []string {
	var out []string
	for _, m := range pipeToShell.FindAllString(foldLines.ReplaceAllString(body, " "), -1) {
		out = append(out, "pipes a download into a shell: "+m)
	}
	for _, m := range unpinnedGlobalInstall.FindAllString(body, -1) {
		out = append(out, "installs forge at no particular version: "+strings.TrimSpace(m))
	}
	return out
}

func TestShippedSkillsInstallPinnedReleases(t *testing.T) {
	t.Parallel()
	skills := installGuidanceSkills(t)
	if len(skills) < 50 {
		t.Fatalf("found only %d skills; the walk is looking in the wrong place", len(skills))
	}
	for where, body := range skills {
		for _, f := range installFindings(body) {
			t.Errorf("%s %s\n\tinstall an exact release instead: npm install -g \"@reliantlabs/forge@<x.y.z>\" "+
				"(or the version forge.yaml's forge_version pins); install.sh stays for people at a terminal (docs/install.md)", where, f)
		}
	}
}

// The workflows forge scaffolds are copied into a project as they are, and run
// on every push: they install third-party tools the same way, from a pinned
// release verified against its checksum (k3d: templates.K3dVersion), never a
// script piped from a moving branch.
func TestScaffoldedWorkflowsInstallPinnedReleases(t *testing.T) {
	t.Parallel()
	ci := templates.CITemplates("github")
	names, err := ci.List("")
	if err != nil {
		t.Fatalf("list CI templates: %v", err)
	}
	if len(names) < 5 {
		t.Fatalf("found only %d CI templates; the walk is looking in the wrong place", len(names))
	}
	for _, name := range names {
		body, err := ci.Get(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, f := range installFindings(string(body)) {
			t.Errorf("internal/templates/ci/github/%s %s\n\tinstall the tool's pinned release and verify its checksum (see installK3dRun)", name, f)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		hint := templates.K3dLinuxInstallHint(arch)
		if hint == "" || !strings.Contains(hint, "sha256sum -c") || !strings.Contains(hint, "/"+templates.K3dVersion+"/") {
			t.Errorf("forge doctor's k3d hint for linux/%s is not the pinned, verified release: %q", arch, hint)
		}
		if f := installFindings(hint); len(f) != 0 {
			t.Errorf("forge doctor's k3d hint for linux/%s: %v", arch, f)
		}
	}
}

// TestInstallFindingsCatchWhatTheyClaim keeps the patterns honest: each must
// catch its known-bad shapes and pass the pinned forms the skills now use.
func TestInstallFindingsCatchWhatTheyClaim(t *testing.T) {
	t.Parallel()
	bad := []string{
		"curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh",
		"one job that installs forge (`curl -fsSL\n  https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh`), runs",
		"curl -s https://example.com/install.sh | sudo bash",
		"run: curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash",
		"curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh \\\n  | TAG=\"$K3D_VERSION\" bash",
		"wget -qO- https://example.com/i.sh | bash",
		"irm https://raw.githubusercontent.com/reliant-labs/forge/main/install.ps1 | iex",
		"      - run: npm i -g @reliantlabs/forge\n",
		"npm install --global @reliantlabs/forge",
	}
	good := []string{
		`npm install -g "@reliantlabs/forge@${FORGE_VERSION}"`,
		"npm i -g @reliantlabs/forge@1.10.0",
		"npx -y @reliantlabs/forge@1.10.0 mcp",
		"npm install -g @reliantlabs/forge-web-runtime",
		`curl -sS -b "$JAR" -H 'Content-Type: application/json' -d '{}' "$API/x" | jq .`,
		"curl a real endpoint in dev | and on the staging URL",
		templates.K3dLinuxInstallHint("amd64"),
	}
	for _, s := range bad {
		if len(installFindings(s)) == 0 {
			t.Errorf("not caught: %q", s)
		}
	}
	for _, s := range good {
		if f := installFindings(s); len(f) != 0 {
			t.Errorf("false positive on %q: %v", s, f)
		}
	}
}
