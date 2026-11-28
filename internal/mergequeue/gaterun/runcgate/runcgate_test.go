// Copyright (c) 2025 Reliant Labs
package runcgate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun/gatehost"
)

func spec() gatehost.Spec {
	env, _ := gaterun.GateEnv("gate-b1-1", map[string]string{"GOFLAGS": "-mod=mod"})
	return gatehost.Spec{
		RunID: "gate-b1-1", Image: "gate:test", Command: []string{"/bin/sh", "-c", "exit 0"},
		SourceDir: "/tmp/src", Env: env, VulnDBDir: "/var/lib/forge/vulndb",
		Limits: gatehost.Limits{CPUs: 4, MemoryBytes: 8 << 30, Pids: 4096, DiskBytes: 1 << 30, Wall: time.Minute},
	}
}

func TestCreateArgsMountNothingOfTheHostButTheDatabaseReadOnly(t *testing.T) {
	args := createArgs(spec())
	joined := strings.Join(args, " ")
	for _, banned := range []string{"--privileged", "-v ", "--volume", "docker.sock", "--pid ", "--network host", "--ipc", "--cap-add", "--env-file", "--userns"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("create args hold %q: %s", banned, joined)
		}
	}
	var mounts []string
	for i, a := range args {
		if a == "--mount" {
			mounts = append(mounts, args[i+1])
		}
	}
	want := []string{
		"type=volume,source=reliant-gate-gate-b1-1-work,target=/gate",
		"type=bind,source=/var/lib/forge/vulndb,target=/vulndb,readonly",
	}
	if !slices.Equal(mounts, want) {
		t.Fatalf("mounts %v, want %v", mounts, want)
	}
	for _, need := range []string{
		"--runtime runc", "--cap-drop ALL", "--security-opt no-new-privileges", "--read-only",
		"--cpus 4", "--memory 8589934592", "--memory-swap 8589934592", "--pids-limit 4096",
		"--network bridge", "--label reliant.run-id=gate-b1-1",
		"--env GOFLAGS=-mod=mod", "--entrypoint /bin/sh", "TZ=UTC gate:test -c exit 0",
	} {
		if !strings.Contains(joined, need) {
			t.Fatalf("create args miss %q: %s", need, joined)
		}
	}
	var envs []string
	for i, a := range args {
		if a == "--env" {
			envs = append(envs, args[i+1])
		}
	}
	if !slices.Equal(envs, spec().Env) {
		t.Fatalf("env %v, want exactly the gate's %v", envs, spec().Env)
	}

	s := spec()
	s.VulnDBDir = ""
	if strings.Contains(strings.Join(createArgs(s), " "), "type=bind") {
		t.Fatal("a bind mount without a database")
	}
}

// fakeDocker answers the CLI calls Run makes.
type fakeDocker struct {
	mu        sync.Mutex
	calls     [][]string
	startExit int
	startErr  error
	// startBlocks makes `start` wait for its context (a gate past its wall).
	startBlocks bool
	state       containerState
	extraEnv    string
	// cacheLabels is the cache volume's labels as `volume inspect` reports
	// them; "" is the labels the backend gives a cache it created.
	cacheLabels string
}

func (d *fakeDocker) run(ctx context.Context, _ io.Reader, stdout, _ io.Writer, args ...string) (int, error) {
	d.mu.Lock()
	d.calls = append(d.calls, args)
	d.mu.Unlock()
	cmd := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(cmd, "image inspect"):
		_, _ = io.WriteString(stdout, `["PATH=/usr/bin","IMAGE_VAR=1"]`)
	case strings.HasPrefix(cmd, "inspect --format {{json .Config.Env}}"):
		env := append(spec().Env, "IMAGE_VAR=1")
		if d.extraEnv != "" {
			env = append(env, d.extraEnv)
		}
		b, _ := json.Marshal(env)
		_, _ = stdout.Write(b)
	case strings.HasPrefix(cmd, "volume inspect --format {{json .Labels}}"):
		labels := d.cacheLabels
		if labels == "" {
			labels = `{"reliant.gate.backend":"runc","reliant.gate.cache":"` + args[len(args)-1] + `"}`
		}
		_, _ = io.WriteString(stdout, labels)
	case strings.HasPrefix(cmd, "inspect --format {{json .State}}"):
		b, _ := json.Marshal(d.state)
		_, _ = stdout.Write(b)
	case strings.HasPrefix(cmd, "start"):
		if d.startBlocks {
			<-ctx.Done()
			return -1, ctx.Err()
		}
		return d.startExit, d.startErr
	}
	return 0, nil
}

