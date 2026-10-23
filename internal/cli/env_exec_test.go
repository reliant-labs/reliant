package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/secrets"
)

// TestEnvExecHelper is a hermetic local subprocess fixture; its inputs are
// written by the test, not read from the repository by the child.
func TestEnvExecHelper(t *testing.T) {
	if os.Getenv("FORGE_EXEC_HELPER") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	args = args[1:]
	if len(args) > 0 && args[0] == "signal-child" {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		fmt.Println("ready")
		<-ch
		os.Exit(37)
	}
	if len(args) > 0 && args[0] == "signal-parent" {
		c := exec.Command(os.Args[0], "-test.run=^TestEnvExecHelper$", "--", "signal-child")
		c.Env = os.Environ()
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		err := runEnvExecChild(context.Background(), c)
		var coded interface{ ExitCode() int }
		if errors.As(err, &coded) {
			os.Exit(coded.ExitCode())
		}
		os.Exit(1)
	}
	if len(args) > 0 && args[0] == "exit" {
		code, _ := strconv.Atoi(args[1])
		os.Exit(code)
	}
	input, _ := io.ReadAll(os.Stdin)
	values := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			values[k] = v
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(struct {
		Env   map[string]string
		Args  []string
		Input string
	}{values, args, string(input)})
	fmt.Fprint(os.Stderr, "fixture stderr")
	os.Exit(0)
}

func execHelperArgs(t *testing.T, args ...string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return append([]string{exe, "-test.run=^TestEnvExecHelper$", "--"}, args...)
}

func executeEnvFixture(t *testing.T, prefix, argv []string) (string, string, error) {
	t.Helper()
	cmd := newEnvCmd()
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetIn(strings.NewReader("fixture stdin"))
	cmd.SetArgs(append(append([]string{"exec"}, prefix...), append([]string{"--"}, argv...)...))
	err := cmd.Execute()
	return out.String(), errout.String(), err
}

func TestEnvExecMatchesConfigAndLiteralArgv(t *testing.T) {
	withKCLFixture(t, envConfigFixture)
	t.Setenv("FORGE_EXEC_HELPER", "1")
	t.Setenv("LOG_LEVEL", "ambient-must-lose")
	t.Setenv("EXEC_INHERITED", "kept")
	// The running stack's persisted coordinates must win in BOTH commands.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	statePath, err := upEnvStatePath(projectIDForDir(projectDirForKCL()), "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"database_url":"postgres://127.0.0.1:25432/launched"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var report envConfigReport
	if err := json.Unmarshal([]byte(runEnvConfig(t, "dev", "--workload", "api", "--json")), &report); err != nil {
		t.Fatal(err)
	}
	if report.Workloads[0].Env["DATABASE_URL"] != "postgres://127.0.0.1:25432/launched" {
		t.Fatal("launch coordinate not overlaid")
	}
	argv := []string{"a b", "", "; touch SHOULD_NOT_EXIST", "$(echo injection)", "--workload", "migrate", "a=b", "two\nlines"}
	out, errout, err := executeEnvFixture(t, []string{"dev"}, execHelperArgs(t, argv...))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Env   map[string]string
		Args  []string
		Input string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("helper output: %v: %s", err, out)
	}
	for key, want := range report.Workloads[0].Env {
		if got.Env[key] != want {
			t.Errorf("env differs for %s", key)
		}
	}
	if !reflect.DeepEqual(got.Args, argv) {
		t.Fatalf("argv boundaries lost: %#v", got.Args)
	}
	if got.Env["EXEC_INHERITED"] != "kept" || got.Input != "fixture stdin" || errout != "fixture stderr" {
		t.Fatalf("stdio/inheritance mismatch: %q %q", got.Input, errout)
	}
	if _, err := os.Stat("SHOULD_NOT_EXIST"); !os.IsNotExist(err) {
		t.Fatal("argv was interpreted by a shell")
	}
}

