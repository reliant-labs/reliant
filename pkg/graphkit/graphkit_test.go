package graphkit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/graphkit"
	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// pool and listener stand in for a provider and a listener; graph is the
// shape a generated graph_gen.go takes, with compose.go's constructors
// inlined.
type pool struct {
	closed bool
	ready  error
}

func (p *pool) Close(context.Context) error { p.closed = true; return nil }
func (p *pool) Ready(context.Context) error { return p.ready }

type handler struct{ pool *pool }

type listener struct{ h *handler }

func (l *listener) Run(ctx context.Context) error { <-ctx.Done(); return nil }

type graph struct {
	b graphkit.Builder
	m struct {
		Pool     graphkit.Memo[*pool]
		Handler  graphkit.Memo[*handler]
		Listener graphkit.Memo[*listener]
		Other    graphkit.Memo[*handler]
	}
	built     map[string]int
	poolErr   error
	poolPanic bool
	cycle     bool
	poolGate  chan struct{} // when set, the pool's constructor blocks until it closes
}

func (g *graph) count(n string) { g.built[n]++ }

func (g *graph) Pool() *pool {
	return graphkit.Get(&g.b, &g.m.Pool, "pool", func() (*pool, error) {
		g.count("pool")
		if g.poolGate != nil {
			<-g.poolGate
		}
		if g.poolPanic {
			panic("boom")
		}
		if g.cycle {
			g.Listener()
		}
		return &pool{}, g.poolErr
	}, func(c *pool) lifecyclekit.Node {
		if c == nil {
			return lifecyclekit.Node{}
		}
		return lifecyclekit.Node{Close: c.Close, Ready: c.Ready}
	})
}

func (g *graph) Handler() *handler {
	return graphkit.Get(&g.b, &g.m.Handler, "handler", func() (*handler, error) {
		g.count("handler")
		return &handler{pool: g.Pool()}, nil
	}, nil)
}

func (g *graph) Other() *handler {
	return graphkit.Get(&g.b, &g.m.Other, "other", func() (*handler, error) {
		g.count("other")
		return &handler{}, nil
	}, nil)
}

func (g *graph) Listener() *listener {
	return graphkit.Get(&g.b, &g.m.Listener, "listener", func() (*listener, error) {
		g.count("listener")
		return &listener{h: g.Handler()}, nil
	}, func(c *listener) lifecyclekit.Node { return lifecyclekit.Node{Run: c.Run} })
}

func newGraph() *graph { return &graph{built: map[string]int{}} }

func TestBuild_ConstructsTheClosureOnceDependenciesFirst(t *testing.T) {
	g := newGraph()
	if err := g.b.Build("server", func() { g.Listener(); g.Handler() }); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(g.b.Built(), ","), "pool,handler,listener"; got != want {
		t.Fatalf("built %s, want %s", got, want)
	}
	for n, c := range g.built {
		if c != 1 {
			t.Errorf("%s constructed %d times", n, c)
		}
	}
	if g.built["other"] != 0 {
		t.Fatal("a node the root does not reach was constructed")
	}
	var names []string
	for _, n := range g.b.Nodes() {
		names = append(names, n.Name)
	}
	if got := strings.Join(names, ","); got != "pool,listener" {
		t.Fatalf("lifecycle nodes %s, want pool,listener (handler has no lifecycle)", got)
	}
}

func TestBuild_ErrorNamesThePath(t *testing.T) {
	g := newGraph()
	g.poolErr = errors.New("dial refused")
	err := g.b.Build("server", func() { g.Listener() })
	if err == nil || err.Error() != "build server: listener → handler → pool: dial refused" {
		t.Fatalf("err = %v", err)
	}
	if g.b.Sealed() {
		t.Fatal("a failed Build sealed the graph")
	}
}

