package installers

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The reliant-forge plugin (plugins/reliant-forge) is one directory that three
// harnesses install: Claude Code reads .claude-plugin/plugin.json, Codex
// .codex-plugin/plugin.json and Cursor .cursor-plugin/plugin.json. All three
// run the same .mcp.json and the same skills/. This repository is the
// marketplace each of them adds by its GitHub name (reliant-labs/forge), from
// .claude-plugin/marketplace.json, .agents/plugins/marketplace.json and
// .cursor-plugin/marketplace.json. scripts/release-forge.sh moves the three
// manifests' versions with the npm package's.
const pluginDir = "plugins/reliant-forge"

var pluginManifests = []string{
	pluginDir + "/.claude-plugin/plugin.json",
	pluginDir + "/.codex-plugin/plugin.json",
	pluginDir + "/.cursor-plugin/plugin.json",
}

type pluginMarketplace struct {
	Name    string `json:"name"`
	Plugins []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Source      json.RawMessage `json:"source"`
	} `json:"plugins"`
}

var (
	// exactPin is the npm package at an exact final version.
	exactPin = regexp.MustCompile(`@reliantlabs/forge@[0-9]+\.[0-9]+\.[0-9]+\b`)
	// anyLaunch is any npx launch of the package, pinned or not.
	anyLaunch = regexp.MustCompile("npx -y @reliantlabs/forge[^\\s`\"']*")
)

func readMarketplace(t *testing.T, rel string) pluginMarketplace {
	t.Helper()
	var m pluginMarketplace
	readJSON(t, rel, &m)
	return m
}

