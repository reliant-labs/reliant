// Copyright (c) 2025 Reliant Labs

package daemonpolicy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// Protected credential material.
//
// `credentials: none` used to strip the child's environment and nothing else.
// A child run as the daemon's Unix user can read the daemon's own files, and
// the daemon's credential file holds the PAT every managed daemon runs on, so
// "no credentials" was a claim about environment variables only (the
// 2026-10-25 review, H2). These are the files and sockets that make the claim
// true. Two things consume them:
//
//   - the shell sandbox (internal/credsandbox) removes all of them from a
//     credentials-none child's view of the filesystem;
//   - the daemon's file tools refuse the Reliant-owned ones (ReliantCredentialFiles)
//     to every caller, because `view` on the file puts a credential into a
//     model's context, and from there into whatever the model writes.

// Credential is one protected location.
type Credential struct {
	// Path is absolute and cleaned.
	Path string
	// Dir is true when Path is a directory that holds credentials as a whole
	// (~/.ssh); false for a file or socket that sits among other things.
	Dir bool
}

type homeEntry struct {
	rel string
	dir bool
}

// protectedHome are credential stores under the user's home that Reliant's
// "no GitHub, git, ssh or cloud credentials" promise names or implies.
var protectedHome = []homeEntry{
	// The daemon's PAT and the forge token cache next to it.
	{".reliant/daemon.json", false},
	{".reliant/forge-token-cache.json", false},
	// git, ssh, gpg.
	{".git-credentials", false},
	{".config/git/credentials", false},
	{".config/gh", true},
	{".config/hub", true},
	{".ssh", true},
	{".gnupg", true},
	// Package registries.
	{".netrc", false},
	{".npmrc", false},
	{".pypirc", false},
	{".cargo/credentials", false},
	{".cargo/credentials.toml", false},
	// Clouds and clusters.
	{".aws", true},
	{".azure", true},
	{".config/gcloud", true},
	{".docker/config.json", false},
	{".docker/.token_seed", false},
	{".docker/.token_seed.lock", false},
	{".kube/config", false},
	{".kube/gke_gcloud_auth_plugin_cache", false},
	// Firebase and Expo CLI logins.
	{".config/configstore", true},
	{".expo/state.json", false},
	// Model-provider logins.
	{".claude", true},
	{".codex", true},
}

// protectedSocketPaths are sockets whose holder can read any file on the
// machine as root (a container engine).
var protectedSocketPaths = []string{
	"/run/docker.sock",
	"/var/run/docker.sock",
	"/run/containerd/containerd.sock",
	"/run/podman/podman.sock",
}

var (
	dataDirMu sync.RWMutex
	dataDir   string
)

// SetDaemonDataDir records the daemon's data directory (its identity, state
// and logs), which a credentials-none command must not see. Called once at
// startup.
func SetDaemonDataDir(dir string) {
	dataDirMu.Lock()
	defer dataDirMu.Unlock()
	dataDir = strings.TrimSpace(dir)
}

func daemonDataDir() string {
	dataDirMu.RLock()
	defer dataDirMu.RUnlock()
	return dataDir
}

// ProtectedCredentials returns every credential location for the process's
// user, cleaned and de-duplicated: the home-relative stores, the shared forge
// credentials file wherever FORGE_HOME or XDG_CONFIG_HOME put it (with the
// temp and lock files its writers leave beside it), the container engines'
// sockets that exist, and the daemon data directory.
func ProtectedCredentials() []Credential {
	home, _ := os.UserHomeDir()
	out := protectedCredentials(home, forgeCredentialsPath(), daemonDataDir())
	for _, s := range protectedSockets() {
		out = appendCredential(out, Credential{Path: s})
	}
	return out
}

func forgeCredentialsPath() string {
	p, err := credentials.PathFromEnv()
	if err != nil {
		return ""
	}
	return p
}

func appendCredential(list []Credential, c Credential) []Credential {
	if c.Path == "" || !filepath.IsAbs(c.Path) {
		return list
	}
	c.Path = filepath.Clean(c.Path)
	if c.Path == "/" {
		return list
	}
	for _, have := range list {
		if have.Path == c.Path {
			return list
		}
	}
	return append(list, c)
}