func TestEnvExecRuntimeRefusalBeforeSecrets(t *testing.T) {
	for _, rt := range []string{"hosted", "cluster", "build-only"} {
		t.Run(rt, func(t *testing.T) {
			// An invalid provider must never be instantiated for a remote workload.
			fixture := fmt.Sprintf(`{"output":{"secret_provider":{"type":"DO-NOT-RESOLVE"},"workloads":[{"name":"api","kind":"service","runtime":{"type":%q},"spec":{"kind":"service","env":[{"name":"TOKEN","value":"never-log-me"}]}}]}}`, rt)
			withKCLFixture(t, fixture)
			old := pullLocalSecretsFor
			pullLocalSecretsFor = func(_ctx context.Context, _env string, _e *KCLEntities) (map[string]string, error) {
				t.Fatal("remote secrets accessed")
				return nil, nil
			}
			defer func() { pullLocalSecretsFor = old }()
			out, errout, err := executeEnvFixture(t, []string{"prod"}, []string{"never-start-this"})
			if err == nil || !strings.Contains(err.Error(), "only supports OnHost/OnCompose") {
				t.Fatalf("wanted runtime refusal, got %v", err)
			}
			if strings.Contains(out+errout+err.Error(), "never-log-me") {
				t.Fatal("secret appeared in diagnostics")
			}
		})
	}
}

func TestEnvExecExitCode(t *testing.T) {
	withKCLFixture(t, envConfigFixture)
	t.Setenv("FORGE_EXEC_HELPER", "1")
	for _, code := range []int{0, 7, 42, 123} {
		_, _, err := executeEnvFixture(t, []string{"dev", "--workload", "api"}, execHelperArgs(t, "exit", strconv.Itoa(code)))
		if code == 0 {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		var coded interface{ ExitCode() int }
		if !errors.As(err, &coded) || coded.ExitCode() != code {
			t.Fatalf("exit %d: %v", code, err)
		}
	}
	_, _, err := executeEnvFixture(t, []string{"dev"}, []string{filepath.Join(t.TempDir(), "not-found")})
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 127 {
		t.Fatalf("missing executable: %v", err)
	}
}

func TestEnvExecRegistered(t *testing.T) {
	cmd, _, err := newEnvCmd().Find([]string{"exec"})
	if err != nil || cmd.Name() != "exec" {
		t.Fatalf("env exec is not registered: %v", err)
	}
}

func TestEnvExecDefaultAndValidation(t *testing.T) {
	withKCLFixture(t, strings.ReplaceAll(envConfigFixture, `"api"`, `"renamed-api"`))
	_, _, err := executeEnvFixture(t, []string{"dev"}, []string{"must-not-run"})
	if err == nil || !strings.Contains(err.Error(), "--workload") {
		t.Fatalf("default must not guess: %v", err)
	}
	for _, args := range [][]string{{"dev"}, {"dev", "echo"}, {"dev", "--"}, {"--", "dev", "echo"}} {
		cmd := newEnvExecCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("invalid syntax accepted: %q", args)
		}
	}
	e := &KCLEntities{Workloads: []WorkloadEntity{{Name: "api"}, {Name: "API"}}}
	if _, err := selectExecWorkload(e, "api"); err == nil {
		t.Fatal("ambiguous case-folded name accepted")
	}
}

