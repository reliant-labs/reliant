package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

func isolatedKCLProject(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"forge.yaml":              "name: " + name + "\nmodule: example.com/" + name + "\n",
		"deploy/kcl/kcl.mod":      "[package]\nname = \"isolation\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n",
		"payload.txt":             name,
		"deploy/kcl/dev/values.k": "import file\nvalue = file.read(\"payload.txt\")\n",
		"deploy/kcl/dev/main.k": `import kcl_plugin.forge as fp
_port = fp.resolve_port("isolation-test", 0)
_block = fp.allocate_port(31000, "isolated")
_written = fp.write_file("generated/config.txt", value)
output = {value = value, port = _port, block = _block, option = option("custom", default="default")}
`,
	} {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestIsolatedKCLProjectRootAndCapabilities(t *testing.T) {
	if testing.Short() {
		t.Skip("executes isolated KCL child processes; runs in task test")
	}
	resetDevStackGlobals(t)
	dir := isolatedKCLProject(t, "rooted")
	cwd, _ := os.Getwd()
	for _, mode := range []string{"read", "inspect", "claim"} {
		t.Run(mode, func(t *testing.T) {
			rememberKCLContext(dir, "dev", kclRenderContext{Blocks: mode, Materialize: mode == "claim"})
			out, err := renderKCLRaw(t.Context(), dir, "dev", `custom="from-parent"`)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Output struct {
					Value, Option string
					Port, Block   int
				}
			}
			if err := json.Unmarshal(out, &doc); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			if doc.Output.Value != "rooted" || doc.Output.Option != "from-parent" || doc.Output.Port == 0 || doc.Output.Block < 31000 {
				t.Fatalf("child result: %s", out)
			}
			b, err := os.ReadFile(filepath.Join(dir, "generated/config.txt"))
			if mode == "claim" {
				if err != nil || string(b) != "rooted" {
					t.Fatalf("launch materializer not preserved: %q, %v", b, err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("read-only evaluation wrote file: %q, %v", b, err)
			}
			_, portErr := os.Stat(filepath.Join(dir, ".forge", "host-ports.json"))
			_, blockErr := os.Stat(filepath.Join(dir, ".forge", "blocks.json"))
			if mode == "claim" {
				if portErr != nil || blockErr != nil {
					t.Fatalf("launch port/block state missing: %v, %v", portErr, blockErr)
				}
			} else if !os.IsNotExist(portErr) || !os.IsNotExist(blockErr) {
				t.Fatalf("read/inspect claimed state: %v, %v", portErr, blockErr)
			}
			if got, _ := os.Getwd(); got != cwd {
				t.Fatalf("parent cwd changed: %s != %s", got, cwd)
			}
		})
	}
}

func TestIsolatedKCLConcurrentProjectsDoNotChangeParentCwd(t *testing.T) {
	if testing.Short() {
		t.Skip("executes concurrent isolated KCL child processes; runs in task test")
	}
	a, b := isolatedKCLProject(t, "alpha"), isolatedKCLProject(t, "beta")
	cwd, _ := os.Getwd()
	var wg sync.WaitGroup
	for _, dir := range []string{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := renderKCLRaw(t.Context(), dir, "dev")
			if err != nil {
				t.Error(err)
				return
			}
			want, _ := os.ReadFile(filepath.Join(dir, "payload.txt"))
			var doc struct{ Output struct{ Value string } }
			if err := json.Unmarshal(out, &doc); err != nil || doc.Output.Value != string(want) {
				t.Errorf("cross-project result %s: %s (%v)", dir, out, err)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		case <-time.After(time.Millisecond):
			if got, _ := os.Getwd(); got != cwd {
				t.Fatalf("concurrent child leaked cwd: %s", got)
			}
		}
	}
}

func TestIsolatedKCLCancellationAndErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("executes isolated KCL child processes; runs in task test")
	}
	dir := isolatedKCLProject(t, "cancelled")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := renderKCLRaw(ctx, dir, "dev"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled render: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "payload.txt")); err != nil {
		t.Fatal(err)
	}
	_, err := renderKCLRaw(t.Context(), dir, "dev")
	if err == nil || !strings.Contains(err.Error(), "payload.txt") {
		t.Fatalf("missing file diagnostic lost: %v", err)
	}
}