func readJSON(t *testing.T, rel string, v any) {
	t.Helper()
	data, err := os.ReadFile(repoFile(t, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("%s: %v", rel, err)
	}
}

func TestReliantForgePlugin(t *testing.T) {
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	readJSON(t, "npm/package.json", &pkg)

	// The plugin's name is its install id (reliant-forge@reliant-labs) and the
	// prefix of its skills (/reliant-forge:deploy). A published name never
	// changes: renaming it uninstalls it for everyone who has it.
	for _, rel := range pluginManifests {
		var m struct {
			Name        string            `json:"name"`
			Version     string            `json:"version"`
			License     string            `json:"license"`
			Skills      string            `json:"skills"`
			MCPServers  string            `json:"mcpServers"`
			Logo        string            `json:"logo"`
			Icon        string            `json:"icon"`
			Interface   map[string]any    `json:"interface"`
			Author      map[string]string `json:"author"`
			Description string            `json:"description"`
		}
		readJSON(t, rel, &m)
		if m.Name != "reliant-forge" {
			t.Errorf("%s name = %q, want reliant-forge", rel, m.Name)
		}
		if m.Version != pkg.Version {
			t.Errorf("%s version %s, npm/package.json %s: the release moves them together", rel, m.Version, pkg.Version)
		}
		if m.License != "Apache-2.0" || m.Author["name"] != "Reliant Labs" {
			t.Errorf("%s license %q, author %q: want Apache-2.0 by Reliant Labs", rel, m.License, m.Author["name"])
		}
		// Codex and Cursor take the components by path; Claude Code finds
		// .mcp.json and skills/ at the plugin root by convention.
		if !strings.Contains(rel, ".claude-plugin") && (m.Skills != "./skills/" || m.MCPServers != "./.mcp.json") {
			t.Errorf("%s skills %q, mcpServers %q: want ./skills/ and ./.mcp.json, the files every harness shares", rel, m.Skills, m.MCPServers)
		}
		for _, img := range []string{m.Logo, m.Icon} {
			if img != "" {
				repoFile(t, filepath.Join(pluginDir, img))
			}
		}
		if strings.Contains(rel, ".codex-plugin") {
			for _, k := range []string{"displayName", "shortDescription", "longDescription", "developerName", "logo", "websiteURL", "privacyPolicyURL", "termsOfServiceURL"} {
				if s, _ := m.Interface[k].(string); s == "" {
					t.Errorf("%s interface.%s is empty: the Codex plugin directory shows it", rel, k)
				}
			}
			if s, _ := m.Interface["logo"].(string); s != "" {
				repoFile(t, filepath.Join(pluginDir, s))
			}
		}
	}

	// One server, started the way every other listing starts it, but at an
	// exact version: Anthropic's directory refuses an unpinned launcher. The
	// release moves the pin to each final version (npm gets no prerelease).
	pin := pkg.Version
	if strings.Contains(pin, "-") {
		pin = "" // a prerelease's bookkeeping leaves the last final pin
	}
	var mcpConfig struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	readJSON(t, pluginDir+"/.mcp.json", &mcpConfig)
	forge, ok := mcpConfig.MCPServers["forge"]
	args := strings.Join(forge.Args, " ")
	if len(mcpConfig.MCPServers) != 1 || !ok || forge.Command != "npx" ||
		!exactPin.MatchString(args) || pin != "" && args != "-y "+pkg.Name+"@"+pin+" mcp" {
		t.Errorf("%s/.mcp.json = %+v, want the one server forge: npx -y %s@%s mcp", pluginDir, mcpConfig.MCPServers, pkg.Name, pkg.Version)
	}
	// Every command the plugin's files name runs that same pinned package.
	pinned := exactPin.FindString(args)
	err := filepath.WalkDir(filepath.Dir(repoFile(t, pluginDir+"/.mcp.json")), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) == ".png" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, use := range anyLaunch.FindAllString(string(data), -1) {
			if use != "npx -y "+pinned {
				t.Errorf("%s runs %q; every launch in the plugin is %q, which the release moves", path, use, "npx -y "+pinned)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	skill, err := os.ReadFile(repoFile(t, pluginDir+"/skills/deploy/SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if front, _, ok := strings.Cut(string(skill), "\n---\n"); !ok || !strings.HasPrefix(front, "---\nname: deploy\ndescription: ") {
		t.Errorf("skills/deploy/SKILL.md must open with front matter naming deploy and describing it:\n%s", front)
	}
	for _, want := range []string{"deploy_claimable", "env deploy prod --claimable", "claim link", "72 hours", "obs check prod --wait --json"} {
		if !strings.Contains(string(skill), want) {
			t.Errorf("skills/deploy/SKILL.md does not mention %q", want)
		}
	}

	// Each marketplace lists the plugin from this repository, under the one
	// marketplace name an install id ends with.
	for rel, wantSource := range map[string]string{
		".claude-plugin/marketplace.json":  `"./plugins/reliant-forge"`,
		".cursor-plugin/marketplace.json":  `"plugins/reliant-forge"`,
		".agents/plugins/marketplace.json": `{"path":"./plugins/reliant-forge","source":"local"}`,
	} {
		m := readMarketplace(t, rel)
		if m.Name != "reliant-labs" || len(m.Plugins) != 1 || m.Plugins[0].Name != "reliant-forge" {
			t.Errorf("%s: marketplace %q with %d plugins, want reliant-labs listing reliant-forge alone", rel, m.Name, len(m.Plugins))
			continue
		}
		var got any
		if err := json.Unmarshal(m.Plugins[0].Source, &got); err != nil {
			t.Fatalf("%s source: %v", rel, err)
		}
		norm, _ := json.Marshal(got)
		if string(norm) != wantSource {
			t.Errorf("%s source = %s, want %s", rel, norm, wantSource)
		}
	}
}

// Execute only the release script's bookkeeping blocks, as
// TestGeminiExtensionReleaseVersionSync does: each plugin manifest is staged
// in the bookkeeping commit and its top-level version moves to the release's,
// and nothing else in it changes.
func TestPluginManifestsReleaseVersionSync(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}
	body, err := os.ReadFile(repoFile(t, "scripts/release-forge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	block := func(start, end string) string {
		t.Helper()
		_, rest, ok := strings.Cut(string(body), start)
		if !ok {
			t.Fatalf("release script missing %q", start)
		}
		selected, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatalf("release script missing %q", end)
		}
		return start + selected
	}
	setup := block("BACKUP_DIR=", "# The web runtime's bookkeeping")
	sync := block("# The npm shim's version IS", "# ── 7b. CHANGELOG.md")

	const manifest = "{\n  \"name\": \"reliant-forge\",\n  \"version\": \"1.3.0\",\n  \"author\": {\n    \"version\": \"keep\"\n  }\n}\n"
	const skill = "Run `npx -y @reliantlabs/forge@1.3.0 env deploy prod --claimable`.\nThe server: @reliantlabs/forge@1.3.0 mcp. Not the web runtime: @reliantlabs/forge-web-runtime@1.3.0.\n"
	pinnedFiles := []string{pluginDir + "/.mcp.json", pluginDir + "/skills/deploy/SKILL.md"}
	for _, tc := range []struct {
		name, version, skill    string
		wantManifest, wantSkill string
		wantFailure             bool
	}{
		{"final", "v1.6.0", skill, "1.6.0", strings.ReplaceAll(skill, "forge@1.3.0", "forge@1.6.0"), false},
		{"prerelease keeps the pin", "v1.6.0-rc.1", skill, "1.6.0-rc.1", skill, false},
		{"unpinned", "v1.6.0", strings.Replace(skill, "forge@1.3.0 env", "forge@latest env", 1), "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			check := ""
			write := func(rel, body string) {
				t.Helper()
				path := filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				check += `[[ " ${TOUCHED[*]} " == *" ` + rel + ` "* ]]` + "\n"
			}
			for _, rel := range pluginManifests {
				write(rel, manifest)
			}
			for _, rel := range pinnedFiles {
				write(rel, tc.skill)
			}
			cmd := exec.Command("bash", "-c", "set -euo pipefail\nVERSION_FILES=()\nNEXT_FILE=NEXT_VERSION\n"+setup+`
trap 'rm -rf "$BACKUP_DIR"' EXIT
`+check+sync)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "VERSION="+tc.version)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantFailure {
				t.Fatalf("failure=%v, want %v:\n%s", err, tc.wantFailure, out)
			}
			if tc.wantFailure {
				return
			}
			for rel, want := range map[string]string{
				pluginManifests[0]: strings.Replace(manifest, "1.3.0", tc.wantManifest, 1),
				pluginManifests[1]: strings.Replace(manifest, "1.3.0", tc.wantManifest, 1),
				pluginManifests[2]: strings.Replace(manifest, "1.3.0", tc.wantManifest, 1),
				pinnedFiles[0]:     tc.wantSkill,
				pinnedFiles[1]:     tc.wantSkill,
			} {
				got, err := os.ReadFile(filepath.Join(root, rel))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != want {
					t.Errorf("%s = %s, want %s", rel, got, want)
				}
			}
		})
	}
}
