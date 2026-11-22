package hostinfra

// Where a host-infra instance's log goes.
//
// Every process `forge env up` runs logs to ONE directory,
// .forge/logs/<env>/<name>.log: the dev-logs convention every doc, agent and
// grep relies on, and the only directory the local ClickStack collector
// tails (otel-collector.yaml.tmpl's filelog/forge, which takes service.name
// from the file name and deployment.environment.name from the directory).
//
// Host infra used to log beside its data instead
// (.forge/hostinfra/<name>/<engine>.log), so postgres, zitadel, nats,
// temporal and mailpit were the one set of processes whose logs never
// reached the local store and were missing from the directory people grep.
// The log now lands in the shared directory, named for the WORKLOAD (the same
// value the rest of the stack's files and service.name carry), and the old
// path is kept working as a symlink to it, because scripts and muscle memory
// read it.

import (
	"os"
	"path/filepath"
	"strings"
)

// LogPath is the log file of the process `name` in environment env: the one
// file `forge env up` writes for it, under .forge/logs/<env>/. "/" and ":" in
// the name become "_", so a label such as "frontend:web" is a flat file name.
func LogPath(projectDir, env, name string) string {
	safe := strings.NewReplacer("/", "_", ":", "_").Replace(name)
	return filepath.Join(projectDir, ".forge", "logs", env, safe+".log")
}

// openLog opens the instance's log for a start, creating its directory.
//
// legacyFile is the file name the engine used to write beside its data
// (postgres.log, zitadel.log, nats.log, …). With an Env the log is
// LogPath(projectDir, Env, Name), and that legacy path becomes a symlink to
// it. Without one (a Spec built by hand, with no environment to name) the log
// stays at the legacy path, as it always was.
//
// flag is the os.OpenFile mode on top of O_CREATE|O_WRONLY: O_TRUNC for an
// engine whose log is one run's, O_APPEND for one whose history is worth
// keeping (postgres's initdb transcript).
func (s Spec) openLog(projectDir, legacyFile string, flag int) (*os.File, string, error) {
	legacy := filepath.Join(s.dataDir(projectDir), legacyFile)
	logPath := legacy
	if s.Env != "" {
		logPath = LogPath(projectDir, s.Env, s.Name)
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return nil, "", err
		}
		adoptLegacyLog(legacy, logPath)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|flag, 0o644) // #nosec G304 -- path derived from the project's own declaration
	if err != nil {
		return nil, "", err
	}
	if logPath != legacy {
		linkLegacyLog(legacy, logPath)
	}
	return f, logPath, nil
}

// adoptLegacyLog moves a log an earlier forge wrote at the legacy path to
// the new one, when the new one does not exist yet, so the history an
// appending engine kept (postgres's) follows it. A legacy path that is
// already a symlink, or absent, is left for linkLegacyLog.
func adoptLegacyLog(legacy, logPath string) {
	info, err := os.Lstat(legacy)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if _, err := os.Lstat(logPath); err == nil {
		return
	}
	// A failed rename (a data_dir on another filesystem) leaves the file for
	// linkLegacyLog to replace: the engine's next lines go to the new path
	// either way.
	_ = os.Rename(legacy, logPath)
}

// linkLegacyLog makes the legacy path a symlink to logPath, so anything that
// still reads .forge/hostinfra/<name>/<engine>.log reads the live log.
//
// Relative when it can be, so the link survives moving the checkout.
// Best-effort: a filesystem or platform that cannot hold a symlink (Windows
// without the privilege) costs the old spelling, never the start.
func linkLegacyLog(legacy, logPath string) {
	target := logPath
	if rel, err := filepath.Rel(filepath.Dir(legacy), logPath); err == nil {
		target = rel
	}
	if info, err := os.Lstat(legacy); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			if cur, err := os.Readlink(legacy); err == nil && cur == target {
				return
			}
		}
		// A regular file here is a log an earlier forge wrote that could
		// not be moved, or one the new path already supersedes. Left in
		// place it would read as the live log and go stale.
		if err := os.Remove(legacy); err != nil {
			return
		}
	}
	_ = os.Symlink(target, legacy)
}
