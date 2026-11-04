package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"
)

// The "generate hooks (forge.yaml)" step: the project's own commands, run
// after every step that writes forge's output and before the go mod tidy +
// go build that validate it (see config.GenerateHook for the contract, and
// the step's comment in generateSteps for why it sits that late).
//
// THEY ALWAYS RUN. There is no flag or variable that skips them, on purpose:
// a hook's output is generated code the rest of the tree compiles against,
// and a skipped hook leaves it stale while validation still passes — sqlc's
// output compiles against itself, so stale queries surface at run time, not
// at generate. Measured on reliant, `make sqlc` takes about 3s of a
// multi-minute generate, so a skip would buy nothing worth that risk. A hook
// whose tool is missing fails with the tool's name and the fix.
//
// FORGE_NO_GIT_HOOKS (formerly FORGE_NO_HOOKS) is a different thing: it stops
// forge configuring GIT hooks in the clone (githooks.go). Agents set the old
// name expecting it to skip these, so when either is set the step says, at
// the point they would look, that it does not.

// generateHooksStep names the step in the plan, in presets and in output.
const generateHooksStep = "generate hooks (forge.yaml)"

// generateHooksTidyStep tidies the root module again after the hooks ran.
const generateHooksTidyStep = "go mod tidy (after generate hooks)"

// hasGenerateHooks gates the step on the project declaring any hook.
func hasGenerateHooks(ctx *pipelineContext) bool {
	return ctx.Cfg != nil && len(ctx.Cfg.Generate.Hooks) > 0
}

// writesGenerateHooks declares every hook's `writes:` paths, so the pipeline
// journals them before the step runs and a failed generate restores them.
func writesGenerateHooks(ctx *pipelineContext) []string {
	if ctx.Cfg == nil {
		return nil
	}
	var out []string
	for _, h := range ctx.Cfg.Generate.Hooks {
		for _, w := range h.Writes {
			if p := cleanProjectRel(w); p != "" {
				out = append(out, p)
			}
		}
	}
	return dedupeSorted(out)
}

func stepGenerateHooks(ctx *pipelineContext) error {
	return runGenerateHooks(ctx.ProjectDir, ctx.Cfg.Generate.Hooks, os.Stdout, os.Stderr)
}

// generateHooksLogDir holds each hook's full combined output from the most
// recent run, one <name>.log per hook (project-relative, under the
// gitignored .forge/). A failed hook's error points here.
const generateHooksLogDir = ".forge/generate-hooks"

// hookOutputTailLines bounds the output a failed hook's error carries and
// the failure report repeats. A hook's diagnosis — a compiler error, a
// panic — sits at the end of what it printed, so it is the TAIL that is
// kept; the log file has the rest.
const hookOutputTailLines = 200

// hookFailureError is a generate hook that failed: which one, what it ran,
// and the tail of its combined stdout+stderr. The hook's output streams as
// it runs, but by the time the run ends it has scrolled away behind the
// rollback report (500 reverted files on reliant), and the error alone said
// `exit status 1` — the cause was invisible exactly where an agent looks
// for it. The pipeline's failure path repeats Tail last (reprintStepOutput)
// and writes it to the failed-generate error file.
type hookFailureError struct {
	Hook    string
	Command string
	// Tail is the last hookOutputTailLines lines the hook printed.
	Tail []string
	// Omitted counts the lines before Tail that are only in the log.
	Omitted int
	// LogPath is the project-relative full log; "" when it could not be
	// written.
	LogPath string
	err     error
}

func (e *hookFailureError) Error() string {
	msg := fmt.Sprintf("generate hook %q (%s) failed: %v", e.Hook, e.Command, e.err)
	switch {
	case len(e.Tail) == 0:
		msg += " — it printed nothing"
	case e.LogPath != "":
		msg += fmt.Sprintf(" — the last %d line(s) of its output are repeated at the end of this report; full output: %s", len(e.Tail), e.LogPath)
	default:
		msg += fmt.Sprintf(" — the last %d line(s) of its output are repeated at the end of this report", len(e.Tail))
	}
	return msg
}

func (e *hookFailureError) Unwrap() error { return e.err }

// runGenerateHooks runs hooks in declaration order and stops at the first
// failure. A failed hook fails the generate: its output is now stale or
// partial, and the validation build that follows would report the symptom
// somewhere far from the cause.
//
// Each hook's output streams to stdout/stderr as it runs AND is captured:
// in full to generateHooksLogDir/<name>.log, and its last
// hookOutputTailLines lines into the *hookFailureError a failure returns.
func runGenerateHooks(projectDir string, hooks []config.GenerateHook, stdout, stderr io.Writer) error {
	fmt.Fprintf(stdout, "🪝 Running %d generate hook(s) from forge.yaml generate.hooks: %s\n", len(hooks), hookNames(hooks))
	if v := gitHooksOptOut(); v != "" {
		fmt.Fprintf(stdout, "   %s is set: it stops forge configuring git hooks in this clone and does not skip these — generate hooks always run.\n", v)
	}
	for _, h := range hooks {
		dir := projectDir
		if h.Dir != "" {
			dir = filepath.Join(projectDir, filepath.FromSlash(h.Dir))
		}
		argv := strings.Join(h.Command, " ")
		fmt.Fprintf(stdout, "🪝 generate hook %q: %s\n", h.Name, argv)
		start := time.Now()
		cmd := exec.Command(h.Command[0], h.Command[1:]...)
		cmd.Dir = dir
		cmd.Env = goexec.Env()
		// A hook runs the same pinned codegen tools as forge's own steps:
		// go.mod's buf, protoc-gen-go and protoc-gen-connect-go first on its
		// PATH (and a `buf` hook execs the pinned buf itself). A hook is
		// usually a second buf template or a generator over the same protos,
		// and on PATH's tools it wrote bytes the committed tree, generated
		// with the pins, did not match.
		if err := withPinnedTools(cmd, projectDir); err != nil {
			return fmt.Errorf("generate hook %q: %w", h.Name, err)
		}
		capture, logRel := newHookCapture(projectDir, h.Name)
		cmd.Stdout, cmd.Stderr = teeHookOutput(stdout, stderr, capture)
		runErr := cmd.Run()
		capture.close()
		if runErr != nil {
			if errors.Is(runErr, exec.ErrNotFound) {
				return fmt.Errorf("generate hook %q: %q was not found on PATH — install it, or remove the hook from forge.yaml generate.hooks", h.Name, h.Command[0])
			}
			tail, omitted := capture.tail()
			return &hookFailureError{Hook: h.Name, Command: argv, Tail: tail, Omitted: omitted, LogPath: logRel, err: runErr}
		}
		fmt.Fprintf(stdout, "   ✓ generate hook %q ran (%.1fs)\n", h.Name, time.Since(start).Seconds())
	}
	return nil
}

