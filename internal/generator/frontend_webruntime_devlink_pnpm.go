// The pnpm twin of the dev web-runtime bridge.
//
// pnpm reads its workspace from pnpm-workspace.yaml and never from
// package.json `workspaces`, so the npm workspace root above does nothing for
// a pnpm frontend: under a dev forge it silently installed the REGISTRY
// web-runtime. The twin is a second gitignored root file:
//
//	<project>/pnpm-workspace.yaml
//	    packages:  [frontends/web, …]
//	    overrides: {"@reliantlabs/forge-web-runtime": "link:./.forge-link/web-runtime"}
//	<project>/.forge-link/web-runtime -> ../../forge/web-runtime   (the same symlink)
//
// A root `pnpm install` makes frontends/<name>/node_modules/@reliantlabs/
// forge-web-runtime a symlink into the checkout. Measured on pnpm 12:
//
//   - the frontend's tracked pnpm-lock.yaml stays byte-identical (the root
//     writes its own, gitignored, pnpm-lock.yaml);
//   - the checkout's own node_modules is not touched — `link:` installs
//     nothing into its target, where a workspace MEMBER would get its
//     dependencies installed into the forge checkout;
//   - a later `pnpm install --frozen-lockfile` inside the frontend keeps the
//     link.
//
// A link, not a membership, is also why no peer-dedup work is needed beyond
// what the npm bridge already relies on: the runtime is a symlink to a tree
// with its own node_modules in both, and next.config.ts / vite.config.ts /
// the tsconfig peer pins redirect its peers to the app's copy either way.
//
// Each scaffolded pnpm frontend is its own workspace root (its overrides and
// allowBuilds live in its own pnpm-workspace.yaml), and pnpm installs at the
// NEAREST one, so a plain `pnpm install` in the frontend never sees this root.
// forge's installs run at the root instead (frontenddeps.PnpmInstallRoot);
// the root carries every member's overrides and allowBuilds, because a
// member's own settings are not read when the root installs it.
//
// Only frontends that ARE their own pnpm root are members. A frontend of the
// frontend-workspaces layout has no pnpm-workspace.yaml of its own — the
// project's TRACKED root file is its workspace — and that root is the user's,
// so the bridge stays out of it.
package generator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/frontenddeps"
)

// devPnpmWorkspaceName is the gitignored root file the pnpm bridge writes.
const devPnpmWorkspaceName = "pnpm-workspace.yaml"

// devBridgePnpmIgnoreEntries are ignored before the pnpm bridge writes
// anything, alongside devBridgeIgnoreEntries.
var devBridgePnpmIgnoreEntries = []string{
	"/" + devPnpmWorkspaceName,
	"/pnpm-lock.yaml",
}

// isPnpmBridgeMember reports whether the frontend at feDir is a pnpm project
// that is its own workspace root, and so one the pnpm bridge adopts.
func isPnpmBridgeMember(feDir string) bool {
	return frontenddeps.PackageManager(feDir, "") == "pnpm" &&
		fileExists(filepath.Join(feDir, devPnpmWorkspaceName))
}

// isForgeOwnedPnpmWorkspace reports whether path is a root
// pnpm-workspace.yaml this bridge wrote.
func isForgeOwnedPnpmWorkspace(path string) bool {
	body, err := os.ReadFile(path)
	return err == nil && strings.HasPrefix(string(body), frontenddeps.PnpmBridgeMarker)
}

// pnpmBridgeManifest is the settings part of the bridge's root file. yaml.v3
// writes map keys sorted, so the rendered file is deterministic.
type pnpmBridgeManifest struct {
	VerifyDepsBeforeRun bool              `yaml:"verifyDepsBeforeRun"`
	Packages            []string          `yaml:"packages"`
	Overrides           map[string]string `yaml:"overrides"`
	AllowBuilds         map[string]any    `yaml:"allowBuilds,omitempty"`
}

