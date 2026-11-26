package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/config"
)

// covCIsolateKubeconfig keeps withKubeconfigLock's lock file out of the real ~/.kube.
func covCIsolateKubeconfig(t *testing.T) {
	t.Helper()
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "kubeconfig"))
}

func TestCovC_ListK3dClusters(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    []string
		wantErr string
	}{
		{"two clusters", `echo '[{"name":"a","serversRunning":1,"serversCount":1},{"name":"b"}]'`, []string{"a", "b"}, ""},
		{"empty array", `echo '[]'`, nil, ""},
		{"empty output", `exit 0`, nil, ""},
		{"non-JSON chatter explains the likely env var", `echo 'level=debug msg=hello'`, nil, "usually a log-verbosity env var"},
		{"JSON array that does not decode", `echo '[{"name": 5}]'`, nil, "parse k3d cluster list output"},
		{"k3d failing carries its stderr", `echo 'docker not running' >&2; exit 1`, nil, "docker not running"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			covCInstallTools(t, map[string]string{"k3d": tc.body})
			got, err := listK3dClusters(context.Background())
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v; want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range got {
				names = append(names, e.Name)
			}
			if !slices.Equal(names, tc.want) {
				t.Fatalf("names = %v; want %v", names, tc.want)
			}
		})
	}
	t.Run("k3d missing from PATH points at the installer", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := listK3dClusters(context.Background())
		if err == nil || !strings.Contains(err.Error(), "install k3d: https://k3d.io") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_LookupK3dClusterRuntimeState(t *testing.T) {
	orig := listK3dClustersFn
	t.Cleanup(func() { listK3dClustersFn = orig })
	listK3dClustersFn = func(context.Context) ([]k3dClusterListEntry, error) {
		return []k3dClusterListEntry{
			{Name: "up", ServersRunning: 1, ServersCount: 1},
			{Name: "half", ServersRunning: 1, ServersCount: 1, AgentsRunning: 1, AgentsCount: 2},
		}, nil
	}
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		want k3dClusterRuntimeState
	}{
		{"up", k3dClusterRuntimeState{Exists: true, Running: true}},
		{"half", k3dClusterRuntimeState{Exists: true}},
		{"absent", k3dClusterRuntimeState{}},
	} {
		got, err := lookupK3dClusterRuntimeState(ctx, tc.name)
		if err != nil || got != tc.want {
			t.Errorf("%s = %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}
	if ok, err := clusterExists(ctx, "half"); !ok || err != nil {
		t.Errorf("clusterExists(half) = %v, %v; a stopped cluster still exists", ok, err)
	}
	boom := errors.New("k3d broke")
	listK3dClustersFn = func(context.Context) ([]k3dClusterListEntry, error) { return nil, boom }
	if _, err := lookupK3dClusterRuntimeState(ctx, "up"); !errors.Is(err, boom) {
		t.Errorf("lookup err = %v", err)
	}
	if _, err := clusterExists(ctx, "up"); !errors.Is(err, boom) {
		t.Errorf("clusterExists err = %v", err)
	}
}

func TestCovC_K3dCommandRunners(t *testing.T) {
	covCIsolateKubeconfig(t)
	logPath := covCInstallTools(t, map[string]string{
		"k3d":    `case "$*" in *fail*) echo "k3d refused" >&2; exit 1;; esac`,
		"docker": `case "$*" in *"rm -f k3d-gone-tools"*) echo "Error: No such container: k3d-gone-tools"; exit 1;; *"rm -f k3d-bad-tools"*) echo "denied"; exit 1;; esac`,
	})
	ctx := context.Background()
	if err := runK3dClusterStartCommand(ctx, "cp"); err != nil {
		t.Fatal(err)
	}
	if err := runK3dClusterCreateCommand(ctx, []string{"cluster", "create", "cp"}); err != nil {
		t.Fatal(err)
	}
	if err := runK3dClusterCreateCommand(ctx, []string{"cluster", "fail"}); err == nil {
		t.Error("a failing k3d create must surface")
	}
	if err := mergeK3dKubeconfig(ctx, "cp"); err != nil {
		t.Fatal(err)
	}
	if err := mergeK3dKubeconfig(ctx, "fail"); err == nil || !strings.Contains(err.Error(), `merge kubeconfig for k3d cluster "fail"`) {
		t.Errorf("merge err = %v", err)
	}
	if err := deleteK3dClusterFn(ctx, "cp"); err != nil {
		t.Fatal(err)
	}
	if err := deleteK3dClusterFn(ctx, "fail"); err == nil || !strings.Contains(err.Error(), "k3d cluster delete fail") {
		t.Errorf("delete err = %v", err)
	}
	if err := cleanupK3dStartTools(ctx, "gone"); err != nil {
		t.Errorf("an already-removed tools container is fine: %v", err)
	}
	if err := cleanupK3dStartTools(ctx, "bad"); err == nil || !strings.Contains(err.Error(), "remove temporary container k3d-bad-tools") {
		t.Errorf("cleanup err = %v", err)
	}
	calls := covCCalls(t, logPath)
	for _, want := range []string{
		"k3d cluster start cp --wait --timeout 1m30s",
		"k3d cluster create cp",
		"k3d kubeconfig merge cp --kubeconfig-merge-default --kubeconfig-switch-context=false",
		"k3d cluster delete cp",
	} {
		if !covCHasCall(calls, want) {
			t.Errorf("missing %q in %v", want, calls)
		}
	}
}

func TestCovC_FinishK3dClusterLifecycle(t *testing.T) {
	ctx := context.Background()
	cmdErr := errors.New("exit status 1")
	cleanupOK := func(context.Context, string) error { return nil }
	running := func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	stopped := func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true}, nil
	}

	expired, cancel := context.WithTimeout(ctx, time.Nanosecond)
	defer cancel()
	<-expired.Done()
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()

	tests := []struct {
		name       string
		parent     context.Context
		commandCtx context.Context
		err        error
		lookup     func(context.Context, string) (k3dClusterRuntimeState, error)
		cleanup    func(context.Context, string) error
		wantErr    string
		wantIs     error
	}{
		{"success tolerates a cleanup warning", ctx, ctx, nil, running, func(context.Context, string) error { return errors.New("rm failed") }, "", nil},
		{"failed command but the cluster is running continues", ctx, ctx, cmdErr, running, cleanupOK, "", nil},
		{"failed command on a stopped cluster fails", ctx, ctx, cmdErr, stopped, cleanupOK, `k3d cluster start "cp": exit status 1`, cmdErr},
		{"timed-out command says so", ctx, expired, cmdErr, stopped, cleanupOK, "timed out after 1m30s", nil},
		{"state recheck failure is attached", ctx, ctx, cmdErr, func(context.Context, string) (k3dClusterRuntimeState, error) {
			return k3dClusterRuntimeState{}, errors.New("list failed")
		}, cleanupOK, "state recheck also failed: list failed", cmdErr},
		{"user cancellation wins", cancelled, ctx, cmdErr, running, cleanupOK, "", context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			captureStdout(t, func() {
				err = finishK3dClusterLifecycle(tc.parent, tc.commandCtx, "cp", "start", tc.err, tc.lookup, tc.cleanup)
			})
			if tc.wantErr == "" && tc.wantIs == nil {
				if err != nil {
					t.Fatalf("err = %v; want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v; want %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v; want errors.Is %v", err, tc.wantIs)
			}
		})
	}
}

func TestCovC_RunK3dClusterLifecycle(t *testing.T) {
	origPoll, origGrace := k3dClusterStartPollInterval, k3dClusterStartHealthyGrace
	t.Cleanup(func() { k3dClusterStartPollInterval, k3dClusterStartHealthyGrace = origPoll, origGrace })
	k3dClusterStartPollInterval = time.Millisecond
	k3dClusterStartHealthyGrace = 5 * time.Millisecond
	ctx := context.Background()
	running := func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	cleaned := 0
	cleanup := func(context.Context, string) error { cleaned++; return nil }

	t.Run("a client wedged after the cluster is running is cancelled and the run continues", func(t *testing.T) {
		cleaned = 0
		err := runK3dClusterLifecycle(ctx, "cp", "start",
			func(c context.Context) error { <-c.Done(); return c.Err() }, running, cleanup)
		if err != nil {
			t.Fatalf("err = %v; a running cluster with a wedged client must continue", err)
		}
		if cleaned != 1 {
			t.Errorf("tools container cleanup ran %d times; want 1", cleaned)
		}
	})
	t.Run("a command that returns on its own goes through the finish rules", func(t *testing.T) {
		boom := errors.New("exit 1")
		err := runK3dClusterLifecycle(ctx, "cp", "create",
			func(context.Context) error { return boom },
			func(context.Context, string) (k3dClusterRuntimeState, error) { return k3dClusterRuntimeState{}, nil }, cleanup)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), `k3d cluster create "cp"`) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("user cancellation cancels the client and still cleans up", func(t *testing.T) {
		cleaned = 0
		parent, cancel := context.WithCancel(ctx)
		started := make(chan struct{})
		go func() { <-started; cancel() }()
		err := runK3dClusterLifecycle(parent, "cp", "start",
			func(c context.Context) error { close(started); <-c.Done(); return c.Err() },
			func(context.Context, string) (k3dClusterRuntimeState, error) { return k3dClusterRuntimeState{}, nil }, cleanup)
		if !errors.Is(err, context.Canceled) || cleaned != 1 {
			t.Fatalf("err = %v, cleaned = %d", err, cleaned)
		}
	})
}

func TestCovC_K3dServerBootCrashAndEntrypointLog(t *testing.T) {
	// A tar of /var/log with two entrypoint logs; only the newest is reported.
	tarFile := filepath.Join(t.TempDir(), "log.tar")
	covCWriteTar(t, tarFile, map[string]string{
		"log/k3d-entrypoints_20260101.log": "old line",
		"log/k3d-entrypoints_20260102.log": "newest line 1\nnewest line 2",
		"log/other.log":                    "ignored",
	})
	covCInstallTools(t, map[string]string{"docker": `case "$*" in
  "ps -a"*) echo "k3d-c-server-0"; echo "k3d-c-server-1";;
  *"inspect --format"*"k3d-c-server-0"*) echo "3 restarting exit=1 oom=false boot failed";;
  *"inspect --format"*"k3d-c-server-1"*) echo "0 running exit=0 oom=false ";;
  "logs --tail 25 k3d-c-server-0") echo "fatal: cgroup";;
  "cp k3d-c-server-0:/var/log/. -") cat "$COVC_TAR";;
  "cp "*) exit 1;;
esac`})
	t.Setenv("COVC_TAR", tarFile)
	got := k3dServerBootCrash(context.Background(), "c")
	for _, want := range []string{"k3d-c-server-0: restarts=3 restarting exit=1 oom=false boot failed", "fatal: cgroup", "k3d entrypoint log:\nnewest line 1\nnewest line 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("crash report lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "k3d-c-server-1") || strings.Contains(got, "old line") || strings.Contains(got, "ignored") {
		t.Errorf("healthy node or stale/unrelated logs leaked into the report:\n%s", got)
	}
	if ep := k3dEntrypointLog(context.Background(), "k3d-c-server-1"); ep != "" {
		t.Errorf("unreadable container log must be empty, got %q", ep)
	}
	t.Run("docker unavailable yields no report", func(t *testing.T) {
		covCInstallTools(t, map[string]string{"docker": `exit 1`})
		if got := k3dServerBootCrash(context.Background(), "c"); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestCovC_KubectlContextHelpers(t *testing.T) {
	covCIsolateKubeconfig(t)
	logPath := covCInstallTools(t, map[string]string{"kubectl": `case "$*" in
  *"current-context"*) echo " k3d-cp "; exit 0;;
  *"use-context k3d-bad"*) exit 1;;
esac`})
	ctx := context.Background()
	if got := currentKubectlContext(ctx); got != "k3d-cp" {
		t.Errorf("current context = %q", got)
	}
	if err := pinKubectlContext(ctx, "cp"); err != nil {
		t.Fatal(err)
	}
	if !covCHasCall(covCCalls(t, logPath), "kubectl config use-context k3d-cp") {
		t.Errorf("calls = %v", covCCalls(t, logPath))
	}
	if err := pinKubectlContext(ctx, "bad"); err == nil || !strings.Contains(err.Error(), "kubectl config use-context k3d-bad") {
		t.Errorf("err = %v", err)
	}
	covCInstallTools(t, map[string]string{"kubectl": `exit 1`})
	if got := currentKubectlContext(ctx); got != "" {
		t.Errorf("no kubectl context = %q; want empty", got)
	}
}

func TestCovC_NestedSecondariesOf(t *testing.T) {
	logPath := covCInstallTools(t, map[string]string{"docker": `case "$*" in
  *"k3d.cluster.network=k3d-fail"*) echo "daemon down" >&2; exit 1;;
  *) printf 'owner\nwl\nwl\n\nother\n';;
esac`})
	got, err := nestedSecondariesOf(context.Background(), "owner")
	if err != nil || !slices.Equal(got, []string{"wl", "other"}) {
		t.Fatalf("= %v, %v; want the deduplicated secondaries without the owner", got, err)
	}
	if !covCHasCall(covCCalls(t, logPath), "label=k3d.cluster.network=k3d-owner") {
		t.Errorf("must filter on the owner's network: %v", covCCalls(t, logPath))
	}
	if _, err := nestedSecondariesOf(context.Background(), "fail"); err == nil || !strings.Contains(err.Error(), "docker ps") || !strings.Contains(err.Error(), "daemon down") {
		t.Errorf("err = %v", err)
	}
}

func TestCovC_ResumeExistingDevCluster(t *testing.T) {
	origStart, origPin, origWait := runK3dClusterStartCommandFn, pinKubectlContextFn, nodeReadyAttemptFn
	origLookup, origCleanup := lookupK3dStateAfterStartFn, cleanupK3dStartToolsFn
	t.Cleanup(func() {
		runK3dClusterStartCommandFn, pinKubectlContextFn, nodeReadyAttemptFn = origStart, origPin, origWait
		lookupK3dStateAfterStartFn, cleanupK3dStartToolsFn = origLookup, origCleanup
	})
	var events []string
	runK3dClusterStartCommandFn = func(_ context.Context, name string) error { events = append(events, "start:"+name); return nil }
	lookupK3dStateAfterStartFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true}, nil
	}
	cleanupK3dStartToolsFn = func(context.Context, string) error { return nil }
	pinKubectlContextFn = func(_ context.Context, name string) error { events = append(events, "pin:"+name); return nil }
	nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) {
		events = append(events, "ready")
		return "", nil
	}
	ctx := context.Background()

	run := func(state k3dClusterRuntimeState, wait bool) (string, error) {
		events = nil
		var err error
		out := captureStdout(t, func() { err = resumeExistingDevCluster(ctx, "cp", state, wait) })
		return out, err
	}
	out, err := run(k3dClusterRuntimeState{Exists: true, Running: true}, false)
	if err != nil || !strings.Contains(out, "already exists and is running — no-op") || !slices.Equal(events, []string{"pin:cp"}) {
		t.Fatalf("running: err %v events %v out %q", err, events, out)
	}
	out, err = run(k3dClusterRuntimeState{Exists: true}, true)
	if err != nil || !strings.Contains(out, "exists but is stopped — starting") || !slices.Equal(events, []string{"start:cp", "pin:cp", "ready"}) {
		t.Fatalf("stopped+wait: err %v events %v out %q", err, events, out)
	}
	pinBoom := errors.New("no kubectl")
	pinKubectlContextFn = func(context.Context, string) error { return pinBoom }
	if _, err = run(k3dClusterRuntimeState{Exists: true, Running: true}, true); !errors.Is(err, pinBoom) {
		t.Fatalf("pin failure = %v", err)
	}
	pinKubectlContextFn = func(context.Context, string) error { return nil }
	waitBoom := errors.New("not ready")
	origBudget, origInterval := nodeReadyWaitBudget, nodeReadyRetryInterval
	t.Cleanup(func() { nodeReadyWaitBudget, nodeReadyRetryInterval = origBudget, origInterval })
	nodeReadyWaitBudget, nodeReadyRetryInterval = 20*time.Millisecond, time.Millisecond
	nodeReadyAttemptFn = func(context.Context, string, time.Duration) (string, error) { return "", waitBoom }
	if _, err = run(k3dClusterRuntimeState{Exists: true, Running: true}, true); err == nil || !strings.Contains(err.Error(), "wait for cluster nodes") {
		t.Fatalf("wait failure = %v", err)
	}
	runK3dClusterStartCommandFn = func(context.Context, string) error { return errors.New("start refused") }
	if _, err = run(k3dClusterRuntimeState{Exists: true}, false); err == nil || !strings.Contains(err.Error(), "start refused") {
		t.Fatalf("start failure = %v", err)
	}
}

func TestCovC_RunDevClusterUpAndDownGates(t *testing.T) {
	// forge's own forge.yaml has deploy off, so a project with no deploy tree must refuse.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: "+testProjectName+"\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	ctx := context.Background()
	for name, fn := range map[string]func() error{
		"up":       func() error { return runDevClusterUp(ctx, "deploy/k3d.yaml", false) },
		"down":     func() error { return runDevClusterDown(ctx, "deploy/k3d.yaml") },
		"env up":   func() error { return runEnvClustersUp(ctx, "dev", false) },
		"env down": func() error { return runEnvClustersDown(ctx, "dev") },
	} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), config.DisabledFeatureError(config.FeatureDeploy).Error()) {
			t.Errorf("%s: err = %v; want the disabled-deploy refusal", name, err)
		}
	}
}