// teeHookOutput returns the writers a hook's stdout and stderr go to: the
// caller's, plus capture. When the caller passes one writer for both, both
// streams get the SAME writer back, so exec.Cmd serializes them onto it as
// it does for an untee'd shared writer.
func teeHookOutput(stdout, stderr io.Writer, capture *hookCapture) (io.Writer, io.Writer) {
	out := io.MultiWriter(stdout, capture)
	if sameWriter(stdout, stderr) {
		return out, out
	}
	return out, io.MultiWriter(stderr, capture)
}

// sameWriter reports a == b without panicking on an uncomparable dynamic
// type (which is then never "the same").
func sameWriter(a, b io.Writer) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// hookCapture records one hook's combined output: every byte to its log
// file, and the last hookOutputTailLines complete lines in memory. Both
// streams write to it, from exec's copying goroutines, so it locks.
type hookCapture struct {
	mu      sync.Mutex
	log     *os.File
	lines   []string
	partial []byte
	total   int
}

// hookPartialLineCap bounds a line still being written, so a hook that
// prints megabytes without a newline cannot grow the buffer unbounded.
const hookPartialLineCap = 64 << 10

// newHookCapture opens the hook's log under generateHooksLogDir, returning
// its project-relative path ("" when it could not be created: the tail is
// still captured).
func newHookCapture(projectDir, hook string) (*hookCapture, string) {
	c := &hookCapture{}
	rel := filepath.ToSlash(filepath.Join(generateHooksLogDir, hookLogName(hook)))
	abs := filepath.Join(projectDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return c, ""
	}
	f, err := os.Create(abs)
	if err != nil {
		return c, ""
	}
	c.log = f
	return c, rel
}

// hookLogName is the log file name for a hook: its name with anything that
// is not safe in a file name replaced.
func hookLogName(hook string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, hook)
	if strings.Trim(safe, "._") == "" {
		safe = "hook"
	}
	return safe + ".log"
}

func (c *hookCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.log != nil {
		_, _ = c.log.Write(p)
	}
	c.partial = append(c.partial, p...)
	for {
		i := bytes.IndexByte(c.partial, '\n')
		if i < 0 {
			break
		}
		c.push(string(c.partial[:i]))
		c.partial = c.partial[i+1:]
	}
	if len(c.partial) > hookPartialLineCap {
		c.push(string(c.partial))
		c.partial = nil
	}
	return len(p), nil
}

func (c *hookCapture) push(line string) {
	c.total++
	c.lines = append(c.lines, strings.TrimRight(line, "\r"))
	if len(c.lines) > hookOutputTailLines {
		c.lines = c.lines[len(c.lines)-hookOutputTailLines:]
	}
}

// close flushes an unterminated last line and closes the log.
func (c *hookCapture) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.partial) > 0 {
		c.push(string(c.partial))
		c.partial = nil
	}
	if c.log != nil {
		_ = c.log.Close()
		c.log = nil
	}
}

// tail returns the captured last lines and how many earlier ones were
// dropped from memory.
func (c *hookCapture) tail() ([]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.lines), c.total - len(c.lines)
}

// hookNames lists the hooks by name, in declaration order.
func hookNames(hooks []config.GenerateHook) string {
	names := make([]string, len(hooks))
	for i, h := range hooks {
		names[i] = h.Name
	}
	return strings.Join(names, ", ")
}

// reportGenerateHooksNotRun says so when a run narrowed by --steps or
// --templates-only left out the generate hooks of a project that declares
// some. Without it the run was silent about them, and its output looked like
// a full generate whose hook outputs were current.
func reportGenerateHooksNotRun(w io.Writer, ctx *pipelineContext, steps []GenStep, flags pipelineFlags) {
	if !hasGenerateHooks(ctx) {
		return
	}
	for _, s := range steps {
		if s.Name == generateHooksStep {
			return
		}
	}
	var writes string
	if paths := writesGenerateHooks(ctx); len(paths) > 0 {
		writes = " (" + strings.Join(paths, ", ") + ")"
	}
	fmt.Fprintf(w, "⏩ generate hooks NOT run (%s): %s — their output%s was not refreshed; a full `forge generate` runs them.\n",
		strings.TrimSpace(planFlagEcho(flags)), hookNames(ctx.Cfg.Generate.Hooks), writes)
}
