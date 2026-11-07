package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStartupDependencyRuntimeDecode(t *testing.T) {
	for _, tc := range []struct {
		raw          string
		dependencies int
		portless     bool
	}{
		{`{"runner":"binary","startup_after":["web"],"listen_ports":[]}`, 1, true},
		{`{"runner":"air"}`, 0, false},
	} {
		var h HostRuntime
		if err := json.Unmarshal([]byte(tc.raw), &h); err != nil {
			t.Fatal(err)
		}
		if len(h.StartupAfter) != tc.dependencies || (h.ListenPorts != nil) != tc.portless {
			t.Fatalf("decoded %+v", h)
		}
	}
}

func startupFixture() *KCLEntities {
	w := hostWL("desktop")
	w.Runtime.Host.StartupAfter = []string{"web"}
	return &KCLEntities{Workloads: []WorkloadEntity{w}, Frontends: []FrontendEntity{{Name: "web", Port: 1234}}}
}
func TestStartupDependencyFrontendWait(t *testing.T) {
	for _, kind := range []string{"delayed", "foreign", "exited", "timeout", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			timeout := time.Second
			if kind == "timeout" {
				timeout = time.Nanosecond
			}
			err := waitFrontendStartup(ctx, "web", startupFixture(), "dev", timeout, time.Millisecond, func(name string, port int) hostReadyResult {
				if name != "web" || port != 1234 {
					t.Fatalf("wrong frontend probe %s:%d", name, port)
				}
				calls++
				r := hostReadyResult{name: "frontend:" + name, port: port, state: portReadyNobody}
				switch kind {
				case "delayed":
					if calls == 2 {
						r.state = portReadyOurs
					}
				case "foreign":
					r.state = portReadyForeign
				case "exited":
					r.state = portReadyOurs
					r.exited = "exit status 0"
				case "cancel":
					cancel()
				}
				return r
			})
			if kind == "delayed" {
				if err != nil || calls != 2 {
					t.Fatalf("%d %v", calls, err)
				}
				return
			}
			if err == nil {
				t.Fatal("unready frontend passed")
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if kind == "timeout" && !strings.Contains(err.Error(), "timed out") {
				t.Fatal(err)
			}
		})
	}
}

func TestStartupDependencyMultihop(t *testing.T) {
	a, b := hostWL("a"), hostWL("b")
	a.Runtime.Host.StartupAfter = []string{"b"}
	b.Runtime.Host.StartupAfter = []string{"web"}
	e := &KCLEntities{Workloads: []WorkloadEntity{a, b}, Frontends: []FrontendEntity{{Name: "web"}}}
	if _, err := hostStartupPlan(e, []string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "select both") {
		t.Fatalf("transitive target: %v", err)
	}
	b.Runtime.Host.StartupAfter = []string{"a"}
	if _, err := hostStartupPlan(e, nil); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("multihop cycle: %v", err)
	}
	for _, kind := range []string{"cluster", "job", "tool"} {
		t.Run(kind, func(t *testing.T) {
			e := startupFixture()
			w := hostWL("web")
			e.Frontends = nil
			if kind == "cluster" {
				w.Runtime.Type = RuntimeCluster
			} else {
				w.Kind = kind
			}
			e.Workloads = append(e.Workloads, w)
			if _, err := hostStartupPlan(e, nil); err == nil {
				t.Fatal("invalid dependency accepted")
			}
		})
	}
}

func TestStartupDependencyValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*KCLEntities)
		targets []string
		want    string
	}{
		{name: "valid"},
		{name: "missing", mutate: func(e *KCLEntities) { e.Frontends = nil }, want: "missing"},
		{name: "cycle", mutate: func(e *KCLEntities) { e.Workloads[0].Runtime.Host.StartupAfter = []string{"desktop"} }, want: "cycle"},
		{name: "ambiguous", mutate: func(e *KCLEntities) { e.Frontends = append(e.Frontends, FrontendEntity{Name: "desktop"}) }, want: "ambiguous"},
		{name: "strict selection", targets: []string{"desktop"}, want: "select both"},
		{name: "selected together", targets: []string{"desktop", "web"}},
		{name: "unselected consumer", targets: []string{"web"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := startupFixture()
			if tc.mutate != nil {
				tc.mutate(e)
			}
			_, err := hostStartupPlan(e, tc.targets)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
	// Legacy configurations are not subjected to new namespace restrictions.
	e := startupFixture()
	e.Workloads[0].Runtime.Host.StartupAfter = nil
	e.Frontends[0].Name = "desktop"
	if _, err := hostStartupPlan(e, nil); err != nil {
		t.Fatal(err)
	}
}
func TestStartupDependencyLaunchOrderAndFailure(t *testing.T) {
	nodes, err := hostStartupPlan(startupFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	start := func(n startupNode) error { events = append(events, "start:"+n.name); return nil }
	ready := func(n startupNode) error { events = append(events, "ready:"+n.name); return nil }
	if err := launchStartupPlan(t.Context(), nodes, start, ready); err != nil {
		t.Fatal(err)
	}
	if want := []string{"start:web", "ready:web", "start:desktop"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("%v", events)
	}
	events = nil
	failure := errors.New("dependency exited")
	if err := launchStartupPlan(t.Context(), nodes, start, func(startupNode) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"start:web"}) {
		t.Fatalf("consumer launched after failure: %v", events)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	events = nil
	if err := launchStartupPlan(ctx, nodes, start, ready); !errors.Is(err, context.Canceled) || len(events) != 0 {
		t.Fatalf("cancellation: %v %v", err, events)
	}
}
func TestStartupDependencyRechecksEarlierDependency(t *testing.T) {
	for _, kind := range []string{"exited", "lost listener"} {
		t.Run(kind, func(t *testing.T) {
			nodes := []startupNode{{name: "desktop", after: []string{"web", "api"}}, {name: "web", frontend: true}, {name: "api"}}
			var launched []string
			web := hostReadyResult{name: "web", state: portReadyOurs}
			err := launchStartupPlan(t.Context(), nodes, func(n startupNode) error { launched = append(launched, n.name); return nil }, func(n startupNode) error {
				if n.name == "api" {
					if kind == "exited" {
						web.exited = "exit status 0"
					} else {
						web.state = portReadyNobody
					}
				}
				return nil
			}, func(deps []startupNode) error {
				if len(deps) > 0 {
					if bad := hostReadyUnready([]hostReadyResult{web}); len(bad) > 0 {
						return hostReadyError("dev", bad, nil)
					}
				}
				return nil
			})
			if err == nil || !reflect.DeepEqual(launched, []string{"web", "api"}) {
				t.Fatalf("consumer launched against regressed dependency: %v %v", launched, err)
			}
		})
	}
}

func TestStartupDependencyPortlessAndListeningExits(t *testing.T) {
	for _, port := range []int{0, 8080} {
		err := waitHostReadiness(t.Context(), "dev", nil, time.Hour, time.Hour, io.Discard, func() []hostReadyResult {
			return []hostReadyResult{{name: "desktop", port: port, state: portReadyOurs, exited: "exit status 0"}}
		})
		if err == nil || !strings.Contains(err.Error(), "exit status 0") {
			t.Fatalf("port %d: %v", port, err)
		}
	}
}
func TestStartupDependencyPortlessRegistry(t *testing.T) {
	done := make(chan struct{})
	p := &procRegistry{processes: []*managedProcess{{name: "desktop", done: done}}}
	if !p.hasRunning("desktop") || p.hasRunning("missing") {
		t.Fatal("incorrect live process state")
	}
	close(done)
	if p.hasRunning("desktop") || p.exitReason("desktop") == "" {
		t.Fatal("exit not reflected")
	}
}