func TestCovC_RunDevClusterUpExistingAndMissingConfig(t *testing.T) {
	deployEnabledProject(t)
	orig := struct {
		state func(context.Context, string) (k3dClusterRuntimeState, error)
		pin   func(context.Context, string) error
		ens   func(context.Context) error
	}{lookupClusterStateForUpFn, pinKubectlContextFn, nil}
	_ = orig
	t.Cleanup(func() { lookupClusterStateForUpFn, pinKubectlContextFn = orig.state, orig.pin })
	pinned := ""
	pinKubectlContextFn = func(_ context.Context, name string) error { pinned = name; return nil }
	lookupClusterStateForUpFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true, Running: true}, nil
	}
	var err error
	out := captureStdout(t, func() { err = runDevClusterUp(context.Background(), "deploy/k3d.yaml", false) })
	if err != nil || pinned != testProjectName || !strings.Contains(out, "no-op") {
		t.Fatalf("existing cluster: err %v pinned %q out %q", err, pinned, out)
	}

	boom := errors.New("k3d not installed")
	lookupClusterStateForUpFn = func(context.Context, string) (k3dClusterRuntimeState, error) { return k3dClusterRuntimeState{}, boom }
	if err := runDevClusterUp(context.Background(), "deploy/k3d.yaml", false); !errors.Is(err, boom) {
		t.Fatalf("up lookup err = %v", err)
	}
	if err := runDevClusterDown(context.Background(), "deploy/k3d.yaml"); !errors.Is(err, boom) {
		t.Fatalf("down lookup err = %v", err)
	}

	lookupClusterStateForUpFn = func(context.Context, string) (k3dClusterRuntimeState, error) { return k3dClusterRuntimeState{}, nil }
	out = captureStdout(t, func() { err = runDevClusterDown(context.Background(), "deploy/k3d.yaml") })
	if err != nil || !strings.Contains(out, "not found — no-op") {
		t.Fatalf("down of an absent cluster: err %v out %q", err, out)
	}
}