func (d *fakeDocker) called(prefix string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.calls {
		if strings.HasPrefix(strings.Join(c, " "), prefix) {
			return true
		}
	}
	return false
}

func TestRunClassifiesTheEnd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		docker *fakeDocker
		class  gaterun.Class
		check  func(gatehost.Result) bool
	}{
		{"pass", &fakeDocker{state: containerState{Status: "exited"}}, gaterun.ClassPass, nil},
		{"fail", &fakeDocker{startExit: 1, state: containerState{Status: "exited", ExitCode: 1}}, gaterun.ClassFail, nil},
		{"unproven exit", &fakeDocker{startExit: 2, state: containerState{Status: "exited", ExitCode: 2}}, gaterun.ClassUnproven, nil},
		{"oom", &fakeDocker{startExit: 137, state: containerState{Status: "exited", ExitCode: 137, OOMKilled: true}}, gaterun.ClassUnproven,
			func(r gatehost.Result) bool { return r.OOMKilled }},
		{"still running after the attach", &fakeDocker{state: containerState{Status: "running"}}, gaterun.ClassUnproven, nil},
		{"lost attach", &fakeDocker{startErr: errors.New("connection reset"), state: containerState{Status: "exited"}}, gaterun.ClassUnproven, nil},
		{"attach and engine disagree", &fakeDocker{startExit: 0, state: containerState{Status: "exited", ExitCode: 1}}, gaterun.ClassUnproven, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &Backend{run: tc.docker.run}
			res, err := b.Run(context.Background(), spec())
			if err != nil {
				t.Fatal(err)
			}
			if res.Class != tc.class || (tc.check != nil && !tc.check(res)) {
				t.Fatalf("result %+v, want %s", res, tc.class)
			}
		})
	}
}

func TestRunPastItsWallIsUnprovenAndKilled(t *testing.T) {
	d := &fakeDocker{startBlocks: true, state: containerState{Status: "running"}}
	s := spec()
	s.Limits.Wall = 10 * time.Millisecond
	res, err := (&Backend{run: d.run}).Run(context.Background(), s)
	if err != nil || res.Class != gaterun.ClassUnproven || !res.TimedOut {
		t.Fatalf("%+v %v", res, err)
	}
	if !d.called("kill reliant-gate-gate-b1-1") {
		t.Fatal("the gate was not killed")
	}
}

func TestRunRefusesAnInjectedVariable(t *testing.T) {
	d := &fakeDocker{extraEnv: "HTTPS_PROXY=http://proxy.internal:3128", state: containerState{Status: "exited"}}
	_, err := (&Backend{run: d.run}).Run(context.Background(), spec())
	if !errors.Is(err, ErrEnvInjected) || !strings.Contains(err.Error(), "HTTPS_PROXY") {
		t.Fatalf("injected env: %v", err)
	}
	if d.called("start") {
		t.Fatal("a gate with an injected variable was started")
	}
}

func TestTeardownRemovesByLabel(t *testing.T) {
	d := &fakeDocker{}
	b := &Backend{run: func(ctx context.Context, in io.Reader, out, errw io.Writer, args ...string) (int, error) {
		if args[0] == "ps" || args[1] == "ls" {
			_, _ = io.WriteString(out, "id1\n")
		}
		return d.run(ctx, in, out, errw, args...)
	}}
	if err := b.Teardown(context.Background(), "gate-b1-1"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ps -aq --filter label=reliant.run-id=gate-b1-1", "rm -fv id1", "volume rm id1"} {
		if !d.called(want) {
			t.Fatalf("teardown did not run %q: %v", want, d.calls)
		}
	}
}

