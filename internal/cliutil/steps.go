package cliutil

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// StepRunner runs a command's steps — install the dependencies, generate the
// code, tidy the module — and reports each as ONE line:
//
//	✓ frontend dependencies — 631 packages installed (6.1s)
//
// Everything a step prints meanwhile, forge's own progress lines and every
// child process it starts (pnpm, go mod tidy, buf, an embedded postgres), is
// appended to a log file instead of the terminal. A child's progress bars,
// deprecation warnings and dependency listings are its own business; a person
// reading `forge project new` needs to know which steps ran and whether they
// worked. When one did not, the runner prints the tail of what it said and
// where the rest is:
//
//	✗ frontend dependencies (31.2s)
//	    │ ERR_PNPM_FETCH_404  GET https://registry.npmjs.org/nxet: Not Found - 404
//	    full output: shop/.forge/logs/project-new.log
//
// A line the step printed that starts with ⚠️ — forge's mark for something
// the user should read — is repeated under the step's line, so capture never
// hides a warning.
//
// Verbose streams everything instead, exactly as the steps print it, with
// the step line after each.
//
// The capture is process-wide (redirectOutput), so steps run one at a time.
type StepRunner struct {
	label    string // heartbeat prefix: "project new"
	verbose  bool
	interval time.Duration // first heartbeat; a test shortens it

	file *os.File // the log; nil until the first quiet step
	path string
}

// Step is one unit a StepRunner reports.
type Step struct {
	// Title names the step in its one line: "frontend dependencies".
	Title string
	// Header is printed before the step under verbose — the line the
	// command printed before it had a step runner. Empty prints none.
	Header string
	// Run does the work.
	Run func() error
	// Summary, when set, reduces the step's captured output to the few
	// words after its title ("631 packages installed"). It sees nothing
	// under verbose, where nothing is captured.
	Summary func(output string) string
}

// stepTailLines is how much of a failed step's output is shown.
const stepTailLines = 15

// NewStepRunner returns a runner for the command named label. The log starts
// in the temp directory, because the step that creates the command's own
// directory may fail and take that directory with it; MoveLog settles it
// once there is somewhere for it to stay.
func NewStepRunner(label string, verbose bool) *StepRunner {
	return &StepRunner{label: label, verbose: verbose, interval: defaultHeartbeatInterval}
}

// LogPath is where the captured output is, or "" when nothing was captured.
func (r *StepRunner) LogPath() string {
	if r.file == nil {
		return ""
	}
	return r.path
}

// PrintLogPath prints, once the steps are done, where their output is —
// relative to the working directory when it is under it. Nothing when
// nothing was captured (verbose).
func (r *StepRunner) PrintLogPath(w io.Writer) {
	if p := r.LogPath(); p != "" {
		fmt.Fprintf(w, "  full output of each step: %s\n", displayPath(p))
	}
}

// Run runs s and reports it. The error is s.Run's, returned so the caller
// decides whether the command goes on.
func (r *StepRunner) Run(s Step) error {
	if r.verbose {
		return r.runStreaming(s)
	}
	if err := r.openLog(); err != nil {
		// No log, no capture: stream rather than lose the output.
		return r.runStreaming(s)
	}
	out, errOut := os.Stdout, os.Stderr
	offset, _ := r.file.Seek(0, io.SeekEnd)
	fmt.Fprintf(r.file, "\n── %s ── %s\n", s.Title, time.Now().Format(time.RFC3339))

	began := time.Now()
	err := r.captured(s.Title, s.Run, errOut)
	took := time.Since(began)

	output := r.readFrom(offset)
	if err != nil {
		fmt.Fprintf(r.file, "── %s: failed after %s: %v\n", s.Title, formatStepDuration(took), err)
	} else {
		fmt.Fprintf(r.file, "── %s: done in %s\n", s.Title, formatStepDuration(took))
	}

	if err != nil {
		fmt.Fprintf(out, "  ✗ %s (%s)\n", s.Title, formatStepDuration(took))
		for _, line := range TailLines(output, stepTailLines) {
			fmt.Fprintf(out, "    │ %s\n", line)
		}
		fmt.Fprintf(out, "    full output: %s\n", displayPath(r.path))
		return err
	}
	line := "  ✓ " + s.Title
	if s.Summary != nil {
		if sum := s.Summary(output); sum != "" {
			line += " — " + sum
		}
	}
	fmt.Fprintf(out, "%s (%s)\n", line, formatStepDuration(took))
	for _, w := range warningLines(output) {
		fmt.Fprintf(out, "    %s\n", w)
	}
	return nil
}