func TestCovC_RunDevClusterDownReportsDependentDeleteFailure(t *testing.T) {
	deployEnabledProject(t)
	calls := stubEnvClusters(t, nil, map[string]bool{testProjectName: true})
	boom := errors.New("delete refused")
	deleteK3dClusterFn = func(_ context.Context, name string) error {
		*calls = append(*calls, "delete:"+name)
		if strings.HasSuffix(name, "-dep") {
			return boom
		}
		return nil
	}
	nestedSecondariesOfFn = func(context.Context, string) ([]string, error) { return []string{testProjectName + "-dep"}, nil }
	var err error
	captureStdout(t, func() { err = runDevClusterDown(context.Background(), "deploy/k3d.yaml") })
	if !errors.Is(err, boom) || slices.Contains(*calls, "delete:"+testProjectName) {
		t.Fatalf("err %v calls %v; the owner must not be deleted once a dependent fails", err, *calls)
	}
	nestedSecondariesOfFn = func(context.Context, string) ([]string, error) { return nil, errors.New("docker ps failed") }
	captureStdout(t, func() { err = runDevClusterDown(context.Background(), "deploy/k3d.yaml") })
	if err == nil || !strings.Contains(err.Error(), "find clusters nested on") {
		t.Fatalf("err = %v", err)
	}
}

