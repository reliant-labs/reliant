package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/pkg/observe"
)

// upT0 is the fixed instant every test's clock starts at.
var upT0 = time.Date(2026, 11, 18, 9, 30, 0, 0, time.UTC)

// fakeUpTableClock runs the table on a fixed clock: upTableSleep advances it instead of
// waiting, and returns false once ctx is done.
func fakeUpTableClock(t *testing.T) *time.Time {
	t.Helper()
	now := upT0
	prevNow, prevSleep := upTableNow, upTableSleep
	upTableNow = func() time.Time { return now }
	upTableSleep = func(ctx context.Context, d time.Duration) bool {
		if ctx.Err() != nil {
			return false
		}
		now = now.Add(d)
		return true
	}
	t.Cleanup(func() { upTableNow, upTableSleep = prevNow, prevSleep })
	return &now
}

func stubUpEndpoints(t *testing.T, q func(context.Context, string, time.Time) ([]obsEndpointDoc, error)) {
	t.Helper()
	prev := upEndpointsQuery
	upEndpointsQuery = q
	t.Cleanup(func() { upEndpointsQuery = prev })
}

func TestPrintUpEndpointsTable(t *testing.T) {
	fakeUpTableClock(t)
	var from time.Time
	stubUpEndpoints(t, func(_ context.Context, env string, since time.Time) ([]obsEndpointDoc, error) {
		if env != "dev" {
			t.Errorf("queried env %q, want dev", env)
		}
		from = since
		return []obsEndpointDoc{
			{Service: "api", Name: "shop.v1.ShopService/ListWidgets", Requests: 42, ErrorRatio: 0.0238, P50Ms: 3.2, P95Ms: 18, P99Ms: 1250},
			{Service: "api", Name: "shop.v1.ShopService/CreateWidget", Requests: 7, P50Ms: 9.5, P95Ms: 12, P99Ms: 14},
		}, nil
	})
	var out bytes.Buffer
	printUpEndpoints(context.Background(), &out, "dev", upHealthProbes{started: upT0, at: upT0})
	got := out.String()
	if !from.Equal(upT0) {
		t.Errorf("queried from %s, want since env up started (%s)", from, upT0)
	}
	for _, want := range []string{
		"Per-RPC latency and errors since env up started at " + upT0.Local().Format("15:04") + ", no instrumentation code (`" + Name() + " obs endpoints dev` for more):",
		"ENDPOINT", "REQUESTS", "ERRORS", "P50", "P95", "P99",
		"shop.v1.ShopService/ListWidgets", "42", "2.4%", "3.2ms", "18ms", "1.25s",
		"shop.v1.ShopService/CreateWidget",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
	if n := strings.Count(strings.TrimSpace(got), "\n") + 1; n != 4 {
		t.Errorf("table is %d lines, want heading + header + 2 rows:\n%s", n, got)
	}
}

// Nothing served yet or no telemetry backend: one line naming the command
// and what it shows, and never an error.
func TestPrintUpEndpointsFallsBackToOneLine(t *testing.T) {
	for name, q := range map[string]func(context.Context, string, time.Time) ([]obsEndpointDoc, error){
		"no requests yet": func(context.Context, string, time.Time) ([]obsEndpointDoc, error) { return nil, nil },
		"no backend": func(context.Context, string, time.Time) ([]obsEndpointDoc, error) {
			return nil, errors.New("clickstack is not running")
		},
	} {
		t.Run(name, func(t *testing.T) {
			fakeUpTableClock(t)
			stubUpEndpoints(t, q)
			var out bytes.Buffer
			printUpEndpoints(context.Background(), &out, "dev", upHealthProbes{started: upT0})
			got := strings.TrimSpace(out.String())
			if strings.Count(got, "\n") != 0 || !strings.Contains(got, "`"+Name()+" obs endpoints dev` shows p50/p95/p99 and the error rate per RPC") {
				t.Errorf("want the one line, got:\n%s", got)
			}
		})
	}
}

// The real query is `obs endpoints <env> --from <start> --json` run
// in-process: rows come back decoded, at most upEndpointsTop, over the window
// since env up started, with the health check's row marked.
func TestUpEndpointsQueryRunsObsEndpoints(t *testing.T) {
	var asked obsbackend.EndpointsRequest
	old := obsDepsOverride
	t.Cleanup(func() { obsDepsOverride = old })
	obsDepsOverride = &obsDeps{
		Facts: func(context.Context, io.Writer, string) (obsbackend.Facts, error) { return devFacts(), nil },
		Local: func(string) obsbackend.Backend {
			return &obsbackend.MockBackend{EndpointsFunc: func(_ context.Context, req obsbackend.EndpointsRequest) (obsbackend.EndpointsResult, error) {
				asked = req
				return obsbackend.EndpointsResult{Endpoints: []obsbackend.Endpoint{
					{Service: "api", Name: "shop.v1.ShopService/ListWidgets", Requests: 3, P50Ms: 2},
					{Service: "api", Name: "grpc.health.v1.Health/Check", Requests: 1, P50Ms: 0.4},
				}}, nil
			}}
		},
		Now:     func() time.Time { return obsNow },
		Release: func(context.Context, string, time.Time) (*liveRelease, error) { return nil, nil },
	}
	from := obsNow.Add(-10 * time.Minute)
	rows, err := upEndpointsQuery(context.Background(), "dev", from)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Name != "shop.v1.ShopService/ListWidgets" || rows[0].Health {
		t.Errorf("rows = %+v", rows)
	}
	if len(rows) == 2 && !rows[1].Health {
		t.Errorf("the health check's row is not marked: %+v", rows[1])
	}
	if asked.Top != upEndpointsTop {
		t.Errorf("asked for top %d, want %d", asked.Top, upEndpointsTop)
	}
	if !asked.Range.From.Equal(from.Truncate(time.Second)) {
		t.Errorf("window from %s, want %s (since env up started)", asked.Range.From, from)
	}
}

// The first env up of a fresh stack: the health check env up sent is the one
// row, labelled. The table waits for its span to be stored, within
// upEndpointsSettle of the probe.
func TestPrintUpEndpointsWaitsForTheHealthProbe(t *testing.T) {
	now := fakeUpTableClock(t)
	calls := 0
	stubUpEndpoints(t, func(context.Context, string, time.Time) ([]obsEndpointDoc, error) {
		calls++
		if calls < 3 { // not stored yet
			return nil, nil
		}
		return []obsEndpointDoc{{Service: "api", Name: "grpc.health.v1.Health/Check", Requests: 1, P50Ms: 0.4, P95Ms: 0.4, P99Ms: 0.4, Health: true}}, nil
	})
	var out bytes.Buffer
	printUpEndpoints(context.Background(), &out, "dev", upHealthProbes{started: upT0, at: upT0, answered: 1})
	got := out.String()
	for _, want := range []string{
		"grpc.health.v1.Health/Check  (health check; env up sent one, and release health leaves env up's out)",
		"Your own RPCs join the table as they serve requests.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
	if calls != 3 || !now.Equal(upT0.Add(2*upEndpointsPoll)) {
		t.Errorf("queried %d times over %s, want 3 over two polls", calls, now.Sub(upT0))
	}

	// Never stored (a slow pipeline): the line, by the settle deadline.
	*now = upT0
	calls = 0
	stubUpEndpoints(t, func(context.Context, string, time.Time) ([]obsEndpointDoc, error) { calls++; return nil, nil })
	out.Reset()
	printUpEndpoints(context.Background(), &out, "dev", upHealthProbes{started: upT0, at: upT0, answered: 1})
	if now.Sub(upT0) > upEndpointsSettle || !strings.Contains(out.String(), "once it has served requests") {
		t.Errorf("after %s: %s", now.Sub(upT0), out.String())
	}
}

// A store that keeps failing ends the wait: no spinning to the deadline.
func TestReadUpEndpointsStopsOnPersistentErrors(t *testing.T) {
	now := fakeUpTableClock(t)
	calls := 0
	stubUpEndpoints(t, func(context.Context, string, time.Time) ([]obsEndpointDoc, error) {
		calls++
		return nil, errors.New("query refused")
	})
	if rows := readUpEndpoints(context.Background(), "dev", upHealthProbes{started: upT0, at: upT0, answered: 1}); rows != nil {
		t.Fatalf("rows = %v", rows)
	}
	if calls != upEndpointsMaxErrors || now.Sub(upT0) >= upEndpointsSettle {
		t.Errorf("%d queries over %s, want %d and an early stop", calls, now.Sub(upT0), upEndpointsMaxErrors)
	}
}

// Cancelling env up (Ctrl-C) ends the wait at once: no further query, the
// query in flight sees the cancellation, and nothing is printed.
func TestReadUpEndpointsStopsOnCancel(t *testing.T) {
	fakeUpTableClock(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	stubUpEndpoints(t, func(qctx context.Context, _ string, _ time.Time) ([]obsEndpointDoc, error) {
		calls++
		cancel() // the user hits Ctrl-C while the first query runs
		if qctx.Err() == nil {
			t.Error("the query's context does not follow the run's: a cancelled env up would wait it out")
		}
		return nil, nil
	})
	var out bytes.Buffer
	printUpEndpoints(ctx, &out, "dev", upHealthProbes{started: upT0, at: upT0, answered: 1})
	if calls != 1 || out.Len() != 0 {
		t.Errorf("%d queries, printed %q; want one query and nothing after the cancel", calls, out.String())
	}

	// Already cancelled: no query at all.
	calls = 0
	printUpEndpoints(ctx, &out, "dev", upHealthProbes{started: upT0, at: upT0, answered: 1})
	if calls != 0 {
		t.Errorf("queried %d times after the run was cancelled", calls)
	}
}

// env up calls the health rpc once on every host service with a port, and
// counts the ones that answered; frontends and portless workers get none.
func TestSendUpHealthProbes(t *testing.T) {
	fakeUpTableClock(t)
	var asked []string
	prev := upHealthProbe
	upHealthProbe = func(_ context.Context, base string) error {
		asked = append(asked, base)
		if strings.Contains(base, "9999") {
			return errors.New("404: an older forge/pkg serves no health rpc")
		}
		return nil
	}
	t.Cleanup(func() { upHealthProbe = prev })
	started := upT0.Add(-time.Minute)
	p := sendUpHealthProbes(context.Background(), started, []upServiceRow{
		{Name: "api", Kind: "host", URL: "http://localhost:8080", Port: 8080},
		{Name: "old", Kind: "host", Port: 9999},
		{Name: "worker", Kind: "host"},
		{Name: "web", Kind: "frontend", URL: "http://localhost:3000", Port: 3000},
	})
	if strings.Join(asked, ",") != "http://localhost:8080,http://127.0.0.1:9999" {
		t.Errorf("probed %v", asked)
	}
	if p.answered != 1 || !p.at.Equal(upT0) || !p.started.Equal(started) {
		t.Errorf("probes = %+v, want 1 answered at %s, started %s", p, upT0, started)
	}
}

// The real probe is a Connect unary POST of {} to the health rpc, declared a
// probe with observe.HeaderHealthProbe.
func TestUpHealthProbeIsAConnectCall(t *testing.T) {
	var method, path, ctype, probe, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, ctype, probe, body = r.Method, r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get(observe.HeaderHealthProbe), string(b)
		_, _ = w.Write([]byte(`{"status":"SERVING"}`))
	}))
	defer srv.Close()
	if err := upHealthProbe(context.Background(), srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/grpc.health.v1.Health/Check" || ctype != "application/json" || body != "{}" || probe != "env-up" {
		t.Errorf("sent %s %s (%s, probe %q) %q", method, path, ctype, probe, body)
	}
}