func TestBuild_PanicInAConstructorIsAnErrorOnItsNode(t *testing.T) {
	g := newGraph()
	g.poolPanic = true
	err := g.b.Build("server", func() { g.Listener() })
	if err == nil || !strings.Contains(err.Error(), "listener → handler → pool: panic: boom") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuild_RuntimeCycleIsNamed(t *testing.T) {
	g := newGraph()
	g.cycle = true
	err := g.b.Build("server", func() { g.Listener() })
	if err == nil || !strings.Contains(err.Error(), "dependency cycle listener → handler → pool → listener") {
		t.Fatalf("err = %v", err)
	}
}

func TestSealed_AccessorForAnUnbuiltNodePanics(t *testing.T) {
	g := newGraph()
	if err := g.b.Build("worker", func() { g.Pool() }); err != nil {
		t.Fatal(err)
	}
	g.Pool() // built: still readable
	defer func() {
		r := recover()
		err, _ := r.(error)
		if !errors.Is(err, graphkit.ErrSealed) || !strings.Contains(err.Error(), "other") {
			t.Fatalf("recover = %v, want ErrSealed naming other", r)
		}
	}()
	g.Other()
}

func TestBuild_SecondRootIsAnError(t *testing.T) {
	g := newGraph()
	if err := g.b.Build("a", func() {}); err != nil {
		t.Fatal(err)
	}
	if err := g.b.Build("b", func() {}); err == nil {
		t.Fatal("building a second root on a sealed graph succeeded")
	}
}

func TestCatch_UnsealedUse(t *testing.T) {
	g := newGraph()
	g.poolErr = errors.New("nope")
	if err := graphkit.Catch(func() { g.Handler() }); err == nil || !strings.Contains(err.Error(), "handler → pool: nope") {
		t.Fatalf("err = %v", err)
	}
	g.poolErr = nil
	if err := graphkit.Catch(func() { g.Handler() }); err != nil {
		t.Fatalf("retry after a failure: %v (a stale path would report a cycle)", err)
	}
}

func TestSupervise_ReadyNodesJoinTheProbes(t *testing.T) {
	g := newGraph()
	if err := g.b.Build("server", func() { g.Listener() }); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	probes := &serverkit.Probes{Drainer: &serverkit.Drainer{PreStopDelay: -1, Logger: logger}, Logger: logger}
	readyz := probes.Front()["/readyz"]
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.b.Supervise(ctx, lifecyclekit.Policy{Logger: logger}, probes) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := httptest.NewRecorder()
		readyz.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code == http.StatusOK {
			if !strings.Contains(rec.Body.String(), `"name":"pool"`) {
				t.Fatalf("/readyz = %s, want the pool's check", rec.Body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/readyz never became ready: %d %s", rec.Code, rec.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.Pool().ready = errors.New("gone")
	rec := httptest.NewRecorder()
	readyz.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz with a failing node = %d, want 503", rec.Code)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !g.Pool().closed {
		t.Fatal("the pool was not closed on stop")
	}
}

func TestSupervise_BeforeBuildIsAnError(t *testing.T) {
	var b graphkit.Builder
	if err := b.Supervise(context.Background(), lifecyclekit.Policy{}, nil); err == nil {
		t.Fatal("Supervise before Build succeeded")
	}
}

func TestSupervise_FirstNodesStartBeforeAndStopAfterTheGraph(t *testing.T) {
	g := newGraph()
	if err := g.b.Build("worker", func() { g.Listener() }); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var mu sync.Mutex
	var events []string
	record := func(e string) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}
	probe := lifecyclekit.Node{
		Name: "probe_listener",
		Run: func(ctx context.Context) error {
			record("probe run")
			<-ctx.Done()
			return nil
		},
		Close: func(context.Context) error {
			mu.Lock()
			poolClosed := g.Pool().closed
			mu.Unlock()
			if !poolClosed {
				t.Error("the first node stopped before the graph's pool")
			}
			record("probe close")
			return nil
		},
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- g.b.Supervise(ctx, lifecyclekit.Policy{Logger: logger, Drainer: &serverkit.Drainer{PreStopDelay: -1, Logger: logger}}, nil, probe)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		started := len(events) > 0
		mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first node never ran")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, ", "); got != "probe run, probe close" {
		t.Fatalf("events = %s", got)
	}
}

// progress records the construction a Builder reports, as "+name" when a
// constructor starts and "-name" (or "-name!" on an error) when it ends.
type progress struct {
	mu     sync.Mutex
	events []string
}

func (p *progress) Constructing(node string) func(error) {
	p.record("+" + node)
	return func(err error) {
		if err != nil {
			p.record("-" + node + "!")
			return
		}
		p.record("-" + node)
	}
}

func (p *progress) record(e string) {
	p.mu.Lock()
	p.events = append(p.events, e)
	p.mu.Unlock()
}

func (p *progress) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.events, " ")
}

