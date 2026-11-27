package cli

// Environment KCL must read project-relative files without changing an
// embedder's process cwd. Reuse kcl eval's JSON/-D protocol in a child whose
// cwd is the project. Capabilities are chosen by the operation, not KCL or
// ambient environment variables.
import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/internal/kclplugin"
)

type kclRenderContext struct {
	Blocks      string // empty: unarmed; inspect: existing blocks; claim: launch
	Materialize bool
	Active      bool // operation armed dev-stack git facts
}

var kclContexts = struct {
	sync.Mutex
	byTarget map[string]kclRenderContext
}{byTarget: map[string]kclRenderContext{}}

func kclContextKey(projectDir, env string) string {
	abs, _ := filepath.Abs(projectDir)
	return filepath.Clean(abs) + "\x00" + env
}

func rememberKCLContext(projectDir, env string, c kclRenderContext) {
	kclContexts.Lock()
	defer kclContexts.Unlock()
	kclContexts.byTarget[kclContextKey(projectDir, env)] = c
}

func rememberedKCLContext(projectDir, env string) kclRenderContext {
	kclContexts.Lock()
	defer kclContexts.Unlock()
	return kclContexts.byTarget[kclContextKey(projectDir, env)]
}

// Seam for deterministic cancellation tests; production always execs itself.
var newKCLEvalProcess = exec.CommandContext

func evalKCLSubprocess(ctx context.Context, projectDir, source, env string, options []string) ([]byte, error) {
	return evalKCLCommand(ctx, projectDir, source, env, options, nil, "json")
}

func evalKCLCommand(ctx context.Context, projectDir, source, env string, options, selectors []string, format string) ([]byte, error) {
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(source) {
		source, err = filepath.Abs(source)
	}
	if err != nil {
		return nil, err
	}
	route, err := forgeExecCommand()
	if err != nil {
		return nil, err
	}
	c := rememberedKCLContext(root, env)
	// Internal re-entry must stay on this binary, not delegate to an older
	// project pin that cannot understand the render-context flags.
	args := append(slices.Clone(route[1:]), "-C", root, "kcl", "eval", source, "--allow-version-skew", "--format", format, "--process-rooted")
	for _, selector := range selectors {
		args = append(args, "-S", selector)
	}
	mode := c.Blocks
	if mode == "" {
		mode = "read"
	}
	if env != "" {
		args = append(args, "--render-env", env, "--render-context", mode)
		if c.Active {
			args = append(args, "--render-active")
		}
		if c.Materialize {
			args = append(args, "--materialize")
		}
	}
	for _, option := range options {
		args = append(args, "-D", option)
	}
	cmd := newKCLEvalProcess(ctx, route[0], args...)
	cmd.Dir = root
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A cancelled native evaluation must not leave a child or an inherited
	// pipe behind. CommandContext kills this child and Run always reaps it.
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("evaluate KCL in %s: %w\n%s", root, err, kclChildFailureOutput(stderr.Bytes(), stdout.Bytes()))
	}
	if stderr.Len() > 0 {
		_, _ = os.Stderr.Write(stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

// kclChildFailureTailLines bounds how much of a failed child's stdout is
// quoted when it said nothing on stderr.
const kclChildFailureTailLines = 20

// kclChildFailureOutput is what a failed `forge kcl eval` child said, for the
// error that reports it. It is the child's stderr, where kcl and forge print
// the failure. When that is empty, the failure is wherever the child did
// print — the tail of its stdout — and when the child printed nothing at all
// the error says so, rather than ending at a bare "exit status 1" that reads
// as if forge dropped the reason (forge bug reports c8b7a3f6, aef6d25f).
func kclChildFailureOutput(stderr, stdout []byte) string {
	if len(bytes.TrimSpace(stderr)) > 0 {
		return string(stderr)
	}
	if out := bytes.TrimSpace(stdout); len(out) > 0 {
		lines := strings.Split(string(out), "\n")
		if len(lines) > kclChildFailureTailLines {
			lines = lines[len(lines)-kclChildFailureTailLines:]
		}
		return kclChildStdoutTailHeader + "\n" + strings.Join(lines, "\n") + "\n"
	}
	return kclChildSilentNote + "\n"
}

const (
	kclChildStdoutTailHeader = "(the KCL child printed nothing on stderr; the end of its stdout:)"
	kclChildSilentNote       = "(the KCL child printed nothing on stderr or stdout before it exited)"
)

func armChildKCLContext(projectDir, env, mode string, materialize, active bool) error {
	if mode != "read" && mode != "inspect" && mode != "claim" {
		return fmt.Errorf("invalid --render-context %q: use read, inspect or claim", mode)
	}
	if materialize && mode != "claim" {
		return fmt.Errorf("--materialize requires --render-context claim")
	}
	armReadOnlyKCLContext(projectDir)
	if active {
		devstack.SetActive(devstack.Resolve(projectDir))
	}
	kclplugin.UseStackPortStore(projectDir, env, mode != "claim")
	switch mode {
	case "inspect":
		kclplugin.UseBlockAllocator(inspectBlockAllocator(projectDir, env, os.Stderr))
	case "claim":
		// Reuse the launch arming path; renderToLaunch does not probe KCL,
		// so the child cannot recurse into another evaluation process.
		_, restore := activateDevStack(context.Background(), projectDir, env, renderToLaunch, claimNewBlocks)
		defer restore()
	}
	if materialize {
		armMaterializer(projectDir, env)
	}
	return nil
}