func TestTheWarmCacheVolumeIsMountedReadWriteAtTheCacheMount(t *testing.T) {
	s := spec()
	s.CacheVolume = "reliant-gate-cache-acme-widgets"
	args := createArgs(s)
	var mounts []string
	for i, a := range args {
		if a == "--mount" {
			mounts = append(mounts, args[i+1])
		}
	}
	want := []string{
		"type=volume,source=reliant-gate-gate-b1-1-work,target=/gate",
		"type=volume,source=reliant-gate-cache-acme-widgets,target=/gate/cache",
		"type=bind,source=/var/lib/forge/vulndb,target=/vulndb,readonly",
	}
	if !slices.Equal(mounts, want) {
		t.Fatalf("mounts %v, want %v", mounts, want)
	}
}

func TestACacheVolumeOfAnotherShapeIsRefused(t *testing.T) {
	for _, name := range []string{"reliant-gate-gate-b1-1-work", "/var/lib/docker", "reliant-gate-cache-", "reliant-gate-cache-a,readonly=false"} {
		s := spec()
		s.CacheVolume = name
		if err := validate(s); !errors.Is(err, ErrInvalidSpec) {
			t.Errorf("cache volume %q: %v, want refused", name, err)
		}
	}
}

func TestRunCreatesTheCacheVolumeOutsideTheGateAndLogsItsSize(t *testing.T) {
	d := &fakeDocker{state: containerState{Status: "exited"}}
	b := &Backend{run: func(ctx context.Context, in io.Reader, out, errw io.Writer, args ...string) (int, error) {
		if strings.HasPrefix(strings.Join(args, " "), "system df -v") {
			_, _ = io.WriteString(out, `[{"Name":"other","Size":"1GB"},{"Name":"reliant-gate-cache-acme-widgets","Size":"2.5GB"}]`)
		}
		return d.run(ctx, in, out, errw, args...)
	}}
	s := spec()
	s.CacheVolume = "reliant-gate-cache-acme-widgets"
	var log strings.Builder
	s.Stdout = &log
	if _, err := b.Run(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	var create []string
	for _, c := range d.calls {
		if len(c) > 2 && c[0] == "volume" && c[1] == "create" && c[len(c)-1] == s.CacheVolume {
			create = c
		}
	}
	if create == nil {
		t.Fatalf("the cache volume was not created: %v", d.calls)
	}
	joined := strings.Join(create, " ")
	if !strings.Contains(joined, "--label reliant.gate.cache=reliant-gate-cache-acme-widgets") {
		t.Fatalf("cache volume not labelled for the janitor: %s", joined)
	}
	// Teardown removes what carries the run's label; the cache must not.
	if strings.Contains(joined, LabelRunID) {
		t.Fatalf("cache volume carries the run id, so Teardown would remove it: %s", joined)
	}
	if got := strings.Count(log.String(), "warm cache volume reliant-gate-cache-acme-widgets"); got != 2 {
		t.Fatalf("logged the cache %d times, want before and after:\n%s", got, log.String())
	}
	if !strings.Contains(log.String(), "(2.5GB)") || !strings.Contains(log.String(), "is 2.5GB after the gate") {
		t.Fatalf("cache size not logged:\n%s", log.String())
	}
}

func TestRunWithoutACacheVolumeCreatesNone(t *testing.T) {
	d := &fakeDocker{state: containerState{Status: "exited"}}
	if _, err := (&Backend{run: d.run}).Run(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	if d.called("volume create --label reliant.gate.cache") || d.called("system df") {
		t.Fatalf("a gate without a cache touched one: %v", d.calls)
	}
	// The tree is copied owned by the image's (non-root) user.
	if !d.called("cp -a /tmp/src/. reliant-gate-gate-b1-1:/gate/src") {
		t.Fatalf("the tree was not copied with -a: %v", d.calls)
	}
}

// createdMounts runs s through Run against a fake engine and returns the
// --mount values of its `docker create`, and what the gate logged.
func createdMounts(t *testing.T, s gatehost.Spec) (mounts []string, log string) {
	t.Helper()
	d := &fakeDocker{state: containerState{Status: "exited"}}
	var out strings.Builder
	s.Stdout = &out
	if _, err := (&Backend{run: d.run}).Run(context.Background(), s); err != nil {
		t.Fatalf("a trusted cache problem must never fail the gate: %v", err)
	}
	for _, c := range d.calls {
		if len(c) > 1 && c[0] == "create" {
			for i, a := range c {
				if a == "--mount" {
					mounts = append(mounts, c[i+1])
				}
			}
		}
	}
	return mounts, out.String()
}

func trustedMount(dir string) string {
	return "type=bind,source=" + dir + ",target=/gate/trusted-go-build,readonly"
}

func TestTheTrustedGoCacheIsMountedReadOnlyWhenValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := spec()
	s.TrustedGoCacheDir = dir
	mounts, log := createdMounts(t, s)
	want := []string{
		"type=volume,source=reliant-gate-gate-b1-1-work,target=/gate",
		"type=bind,source=/var/lib/forge/vulndb,target=/vulndb,readonly",
		trustedMount(dir),
	}
	if !slices.Equal(mounts, want) {
		t.Fatalf("mounts %v, want %v", mounts, want)
	}
	if !strings.Contains(log, "trusted Go cache "+dir+" read-only at /gate/trusted-go-build") {
		t.Fatalf("the mount was not logged:\n%s", log)
	}
}

func TestAnUnusableTrustedGoCacheIsNotMounted(t *testing.T) {
	root := t.TempDir()
	worldWritable := filepath.Join(root, "world")
	groupWritable := filepath.Join(root, "group")
	for _, d := range []struct {
		path string
		mode os.FileMode
	}{{worldWritable, 0o777}, {groupWritable, 0o775}} {
		if err := os.Mkdir(d.path, 0o700); err != nil {
			t.Fatal(err)
		}
		// Chmod, not Mkdir's mode: the umask would clear the bits under test.
		if err := os.Chmod(d.path, d.mode); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, dir, why string }{
		{"missing", filepath.Join(root, "missing"), "does not exist"},
		{"world-writable", worldWritable, "group- or world-writable"},
		{"group-writable", groupWritable, "group- or world-writable"},
		{"symlink", link, "is a symlink"},
		{"file", file, "is not a directory"},
		{"relative", "go-build-trusted", "not a clean absolute path"},
		{"unclean", target + "/../real", "not a clean absolute path"},
		{"comma", root + "/a,readonly=false", "mount option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := spec()
			s.TrustedGoCacheDir = tc.dir
			mounts, log := createdMounts(t, s)
			for _, m := range mounts {
				if strings.Contains(m, gaterun.TrustedGoCacheMount) {
					t.Fatalf("mounted %q: %v", tc.dir, mounts)
				}
			}
			if strings.Count(log, "trusted Go cache not mounted") != 1 || !strings.Contains(log, tc.why) {
				t.Fatalf("want one line saying %q, got:\n%s", tc.why, log)
			}
		})
	}
}

