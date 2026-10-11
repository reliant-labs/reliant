// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/repo"
)

// A daemon never holds a GitHub token at rest. git asks `reliant auth
// git-credential` for the user's current token at the moment it needs to
// authenticate; the helper asks the server. This file wires git to that helper
// and heals checkouts that an older release left a token in.
//
// Managed daemons (Reliant's own sandbox) write the wiring once into the
// global gitconfig. Local daemons run as the user with the user's own git
// setup, so they never touch it: only the git subprocesses the daemon itself
// starts (clone, reclone) get the helper, through per-invocation environment
// config. Confined (connector) children inherit neither: daemonpolicy's env
// allowlist carries no GIT_CONFIG_*.

// gitCredentialURL is the URL scope the helper is bound to. Never a catch-all
// credential.helper: a user's other hosts are not ours to answer.
const gitCredentialURL = "https://github.com"

// managedProjectsDir is a variable only so the package tests can point it away
// from a real workspace's repositories.
var managedProjectsDir = "/home/workspace/projects"

var gitHelper struct {
	sync.Mutex
	argv []string
}

// SetGitCredentialHelper records the helper command (the reliant binary, its
// `auth git-credential` subcommand and the session pin) git is pointed at.
func SetGitCredentialHelper(argv []string) {
	gitHelper.Lock()
	defer gitHelper.Unlock()
	gitHelper.argv = append([]string(nil), argv...)
}

func gitCredentialHelperArgv() []string {
	gitHelper.Lock()
	argv := append([]string(nil), gitHelper.argv...)
	gitHelper.Unlock()
	if len(argv) > 0 {
		return argv
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	return []string{exe, "auth", "git-credential"}
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// gitCredentialHelperValue is the credential.<url>.helper value: git runs a
// leading "!" value through the shell, with the operation appended.
func gitCredentialHelperValue(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		q[i] = shellQuote(a)
	}
	return "!" + strings.Join(q, " ")
}

func gitCredentialHelperKey() string {
	return "credential." + gitCredentialURL + ".helper"
}

// gitCredentialEnv returns env extended so a git subprocess authenticates to
// github.com through the helper for this one invocation. An inherited helper
// for that URL is cleared first (an empty value, as git requires). Nothing is
// persisted.
func gitCredentialEnv(env []string) []string {
	argv := gitCredentialHelperArgv()
	if len(argv) == 0 {
		return env
	}
	n := 0
	rest := make([]string, 0, len(env)+5)
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT="); ok {
			n, _ = strconv.Atoi(v)
			continue
		}
		rest = append(rest, kv)
	}
	rest = append(rest,
		"GIT_CONFIG_COUNT="+strconv.Itoa(n+2),
		"GIT_CONFIG_KEY_"+strconv.Itoa(n)+"="+gitCredentialHelperKey(),
		"GIT_CONFIG_VALUE_"+strconv.Itoa(n)+"=",
		"GIT_CONFIG_KEY_"+strconv.Itoa(n+1)+"="+gitCredentialHelperKey(),
		"GIT_CONFIG_VALUE_"+strconv.Itoa(n+1)+"="+gitCredentialHelperValue(argv),
	)
	return rest
}

// setupManagedGitCredentials runs once at daemon start. A no-op off a managed
// daemon. Best-effort: nothing here may block or fail the daemon.
func setupManagedGitCredentials() {
	if !IsManagedEnvironment() {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		logging.Warn(logPrefix+" git credential setup skipped: no home directory", "error", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	if argv := gitCredentialHelperArgv(); len(argv) > 0 {
		if err := wireGlobalGitCredentialHelper(ctx, argv); err != nil {
			logging.Warn(logPrefix+" Failed to configure the git credential helper", "error", err)
		}
	}
	go func() {
		remotes, lines := healLeakedGitCredentials(ctx, managedProjectsDir, home)
		cancel()
		if remotes > 0 || lines > 0 {
			logging.Info(logPrefix+" Removed git tokens left by an older release",
				"remote_urls", remotes, "credential_lines", lines)
		}
	}()
}

func gitGlobal(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"config", "--global"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("git config --global %s: %v", args[0], err)
	}
	return out.String(), nil
}

// wireGlobalGitCredentialHelper idempotently sets the URL-scoped helper in the
// global gitconfig and removes the `credential.helper store` the previous
// release wrote — only when that is exactly the one value present.
func wireGlobalGitCredentialHelper(ctx context.Context, argv []string) error {
	if _, err := gitGlobal(ctx, "--replace-all", gitCredentialHelperKey(), gitCredentialHelperValue(argv)); err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "git", "config", "--global", "--get-all", "credential.helper").Output()
	if err == nil && strings.TrimSpace(string(out)) == "store" {
		if _, err := gitGlobal(ctx, "--unset-all", "credential.helper"); err != nil {
			return err
		}
	}
	return nil
}

// healLeakedGitCredentials removes tokens older releases persisted: a password
// in the userinfo of any remote URL of a checkout under projectsDir, and the
// x-access-token lines of ~/.git-credentials. It returns counts only; values
// are never logged. Best-effort.
func healLeakedGitCredentials(ctx context.Context, projectsDir, home string) (remotes, lines int) {
	found, err := repo.Discover(ctx, projectsDir, 0)
	if err != nil {
		logging.Warn(logPrefix+" git token heal: could not scan projects", "error", err)
	}
	for _, f := range found {
		dir := filepath.Join(projectsDir, f.RelativePath)
		remotes += healRemoteURLs(ctx, dir)
	}
	if home != "" {
		n, err := pruneGitCredentialsFile(filepath.Join(home, ".git-credentials"))
		if err != nil {
			logging.Warn(logPrefix+" git token heal: could not clean .git-credentials", "error", err)
		}
		lines = n
	}
	return remotes, lines
}

// stripURLPassword returns rawURL without userinfo when it carries a
// password, and whether it did.
func stripURLPassword(rawURL string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.User == nil {
		return "", false
	}
	if _, has := u.User.Password(); !has {
		return "", false
	}
	u.User = nil
	return u.String(), true
}

func healRemoteURLs(ctx context.Context, dir string) int {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "-z", "--local", "--get-regexp", `^remote\..*\.(push)?url$`).Output()
	if err != nil {
		return 0 // exit 1 = no remotes
	}
	n := 0
	for _, rec := range strings.Split(string(out), "\x00") {
		key, val, ok := strings.Cut(rec, "\n")
		if !ok {
			continue
		}
		clean, leaked := stripURLPassword(val)
		if !leaked {
			continue
		}
		// --fixed-value pins the replacement to this exact (secret) value, so
		// a multi-valued key keeps its other entries.
		if err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--local", "--fixed-value", key, clean, val).Run(); err != nil {
			logging.Warn(logPrefix+" git token heal: could not rewrite a remote URL", "repo", dir, "key", key)
			continue
		}
		n++
	}
	return n
}

// pruneGitCredentialsFile drops github.com x-access-token lines (what the old
// code wrote), keeps everything else, deletes the file if nothing remains and
// keeps mode 0600.
func pruneGitCredentialsFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var kept []string
	removed := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if u, err := url.Parse(strings.TrimSpace(line)); err == nil && u.User != nil && u.User.Username() == "x-access-token" && strings.EqualFold(u.Host, "github.com") {
			removed++
			continue
		}
		kept = append(kept, line)
	}
	if removed == 0 {
		return 0, nil
	}
	if len(kept) == 0 {
		return removed, os.Remove(path)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".git-credentials-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(strings.Join(kept, "\n") + "\n"); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return 0, err
	}
	return removed, os.Rename(tmp.Name(), path)
}