func TestEnvExecComposeUsesHostLauncherValues(t *testing.T) {
	withKCLFixture(t, `{"output":{"workloads":[{"name":"api","kind":"service","runtime":{"type":"compose","env_file":"local.env","env":{"DATABASE_URL":"postgres://127.0.0.1:15432/dev","COMPOSE_PORT":"15432"}},"spec":{"kind":"service","env":[{"name":"DATABASE_URL","value":"postgres://container-dns:5432/dev"}]}}]}}`)
	if err := os.WriteFile("local.env", []byte("FROM_FILE=file\nDATABASE_URL=not-the-declared-one\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_EXEC_HELPER", "1")
	t.Setenv("COMPOSE_PORT", "wrong")
	var report envConfigReport
	if err := json.Unmarshal([]byte(runEnvConfig(t, "dev", "--workload", "api", "--json")), &report); err != nil {
		t.Fatal(err)
	}
	out, _, err := executeEnvFixture(t, []string{"dev"}, execHelperArgs(t))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Env map[string]string }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	for k, want := range report.Workloads[0].Env {
		if got.Env[k] != want {
			t.Errorf("Compose differs for %s", k)
		}
	}
	if got.Env["DATABASE_URL"] != "postgres://127.0.0.1:15432/dev" || got.Env["FROM_FILE"] != "file" || got.Env["COMPOSE_PORT"] != "15432" {
		t.Fatalf("wrong host-side Compose projection")
	}
}

func TestEnvExecConfigMatchesUpAndScopedSecrets(t *testing.T) {
	withKCLFixture(t, `{"output":{"secret_provider":{"type":"file","path":"secrets.yaml"},"workloads":[{"name":"api","kind":"service","runtime":{"type":"host","launch_env":{"PORT":"8181"}},"spec":{"kind":"service","env":[{"name":"TOKEN","secretRef":{"name":"app","key":"TOKEN"}},{"name":"EMPTY","value":""}]}}]}}`)
	if err := os.WriteFile("secrets.yaml", []byte("TOKEN: local-secret\nUNDECLARED: hidden\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FORGE_EXEC_HELPER", "1")
	e, err := readEnvConfigEntities(t.Context(), projectDirForKCL(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	store, err := loadProjectStore()
	if err != nil {
		t.Fatal(err)
	}
	prov, err := secretProviderFromEntities(e, projectDirForKCL())
	if err != nil {
		t.Fatal(err)
	}
	if err := secrets.ValidateDeclaredRefs(prov, secretRefsForService(&e.Workloads[0]), ""); err != nil {
		t.Fatal(err)
	}
	up, _, err := buildHostServiceCmd(t.Context(), store.Config(), e.Workloads[0], prov.All(), "dev")
	// No build/runner is required by env exec; use an explicit command only
	// for comparing the up environment, not for running it.
	if err != nil {
		e.Workloads[0].Spec.Command = []string{"not-run"}
		up, _, err = buildHostServiceCmd(t.Context(), store.Config(), e.Workloads[0], prov.All(), "dev")
	}
	if err != nil {
		t.Fatal(err)
	}
	out, errout, err := executeEnvFixture(t, []string{"dev"}, execHelperArgs(t))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Env map[string]string }
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	for _, kv := range up.Env {
		k, v, _ := strings.Cut(kv, "=")
		// up pins its Go/air toolchain PATH; env exec must preserve the
		// caller's PATH for arbitrary programs rather than select a runner.
		if k != "PATH" && got.Env[k] != v {
			t.Errorf("up differs for %s", k)
		}
	}
	if got.Env["TOKEN"] != "local-secret" || got.Env["UNDECLARED"] != "" || strings.Contains(errout, "local-secret") {
		t.Fatal("secret scope or logging violated")
	}
}

func TestEnvExecForwardsSignalAndPreservesChildStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal forwarding")
	}
	if testing.Short() {
		t.Skip("nested subprocess signal proof; runs in task test")
	}
	t.Setenv("FORGE_EXEC_HELPER", "1")
	argv := execHelperArgs(t, "signal-parent")
	c := exec.Command(argv[0], argv[1:]...)
	c.Env = os.Environ()
	stdout, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill() })
	ready := make(chan bool, 1)
	go func() { scan := bufio.NewScanner(stdout); ready <- scan.Scan() && scan.Text() == "ready" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("child did not become ready")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child readiness timed out")
	}
	if err := c.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 37 {
			t.Fatalf("forwarded SIGTERM must preserve graceful child status 37: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("signal did not reach child")
	}
}