func protectedCredentials(home, forgeCredentials, dataDir string) []Credential {
	var out []Credential
	if home != "" {
		for _, e := range protectedHome {
			out = appendCredential(out, Credential{Path: filepath.Join(home, e.rel), Dir: e.dir})
		}
	}
	for _, p := range reliantCredentialFiles(home, forgeCredentials) {
		out = appendCredential(out, Credential{Path: p})
	}
	out = appendCredential(out, Credential{Path: dataDir, Dir: true})
	return out
}

// protectedSockets are the container engines' sockets that exist on this
// machine, DOCKER_HOST's included. A missing socket is omitted: there is
// nothing to reach.
func protectedSockets() []string {
	candidates := append([]string(nil), protectedSocketPaths...)
	if host := os.Getenv("DOCKER_HOST"); strings.HasPrefix(host, "unix://") { //nolint:forbidigo // DOCKER_HOST names the container engine's socket; not Reliant configuration
		candidates = append(candidates, strings.TrimPrefix(host, "unix://"))
	}
	if run := os.Getenv("XDG_RUNTIME_DIR"); run != "" { //nolint:forbidigo // locates rootless engines' sockets; not Reliant configuration
		candidates = append(candidates,
			filepath.Join(run, "docker.sock"),
			filepath.Join(run, "podman", "podman.sock"))
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range candidates {
		if !filepath.IsAbs(c) || seen[c] {
			continue
		}
		seen[c] = true
		if _, err := os.Lstat(c); err == nil {
			out = append(out, filepath.Clean(c))
		}
	}
	return out
}

// ReliantCredentialFiles are the credential files Reliant itself writes: the
// daemon's PAT, the forge token cache and the shared forge credentials file.
// No tool has a reason to read or write them, so the daemon's file tools
// refuse them to every caller.
func ReliantCredentialFiles() []string {
	home, _ := os.UserHomeDir()
	return reliantCredentialFiles(home, forgeCredentialsPath())
}

func reliantCredentialFiles(home, forgeCredentials string) []string {
	var out []string
	if home != "" {
		out = append(out,
			filepath.Join(home, ".reliant", "daemon.json"),
			filepath.Join(home, ".reliant", "forge-token-cache.json"))
	}
	if forgeCredentials != "" {
		out = append(out, forgeCredentials, forgeCredentials+".tmp", forgeCredentials+".lock")
	}
	var clean []string
	for _, p := range out {
		if filepath.IsAbs(p) {
			clean = append(clean, filepath.Clean(p))
		}
	}
	return clean
}

// ErrCredentialFile is the sentinel for a file tool refused a Reliant
// credential file.
var ErrCredentialFile = errors.New("refused: this is a Reliant credential file")

// DenyCredentialFile returns an error wrapping ErrCredentialFile when path is
// one of the Reliant credential files, and nil otherwise. The daemon's file
// tools call it (through ResolveFile and ResolveDir) before they touch a path,
// for every caller: no tool has a reason to read or write the daemon's own
// credentials, and a model that reads them can be talked into sending them out.
func DenyCredentialFile(path string) error {
	if IsReliantCredentialFile(path) {
		return fmt.Errorf("%w; the daemon's file tools never read or write it", ErrCredentialFile)
	}
	return nil
}

// IsReliantCredentialFile reports whether path is one of ReliantCredentialFiles.
// It resolves symlinks first, so a link to ~/.reliant/daemon.json is the
// daemon's credential file, and compares files by identity, so a hard link or a
// second mount of the same disk is too. A path that does not exist yet is judged
// by its nearest existing parent plus the remainder.
func IsReliantCredentialFile(path string) bool {
	return isCredentialFile(path, ReliantCredentialFiles())
}

func isCredentialFile(path string, protected []string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	real := ResolveLoose(path)
	for _, p := range protected {
		if real == p || real == ResolveLoose(p) {
			return true
		}
	}
	info, err := os.Stat(real)
	if err != nil {
		return false
	}
	for _, p := range protected {
		if pi, err := os.Stat(p); err == nil && os.SameFile(info, pi) {
			return true
		}
	}
	return false
}

// ResolveLoose is filepath.EvalSymlinks that tolerates a missing tail: the
// existing prefix is resolved and the rest appended unchanged.
func ResolveLoose(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	rest := ""
	cur := abs
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
