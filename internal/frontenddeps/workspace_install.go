package frontenddeps

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkspaceInstallDir locates a missing dependency tree's install directory
// without inspecting node_modules. Only workspaces inside boundary that adopt
// dir are eligible; otherwise the frontend installs locally.
func WorkspaceInstallDir(dir, boundary string) string {
	member, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	limit, err := filepath.Abs(boundary)
	if err != nil {
		return dir
	}
	// Build the bounded walk once. In particular, a project which is not
	// an ancestor must not cause a scan (or loop at the filesystem root).
	var parents []string
	for p := member; p != limit; {
		parent := filepath.Dir(p)
		if parent == p {
			return dir
		}
		parents = append(parents, parent)
		p = parent
	}

	// A forge-managed bridge is installed by EnsureTo from the member:
	// it may first need to create that member's tracked registry lockfile.
	for _, p := range parents {
		if _, bridge := pnpmBridgeAdopts(p, member); bridge {
			return dir
		}
	}
	if fileExists(filepath.Join(member, "pnpm-workspace.yaml")) {
		return dir // this frontend is its own workspace
	}
	for _, p := range parents {
		if body, err := os.ReadFile(filepath.Join(p, "pnpm-workspace.yaml")); err == nil {
			// An active Yarn root takes membership from package.json, not
			// a leftover pnpm manifest. Use the same manager selection as
			// the install that would run there.
			if PackageManager(p, "") == "yarn" {
				if workspaceAdopts(p, member) {
					return p
				}
				continue
			}
			if pnpmWorkspaceAdopts(p, member, body) {
				return p
			}
			return dir // pnpm stops at the nearest workspace file
		}
		if workspaceAdopts(p, member) {
			return p
		}
	}
	return dir
}

func pnpmWorkspaceAdopts(root, member string, body []byte) bool {
	var ws struct {
		Packages []string `yaml:"packages"`
	}
	if yaml.Unmarshal(body, &ws) != nil {
		return false
	}
	matched := false
	for _, pattern := range ws.Packages {
		excluded := strings.HasPrefix(pattern, "!")
		if !matchesMember(root, strings.TrimPrefix(pattern, "!"), member) {
			continue
		}
		if excluded {
			return false
		}
		matched = true
	}
	return matched
}