func TestTheTrustedGoCacheSettingOffMountsNothing(t *testing.T) {
	s := spec()
	s.TrustedGoCacheDir = gatehost.TrustedGoCacheDir("off")
	mounts, log := createdMounts(t, s)
	for _, m := range mounts {
		if strings.Contains(m, gaterun.TrustedGoCacheMount) {
			t.Fatalf("off mounted a trusted cache: %v", mounts)
		}
	}
	if strings.Contains(log, "trusted Go cache") {
		t.Fatalf("off logged a trusted cache:\n%s", log)
	}
}

func TestRunRefusesACacheVolumeItDidNotCreate(t *testing.T) {
	for _, labels := range []string{`null`, `{"reliant.gate.cache":"reliant-gate-cache-other","reliant.gate.backend":"runc"}`, `{"reliant.gate.cache":"reliant-gate-cache-acme-widgets"}`} {
		d := &fakeDocker{cacheLabels: labels, state: containerState{Status: "exited"}}
		s := spec()
		s.CacheVolume = "reliant-gate-cache-acme-widgets"
		_, err := (&Backend{run: d.run}).Run(context.Background(), s)
		if !errors.Is(err, ErrCacheForeign) {
			t.Fatalf("labels %s: %v, want the foreign cache refused", labels, err)
		}
		if d.called("create --name") || d.called("start") {
			t.Fatalf("labels %s: a gate was created over a foreign cache: %v", labels, d.calls)
		}
	}
}