func TestCovC_RunEnvClustersUpAndDownErrors(t *testing.T) {
	deployEnabledProject(t)
	calls := stubEnvClusters(t, envPair, map[string]bool{"control-plane-v2": true})
	origCharts := ensureEnvPlatformChartsInstalledFn
	t.Cleanup(func() { ensureEnvPlatformChartsInstalledFn = origCharts })
	var chartClusters []string
	ensureEnvPlatformChartsInstalledFn = func(_ context.Context, env string, cs []ClusterEntity) error {
		for _, c := range cs {
			chartClusters = append(chartClusters, env+"/"+c.Name)
		}
		return nil
	}
	ctx := context.Background()

	if err := runEnvClustersUp(ctx, "e2e", false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(chartClusters, []string{"e2e/control-plane-v2", "e2e/cp-daemon-v2"}) {
		t.Errorf("platform charts installed for %v", chartClusters)
	}
	if slices.ContainsFunc(*calls, func(c string) bool { return strings.HasPrefix(c, "wait:") }) {
		t.Errorf("--wait=false must not wait: %v", *calls)
	}

	boom := errors.New("reconcile failed")
	reconcileEnvClustersFn = func(context.Context, []ClusterEntity, string, string) error { return boom }
	if err := runEnvClustersUp(ctx, "e2e", true); !errors.Is(err, boom) {
		t.Errorf("reconcile err = %v", err)
	}
	reconcileEnvClustersFn = func(context.Context, []ClusterEntity, string, string) error { return nil }
	waitDeclaredClusterReadyFn = func(context.Context, string) error { return boom }
	if err := runEnvClustersUp(ctx, "e2e", true); !errors.Is(err, boom) || !strings.Contains(err.Error(), `wait for cluster "control-plane-v2" nodes`) {
		t.Errorf("wait err = %v", err)
	}
	renderEnvClustersFn = func(context.Context, string) ([]ClusterEntity, string, error) { return nil, "", boom }
	if err := runEnvClustersUp(ctx, "e2e", false); !errors.Is(err, boom) {
		t.Errorf("render err = %v", err)
	}
	if err := runEnvClustersDown(ctx, "e2e"); !errors.Is(err, boom) {
		t.Errorf("down render err = %v", err)
	}

	renderEnvClustersFn = func(context.Context, string) ([]ClusterEntity, string, error) {
		return []ClusterEntity{{Name: "gke-1", Provider: "gke"}}, "/proj", nil
	}
	if err := runEnvClustersDown(ctx, "prod"); err == nil || !strings.Contains(err.Error(), `provider="gke"`) {
		t.Errorf("a non-k3d cluster must be refused on down: %v", err)
	}
	renderEnvClustersFn = func(context.Context, string) ([]ClusterEntity, string, error) { return envPair, "/proj", nil }
	lookupClusterStateForUpFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{}, boom
	}
	if err := runEnvClustersDown(ctx, "e2e"); !errors.Is(err, boom) {
		t.Errorf("down lookup err = %v", err)
	}
	lookupClusterStateForUpFn = func(context.Context, string) (k3dClusterRuntimeState, error) {
		return k3dClusterRuntimeState{Exists: true}, nil
	}
	deleteK3dClusterFn = func(context.Context, string) error { return boom }
	var err error
	captureStdout(t, func() { err = runEnvClustersDown(ctx, "e2e") })
	if !errors.Is(err, boom) {
		t.Errorf("down delete err = %v", err)
	}
}

