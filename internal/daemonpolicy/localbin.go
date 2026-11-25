// Copyright (c) 2026 Reliant Labs
package daemonpolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// withUserLocalBin appends $HOME/.local/bin to the child's PATH when that
// directory exists and is not on it already.
//
// `reliant machine login` installs a vendor agent CLI (claude, codex, gemini,
// cursor-agent, copilot, and gh) under $HOME and links it into
// $HOME/.local/bin, the one per-user bin directory a cloud machine keeps
// across stop and start. The daemon's own PATH (a cloud machine's pod spec, a
// laptop's launchd or systemd unit) does not have it, though every login
// shell does (Debian's and Ubuntu's ~/.profile add it, and so does a cloud
// machine's interactive zsh), so a run step or an agent's shell could not
// call a CLI the person had just signed in.
//
// It is appended, never prepended, so it never shadows a system binary, and
// only to the child: the daemon's own environment is unchanged. A request
// that sets PATH itself gets exactly the PATH it asked for. It is a search
// path, not a credential, so a `credentials: none` child gets it too.
func withUserLocalBin(env []string, extra map[string]string) []string {
	if runtime.GOOS == "windows" {
		return env
	}
	if _, set := extra["PATH"]; set {
		return env
	}
	pathAt, home := -1, ""
	for i, kv := range env {
		switch {
		case strings.HasPrefix(kv, "PATH="):
			pathAt = i
		case strings.HasPrefix(kv, "HOME="):
			home = strings.TrimPrefix(kv, "HOME=")
		}
	}
	if pathAt < 0 || home == "" {
		return env
	}
	dir := filepath.Join(home, ".local", "bin")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return env
	}
	path := strings.TrimPrefix(env[pathAt], "PATH=")
	for _, d := range filepath.SplitList(path) {
		if filepath.Clean(d) == dir {
			return env
		}
	}
	if path != "" {
		path += string(os.PathListSeparator)
	}
	out := append([]string(nil), env...)
	out[pathAt] = "PATH=" + path + dir
	return out
}
