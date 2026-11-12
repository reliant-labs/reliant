package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// registryVolume is the registry's data volume as k3d creates it: ANONYMOUS
// (registry:2 declares VOLUME /var/lib/registry, and k3d mounts nothing over
// it), so its name is a bare hex id.
var registryVolume = strings.Repeat("ab", 32)

// fakeRegistryHost emulates the docker CLI and the registry HTTP API for one
// full RegistryGC pass: a running k3d registry holding one live tag and one
// expired untagged manifest, so the plan is non-empty and apply reaches the
// stop / helper / DELETE / garbage-collect / restart sequence.
//
// Only docker and the registry API are faked. Inventory parsing, the
// protected set, the graph plan and the maintenance sequence are the
// production code.
type fakeRegistryHost struct {
	t        *testing.T
	registry string
	port     string

	mu      sync.Mutex
	running map[string]bool
	calls   [][]string
	// failNth makes chosen calls fail: key is a call prefix (joined args),
	// value is the set of 1-based occurrences of that call that fail.
	failNth map[string]map[int]bool
	seen    map[string]int
	// failAlways makes every matching call fail.
	failAlways map[string]bool
	// startDoesNotRun makes `docker start` exit 0 while the container stays
	// stopped, as a registry that crashes on start does.
	startDoesNotRun bool

	tags, revisions string
	// config is the registry's config.yml; the default already enables delete,
	// as a registry created with k3d --delete-enabled does.
	config string
	// cpFails makes `docker cp` fail.
	cpFails bool
}

const (
	configDeleteOn  = "version: 0.1\nstorage:\n  filesystem:\n    rootdirectory: /var/lib/registry\n  delete:\n    enabled: true\n"
	configDeleteOff = "version: 0.1\nstorage:\n  cache:\n    blobdescriptor: inmemory\n  filesystem:\n    rootdirectory: /var/lib/registry\nhttp:\n  addr: :5000\n"
)

func newFakeRegistryHost(t *testing.T) *fakeRegistryHost {
	t.Helper()
	now := time.Now()
	live, orphan := testDigest("a"), testDigest("b")
	manifests := map[string]string{live: singleArch(1, 2), orphan: singleArch(3, 4)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v2/" {
			return
		}
		digest := strings.TrimPrefix(req.URL.Path, "/v2/app/manifests/")
		body, ok := manifests[digest]
		switch {
		case !ok:
			http.NotFound(w, req)
		case req.Method == http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
		default:
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRegistryHost{
		t: t, registry: "k3d-test-registry", port: port,
		running: map[string]bool{"k3d-test-registry": true},
		failNth: map[string]map[int]bool{}, seen: map[string]int{}, failAlways: map[string]bool{},
	}
	const prefix = "/var/lib/registry/docker/registry/v2/repositories/app/_manifests/"
	f.config = configDeleteOn
	f.tags = fmt.Sprintf("%d\t%stags/dev/current/link\t%s\n", now.Unix(), prefix, live)
	old := now.Add(-30 * 24 * time.Hour).Unix()
	f.revisions = fmt.Sprintf("%d\t%srevisions/sha256/%s/link\t%s\n%d\t%srevisions/sha256/%s/link\t%s\n",
		now.Unix(), prefix, strings.TrimPrefix(live, "sha256:"), live,
		old, prefix, strings.TrimPrefix(orphan, "sha256:"), orphan)
	return f
}

func (f *fakeRegistryHost) reg() Registry {
	return Registry{Container: f.registry, Repositories: []string{"app"}, Aliases: []string{"localhost:" + f.port}}
}

// helperName and gcName are the maintenance containers RegistryGC starts.
func (f *fakeRegistryHost) helperName() string { return f.registry + "-forge-retention" }
func (f *fakeRegistryHost) gcName() string     { return f.registry + "-forge-gc" }

func (f *fakeRegistryHost) command(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "docker" {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	for prefix := range f.failAlways {
		if strings.HasPrefix(joined, prefix) {
			return nil, fmt.Errorf("injected failure: docker %s", joined)
		}
	}
	for prefix, nth := range f.failNth {
		if strings.HasPrefix(joined, prefix) {
			f.seen[prefix]++
			if nth[f.seen[prefix]] {
				return nil, fmt.Errorf("injected failure: docker %s", joined)
			}
		}
	}
	switch {
	case args[0] == "inspect" && len(args) == 2:
		return f.inspect(args[1])
	case joined == "ps -aq --filter label=k3d.role=server", joined == "ps -aq":
		return nil, nil
	case args[0] == "ps":
		// ps [-q|-aq] --filter name=^/<name>$
		filter := args[len(args)-1]
		target := strings.TrimSuffix(strings.TrimPrefix(filter, "name=^/"), "$")
		if f.running[target] {
			return []byte(target + "-id\n"), nil
		}
		return nil, nil
	case args[0] == "exec" && strings.HasSuffix(joined, "cat "+registryConfigPath):
		return []byte(f.config), nil
	case args[0] == "cp" && strings.HasPrefix(args[1], f.registry+":"):
		return nil, os.WriteFile(args[2], []byte(f.config), 0o644)
	case args[0] == "cp" && strings.HasSuffix(args[2], f.registry+":"+registryConfigPath):
		if f.cpFails {
			return nil, fmt.Errorf("injected cp failure")
		}
		b, err := os.ReadFile(args[1])
		f.config = string(b)
		return b[:0], err
	case args[0] == "exec" && strings.Contains(joined, "_manifests/tags/"):
		return []byte(f.tags), nil
	case args[0] == "exec" && strings.Contains(joined, "_manifests/revisions/"):
		return []byte(f.revisions), nil
	case args[0] == "stop":
		f.running[args[len(args)-1]] = false
		return nil, nil
	case args[0] == "start":
		if !f.startDoesNotRun {
			f.running[args[1]] = true
		}
		return nil, nil
	case args[0] == "run":
		for i, a := range args {
			if a == "--name" {
				if args[1] == "-d" {
					f.running[args[i+1]] = true
				}
				return []byte("ok"), nil
			}
		}
	}
	return nil, fmt.Errorf("unexpected docker %v", args)
}

func (f *fakeRegistryHost) inspect(name string) ([]byte, error) {
	if name != f.registry && name != f.helperName() {
		return nil, fmt.Errorf("no such container %s", name)
	}
	info := map[string]any{
		"Image":  "sha256:" + strings.Repeat("e", 64),
		"State":  map[string]any{"Running": f.running[name]},
		"Config": map[string]any{"Cmd": []string{"/etc/docker/registry/config.yml"}},
		"Mounts": []map[string]any{{
			"Type": "volume", "Name": registryVolume, "Destination": "/var/lib/registry", "RW": true,
			"Source": "/var/lib/docker/volumes/" + registryVolume + "/_data",
		}},
		"NetworkSettings": map[string]any{
			"Ports":    map[string]any{"5000/tcp": []map[string]string{{"HostPort": f.port}}},
			"Networks": map[string]any{"k3d-test": map[string]any{}},
		},
	}
	b, err := json.Marshal([]any{info})
	return b, err
}

// runs returns every `docker run` invocation, in order.
func (f *fakeRegistryHost) runs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]string
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == "run" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeRegistryHost) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			n++
		}
	}
	return n
}

