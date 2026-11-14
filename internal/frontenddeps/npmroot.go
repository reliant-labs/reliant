package frontenddeps

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// NpmInstallVerb returns the npm verb a reproducible install of dir should
// use: "ci" when npm will find a lockfile to honour, "install" otherwise.
//
// Whether a lockfile exists is NOT a question about dir. npm installs at the
// workspace root that adopts dir when there is one, and `npm ci` reads the
// lockfile THERE — a package-lock.json sitting in dir is ignored. So a tracked
// frontends/<name>/package-lock.json under a root package.json that lists the
// frontend as a workspace, with no root lockfile beside it, makes `npm ci`
// exit EUSAGE "can only install with an existing package-lock.json", while the
// file it names is right there. forge's own dev web-runtime bridge writes
// exactly that root (gitignored, lockfile-less until the first root install),
// which is how every fresh worktree of a bridged project failed `forge
// generate` until node_modules happened to exist.
//
// `install` is the only verb that can succeed in that tree, and it is the
// bridge's documented install path; it leaves the frontend's tracked lockfile
// byte-identical (see generator/frontend_webruntime_devlink.go).
func NpmInstallVerb(dir string) string {
	if hasNpmLockfile(NpmInstallRoot(dir)) {
		return "ci"
	}
	return "install"
}

// NpmInstallRoot returns the directory npm treats as the install root for a
// command run in dir, following npm's own rule (@npmcli/config
// loadLocalPrefix): the nearest directory holding a package.json or
// node_modules is the package, and the nearest ancestor of it whose
// package.json declares workspaces matching that package becomes the root.
//
// Workspace patterns are matched with filepath.Match per pattern, so `**`
// globs are not understood; such a root is reported as not adopting dir. The
// roots forge writes enumerate their members, and a miss here only means the
// caller keeps the dir-local answer it gave before this check existed.
func NpmInstallRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	pkg := ""
	for p := abs; ; {
		if pkg == "" {
			if fileExists(filepath.Join(p, "package.json")) || dirExists(filepath.Join(p, "node_modules")) {
				pkg = p
			}
		} else if workspaceAdopts(p, pkg) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	if pkg == "" {
		return abs
	}
	return pkg
}

// workspaceAdopts reports whether root's package.json declares a workspace
// pattern that matches member (and no negated pattern excludes it).
func workspaceAdopts(root, member string) bool {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return false
	}
	var manifest struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if json.Unmarshal(data, &manifest) != nil || len(manifest.Workspaces) == 0 {
		return false
	}
	// Either an array of patterns or {"packages": [...]} (the yarn-classic
	// shape npm also accepts).
	var patterns []string
	if json.Unmarshal(manifest.Workspaces, &patterns) != nil {
		var obj struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal(manifest.Workspaces, &obj) != nil {
			return false
		}
		patterns = obj.Packages
	}
	matched := false
	for _, pat := range patterns {
		negated := strings.HasPrefix(pat, "!")
		pat = strings.TrimPrefix(pat, "!")
		if !matchesMember(root, pat, member) {
			continue
		}
		if negated {
			return false
		}
		matched = true
	}
	return matched
}

func matchesMember(root, pattern, member string) bool {
	rel, err := filepath.Rel(root, member)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	pattern = filepath.Clean(filepath.FromSlash(strings.TrimPrefix(pattern, "./")))
	ok, err := filepath.Match(pattern, rel)
	return err == nil && ok
}

func hasNpmLockfile(dir string) bool {
	return fileExists(filepath.Join(dir, "package-lock.json")) ||
		fileExists(filepath.Join(dir, "npm-shrinkwrap.json"))
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
