package serverkit

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The defaults are unchanged: a Config that names no paths answers /healthz
// and /readyz, and so does a Probes surface that names none.
func TestProbePaths_DefaultsUnchanged(t *testing.T) {
	var cfg Config
	cfg.Normalize()
	if !slices.Equal(cfg.Probes.Live, []string{"/healthz"}) || !slices.Equal(cfg.Probes.Ready, []string{"/readyz"}) {
		t.Fatalf("Normalize: probes = %+v, want /healthz and /readyz", cfg.Probes)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := &Probes{Drainer: &Drainer{Logger: logger}, Logger: logger}
	if got := frontPaths(p.Front()); !slices.Equal(got, []string{"/healthz", "/readyz"}) {
		t.Fatalf("Front = %v, want [/healthz /readyz]", got)
	}
}

// Custom paths are served — by the boot probes, then by the probe listener
// that takes the address over — and so is a route added with Handle; the
// default paths, no longer configured, are not.
func TestProbePaths_CustomPathsServed(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	paths := ProbePaths{Live: []string{"/health", "/healthz"}, Ready: []string{"/ready"}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	get := func(path string) int {
		t.Helper()
		resp, gerr := http.Get("http://" + addr + path)
		if gerr != nil {
			t.Fatalf("GET %s: %v", path, gerr)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	want := func(phase string, codes map[string]int) {
		t.Helper()
		for path, code := range codes {
			if got := get(path); got != code {
				t.Errorf("%s: GET %s = %d, want %d", phase, path, got, code)
			}
		}
	}

	ctx, boot, err := StartBootProbes(ctx, Config{Addr: addr, Probes: paths}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer boot.Close()
	want("boot", map[string]int{"/health": 200, "/healthz": 200, "/ready": 503, "/readyz": 404})

	p := &Probes{Drainer: &Drainer{Logger: logger}, Logger: logger, Paths: paths}
	if err := p.Handle("/flow-health", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	l, err := NewProbeListener(ProbeListenerDeps{Config: ListenerConfig{Addr: addr}, Probes: p, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Listen(ctx); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run(ctx) }()
	want("serving", map[string]int{"/health": 200, "/healthz": 200, "/ready": 503, "/readyz": 404, "/flow-health": http.StatusTeapot})
	p.Drainer.MarkReady()
	want("ready", map[string]int{"/ready": 200})

	cancel()
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// serverkit.Run's listener builds its probe routes from the same paths.
func TestProbePaths_RunRoutes(t *testing.T) {
	ready := http.NotFoundHandler()
	got := frontPaths(ProbePaths{Live: []string{"/health"}, Ready: []string{"/ready", "/readyz"}}.routes(ready))
	if !slices.Equal(got, []string{"/health", "/ready", "/readyz"}) {
		t.Fatalf("routes = %v", got)
	}
}

func TestProbePaths_Validate(t *testing.T) {
	for name, tc := range map[string]struct {
		paths ProbePaths
		want  string // "" valid
	}{
		"defaults":         {ProbePaths{}, ""},
		"aliases":          {ProbePaths{Live: []string{"/health", "/healthz"}, Ready: []string{"/ready"}}, ""},
		"relative":         {ProbePaths{Live: []string{"health"}}, `does not start with "/"`},
		"root":             {ProbePaths{Ready: []string{"/"}}, "every route"},
		"wildcard":         {ProbePaths{Ready: []string{"/ready/{x}"}}, "not a plain path"},
		"both probes":      {ProbePaths{Live: []string{"/health"}, Ready: []string{"/health"}}, "both a liveness and a readiness"},
		"forge's /metrics": {ProbePaths{Live: []string{"/metrics"}}, "forge's own"},
	} {
		t.Run(name, func(t *testing.T) {
			err := tc.paths.Validate()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("Validate: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	if _, _, err := StartBootProbes(context.Background(), Config{Addr: "127.0.0.1:0", Probes: ProbePaths{Live: []string{"x"}}}, nil); err == nil {
		t.Fatal("StartBootProbes accepted an invalid probe path")
	}
}

func TestProbes_Handle(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := &Probes{Drainer: &Drainer{Logger: logger}, Logger: logger, Metrics: http.NotFoundHandler(), Paths: ProbePaths{Ready: []string{"/ready"}}}
	ok := http.NotFoundHandler()
	for path, want := range map[string]string{
		"/ready":   "already serves",
		"/metrics": "already serves",
		"/healthz": "already serves",
		"health":   `does not start with "/"`,
		"/":        "every route",
	} {
		if err := p.Handle(path, ok); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Handle(%q) = %v, want an error containing %q", path, err, want)
		}
	}
	if err := p.Handle("/flow-health", nil); err == nil {
		t.Error("Handle accepted a nil handler")
	}
	if err := p.Handle("/flow-health", ok); err != nil {
		t.Fatalf("Handle(/flow-health): %v", err)
	}
	if err := p.Handle("/flow-health", ok); err == nil {
		t.Error("Handle accepted the same path twice")
	}
	if got := frontPaths(p.Front()); !slices.Equal(got, []string{"/flow-health", "/healthz", "/metrics", "/ready"}) {
		t.Fatalf("Front = %v", got)
	}
	if err := p.Handle("/late", ok); err == nil || !strings.Contains(err.Error(), "already took") {
		t.Fatalf("Handle after Front = %v, want the late-route error", err)
	}
}

func TestProbePathList(t *testing.T) {
	for in, want := range map[string][]string{
		"":                   nil,
		"/healthz":           {"/healthz"},
		" /health, /healthz": {"/health", "/healthz"},
		"/ready,,":           {"/ready"},
	} {
		if got := ProbePathList(in); !slices.Equal(got, want) {
			t.Errorf("ProbePathList(%q) = %v, want %v", in, got, want)
		}
	}
}

func frontPaths(front map[string]http.Handler) []string {
	out := make([]string, 0, len(front))
	for p := range front {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
