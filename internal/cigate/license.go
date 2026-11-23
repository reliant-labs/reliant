package cigate

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"
)

// licenseCheckArgv is the go-licenses invocation for the project at root.
//
// go-licenses looks up a license for every package it loads, the project's
// own included, and the project's own code is not what a license check is
// for. A fresh project has no LICENSE file, so every first-party package
// logged "Failed to find license" and the check then passed anyway, which
// teaches people to ignore its output. --ignore exempts the project's own
// modules; the dependencies they import are still checked.
func licenseCheckArgv(root string) []string {
	argv := []string{"go-licenses", "check", "./...", "--disallowed_types=forbidden,restricted"}
	for _, prefix := range licenseIgnores(root) {
		argv = append(argv, "--ignore", prefix)
	}
	return argv
}

// licenseIgnores returns the --ignore prefixes that exempt the project's own
// modules: the module at root, and every module a go.work at root uses from
// inside the project tree. A use outside the tree (a --link-forge checkout)
// is a dependency, so it stays checked.
//
// go-licenses matches --ignore as a plain string prefix, not at a path
// boundary: "example.com/app" also exempts a dependency named
// "example.com/app-client". So the bare module path, which also covers a
// package at the module root, is passed only when no required module's path
// extends it that way. Otherwise the prefix is the module's subtree
// ("example.com/app/"), and a package at the module root, if there is one, is
// checked like a dependency.
func licenseIgnores(root string) []string {
	var own []string
	deps := map[string]bool{}
	addModule := func(dir string) {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			return
		}
		f, err := modfile.ParseLax("go.mod", data, nil)
		if err != nil || f.Module == nil {
			return
		}
		own = append(own, f.Module.Mod.Path)
		for _, r := range f.Require {
			deps[r.Mod.Path] = true
		}
	}
	addModule(root)
	if data, err := os.ReadFile(filepath.Join(root, "go.work")); err == nil {
		if wf, err := modfile.ParseWork("go.work", data, nil); err == nil {
			for _, u := range wf.Use {
				dir := u.Path
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(root, dir)
				}
				if rel, err := filepath.Rel(root, dir); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
					addModule(dir)
				}
			}
		}
	}

	isOwn := map[string]bool{}
	for _, m := range own {
		isOwn[m] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range own {
		if seen[m] || coveredBy(m, own) {
			continue
		}
		seen[m] = true
		prefix := m
		for d := range deps {
			if !isOwn[d] && strings.HasPrefix(d, m) && !strings.HasPrefix(d, m+"/") {
				prefix = m + "/"
				break
			}
		}
		out = append(out, prefix)
	}
	return out
}

// coveredBy reports whether module m is nested under another of the
// project's modules (example.com/app/api under example.com/app), whose
// prefix already exempts it.
func coveredBy(m string, own []string) bool {
	for _, o := range own {
		if strings.HasPrefix(m, o+"/") {
			return true
		}
	}
	return false
}
