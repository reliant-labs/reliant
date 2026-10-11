// Copyright (c) 2025 Reliant Labs
package diskwatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Role is why a path is watched. A volume's label is derived from the roles of
// the paths that resolve to it.
type Role string

const (
	RoleHome      Role = "home"
	RoleWorktrees Role = "worktrees"
	RoleCache     Role = "cache"
	RoleDocker    Role = "docker"
	RoleTemp      Role = "temp"
	RoleSystem    Role = "system"
)

// Consumer kinds and categories (reliantv1.DiskConsumer.kind / .category).
const (
	KindGoBuildCache      = "go_build_cache"
	KindGoModCache        = "go_mod_cache"
	KindNpmCache          = "npm_cache"
	KindPnpmStore         = "pnpm_store"
	KindYarnCache         = "yarn_cache"
	KindGolangciLintCache = "golangci_lint_cache"
	KindXDGCache          = "xdg_cache"
	KindDockerImages      = "docker_images"
	KindDockerContainers  = "docker_containers"
	KindDockerVolumes     = "docker_volumes"
	KindDockerBuildCache  = "docker_build_cache"
	KindSystemPackages    = "system_packages"
	KindWorktrees         = "worktrees"
	KindOther             = "other"

	CategoryCaches     = "caches"
	CategoryDocker     = "docker"
	CategorySystem     = "system"
	CategoryWorkspaces = "workspaces"
	CategoryOther      = "other"
)

// DockerRoot is docker's data root on a Linux machine. Only watched when it
// exists: a Mac's Docker Desktop keeps its data inside a VM disk image, which
// is not a volume this machine's builds fill directly.
const DockerRoot = "/var/lib/docker"

// Target is one watched path.
type Target struct {
	Path string
	Role Role
	// Kind and Label name the consumer this directory is measured as; empty
	// for paths that only locate a volume (home, temp, system, docker).
	Kind  string
	Label string
	// MustExist drops the target when the path does not exist, instead of
	// resolving it to its nearest existing ancestor. Set for docker, whose
	// absence means "no docker", not "docker will live on the parent".
	MustExist bool
}

var consumerLabels = map[string]string{
	KindGoBuildCache:      "Go build cache",
	KindGoModCache:        "Go module cache",
	KindNpmCache:          "npm cache",
	KindPnpmStore:         "pnpm store",
	KindYarnCache:         "Yarn cache",
	KindGolangciLintCache: "golangci-lint cache",
	KindXDGCache:          "Tool caches",
	KindDockerImages:      "Docker images",
	KindDockerContainers:  "Docker containers",
	KindDockerVolumes:     "Docker volumes",
	KindDockerBuildCache:  "Docker build cache",
	KindSystemPackages:    "System packages",
	KindWorktrees:         "Workspaces",
	KindOther:             "Other",
}

// ConsumerLabel is the human label for a consumer kind.
func ConsumerLabel(kind string) string {
	if l, ok := consumerLabels[kind]; ok {
		return l
	}
	return kind
}

// Environment is what target discovery reads. Getenv is the daemon's own
// environment, which its shells and tools inherit — on a cloud machine the
// workspace entrypoint exports GOCACHE, GOMODCACHE, npm_config_cache, ... into
// it, so the env IS where the caches are. GoEnv answers `go env <key>` for a
// cache whose variable is unset (nil: no Go toolchain).
type Environment struct {
	Getenv func(string) string
	Home   string
	GoEnv  func(key string) string
	GOOS   string
}

// SystemEnvironment reads the daemon process's environment.
func SystemEnvironment() Environment {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return Environment{Getenv: os.Getenv, Home: home, GoEnv: goEnvOnce(), GOOS: runtime.GOOS}
}

// goEnvOnce returns a lookup over one `go env GOCACHE GOMODCACHE` call, made
// lazily and at most once. Nil when no go binary is on PATH.
func goEnvOnce() func(string) string {
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	var once sync.Once
	vals := map[string]string{}
	return func(key string) string {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, goBin, "env", "GOCACHE", "GOMODCACHE").Output()
			if err != nil {
				return
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) == 2 {
				vals["GOCACHE"], vals["GOMODCACHE"] = strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1])
			}
		})
		return vals[key]
	}
}

// DiscoverTargets lists the paths worth watching, in a fixed order (volume
// identity and labels depend on it): home and the worktree root, then each
// tool cache, docker, temp, and the system dirs.
func DiscoverTargets(env Environment) []Target {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := strings.TrimSpace(env.Getenv(k)); v != "" {
				return v
			}
		}
		return ""
	}
	goEnv := func(key string) string {
		if v := get(key); v != "" {
			return v
		}
		if env.GoEnv != nil {
			return env.GoEnv(key)
		}
		return ""
	}
	home := env.Home
	var out []Target
	seen := map[string]bool{}
	add := func(t Target) {
		if t.Path == "" || !filepath.IsAbs(t.Path) {
			return
		}
		t.Path = filepath.Clean(t.Path)
		if seen[t.Path] {
			return
		}
		seen[t.Path] = true
		if t.Kind != "" {
			t.Label = ConsumerLabel(t.Kind)
		}
		out = append(out, t)
	}
	cache := func(kind, path string) { add(Target{Path: path, Role: RoleCache, Kind: kind}) }
	join := func(base string, elem ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, elem...)...)
	}

	add(Target{Path: home, Role: RoleHome})
	add(Target{Path: join(home, ".reliant", "worktrees"), Role: RoleWorktrees, Kind: KindWorktrees})

	xdg := get("XDG_CACHE_HOME")
	if xdg == "" {
		xdg = join(home, ".cache")
	}
	cache(KindGoBuildCache, goEnv("GOCACHE"))
	cache(KindGoModCache, goEnv("GOMODCACHE"))
	npm := get("npm_config_cache", "NPM_CONFIG_CACHE")
	if npm == "" {
		npm = join(home, ".npm")
	}
	cache(KindNpmCache, npm)
	cache(KindPnpmStore, get("pnpm_config_store_dir", "PNPM_STORE_DIR"))
	cache(KindYarnCache, get("YARN_CACHE_FOLDER"))
	golangci := get("GOLANGCI_LINT_CACHE")
	if golangci == "" {
		golangci = join(xdg, "golangci-lint")
	}
	cache(KindGolangciLintCache, golangci)
	cache(KindXDGCache, xdg)

	if env.GOOS == "linux" {
		add(Target{Path: DockerRoot, Role: RoleDocker, MustExist: true})
	}
	tmp := get("TMPDIR")
	if tmp == "" && env.GOOS != "windows" {
		tmp = "/tmp"
	}
	add(Target{Path: tmp, Role: RoleTemp})
	if env.GOOS == "linux" {
		for _, p := range []string{"/etc", "/usr", "/opt", "/var"} {
			add(Target{Path: p, Role: RoleSystem, MustExist: true})
		}
	}
	return out
}
