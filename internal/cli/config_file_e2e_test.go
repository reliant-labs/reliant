//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE2EConfigFileVariableIsTheProjectsOwn pins, on a scaffolded binary, that
// the config-file path variable belongs to the project: every forge binary
// read FORGE_CONFIG, so a shell that set it for one forge app made every other
// forge binary launched from that shell load the wrong file and die at
// startup. The project-global AppConfig now answers to <PROJECT>_CONFIG.
func TestE2EConfigFileVariableIsTheProjectsOwn(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "cfgfileapp", "--mod", "example.com/cfgfileapp", "--service", "item")
	projectDir := filepath.Join(dir, "cfgfileapp")
	addCorpusForgePkgReplace(t, projectDir)
	runCmd(t, projectDir, forgeBin, "generate")

	gen := readFileE2E(t, filepath.Join(projectDir, "pkg", "config", "config_gen.go"))
	if !strings.Contains(gen, `const ConfigPathEnv = "CFGFILEAPP_CONFIG"`) {
		t.Fatalf("config_gen.go does not name CFGFILEAPP_CONFIG:\n%s", gen)
	}

	bin := filepath.Join(projectDir, "cfgfileapp-bin")
	runCmd(t, projectDir, "go", "build", "-o", bin, "./cmd/cfgfileapp")

	help := runCmdOutput(t, projectDir, bin, "server", "--help")
	if !strings.Contains(help, "also settable via CFGFILEAPP_CONFIG") {
		t.Errorf("`server --help` must name CFGFILEAPP_CONFIG beside --config:\n%s", help)
	}
	if strings.Contains(help, "FORGE_CONFIG") {
		t.Errorf("`server --help` still names FORGE_CONFIG:\n%s", help)
	}

	// The regression: another app's variable is not this binary's. Boot
	// gets past the file layer and stops at the first REAL problem — the
	// required DATABASE_URL — instead of at a file it was never meant to read.
	out := serverBootOutput(t, projectDir, bin, "FORGE_CONFIG=/nonexistent/forge.yaml", "DATABASE_URL=")
	if strings.Contains(out, "does not exist") {
		t.Errorf("FORGE_CONFIG reached the binary's file layer:\n%s", out)
	}
	if !strings.Contains(out, "DATABASE_URL") {
		t.Errorf("boot should stop at the required DATABASE_URL, past the file layer:\n%s", out)
	}

	// Its own variable does reach it, and a missing file names the variable.
	out = serverBootOutput(t, projectDir, bin, "CFGFILEAPP_CONFIG=/nonexistent/cfgfileapp.yaml", "DATABASE_URL=")
	if !strings.Contains(out, "(from env CFGFILEAPP_CONFIG) does not exist") {
		t.Errorf("a missing CFGFILEAPP_CONFIG file must fail boot naming the variable:\n%s", out)
	}
}

// serverBootOutput runs `<bin> server` with env added and returns its combined
// output. It expects the boot to FAIL (each caller withholds a required
// setting), and fails the test if the process is still running after 20s.
func serverBootOutput(t *testing.T, projectDir, bin string, env ...string) string {
	t.Helper()
	cmd := exec.Command(bin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "ENVIRONMENT=development")
	cmd.Env = append(cmd.Env, env...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s server: %v", bin, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("`server` booted with %v; every caller withholds a required setting\n%s", env, out.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("`server` still running 20s after boot with %v\n%s", env, out.String())
	}
	return out.String()
}
