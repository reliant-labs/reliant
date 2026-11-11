// Copyright (c) 2025 Reliant Labs

// Package ghshim names the chat that opened a pull request in its body when
// an agent opens it with plain `gh pr create` from its shell.
//
// Why. Automations that react to a pull request later (control-plane's
// pr-fix: a review asking for changes, the merge queue ejecting it) hand the
// news to the chat that opened it before starting another agent on the same
// branch. They find that chat by the marker `<!-- reliant-chat: <chat id> -->`
// in the body. github__pr_create adds it (internal/llm/tools/owning_chat.go),
// and control-plane's scripts/pr-open.sh adds it, but most pull requests are
// opened with `gh pr create` straight from an agent's shell, and those carried
// none (2026-11-10: forge 0 of 60, reliant 5 of 60), so the automation took
// over pull requests whose chat was still fixing them.
//
// What. The daemon writes a tiny `gh` script into a directory of its own
// (Install) and puts that directory first on PATH for every command a chat
// runs (Shim.Env: only when the command's environment carries ChatIDEnv). The
// script hands `gh pr create` (and its alias `gh pr new`) to this binary
// (Run), which appends the marker to the body given by --body, --body-file
// (a file or "-") and, for --fill, --fill-first and --fill-verbose, to the
// body gh wrote, then runs the real gh. Every other gh command runs the real
// gh directly from the script, without starting this binary.
//
// Never in the way. A body that already names a chat is left alone; a chat id
// that is not a UUID, RELIANT_PR_MARKER=off, an argument list it does not
// fully understand, or any failure of its own runs the real gh with the
// arguments exactly as given. When this binary is missing (a credentials-none
// sandbox hides it, or it was replaced) the script runs the real gh itself.
// gh's exit status and output are its own.
//
//forge:exclude-contract: one concrete argv rewrite plus the shim script this binary installs; there is no second implementation to substitute
package ghshim

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
)

// DispatchArg is the first argument that makes this binary run the shim
// instead of the reliant CLI: `<self> __gh-shim <shim dir> pr create ...`.
const DispatchArg = "__gh-shim"

// ChatIDEnv names the chat a shell command runs for (the shell tool's
// tools.ChatIDEnv). A command without it gets no shim.
const ChatIDEnv = "RELIANT_CHAT_ID"

// OffEnv set to "off" in a command's environment leaves its pull requests
// unmarked.
const OffEnv = "RELIANT_PR_MARKER"

// scriptTag is on the script's second line; findGh skips any gh carrying it,
// so a shim never runs another shim (or itself) as the real gh.
const scriptTag = "reliant-gh-shim"

// Shim is an installed shim directory. A nil *Shim installs nothing on PATH.
type Shim struct {
	dir string
}

// Dir is the directory holding the shim's gh.
func (s *Shim) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// String describes the shim for a startup log line.
func (s *Shim) String() string {
	if s == nil {
		return "gh pr create runs unmarked (no gh shim)"
	}
	return "gh pr create from a chat names the chat in the pull request (gh shim " + s.dir + ")"
}

// Install writes the shim for the executable self into its own directory
// under root (<root>/<hash of self>/gh, so daemons of different builds on one
// machine never overwrite each other's) and returns it. Rewriting an
// identical script is skipped, and a changed one is replaced atomically.
func Install(root, self string) (*Shim, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("not supported on windows (agent shells run powershell)")
	}
	if !filepath.IsAbs(self) || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("install a gh shim: paths must be absolute (root %q, self %q)", root, self)
	}
	sum := sha256.Sum256([]byte(self))
	dir := filepath.Join(root, hex.EncodeToString(sum[:])[:12])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("install a gh shim: %w", err)
	}
	want := []byte(script(self, dir))
	target := filepath.Join(dir, "gh")
	if have, err := os.ReadFile(target); err == nil && bytes.Equal(have, want) {
		return &Shim{dir: dir}, nil
	}
	tmp, err := os.CreateTemp(dir, ".gh-*")
	if err != nil {
		return nil, fmt.Errorf("install a gh shim: %w", err)
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after the rename; best effort otherwise
	_, werr := tmp.Write(want)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o755)); err != nil {
		return nil, fmt.Errorf("install a gh shim: %w", err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return nil, fmt.Errorf("install a gh shim: %w", err)
	}
	return &Shim{dir: dir}, nil
}

// InstallDefault installs the shim for this executable under the user cache
// directory and makes it the Default.
func InstallDefault() (*Shim, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("install a gh shim: own executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("install a gh shim: %w", err)
	}
	s, err := Install(filepath.Join(cache, "reliant", "gh-shim"), self)
	if err != nil {
		return nil, err
	}
	SetDefault(s)
	return s, nil
}

var defaultShim atomic.Pointer[Shim]

// SetDefault sets the shim the daemon's command paths put on PATH. Safe for
// concurrent use.
func SetDefault(s *Shim) { defaultShim.Store(s) }

// Default is the shim SetDefault installed, or nil.
func Default() *Shim { return defaultShim.Load() }

// Env returns env with the shim's directory first on PATH, when env names a
// chat (ChatIDEnv) and sets a PATH. Otherwise, and for a nil *Shim, it returns
// env unchanged. env is never modified; a nil env (inherit the daemon's) is
// returned as is: a command that inherits carries no chat id.
func (s *Shim) Env(env []string) []string {
	if s == nil || env == nil {
		return env
	}
	if chat, _ := lookupEnv(env, ChatIDEnv); strings.TrimSpace(chat) == "" {
		return env
	}
	path, ok := lookupEnv(env, "PATH")
	if !ok {
		return env
	}
	for _, d := range filepath.SplitList(path) {
		if filepath.Clean(d) == s.dir {
			return env
		}
	}
	out := make([]string, len(env), len(env)+1)
	copy(out, env)
	// Appended, not replaced in place: os/exec uses the last value of a key.
	return append(out, "PATH="+s.dir+string(os.PathListSeparator)+path)
}

// lookupEnv is the last value of name in env, as os/exec would use it.
func lookupEnv(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

// script is the gh the shim directory holds. Only `gh pr create` / `gh pr
// new` reach this binary; everything else, and everything when this binary is
// gone, runs the next gh on PATH with the shim's directory removed from PATH.
func script(self, dir string) string {
	return `#!/bin/sh
# ` + scriptTag + `: written by the Reliant daemon, first on PATH in an agent's shell.
# "gh pr create" runs through Reliant, which names the agent's chat in the pull
# request body (<!-- reliant-chat: ... -->); every other gh command runs gh itself.
# RELIANT_PR_MARKER=off leaves pull requests unmarked.
self=` + shQuote(self) + `
here=` + shQuote(dir) + `
case "$1 $2" in
"pr create"|"pr new")
  if [ -x "$self" ]; then exec "$self" ` + DispatchArg + ` "$here" "$@"; fi ;;
esac
p=
old_ifs=$IFS
IFS=:
set -f
for d in $PATH; do
  [ "$d" = "$here" ] || p="${p:+$p:}$d"
done
set +f
IFS=$old_ifs
PATH=$p
export PATH
exec gh "$@"
`
}

// shQuote quotes s for a POSIX shell.
func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