// devPnpmWorkspaceManifest renders the bridge's root pnpm-workspace.yaml for
// members (project-relative). Independent roots can share an install ONLY
// when their overrides and build approvals are identical. Even an absent
// approval is a denial by pnpm's default, so a union would widen policy.
// Comparing entire maps also catches overlapping version/wildcard selectors
// without attempting to reimplement pnpm's selector precedence. Incompatible
// roots keep their standalone installs instead; the bridge fails closed.
func devPnpmWorkspaceManifest(projectDir string, members []string) (string, error) {
	m := pnpmBridgeManifest{
		Packages:    append([]string(nil), members...),
		Overrides:   map[string]string{},
		AllowBuilds: map[string]any{},
	}
	for _, member := range members {
		path := filepath.Join(projectDir, filepath.FromSlash(member), devPnpmWorkspaceName)
		body, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		var own struct {
			Overrides   map[string]string `yaml:"overrides"`
			AllowBuilds map[string]any    `yaml:"allowBuilds"`
		}
		if err := yaml.Unmarshal(body, &own); err != nil {
			return "", fmt.Errorf("%s: %w", path, err)
		}
		for k, v := range own.Overrides {
			if strings.Contains(k, WebRuntimePackage) {
				return "", fmt.Errorf("%s: explicit runtime override %q conflicts with the dev bridge's local runtime; keep standalone installs", path, k)
			}
			// These values are interpreted relative to the settings root.
			// Moving them unchanged would point at a different dependency.
			if strings.HasPrefix(v, "link:") || strings.HasPrefix(v, "file:") {
				target := v[strings.IndexByte(v, ':')+1:]
				if !filepath.IsAbs(target) {
					return "", fmt.Errorf("%s: override %q = %q is relative; cannot move it to the dev bridge root (keep standalone installs)", path, k, v)
				}
			}
		}
		if len(own.Overrides) == 0 {
			own.Overrides = map[string]string{}
		}
		if len(own.AllowBuilds) == 0 {
			own.AllowBuilds = map[string]any{}
		}
		if member == members[0] {
			m.Overrides, m.AllowBuilds = own.Overrides, own.AllowBuilds
		} else {
			// Both sides are named project-relative, in the slash form members
			// are declared in, whatever the host's separator.
			first, this := members[0]+"/"+devPnpmWorkspaceName, member+"/"+devPnpmWorkspaceName
			if !reflect.DeepEqual(m.AllowBuilds, own.AllowBuilds) {
				return "", fmt.Errorf("incompatible allowBuilds in %s and %s: %v versus %v; dev bridge requires identical policies (including absent approvals); keep standalone installs", first, this, m.AllowBuilds, own.AllowBuilds)
			}
			if !reflect.DeepEqual(m.Overrides, own.Overrides) {
				return "", fmt.Errorf("incompatible overrides in %s and %s: %v versus %v; dev bridge requires identical overrides; keep standalone installs", first, this, m.Overrides, own.Overrides)
			}
		}
	}
	m.Overrides[WebRuntimePackage] = "link:./" + devLinkDir + "/web-runtime"
	body, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return frontenddeps.PnpmBridgeMarker + `
# The pnpm twin of a gitignored go.work: it links this project's pnpm frontends
# to a local @reliantlabs/forge-web-runtime checkout (symlinked under
# .forge-link/), so edits in that checkout are live here. Install from THIS
# directory (forge's own installs do); a plain install inside a frontend uses
# its own pnpm-workspace.yaml and does not see this file. The frontends'
# package.json and pnpm-lock.yaml are not changed. Each member's overrides and
# allowBuilds are copied in, because pnpm reads settings from the root only.
# Delete this file and .forge-link/ to go back to the registry copy.
` + string(body), nil
}

// reconcilePnpmBridge writes, refreshes or removes the bridge's root
// pnpm-workspace.yaml for members. A root file forge did not write is never
// touched. Reports whether the file changed.
func reconcilePnpmBridge(projectDir string, members []string) (bool, error) {
	path := filepath.Join(projectDir, devPnpmWorkspaceName)
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return false, err
	case !isForgeOwnedPnpmWorkspace(path):
		return false, nil
	}
	if len(members) == 0 {
		if existing == nil {
			return false, nil
		}
		return true, os.Remove(path)
	}
	want, err := devPnpmWorkspaceManifest(projectDir, members)
	if err != nil {
		// Never leave an older, more permissive overlay active after policy
		// changes. PnpmInstallRoot must fall back to standalone installs.
		if isForgeOwnedPnpmWorkspace(path) {
			if removeErr := os.Remove(path); removeErr != nil {
				return false, errors.Join(err, removeErr)
			}
		}
		return false, err
	}
	if string(existing) == want {
		return false, nil
	}
	return true, atomicfile.WriteFile(path, []byte(want), 0o644)
}

// splitBridgeMembers sorts bridgeMembers into the frontends npm installs
// through the root package.json and those pnpm installs through the root
// pnpm-workspace.yaml. A pnpm frontend that is not its own pnpm root (the
// frontend-workspaces layout) is in neither.
func splitBridgeMembers(projectDir string, members []string) (npmMembers, pnpmMembers []string) {
	for _, m := range members {
		feDir := filepath.Join(projectDir, filepath.FromSlash(m))
		switch {
		case isPnpmBridgeMember(feDir):
			pnpmMembers = append(pnpmMembers, m)
		case frontenddeps.PackageManager(feDir, "") == "pnpm":
		default:
			npmMembers = append(npmMembers, m)
		}
	}
	sort.Strings(npmMembers)
	sort.Strings(pnpmMembers)
	return npmMembers, pnpmMembers
}

// removeForgeOwnedNpmRoot deletes the npm bridge's root package.json and
// .npmrc once no npm frontend is left for them to adopt (every frontend
// moved to pnpm). A user's own files are never touched.
func removeForgeOwnedNpmRoot(projectDir string) {
	if root := filepath.Join(projectDir, "package.json"); isForgeOwnedWorkspaceRoot(root) {
		_ = os.Remove(root)
	}
	if npmrc := filepath.Join(projectDir, devNpmrcName); isForgeOwnedNpmrc(npmrc) {
		_ = os.Remove(npmrc)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
