package frontenddeps

import (
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// PnpmBridgeMarker is the first line of the gitignored root
// pnpm-workspace.yaml a dev build of forge writes to bridge a project's pnpm
// frontends to a local @reliantlabs/forge-web-runtime checkout (see
// generator/frontend_webruntime_devlink.go). It is how forge tells that file
// from a pnpm-workspace.yaml a user wrote — the tracked root of the
// frontend-workspaces layout — which forge never rewrites and never installs
// through as a bridge.
const PnpmBridgeMarker = "# forge-dev-workspace-root: GITIGNORED, written by a DEV build of forge."

// PnpmInstallRoot returns the directory a pnpm install for the frontend at
// dir runs in: the forge dev bridge's root when one adopts dir, else dir.
//
// pnpm installs at the NEAREST pnpm-workspace.yaml, and a scaffolded frontend
// carries its own (its overrides and allowBuilds live there), so a plain
// `pnpm install` in the frontend never sees a bridge root above it. The
// bridge only takes effect when the install runs at the root, which is what
// forge's own installs do through this. The frontend's tracked pnpm-lock.yaml
// is untouched by a root install: the root writes its own, gitignored one.
func PnpmInstallRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for p := filepath.Dir(abs); ; p = filepath.Dir(p) {
		if root, ok := pnpmBridgeAdopts(p, abs); ok {
			return root
		}
		if filepath.Dir(p) == p {
			return dir
		}
	}
}

// pnpmBridgeAdopts reports whether root holds a forge bridge
// pnpm-workspace.yaml that lists member among its packages.
func pnpmBridgeAdopts(root, member string) (string, bool) {
	body, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml"))
	if err != nil || !strings.HasPrefix(string(body), PnpmBridgeMarker) {
		return "", false
	}
	var ws struct {
		Packages []string `yaml:"packages"`
	}
	if yaml.Unmarshal(body, &ws) != nil {
		return "", false
	}
	for _, pat := range ws.Packages {
		if !strings.HasPrefix(pat, "!") && matchesMember(root, pat, member) {
			return root, true
		}
	}
	return "", false
}

// installDir is where runner's install for the frontend at dir runs, and
// whether that is a bridge root rather than dir itself.
func installDir(runner, dir string) (string, bool) {
	if runner != "pnpm" {
		return dir, false
	}
	root := PnpmInstallRoot(dir)
	return root, root != dir
}
