package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

// fakeDockerOnPath puts a `docker` stand-in at the front of PATH that records
// every argv it is given and fails, and returns a reader for those calls. It
// is how a test proves a check never reached the host's docker at all.
func fakeDockerOnPath(t *testing.T) func() string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the docker stand-in is a shell script")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	script := "#!/bin/sh\necho \"docker $*\" >> " + log + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		b, _ := os.ReadFile(log)
		return strings.TrimSpace(string(b))
	}
}

// A project that declares no compose file has no compose stack. The compose
// check already said so without asking docker; Delve — which runs in the
// compose `app-debug` service — did not, and shelled out to `docker compose ps`
// for every `forge env status` of a host-only project. That is the call forge's
// own test tripwire caught from TestRunUpServices_EndToEnd.
func TestComposeChecks_NoComposeFileNeverAskDocker(t *testing.T) {
	calls := fakeDockerOnPath(t)
	env := &Environment{ProjectDir: t.TempDir(), Ports: map[string]string{}}

	for name, check := range map[string]CheckFunc{"compose": CheckDocker, "delve": CheckDelve} {
		res := check(context.Background(), env)
		if res.Status != StatusSkip {
			t.Errorf("%s on a project with no compose file: status %v (%s), want skip", name, res.Status, res.Message)
		}
	}
	if got := calls(); got != "" {
		t.Errorf("a project with no compose file asked docker:\n%s", got)
	}
}

// Every compose question goes through the runner the caller injected, so a
// caller (and its tests) decides whether the host's docker is reached.
func TestComposeChecks_AskTheInjectedRunner(t *testing.T) {
	calls := fakeDockerOnPath(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The doctor runs its checks concurrently, and the compose check is not
	// the only one asking: the telemetry checks query ClickHouse through the
	// same runner. So the record is locked and its order is not asserted.
	var (
		mu    sync.Mutex
		asked []string
	)
	runner := func(_ context.Context, projectDir string, args ...string) ([]byte, error) {
		if projectDir != dir {
			t.Errorf("runner got project dir %q, want %q", projectDir, dir)
		}
		mu.Lock()
		asked = append(asked, strings.Join(args, " "))
		mu.Unlock()
		if args[0] == "ps" {
			return []byte(`{"Service":"postgres","State":"running","Health":"healthy"}` + "\n"), nil
		}
		return nil, errors.New("not published")
	}

	report, err := New(Deps{Compose: runner}).RunRuntime(context.Background(), RuntimeInput{
		ProjectName: "p", ProjectDir: dir, Env: "dev", Signal: "",
	})
	if err != nil {
		t.Fatalf("RunRuntime: %v", err)
	}
	var compose *CheckResult
	for i := range report.Checks {
		if report.Checks[i].Name == composeCheckName {
			compose = &report.Checks[i]
		}
	}
	if compose == nil || compose.Status != StatusPass {
		t.Fatalf("compose check through the injected runner: %+v, want pass", compose)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(asked, "ps --format json") {
		t.Errorf("runner calls: %q, want one to be `ps --format json`", asked)
	}
	if got := calls(); got != "" {
		t.Errorf("an injected runner must be the only way to docker; the host's was asked:\n%s", got)
	}
}
