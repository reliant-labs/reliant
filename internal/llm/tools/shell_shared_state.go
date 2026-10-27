// Copyright (c) 2025 Reliant Labs
package tools

import (
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/diskwatch"
)

// sharedRoot is a directory every agent on the machine depends on.
type sharedRoot struct {
	path  string
	label string
	// loose marks a directory that merely CONTAINS shared state beside unrelated
	// data (~/.cache holds go-build next to every other tool's cache): removing
	// it whole, or globbing all of it, is refused; removing one unrelated child
	// is not.
	loose bool
}

// sharedState is what the guard protects, and the variables a command's paths
// are resolved with.
type sharedState struct {
	home  string
	vars  map[string]string
	roots []sharedRoot
}

// workspaceCaches are the cloud workspace's fixed cache locations, shared by
// every chat on the machine whatever the daemon's own environment says.
var workspaceCaches = []sharedRoot{
	{path: "/var/cache/workspace/go-build", label: "Go build cache"},
	{path: "/var/cache/workspace/go-mod", label: "Go module cache"},
	{path: "/var/cache/workspace/npm", label: "npm cache"},
	{path: "/var/cache/workspace/golangci-lint", label: "golangci-lint cache"},
	{path: "/var/cache/workspace/xdg", label: "tool caches"},
}

// discoverSharedState reads the machine's cache locations from the environment
// the daemon (and so every command it runs) inherits, resolved the way each
// tool resolves them, plus the workspace's fixed locations.
func discoverSharedState(env diskwatch.Environment) sharedState {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(env.Getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	home := env.Home
	join := func(base string, elem ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, elem...)...)
	}
	// first returns the first non-empty value; the sources are functions so a
	// `go env` lookup is only made when the environment did not answer.
	first := func(sources ...func() string) string {
		for _, f := range sources {
			if v := f(); v != "" {
				return v
			}
		}
		return ""
	}
	str := func(v string) func() string { return func() string { return v } }
	envVar := func(keys ...string) func() string { return func() string { return get(keys...) } }
	goEnv := func(key string) func() string {
		return func() string {
			if env.GoEnv == nil {
				return ""
			}
			return env.GoEnv(key)
		}
	}

	xdg := first(envVar("XDG_CACHE_HOME"), str(join(home, ".cache")))
	gopath := first(envVar("GOPATH"), str(join(home, "go")))
	gocache := first(envVar("GOCACHE"), goEnv("GOCACHE"), str(join(xdg, "go-build")))
	gomod := first(envVar("GOMODCACHE"), goEnv("GOMODCACHE"), str(join(gopath, "pkg", "mod")))
	npm := first(envVar("npm_config_cache", "NPM_CONFIG_CACHE"), str(join(home, ".npm")))
	golangci := first(envVar("GOLANGCI_LINT_CACHE"), str(join(xdg, "golangci-lint")))

	st := sharedState{
		home: home,
		vars: map[string]string{
			"HOME":                home,
			"GOPATH":              gopath,
			"GOCACHE":             gocache,
			"GOMODCACHE":          gomod,
			"npm_config_cache":    npm,
			"NPM_CONFIG_CACHE":    npm,
			"GOLANGCI_LINT_CACHE": golangci,
			"XDG_CACHE_HOME":      xdg,
		},
	}
	seen := map[string]bool{}
	add := func(r sharedRoot) {
		if r.path == "" || !path.IsAbs(r.path) {
			return
		}
		r.path = path.Clean(r.path)
		if seen[r.path] {
			return
		}
		seen[r.path] = true
		st.roots = append(st.roots, r)
	}
	add(sharedRoot{path: gocache, label: "Go build cache"})
	add(sharedRoot{path: gomod, label: "Go module cache"})
	add(sharedRoot{path: npm, label: "npm cache"})
	add(sharedRoot{path: golangci, label: "golangci-lint cache"})
	add(sharedRoot{path: xdg, label: "tool caches", loose: home != "" && path.Clean(xdg) == path.Join(home, ".cache")})
	for _, r := range workspaceCaches {
		add(r)
	}
	// The per-user defaults, in case the environment names somewhere else but
	// a tool run with a cleared environment still lands on them.
	add(sharedRoot{path: join(home, ".cache", "go-build"), label: "Go build cache"})
	add(sharedRoot{path: join(gopath, "pkg", "mod"), label: "Go module cache"})
	// pnpm and yarn stores are found by the same discovery the disk guard uses.
	for _, t := range diskwatch.DiscoverTargets(env) {
		if t.Role == diskwatch.RoleCache && (t.Kind == diskwatch.KindPnpmStore || t.Kind == diskwatch.KindYarnCache) {
			add(sharedRoot{path: t.Path, label: t.Label})
		}
	}
	return st
}