func TestCovC_EnvClusterDeleteOrderAndFormatters(t *testing.T) {
	got := envClusterDeleteOrder([]ClusterEntity{
		{Name: "owner"},
		{Name: "sec-a", Network: "k3d-owner", RegistryInherit: true},
		{Name: "owner2"},
		{Name: "sec-b", Network: "k3d-owner", RegistryInherit: true},
	})
	if want := []string{"sec-a", "sec-b", "owner", "owner2"}; !slices.Equal(got, want) {
		t.Fatalf("delete order = %v; want secondaries first, %v", got, want)
	}
	if foundOrMissing(true) != "found" || foundOrMissing(false) != "missing" {
		t.Error("foundOrMissing")
	}
}

func TestCovC_ReloadRefusesWithoutDeploy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("name: "+testProjectName+"\nmodule_path: example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	orig := pinKubectlContextFn
	t.Cleanup(func() { pinKubectlContextFn = orig })
	pinKubectlContextFn = func(context.Context, string) error { return nil }
	err := runDevClusterReload(context.Background(), "deploy/k3d.yaml", "tag", "", true)
	if err == nil || !strings.Contains(err.Error(), config.DisabledFeatureError(config.FeatureDeploy).Error()) {
		t.Fatalf("err = %v", err)
	}
	t.Run("deploy project without dev KCL says to generate", func(t *testing.T) {
		deployEnabledProject(t)
		err := runDevClusterReload(context.Background(), "deploy/k3d.yaml", "tag", "", true)
		if err == nil || !strings.Contains(err.Error(), "dev KCL not found") || !strings.Contains(err.Error(), filepath.Join("deploy", "kcl", "dev", "main.k")) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("pin failure stops a real reload before anything renders", func(t *testing.T) {
		deployEnabledProject(t)
		boom := errors.New("no kubeconfig")
		pinKubectlContextFn = func(context.Context, string) error { return boom }
		if err := runDevClusterReload(context.Background(), "deploy/k3d.yaml", "tag", "", false); !errors.Is(err, boom) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestCovC_GitShortSHA(t *testing.T) {
	covCInstallTools(t, map[string]string{"git": `case "$*" in "rev-parse --short HEAD") echo "abc1234";; *) exit 1;; esac`})
	if got, err := gitShortSHA(context.Background()); err != nil || got != "abc1234" {
		t.Fatalf("= %q, %v", got, err)
	}
	covCInstallTools(t, map[string]string{"git": `exit 128`})
	if _, err := gitShortSHA(context.Background()); err == nil {
		t.Fatal("a failing git must return an error so callers can ask for --image-tag")
	}
}
