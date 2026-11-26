// Copyright (c) 2025 Reliant Labs
package runcgate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/mergequeue/gaterun"
)

func spec() gaterun.Spec {
	env, _ := gaterun.GateEnv("gate-b1-1", map[string]string{"GOFLAGS": "-mod=mod"})
	return gaterun.Spec{
		RunID: "gate-b1-1", Image: "gate:test", Command: []string{"/bin/sh", "-c", "exit 0"},
		SourceDir: "/tmp/src", Env: env, VulnDBDir: "/var/lib/forge/vulndb",
		Limits: gaterun.Limits{CPUs: 4, MemoryBytes: 8 << 30, Pids: 4096, DiskBytes: 1 << 30, Wall: time.Minute},
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
		check  func(gaterun.Result) bool
	}{
		{"pass", &fakeDocker{state: containerState{Status: "exited"}}, gaterun.ClassPass, nil},
		{"fail", &fakeDocker{startExit: 1, state: containerState{Status: "exited", ExitCode: 1}}, gaterun.ClassFail, nil},
		{"unproven exit", &fakeDocker{startExit: 2, state: containerState{Status: "exited", ExitCode: 2}}, gaterun.ClassUnproven, nil},
		{"oom", &fakeDocker{startExit: 137, state: containerState{Status: "exited", ExitCode: 137, OOMKilled: true}}, gaterun.ClassUnproven,
			func(r gaterun.Result) bool { return r.OOMKilled }},
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
