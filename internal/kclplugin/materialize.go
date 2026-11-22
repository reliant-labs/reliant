package kclplugin

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// write_file(path, content) -> str: the forge-owned way for a KCL module to
// materialize a file as a side effect of rendering.
//
// WHY NOT KCL's OWN file.write. A render is not only what `forge env up` does.
// `forge generate`, `forge lint`, `forge ci validate-kcl`, `forge doctor`,
// `forge env config` and `forge env render` all evaluate the same module to
// READ something from it, and evaluating a module fires every file.write in
// it. KCL's runtime has no read-only mode, so forge could only watch those
// writes happen (env render's write scan) or undo them afterwards (generate's
// git-restore guard) — and every other read-only command did neither.
//
// The incident: control-plane's dev KCL writes its shared NATS config,
// deploy/nats/nats.conf (a TRACKED file), with one account per running dev
// stack. `forge ci validate-kcl` evaluates dev with the dev-stack roster
// unarmed, so it rewrote that file with the default account only — deleting
// every other worktree's account from a committed file, in a command whose
// whole job is to check. `task test` reached the same writer.
//
// This builtin moves the write behind forge, where the render's PURPOSE is
// known:
//
//   - ARMED (UseFileWriter) — `forge env up`, and `forge env deploy` of an env
//     that runs on this machine: the command's job IS to materialize this
//     environment, so the file is written.
//   - UNARMED — every other render, and the default: nothing is written. The
//     call still returns the path, so a module that binds it (to force the
//     side effect) evaluates identically, and the suppressed write is recorded
//     so a caller can report it.
//
// The same arming contract as allocate_port and dev_stacks: a command that
// never says why it renders changes nothing on disk.
//
// When armed the write is:
//   - confined to the project root — an absolute path or a `..` that escapes
//     it is an error, because a render must not reach outside the tree it
//     was asked to materialize;
//   - skipped when the file already holds exactly these bytes, so a
//     deterministic generator leaves mtimes alone and a watcher (a NATS
//     SIGHUP, a dev-server reload) fires only on a real change;
//   - atomic (temp file + rename in the same directory), so a reader never
//     sees half a config.
//
// SHARED FILES. `write_file(path, content, shared=True)` writes into the
// repo's PRIMARY checkout instead of the current one (see UseFileWriter's
// sharedRoot). It is for configuration that a machine-shared resource mounts
// — control-plane's NATS config, mounted by the one NATS every worktree's
// stack uses. Written per-checkout, each worktree held its own copy with its
// own roster, the copies diverged until each re-rendered, and the shared NATS
// mounted whichever checkout had last run compose. One file, one location:
// every stack's render updates the copy the shared server actually reads.

var (
	fileWriterMu         sync.Mutex
	fileWriterRoot       string   // "" = unarmed
	fileWriterSharedRoot string   // where shared=True writes; defaults to fileWriterRoot
	suppressed           []string // project-relative paths an unarmed render declined to write
)

// UseFileWriter arms write_file for this process, rooted at projectDir. Pass
// "" to disarm. Call only on a render whose job is to materialize the env on
// this machine.
//
// sharedRoot is where `shared=True` writes land — the primary checkout's
// counterpart of projectDir (devstack.SharedProjectDir). Omitted or "", it
// is projectDir: on the primary checkout the two are the same directory.
func UseFileWriter(projectDir string, sharedRoot ...string) {
	fileWriterMu.Lock()
	defer fileWriterMu.Unlock()
	fileWriterRoot = projectDir
	fileWriterSharedRoot = projectDir
	if len(sharedRoot) > 0 && sharedRoot[0] != "" && projectDir != "" {
		fileWriterSharedRoot = sharedRoot[0]
	}
}

// SuppressedWrites returns, and clears, the paths write_file declined to
// write because no writer was armed. A read-only command that wants to say
// what it did NOT do (`forge env render`) reads this after rendering.
func SuppressedWrites() []string {
	fileWriterMu.Lock()
	defer fileWriterMu.Unlock()
	out := suppressed
	suppressed = nil
	return out
}

// writeFile is the body the plugin calls. shared selects the primary
// checkout's root over the current one; the path is relative either way.
func writeFile(path, content string, shared bool) (string, error) {
	fileWriterMu.Lock()
	root := fileWriterRoot
	if shared {
		root = fileWriterSharedRoot
	}
	if fileWriterRoot == "" {
		suppressed = append(suppressed, path)
		fileWriterMu.Unlock()
		return path, nil
	}
	fileWriterMu.Unlock()

	abs, err := confine(root, path)
	if err != nil {
		return "", err
	}
	if existing, rerr := os.ReadFile(abs); rerr == nil && bytes.Equal(existing, []byte(content)) {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("write_file %s: %w", path, err)
	}
	mode := os.FileMode(0o644)
	if info, serr := os.Stat(abs); serr == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), "."+filepath.Base(abs)+".forge-*")
	if err != nil {
		return "", fmt.Errorf("write_file %s: %w", path, err)
	}
	tmpName := tmp.Name()
	_, werr := tmp.WriteString(content)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmpName, mode)
	}
	if werr == nil {
		werr = os.Rename(tmpName, abs)
	}
	if werr != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("write_file %s: %w", path, werr)
	}
	return path, nil
}

// confine resolves path against root and refuses anything that lands outside
// it.
func confine(root, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("write_file: empty path")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("write_file %q: path must be relative to the project root, not absolute", path)
	}
	abs := filepath.Join(root, filepath.FromSlash(path))
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("write_file %q: path escapes the project root", path)
	}
	return abs, nil
}
