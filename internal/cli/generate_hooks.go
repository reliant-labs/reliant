package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/goexec"
)

// The "generate hooks (forge.yaml)" step: the project's own commands, run
// after forge's code generation and before the go mod tidy + go build that
// validate it (see config.GenerateHook for the contract).
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

// runGenerateHooks runs hooks in declaration order and stops at the first
// failure. A failed hook fails the generate: its output is now stale or
// partial, and the validation build that follows would report the symptom
// somewhere far from the cause.
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
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return fmt.Errorf("generate hook %q: %q was not found on PATH — install it, or remove the hook from forge.yaml generate.hooks", h.Name, h.Command[0])
			}
			return fmt.Errorf("generate hook %q (%s) failed: %w", h.Name, argv, err)
		}
		fmt.Fprintf(stdout, "   ✓ generate hook %q ran (%.1fs)\n", h.Name, time.Since(start).Seconds())
	}
	return nil
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