// captured runs fn with the process's output in the log and title's
// heartbeat on the terminal the process had before.
func (r *StepRunner) captured(title string, fn func() error, terminal io.Writer) error {
	restore := redirectOutput(r.file)
	defer restore()
	stop := HeartbeatEvery(terminal, r.label+": "+title, r.interval)
	defer stop()
	return fn()
}

// runStreaming is Run with nothing captured: the header, the step's own
// output as it prints it, then the step line.
func (r *StepRunner) runStreaming(s Step) error {
	if s.Header != "" {
		fmt.Fprintln(os.Stdout, s.Header)
	}
	began := time.Now()
	// Plain lines even on a terminal: the step's output streams past, and
	// a status line redrawn in place would be glued onto it.
	stop := heartbeat(os.Stderr, r.label+": "+s.Title, r.interval, false)
	err := s.Run()
	stop()
	mark := "✓"
	if err != nil {
		mark = "✗"
	}
	fmt.Fprintf(os.Stdout, "  %s %s (%s)\n", mark, s.Title, formatStepDuration(time.Since(began)))
	return err
}

// MoveLog moves the log to path (creating its directory) and keeps appending
// there. Before any step was captured it only sets where the log will be.
func (r *StepRunner) MoveLog(path string) error {
	if r.verbose {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if r.file == nil {
		r.path = path
		return nil
	}
	if r.path == path {
		return nil
	}
	if err := os.Rename(r.path, path); err == nil {
		r.path = path // the open descriptor follows the file
		return nil
	}
	// Rename refused (another volume; Windows will not rename an open
	// file): copy, then continue in the copy.
	dst, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := r.file.Seek(0, io.SeekStart); err == nil {
		_, err = io.Copy(dst, r.file)
		if err != nil {
			_ = dst.Close()
			return err
		}
	}
	old := r.path
	_ = r.file.Close()
	_ = os.Remove(old)
	r.file, r.path = dst, path
	return nil
}

// Close closes the log.
func (r *StepRunner) Close() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *StepRunner) openLog() error {
	if r.file != nil {
		return nil
	}
	if r.path != "" {
		f, err := os.OpenFile(r.path, os.O_CREATE|os.O_TRUNC|os.O_RDWR|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		r.file = f
		return nil
	}
	f, err := os.CreateTemp("", "forge-"+strings.ReplaceAll(r.label, " ", "-")+"-*.log")
	if err != nil {
		return err
	}
	r.file, r.path = f, f.Name()
	return nil
}

// readFrom returns the log's content from offset on.
func (r *StepRunner) readFrom(offset int64) string {
	f, err := os.Open(r.path)
	if err != nil {
		return ""
	}
	defer f.Close() //nolint:errcheck // read-only
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	b, _ := io.ReadAll(f) // a short read still has the tail worth showing
	return string(b)
}

// ansiEscape matches the terminal escape sequences a child colours or moves
// the cursor with.
var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[\x20-\x2f]*[\x40-\x7e]`)

// TailLines returns the last n non-blank lines of output as a terminal would
// have left them: colour codes dropped, and of a line a progress bar redrew
// with "\r", only what it said last. Each is cut to 200 characters.
func TailLines(output string, n int) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		if i := strings.LastIndex(line, "\r"); i >= 0 {
			line = line[i+1:]
		}
		line = strings.TrimRight(ansiEscape.ReplaceAllString(line, ""), " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if r := []rune(line); len(r) > 200 {
			line = string(r[:199]) + "…"
		}
		lines = append(lines, line)
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// warningLines returns the lines of output forge marked as warnings (⚠️),
// each once, in order.
func warningLines(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range TailLines(output, int(^uint(0)>>1)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "⚠") || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

// formatStepDuration is 1.2s under ten seconds, whole seconds above.
func formatStepDuration(d time.Duration) string {
	if d < 10*time.Second {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// displayPath is path relative to the working directory when it is under
// it, else as given.
func displayPath(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}