// withEnvOverrides returns st with a command's own environment applied: the
// caller pointing GOCACHE at a private directory makes `go clean -cache` private.
func withEnvOverrides(st sharedState, env map[string]string) sharedState {
	if len(env) == 0 {
		return st
	}
	vars := make(map[string]string, len(st.vars)+len(env))
	for k, v := range st.vars {
		vars[k] = v
	}
	for k, v := range env {
		vars[k] = v
	}
	st.vars = vars
	return st
}

// hit reports whether p, an absolute cleaned path that may hold shell glob
// characters, names shared state: a shared directory or something inside one
// (direct), or an ancestor that would take one with it when removed
// recursively (ancestor). A caller that cannot remove directories (rm without
// -r) passes ancestors=false.
func (st sharedState) hit(p string, ancestors bool) (label string, hit bool) {
	if !hasGlob(p) {
		for _, r := range st.roots {
			switch {
			case p == r.path:
				return r.label, true
			case within(p, r.path):
				if !r.loose {
					return r.label, true
				}
			case ancestors && within(r.path, p):
				return r.label, true
			}
		}
		return "", false
	}

	dir, rest := globSplit(p)
	for _, r := range st.roots {
		if globMatch(p, r.path) {
			return r.label, true
		}
		if ancestors {
			for a := path.Dir(r.path); a != "/" && a != "."; a = path.Dir(a) {
				if globMatch(p, a) {
					return r.label, true
				}
			}
		}
		if !r.loose && (dir == r.path || within(dir, r.path)) {
			return r.label, true
		}
		if r.loose && dir == r.path && (rest == "*" || rest == "**") {
			return r.label, true
		}
	}
	return "", false
}

func hasGlob(p string) bool { return strings.ContainsAny(p, "*?[") }

// globSplit splits a glob at its first wildcard: the literal directory before
// it, and the path component the wildcard sits in.
func globSplit(p string) (dir, component string) {
	i := strings.IndexAny(p, "*?[")
	if i < 0 {
		return p, ""
	}
	cut := strings.LastIndexByte(p[:i], '/')
	if cut <= 0 {
		return "/", p[cut+1:]
	}
	dir = p[:cut]
	component = p[cut+1:]
	if j := strings.IndexByte(component, '/'); j >= 0 {
		component = component[:j]
	}
	return dir, component
}

func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// within reports whether p is strictly inside dir.
func within(p, dir string) bool {
	if dir == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, dir+"/")
}

var (
	cacheMu    sync.Mutex
	cached     sharedState
	cachedAt   time.Time
	cacheTTL   = time.Minute
	discoverFn = func() sharedState { return discoverSharedState(diskwatch.SystemEnvironment()) }
)

// defaultState is the running process's view of the machine, rediscovered at
// most once a minute: a `go env` lookup is only made when GOCACHE or GOMODCACHE
// is not in the environment, and this keeps it off the path of every command.
func defaultState() sharedState {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cached.vars == nil || time.Since(cachedAt) > cacheTTL {
		cached, cachedAt = discoverFn(), time.Now()
	}
	return cached
}