func TestReportTo_ConstructionNestsDependenciesInside(t *testing.T) {
	g := newGraph()
	p := &progress{}
	g.b.ReportTo(p)
	if err := g.b.Build("server", func() { g.Listener(); g.Handler() }); err != nil {
		t.Fatal(err)
	}
	// A memoized node is not reported again: handler's second access
	// constructs nothing.
	if got, want := p.String(), "+listener +handler +pool -pool -handler -listener"; got != want {
		t.Fatalf("progress = %q, want %q", got, want)
	}
}

func TestReportTo_AFailureEndsEveryNodeThatReachedIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*graph)
		want string
	}{
		{"error", func(g *graph) { g.poolErr = errors.New("dial refused") }, "+listener +handler +pool -pool! -handler! -listener!"},
		{"panic", func(g *graph) { g.poolPanic = true }, "+listener +handler +pool -pool! -handler! -listener!"},
		{"cycle", func(g *graph) { g.cycle = true }, "+listener +handler +pool -pool! -handler! -listener!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph()
			tc.set(g)
			p := &progress{}
			g.b.ReportTo(p)
			if err := g.b.Build("server", func() { g.Listener() }); err == nil {
				t.Fatal("Build succeeded")
			}
			if got := p.String(); got != tc.want {
				t.Fatalf("progress = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestReportTo_BootProbesNameTheNodeUnderConstruction is the whole hook: a
// constructor blocked at boot is named in the boot probes' /readyz as
// building, beside nothing yet built, and the body flips once it is built.
func TestReportTo_BootProbesNameTheNodeUnderConstruction(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, boot, err := serverkit.StartBootProbes(context.Background(), serverkit.Config{Addr: addr}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer boot.Close()

	g := newGraph()
	g.poolGate = make(chan struct{})
	g.b.ReportTo(serverkit.BootProbesFrom(ctx))
	built := make(chan error, 1)
	go func() { built <- g.b.Build("worker", func() { g.Handler() }) }()

	readyz := func() (int, string) {
		t.Helper()
		resp, gerr := http.Get("http://" + addr + serverkit.DefaultReadinessPath)
		if gerr != nil {
			t.Fatalf("GET readyz: %v", gerr)
		}
		defer func() { _ = resp.Body.Close() }()
		var body struct {
			Status string `json:"status"`
			Checks []struct {
				Name   string `json:"name"`
				OK     bool   `json:"ok"`
				Detail string `json:"detail"`
			} `json:"checks"`
		}
		if derr := json.NewDecoder(resp.Body).Decode(&body); derr != nil {
			t.Fatalf("decode readyz: %v", derr)
		}
		parts := []string{body.Status}
		for _, c := range body.Checks {
			parts = append(parts, fmt.Sprintf("%s=%t/%s", c.Name, c.OK, c.Detail))
		}
		return resp.StatusCode, strings.Join(parts, " ")
	}

	want := "not_ready handler=false/building pool=false/building"
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, got := readyz()
		if code != http.StatusServiceUnavailable {
			t.Fatalf("readyz while booting = %d %s, want 503", code, got)
		}
		if got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readyz = %q, want %q", got, want)
		}
		time.Sleep(5 * time.Millisecond)
	}

	close(g.poolGate)
	if err := <-built; err != nil {
		t.Fatal(err)
	}
	// Built, the nodes are ok — and still 503: the process serves nothing
	// until a listener takes the address over.
	if code, got := readyz(); code != http.StatusServiceUnavailable || got != "not_ready pool=true/ handler=true/" {
		t.Fatalf("readyz once built = %d %q, want 503 \"not_ready pool=true/ handler=true/\"", code, got)
	}
}
