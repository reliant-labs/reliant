package serverkit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// drain_phases_test.go — a shutdown says where its time went.
//
// The sequence used to log "server stopping" and then nothing until an error,
// so a rollout that took the whole grace period could not be diagnosed from
// its own log: the pre-stop pause, a stream nobody ended, a worker ignoring
// its context and a teardown blocked on a pool all looked the same.

// TestRun_LogsEachDrainPhase: every phase writes one "drain: ..." line, in
// order, each carrying `phase` and `elapsed`, and the listener's says what the
// drain did to the streams that were open.
func TestRun_LogsEachDrainPhase(t *testing.T) {
	requireSelfSIGTERM(t)
	// Not parallel — sends SIGTERM.
	addr := freeAddr(t)
	var logs syncBuf
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	mux := http.NewServeMux()
	mux.Handle("/events", serverkit.LongLived(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		http.NewResponseController(w).Flush() //nolint:errcheck // test stream
		<-r.Context().Done()
	})))
	srv := serverkit.Server{Handler: mux, Logger: logger}
	srv.AddWorker(&stubWorker{name: "sweeper"})
	errCh, _ := runInBackground(t, baseConfig(addr), srv)
	waitReady(t, addr, 5*time.Second)

	stream := openStream(t, "http://"+addr+"/events")
	defer func() { _ = stream.Close() }()
	go func() { _, _ = io.Copy(io.Discard, stream) }()

	if err := shutdownAndWait(t, errCh, 30*time.Second); err != nil {
		t.Fatalf("Run returned %v", err)
	}

	// Run drains with the same Drainer and DrainHTTP a hand-built main uses,
	// so each phase is written exactly once — by whichever piece owns it.
	phases := drainPhaseLines(t, logs.String())
	assertPhaseOrder(t, phases, logs.String(), "readiness", "pre_stop", "listener", "workers", "teardown", "total")
	listener := phases[2]
	if listener["streams_ended"] != float64(1) || listener["streams_still_running"] != float64(0) {
		t.Errorf("listener line = %v, want streams_ended=1 streams_still_running=0", listener)
	}
	if phases[3]["workers"] != float64(1) {
		t.Errorf("workers line = %v, want workers=1", phases[3])
	}
}

// A main that drains its own listener with Drainer and DrainHTTP — not Run —
// gets the same phase lines and forge.server.drain.duration points, written
// to the logger and meter it configured them with.
func TestDrainerAndDrainHTTP_LogAndMeasureTheirPhasesOutsideRun(t *testing.T) {
	t.Parallel()
	var logs syncBuf
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")

	cfg := serverkit.Config{Addr: "127.0.0.1:0", PreStopDelay: 10 * time.Millisecond, ShutdownTimeout: 2 * time.Second}
	drainer := serverkit.NewDrainer(cfg)
	drainer.Logger, drainer.Meter = logger, meter
	streams := &serverkit.Streams{Logger: logger, Meter: meter}
	srv := serverkit.NewHTTPServer(cfg, http.NotFoundHandler(), streams)
	ctx, release := streams.Bind(context.Background()) // an open stream, ended by the drain
	go func() { <-ctx.Done(); release() }()

	drainer.Begin()
	shutdown, cancel := drainer.ShutdownContext()
	defer cancel()
	if err := serverkit.DrainHTTP(shutdown, srv, streams); err != nil {
		t.Fatalf("DrainHTTP = %v", err)
	}
	drainer.Complete()
	drainer.Complete() // a second call writes nothing

	phases := drainPhaseLines(t, logs.String())
	assertPhaseOrder(t, phases, logs.String(), "readiness", "pre_stop", "listener", "total")
	if phases[2]["streams_ended"] != float64(1) {
		t.Errorf("listener line = %v, want streams_ended=1", phases[2])
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	points := map[string]uint64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "forge.server.drain.duration" {
				continue
			}
			if m.Unit != "s" {
				t.Errorf("unit = %q, want s (the declaration's)", m.Unit)
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("drain duration is a %T, want a float64 histogram", m.Data)
			}
			for _, dp := range hist.DataPoints {
				v, _ := dp.Attributes.Value("phase")
				points[v.AsString()] += dp.Count
			}
		}
	}
	want := map[string]uint64{"pre_stop": 1, "listener": 1, "total": 1}
	if fmt.Sprint(points) != fmt.Sprint(want) {
		t.Fatalf("forge.server.drain.duration counts by phase = %v, want %v", points, want)
	}
}

// drainPhaseLines returns the "drain: ..." records of a JSON log.
func drainPhaseLines(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var phases []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		var rec map[string]any
		if json.Unmarshal([]byte(line), &rec) == nil {
			if msg, _ := rec["msg"].(string); strings.HasPrefix(msg, "drain: ") {
				phases = append(phases, rec)
			}
		}
	}
	return phases
}

// assertPhaseOrder fails unless the phase lines are exactly want, in order —
// so each once — and every one but readiness carries its elapsed.
func assertPhaseOrder(t *testing.T, phases []map[string]any, logs string, want ...string) {
	t.Helper()
	var order []string
	for _, rec := range phases {
		order = append(order, fmt.Sprint(rec["phase"]))
		if _, ok := rec["elapsed"]; !ok && rec["phase"] != "readiness" {
			t.Errorf("%q has no elapsed: %v", rec["msg"], rec)
		}
	}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("drain phases logged = %v, want %v\n%s", order, want, logs)
	}
}

// The drain's own accounting, which the listener line reports.
func TestStreams_ReportCountsWhatTheDrainDid(t *testing.T) {
	t.Parallel()
	streams := &serverkit.Streams{DrainReserve: 400 * time.Millisecond}
	quick, releaseQuick := streams.Bind(context.Background())
	go func() { <-quick.Done(); releaseQuick() }()
	held, releaseHeld := streams.Bind(context.Background())
	_ = serverkit.Draining(held) // claims the soft phase, then ignores it
	go func() { <-held.Done(); releaseHeld() }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := streams.Drain(ctx); err != nil {
		t.Fatalf("Drain = %v", err)
	}
	streams.BeginDrain() // a second call — NewHTTPServer's hook — counts nothing twice
	_, late := streams.Bind(context.Background())
	late()

	got := streams.Report()
	want := serverkit.DrainReport{Ended: 2, Claimed: 1, HardEnded: 1}
	if got != want {
		t.Fatalf("Report = %+v, want %+v (one ended at once, one claimed then hard-ended, one refused late)", got, want)
	}
}