func (f *fakeRegistryHost) isRunning(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running[name]
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestRegistryGCNeverAsksDockerToRemoveTheRegistryVolume is the H1 regression.
// `docker run --rm --volumes-from <registry>` makes the helper a co-owner of
// the registry's ANONYMOUS data volume, and `--rm` removes anonymous volumes
// on exit (moby removeMountPoints skips only mounts with an explicit source,
// and ignores "in use"). Only the registry container's own reference kept
// every pushed image alive; removing that container during a pass — k3d
// registry delete, a recreate, an agent's reset — let the helper's exit
// delete the store.
//
// A volume mounted BY NAME has an explicit source and is never removed by
// --rm, so that is the only form a maintenance container may use.
func TestRegistryGCNeverAsksDockerToRemoveTheRegistryVolume(t *testing.T) {
	f := newFakeRegistryHost(t)
	r := Runner{Policy: DefaultPolicy(), Command: f.command, PolicyPath: filepath.Join(t.TempDir(), "storage.json")}
	if err := r.RegistryGC(context.Background(), f.reg(), true); err != nil {
		t.Fatalf("RegistryGC: %v", err)
	}
	runs := f.runs()
	if len(runs) != 2 {
		t.Fatalf("want the retention helper and the garbage-collect pass, got %d runs: %v", len(runs), runs)
	}
	for _, args := range runs {
		if hasArg(args, "--volumes-from") {
			t.Errorf("maintenance container inherits volumes from the registry: %v", args)
			if hasArg(args, "--rm") {
				t.Errorf("--rm with --volumes-from: removing this container removes the registry's anonymous volume")
			}
		}
		if !hasArg(args, "type=volume,src="+registryVolume+",dst=/var/lib/registry") {
			t.Errorf("registry data volume is not mounted by name: %v", args)
		}
	}
	if !f.isRunning(f.registry) {
		t.Fatal("registry was not restarted")
	}
}

// TestMaintenanceMountsReproduceEveryMountByName pins the translation from the
// registry's inspected mounts to the helper's: every mount keeps its
// destination and read-only bit, and every source is explicit.
func TestMaintenanceMountsReproduceEveryMountByName(t *testing.T) {
	got, err := maintenanceMounts(containerInfo{Mounts: []containerMount{
		{Type: "volume", Name: registryVolume, Destination: "/var/lib/registry", RW: true},
		{Type: "bind", Source: "/host/config.yml", Destination: "/etc/docker/registry/config.yml", RW: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--mount", "type=volume,src=" + registryVolume + ",dst=/var/lib/registry",
		"--mount", "type=bind,src=/host/config.yml,dst=/etc/docker/registry/config.yml,readonly",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("mounts = %v, want %v", got, want)
	}
	for _, bad := range []containerMount{
		{Type: "volume", Destination: "/var/lib/registry", RW: true},
		{Type: "tmpfs", Destination: "/tmp", RW: true},
		{Type: "bind", Destination: "/etc/x", RW: true},
		{Type: "bind", Source: "/a,b", Destination: "/etc/x", RW: true},
	} {
		if _, err := maintenanceMounts(containerInfo{Mounts: []containerMount{bad}}); err == nil {
			t.Errorf("mount %+v cannot be reproduced by name and must be refused", bad)
		}
	}
}

// gcRunner is a Runner over the fake host whose maintenance state lives in a
// test-owned directory.
func gcRunner(t *testing.T, f *fakeRegistryHost) Runner {
	t.Helper()
	restoreRetryDelay = time.Millisecond
	t.Cleanup(func() { restoreRetryDelay = defaultRestoreRetryDelay })
	return Runner{Policy: DefaultPolicy(), Command: f.command, PolicyPath: filepath.Join(t.TempDir(), "storage.json")}
}

// TestRegistryGCReportsARegistryThatDidNotComeBack is B4's verification half.
// `docker start` exiting 0 says nothing about whether the registry is still
// running a moment later; a registry that crashes on start used to be
// reported as a successful pass and left down.
func TestRegistryGCReportsARegistryThatDidNotComeBack(t *testing.T) {
	f := newFakeRegistryHost(t)
	f.startDoesNotRun = true
	err := gcRunner(t, f).RegistryGC(context.Background(), f.reg(), true)
	if err == nil {
		t.Fatal("RegistryGC reported success with the registry stopped")
	}
	if !strings.Contains(err.Error(), f.registry) {
		t.Fatalf("the error must name the registry that is down: %v", err)
	}
}

// TestRegistryGCRetriesATransientCleanupFailure: one failed `docker ps` while
// confirming the GC writer is gone used to end the pass with the registry
// deliberately left stopped. The writer check is retried, and the registry
// comes back once it succeeds.
func TestRegistryGCRetriesATransientCleanupFailure(t *testing.T) {
	f := newFakeRegistryHost(t)
	// The first `ps` for the gc writer is the pre-flight check; the second
	// is the cleanup's.
	f.failNth["ps -aq --filter name=^/"+f.gcName()+"$"] = map[int]bool{2: true}
	if err := gcRunner(t, f).RegistryGC(context.Background(), f.reg(), true); err != nil {
		t.Fatalf("RegistryGC: %v", err)
	}
	if !f.isRunning(f.registry) {
		t.Fatal("one transient docker failure left the registry stopped")
	}
}

// TestRegistryLeftStoppedIsRestartedByTheNextPass is B4's recovery half. A
// pass that cannot restart the registry (Docker Desktop quitting mid-pass,
// forge SIGKILLed between stop and start) leaves it stopped, and an explicit
// stop disables the unless-stopped restart policy, so nothing brings it back.
// The next pass used to print "registry stopped; skipping" forever. It must
// recognise a registry FORGE stopped and restart it.
func TestRegistryLeftStoppedIsRestartedByTheNextPass(t *testing.T) {
	f := newFakeRegistryHost(t)
	r := gcRunner(t, f)
	// Docker stops cooperating right after the registry is stopped: the
	// helper cannot start, and neither can the registry again.
	f.failAlways["run "] = true
	f.failAlways["start "] = true
	if err := r.RegistryGC(context.Background(), f.reg(), true); err == nil {
		t.Fatal("a pass that could not restart the registry reported success")
	}
	if f.isRunning(f.registry) {
		t.Fatal("precondition: the interrupted pass should have left the registry stopped")
	}

	// Docker is back. The next pass — a preview, even — restores service.
	f.mu.Lock()
	f.failAlways = map[string]bool{}
	f.failNth = map[string]map[int]bool{}
	f.mu.Unlock()
	var out strings.Builder
	r.Out = &out
	if err := r.RegistryGC(context.Background(), f.reg(), false); err != nil {
		t.Fatalf("next pass: %v\n%s", err, out.String())
	}
	if !f.isRunning(f.registry) {
		t.Fatalf("the next pass skipped a registry forge itself left stopped:\n%s", out.String())
	}
	if f.called("start "+f.registry) < 1 {
		t.Fatal("registry was never started")
	}
}

// TestRegistryStoppedByAHumanIsLeftAlone: only a registry forge stopped is
// restarted. One someone stopped on purpose is still skipped.
func TestRegistryStoppedByAHumanIsLeftAlone(t *testing.T) {
	f := newFakeRegistryHost(t)
	f.running[f.registry] = false
	var out strings.Builder
	r := gcRunner(t, f)
	r.Out = &out
	if err := r.RegistryGC(context.Background(), f.reg(), true); err != nil {
		t.Fatal(err)
	}
	if f.isRunning(f.registry) || f.called("start ") != 0 {
		t.Fatalf("restarted a registry forge did not stop:\n%s", out.String())
	}
}

// TestRegistryGCMigratesAnOldRegistryToDeleteEnabledOnce: a registry created
// before forge passed --delete-enabled to k3d gets storage.delete.enabled
// written into its config.yml inside the maintenance window it already takes,
// and is verified delete-capable after the restart.
func TestRegistryGCMigratesAnOldRegistryToDeleteEnabledOnce(t *testing.T) {
	f := newFakeRegistryHost(t)
	f.config = configDeleteOff
	r := gcRunner(t, f)
	var out strings.Builder
	r.Out = &out
	if err := r.RegistryGC(context.Background(), f.reg(), true); err != nil {
		t.Fatalf("RegistryGC: %v\n%s", err, out.String())
	}
	if on, err := configDeleteEnabled([]byte(f.config)); err != nil || !on {
		t.Fatalf("config.yml not migrated: enabled=%v err=%v\n%s", on, err, f.config)
	}
	if !strings.Contains(f.config, "rootdirectory: /var/lib/registry") || !strings.Contains(f.config, "addr: :5000") {
		t.Fatalf("migration dropped unrelated config:\n%s", f.config)
	}
	if !strings.Contains(out.String(), "manifest delete enabled and verified") {
		t.Fatalf("migration not reported:\n%s", out.String())
	}
	if !f.isRunning(f.registry) {
		t.Fatal("registry not restarted")
	}
	// Idempotent: a migrated registry is not touched again.
	before := f.called("cp ")
	if err := r.RegistryGC(context.Background(), f.reg(), true); err != nil {
		t.Fatal(err)
	}
	if f.called("cp ") != before {
		t.Fatal("an already delete-enabled registry was rewritten")
	}
}

// TestRegistryGCPreviewReportsMigrationWithoutTouchingTheRegistry: dry runs
// say the migration would happen and change nothing.
func TestRegistryGCPreviewReportsMigrationWithoutTouchingTheRegistry(t *testing.T) {
	f := newFakeRegistryHost(t)
	f.config = configDeleteOff
	r := gcRunner(t, f)
	var out strings.Builder
	r.Out = &out
	if err := r.RegistryGC(context.Background(), f.reg(), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "manifest delete is not enabled") {
		t.Fatalf("preview did not announce the migration:\n%s", out.String())
	}
	if f.config != configDeleteOff || f.called("cp ") != 0 || f.called("stop ") != 0 {
		t.Fatal("preview modified the registry")
	}
}

// TestRegistryGCStillReclaimsWhenTheConfigEditFails: the edit is best effort;
// the helper deletes through its own environment, and the registry comes back.
func TestRegistryGCStillReclaimsWhenTheConfigEditFails(t *testing.T) {
	f := newFakeRegistryHost(t)
	f.config = configDeleteOff
	f.cpFails = true
	r := gcRunner(t, f)
	var out strings.Builder
	r.Out = &out
	if err := r.RegistryGC(context.Background(), f.reg(), true); err != nil {
		t.Fatalf("RegistryGC: %v", err)
	}
	if !f.isRunning(f.registry) || len(f.runs()) != 2 {
		t.Fatal("pass did not complete after the config edit failed")
	}
	if !strings.Contains(out.String(), "could not enable manifest delete") {
		t.Fatalf("failure not reported:\n%s", out.String())
	}
}

func TestConfigDeleteEnabledParsing(t *testing.T) {
	for config, want := range map[string]bool{configDeleteOn: true, configDeleteOff: false, "storage:\n  delete:\n    enabled: false\n": false} {
		got, err := configDeleteEnabled([]byte(config))
		if err != nil || got != want {
			t.Errorf("configDeleteEnabled(%q) = %v, %v; want %v", config, got, err, want)
		}
	}
}