func TestIsolatedKCLEmbeddedRouteUsesExistingEvalProtocol(t *testing.T) {
	if testing.Short() {
		t.Skip("executes an embedded-route KCL child process; runs in task test")
	}
	dir := isolatedKCLProject(t, "embedded")
	mount := &cobra.Command{Use: "reliant"}
	forge := newTestRootCmd(t)
	mount.AddCommand(forge)
	cmdutil.RecordCmdRoute(forge)
	out, err := renderKCLRaw(t.Context(), dir, "dev")
	if err != nil || !json.Valid(out) {
		t.Fatalf("embedded eval route: %s, %v", out, err)
	}
}

func TestIsolatedKCLCancellationReapsStartedChild(t *testing.T) {
	if testing.Short() {
		t.Skip("executes a cancellable child process; runs in task test")
	}
	dir := isolatedKCLProject(t, "cancel-active")
	prev := newKCLEvalProcess
	t.Cleanup(func() { newKCLEvalProcess = prev })
	var child *exec.Cmd
	started := make(chan struct{})
	newKCLEvalProcess = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		child = exec.CommandContext(ctx, "sleep", "60")
		close(started)
		return child
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := renderKCLRaw(ctx, dir, "dev"); done <- err }()
	<-started
	// CommandContext's cancellation is also required after Start, not just
	// before it. Wait until the child is running without reading Cmd fields.
	time.AfterFunc(50*time.Millisecond, cancel)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || child.ProcessState == nil {
			t.Fatalf("child was not cancelled and waited: %v, %+v", err, child.ProcessState)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled evaluation did not reap child")
	}
}

func TestIsolatedKCLAbsoluteAndRelativeProjectFlag(t *testing.T) {
	if testing.Short() {
		t.Skip("executes isolated KCL child processes; runs in task test")
	}
	dir := isolatedKCLProject(t, "projectflag")
	cwd, _ := os.Getwd()
	rel, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{dir, rel} {
		cmd := newTestRootCmd(t)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs([]string{"-C", root, "kcl", "eval", "deploy/kcl/dev", "-S", "output.value", "--format", "raw"})
		if err := cmd.ExecuteContext(t.Context()); err != nil {
			t.Fatal(err)
		}
		if out.String() != "projectflag" {
			t.Fatalf("-C %q output: %q", root, out.String())
		}
		if got, _ := os.Getwd(); got != cwd {
			t.Fatalf("-C changed parent cwd: %s", got)
		}
	}
}

func TestIsolatedKCLCommandClearsPriorLaunchCapabilities(t *testing.T) {
	dir := t.TempDir()
	rememberKCLContext(dir, "dev", kclRenderContext{Blocks: "claim", Materialize: true})
	root := newTestRootCmd(t)
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"version"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := rememberedKCLContext(dir, "dev"); got != (kclRenderContext{}) {
		t.Fatalf("launch capability leaked across commands: %+v", got)
	}
}

func TestIsolatedKCLProjectOptionsCannotGrantWriteCapabilities(t *testing.T) {
	if testing.Short() {
		t.Skip("executes isolated KCL child processes; runs in task test")
	}
	dir := isolatedKCLProject(t, "untrusted")
	// Neither ambient settings nor arbitrary project -D bindings are a
	// capability channel. Only explicit operation-selected argv may arm it.
	t.Setenv("FORGE_KCL_RENDER_CONTEXT", "claim")
	t.Setenv("FORGE_KCL_MATERIALIZE", "true")
	out, err := renderKCLRaw(t.Context(), dir, "dev", `render_context="claim"`, `materialize=True`)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) {
		t.Fatalf("stdout is not one JSON document: %s", out)
	}
	for _, rel := range []string{"generated/config.txt", ".forge/host-ports.json", ".forge/blocks.json"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatalf("untrusted options wrote %s: %v", rel, err)
		}
	}
}

func TestIsolatedKCLContextRefusesInvalidCapabilities(t *testing.T) {
	for _, tc := range []struct {
		mode        string
		materialize bool
	}{{"", false}, {"write", false}, {"read", true}, {"inspect", true}} {
		if err := armChildKCLContext(t.TempDir(), "dev", tc.mode, tc.materialize, false); err == nil {
			t.Fatalf("accepted capability %+v", tc)
		}
	}
}
