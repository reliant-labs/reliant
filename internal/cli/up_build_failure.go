package cli

import (
	"io"
	"os"
	"path"
	"strings"
)

// A host service started under a hot-reload runner (the scaffolded .air.toml)
// does not exit when its build fails: air prints the compiler's errors, then
// "failed to build, error: …", and keeps watching for the fix. Nothing ever
// binds the port, so the readiness gate used to wait out its whole timeout
// (2m by default) and then guess "compilation or startup may still be in
// progress", while the answer sat in the service's log. The gate now reads
// that log on every poll and fails at once with the compiler's own lines.

// airBuildFailedMarker and airBuildStartMarker are the lines air writes at the
// end of a failed build and at the start of every build.
const (
	airBuildFailedMarker = "failed to build"
	airBuildStartMarker  = "building..."
)

// buildFailureExcerptLines caps the compiler output a readiness error quotes.
const buildFailureExcerptLines = 20

// buildFailure reports the latest build cycle of name's log when that cycle
// failed: the compiler lines between air's "building..." and its "failed to
// build", ending with the failure line. "" when the latest cycle has not
// failed (still building, built, or no air output). The log is this run's:
// procRegistry.start creates it fresh when it starts the runner, before any
// readiness check reads it.
func buildFailure(env, name string) string {
	f, err := os.Open(path.Join(upLogDir(env), name+".log")) // #nosec G304 -- the project's own log dir
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	// The tail is enough: a build cycle's output is short, and a long-lived
	// log is rotated (up_logrotate.go).
	const tail = 1 << 20
	if st, err := f.Stat(); err == nil && st.Size() > tail {
		if _, err := f.Seek(st.Size()-tail, io.SeekStart); err != nil {
			return ""
		}
	}
	body, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	return lastBuildFailure(string(body))
}

// lastBuildFailure is buildFailure over the log text itself. A cycle starts
// at air's "building..." line; it failed when its "failed to build" line
// follows. With stop_on_error = false (the scaffold's) air goes on to start
// the previous binary after a failure, so a later "running..." does not
// clear it: whether that old binary listens is what the port probe answers.
func lastBuildFailure(log string) string {
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")
	start := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(strings.ToLower(lines[i]), airBuildStartMarker) {
			start = i + 1
			break
		}
	}
	failed := -1
	for i := start; i < len(lines); i++ {
		if strings.Contains(strings.ToLower(lines[i]), airBuildFailedMarker) {
			failed = i
			break
		}
	}
	if failed < 0 {
		return ""
	}
	excerpt := make([]string, 0, failed-start+1)
	for _, l := range lines[start : failed+1] {
		if strings.TrimSpace(l) != "" {
			excerpt = append(excerpt, strings.TrimRight(l, " \t"))
		}
	}
	if len(excerpt) > buildFailureExcerptLines {
		excerpt = append(excerpt[:buildFailureExcerptLines-1], "…", excerpt[len(excerpt)-1])
	}
	return strings.Join(excerpt, "\n")
}
