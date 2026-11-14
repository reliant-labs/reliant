package cliev

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/observe"
)

// sink is a fake ingest endpoint that keeps every batch it receives. mu
// guards every field: the handler runs on the server's goroutines, and a
// request the client abandoned can still be in flight when a test changes
// the endpoint's behaviour.
type sink struct {
	mu      sync.Mutex
	batches []otlpPayload
	status  int
	// hang makes the endpoint answer only once the client gives up.
	hang bool
}

// respond sets how the endpoint answers the next request.
func (s *sink) respond(hang bool, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hang, s.status = hang, status
}

func newSink(t *testing.T) (*sink, *httptest.Server) {
	t.Helper()
	s := &sink{status: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body) // the server notices a dropped client only after the body is read
		s.mu.Lock()
		hang, status := s.hang, s.status
		s.mu.Unlock()
		if hang {
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		var p otlpPayload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Errorf("bad OTLP JSON %s: %v", body, err)
		}
		s.mu.Lock()
		s.batches = append(s.batches, p)
		s.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *sink) records() []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]string
	for _, p := range s.batches {
		for _, rl := range p.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, r := range sl.LogRecords {
					m := map[string]string{"_body": r.Body.StringValue}
					for _, a := range r.Attributes {
						m[a.Key] = a.Value.StringValue
					}
					out = append(out, m)
				}
			}
		}
	}
	return out
}

func testConfig(t *testing.T, endpoint string, env map[string]string, stderr *bytes.Buffer) Config {
	t.Helper()
	return Config{
		App: "forge", Version: "1.5.0", Endpoint: endpoint, ConfigDir: t.TempDir(),
		Stderr: stderr, Getenv: func(k string) string { return env[k] },
	}
}

func TestFirstRunSendsOneBatchWithInstallID(t *testing.T) {
	s, srv := newSink(t)
	var stderr bytes.Buffer
	cfg := testConfig(t, srv.URL, nil, &stderr)
	c := New(cfg)
	if !c.Enabled() || !c.FirstRun() {
		t.Fatalf("a fresh install must be enabled and on its first run: %+v", c.Status())
	}
	if !strings.Contains(stderr.String(), "forge telemetry off") || !strings.Contains(stderr.String(), "FORGE_TELEMETRY=off") {
		t.Errorf("notice must say how to opt out: %q", stderr.String())
	}
	ctx := observe.WithActor(context.Background(), observe.Actor{OrgID: "org-1"})
	c.Track(ctx, FirstRunEvent("forge"), c.FirstRunAttrs("claude")...)
	c.Track(ctx, FirstRunEvent("forge")) // deduplicated within the run
	c.Track(ctx, "forge_cli_command", slog.String("command", "env.deploy"), slog.String("repo_path", "/home/someone/x@y.com"))
	c.Flush(context.Background())

	recs := s.records()
	if len(s.batches) != 1 || len(recs) != 2 {
		t.Fatalf("want 1 batch of 2 records, got %d batches, %v", len(s.batches), recs)
	}
	first := recs[0]
	for k, want := range map[string]string{
		"_body": "product.forge_cli_first_run", "event.name": "product.forge_cli_first_run",
		"app.client": "cli", "app.version": "1.5.0", "visitor.id": c.InstallID(), "org.id": "org-1",
		"product.harness": "claude", "product.version": "1.5.0",
	} {
		if first[k] != want {
			t.Errorf("%s = %q, want %q (record %v)", k, first[k], want, first)
		}
	}
	if _, ok := recs[1]["product.repo_path"]; ok {
		t.Errorf("a PII-looking prop must be dropped: %v", recs[1])
	}
	if s.batches[0].ResourceLogs[0].Resource.Attributes[0].Value.StringValue != "forge-cli" {
		t.Errorf("service.name must be forge-cli: %+v", s.batches[0].ResourceLogs[0].Resource)
	}

	// The next run: same install id, no notice, first run already sent.
	stderr.Reset()
	c2 := New(cfg)
	if c2.InstallID() != c.InstallID() || c2.FirstRun() || stderr.Len() != 0 {
		t.Errorf("second run: id %q (want %q), firstRun %v, notice %q", c2.InstallID(), c.InstallID(), c2.FirstRun(), stderr.String())
	}
}

func TestOptOuts(t *testing.T) {
	_, srv := newSink(t)
	for _, tc := range []struct {
		env    map[string]string
		reason string
	}{
		{map[string]string{"DO_NOT_TRACK": "1"}, "do_not_track"},
		{map[string]string{"DO_NOT_TRACK": "1", "FORGE_TELEMETRY": "on"}, "do_not_track"},
		{map[string]string{"FORGE_TELEMETRY": "off"}, "env_off"},
		{map[string]string{"CI": "true"}, "ci"},
		{map[string]string{"CI": "true", "FORGE_TELEMETRY": "on"}, "ci"}, // nothing turns it on in CI
		{map[string]string{"CI": "1"}, "ci"},
		{map[string]string{"GITHUB_ACTIONS": "true"}, "ci"},
		{map[string]string{"GITLAB_CI": "true", "FORGE_TELEMETRY": "on"}, "ci"},
		{map[string]string{"BUILDKITE": "true"}, "ci"},
		{map[string]string{"JENKINS_URL": "https://jenkins.example"}, "ci"},
		{map[string]string{"CI": "false"}, "on"},
		{map[string]string{"DO_NOT_TRACK": "0"}, "on"},
	} {
		var stderr bytes.Buffer
		c := New(testConfig(t, srv.URL, tc.env, &stderr))
		if got := c.Status().Reason; got != tc.reason {
			t.Errorf("env %v: reason %q, want %q", tc.env, got, tc.reason)
		}
		if tc.reason != "on" && (stderr.Len() != 0 || c.InstallID() != "") {
			t.Errorf("env %v: an opted-out run must print nothing and mint no id (notice %q, id %q)", tc.env, stderr.String(), c.InstallID())
		}
	}
	if got := New(testConfig(t, "", nil, &bytes.Buffer{})).Status().Reason; got != "no_endpoint" {
		t.Errorf("no endpoint: reason %q, want no_endpoint", got)
	}
}

func TestTelemetryOffIsRemembered(t *testing.T) {
	s, srv := newSink(t)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	c := New(cfg)
	c.Track(context.Background(), "forge_cli_command")
	if err := c.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	c.Flush(context.Background())
	if n := len(s.records()); n != 0 {
		t.Errorf("telemetry off must drop the pending batch, sent %d", n)
	}
	if got := New(cfg).Status().Reason; got != "opted_out" {
		t.Errorf("next run reason = %q, want opted_out", got)
	}
	if err := c.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !New(cfg).Enabled() {
		t.Error("telemetry on must re-enable")
	}
}

func TestQuietRunSendsNothingUntilTheNoticeWasShown(t *testing.T) {
	s, srv := newSink(t)
	var stderr bytes.Buffer
	cfg := testConfig(t, srv.URL, nil, &stderr)
	cfg.Quiet = true
	c := New(cfg)
	c.Track(context.Background(), "forge_cli_command")
	c.Flush(context.Background())
	if stderr.Len() != 0 || len(s.records()) != 0 {
		t.Fatalf("a --json first run must neither print the notice nor send: %q, %v", stderr.String(), s.records())
	}
	cfg.Quiet = false
	c = New(cfg)
	c.Track(context.Background(), "forge_cli_command")
	c.Flush(context.Background())
	if stderr.Len() == 0 || len(s.records()) != 1 {
		t.Errorf("after the notice the run sends: notice %q, records %v", stderr.String(), s.records())
	}
}

func TestFlushIsBoundedAndDropsFailures(t *testing.T) {
	s, srv := newSink(t)
	s.respond(true, http.StatusOK)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	c := New(cfg)
	c.Track(context.Background(), FirstRunEvent("forge"))
	// The endpoint never answers: Flush returning at all is its own
	// FlushTimeout cancelling the request.
	c.Flush(context.Background())
	if !New(cfg).FirstRun() {
		t.Error("an unsent first-run event must be retried on the next run")
	}

	s.respond(false, http.StatusServiceUnavailable)
	c = New(cfg)
	c.Track(context.Background(), FirstRunEvent("forge"))
	c.Flush(context.Background())
	if !New(cfg).FirstRun() {
		t.Error("a refused first-run event must be retried on the next run")
	}
}

func TestDailyCap(t *testing.T) {
	s, srv := newSink(t)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	cfg.Now = func() time.Time { return now }
	for run := 0; run < 3; run++ {
		c := New(cfg)
		for i := 0; i < 30; i++ {
			c.Track(context.Background(), "ev_"+string(rune('a'+i%26))+string(rune('a'+run)))
		}
		c.Flush(context.Background())
	}
	if n := len(s.records()); n != MaxEventsPerDay {
		t.Errorf("sent %d events in a day, want the cap %d", n, MaxEventsPerDay)
	}
	now = now.Add(24 * time.Hour)
	c := New(cfg)
	c.Track(context.Background(), "next_day")
	c.Flush(context.Background())
	if n := len(s.records()); n != MaxEventsPerDay+1 {
		t.Errorf("the cap must reset the next day: %d", n)
	}
}

func TestSeedInstallID(t *testing.T) {
	_, srv := newSink(t)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	cfg.SeedInstallID = func() string { return "11111111-2222-4333-8444-555555555555" }
	if got := New(cfg).InstallID(); got != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("install id = %q, want the seeded one", got)
	}
	cfg = testConfig(t, srv.URL, nil, &bytes.Buffer{})
	cfg.SeedInstallID = func() string { return "someone@example.com" }
	if got := New(cfg).InstallID(); got == "someone@example.com" || len(got) != 36 {
		t.Errorf("a non-opaque seed must be replaced by a fresh UUID, got %q", got)
	}
}

func TestNilAndDefaultClientAreSafe(t *testing.T) {
	var c *Client
	c.Track(context.Background(), "x")
	c.Flush(context.Background())
	if c.Enabled() || c.InstallID() != "" || c.FirstRun() {
		t.Error("a nil client is off")
	}
	Track(context.Background(), "x") // before Init
	Flush(context.Background())
}

func TestInstallAttribution(t *testing.T) {
	s, srv := newSink(t)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	c := New(cfg)
	if err := c.SetInstallAttribution("rid=v_landing_1234&utm_source=docs&utm_medium=copy+button&email=a@b.c&utm_campaign=someone@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetInstallAttribution("utm_source=later"); err != nil { // first touch wins
		t.Fatal(err)
	}
	got := New(cfg).InstallAttribution()
	if got.Get("rid") != "v_landing_1234" || got.Get("utm_source") != "docs" || got.Get("utm_medium") != "copy button" || got.Has("email") || got.Has("utm_campaign") {
		t.Errorf("InstallAttribution = %v", got)
	}
	c = New(cfg)
	c.Track(context.Background(), FirstRunEvent("forge"), c.FirstRunAttrs("")...)
	c.Flush(context.Background())
	rec := s.records()[0]
	if rec["product.source"] != "docs" || rec["product.medium"] != "copy button" || rec["product.visitor_id"] != "v_landing_1234" {
		t.Errorf("first run carries the install attribution: %v", rec)
	}
	off := testConfig(t, srv.URL, map[string]string{"DO_NOT_TRACK": "1"}, &bytes.Buffer{})
	if err := New(off).SetInstallAttribution("utm_source=docs"); err != nil || len(New(off).InstallAttribution()) != 0 {
		t.Error("nothing is recorded while telemetry is off")
	}
}

func TestQuietEnvSuppressesTheNotice(t *testing.T) {
	var stderr bytes.Buffer
	cfg := testConfig(t, "http://127.0.0.1:1/v1/logs", map[string]string{"FORGE_TELEMETRY_QUIET": "1"}, &stderr)
	c := New(cfg)
	if !c.Enabled() || stderr.Len() != 0 {
		t.Errorf("a quiet run is still on but prints nothing: enabled %v, notice %q", c.Enabled(), stderr.String())
	}
	cfg.Getenv = func(string) string { return "" }
	New(cfg)
	if stderr.Len() == 0 {
		t.Error("the next ordinary run shows the notice")
	}
}

// Owner decision 2: a CI run sends nothing, and its first-run tags never say
// "ci" (there is nothing to tag: CI runs are not sent).
func TestCIRunsSendNothing(t *testing.T) {
	s, srv := newSink(t)
	var stderr bytes.Buffer
	c := New(testConfig(t, srv.URL, map[string]string{"CI": "true", "FORGE_TELEMETRY": "on"}, &stderr))
	c.Track(context.Background(), FirstRunEvent("forge"), c.FirstRunAttrs("")...)
	c.Flush(context.Background())
	if len(s.records()) != 0 || stderr.Len() != 0 || c.InstallID() != "" {
		t.Errorf("a CI run sent %v, printed %q, minted %q", s.records(), stderr.String(), c.InstallID())
	}
	for _, a := range New(testConfig(t, srv.URL, nil, &bytes.Buffer{})).FirstRunAttrs("") {
		if a.Key == "ci" {
			t.Error("the first-run event must not carry a ci tag")
		}
	}
}

// A run that is mid-Flush when another run turns telemetry off must not write
// its stale copy of the state file back over the opt-out: the next run stays
// opted out.
func TestFlushKeepsAnOptOutMadeByAnotherRunDuringThePost(t *testing.T) {
	var cfg Config
	var offErr error
	posted := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		// Another run of the program (its own Client over the same file)
		// runs `telemetry off` while this run's batch is in flight.
		offErr = New(cfg).SetEnabled(false)
		posted <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	cfg = testConfig(t, srv.URL, nil, &bytes.Buffer{})

	c := New(cfg)
	c.Track(context.Background(), "forge_cli_command")
	c.Flush(context.Background())
	select {
	case <-posted:
	default:
		t.Fatal("the batch was never posted")
	}
	if offErr != nil {
		t.Fatal(offErr)
	}
	if got := New(cfg).Status().Reason; got != "opted_out" {
		t.Errorf("next run reason = %q, want opted_out: Flush wrote its stale state over the other run's opt-out", got)
	}
	doc := newState(c.state.path).load()
	if doc.SentToday != 1 || doc.Enabled == nil || *doc.Enabled {
		t.Errorf("state after Flush = %+v, want the opt-out kept and the batch counted", doc)
	}
}

// A run that started before another run's `telemetry off` sends nothing at
// Flush: the opt-out is read again before the post.
func TestFlushSendsNothingAfterAnotherRunOptedOut(t *testing.T) {
	s, srv := newSink(t)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	c := New(cfg)
	c.Track(context.Background(), "forge_cli_command")
	if err := New(cfg).SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	c.Flush(context.Background())
	if n := len(s.records()); n != 0 {
		t.Errorf("sent %d record(s) after another run opted out", n)
	}
}

// Transmits is what gates anything that identifies the install: false on a
// quiet first run (the notice has not been shown), true once it has.
func TestTransmitsWaitsForTheNotice(t *testing.T) {
	_, srv := newSink(t)
	var stderr bytes.Buffer
	cfg := testConfig(t, srv.URL, nil, &stderr)
	cfg.Quiet = true
	quiet := New(cfg)
	if !quiet.Enabled() || quiet.Transmits() {
		t.Fatalf("quiet first run: Enabled=%v Transmits=%v, want true/false", quiet.Enabled(), quiet.Transmits())
	}
	cfg.Quiet = false
	if c := New(cfg); !c.Transmits() || stderr.Len() == 0 {
		t.Errorf("after the notice: Transmits=%v notice=%q", c.Transmits(), stderr.String())
	}
	var nilClient *Client
	if nilClient.Transmits() {
		t.Error("a nil client transmits")
	}
}

// A run that is still going when another run turns telemetry off stops
// transmitting at once: Transmits reads the opt-out from the file, not from
// what this run saw at start.
func TestTransmitsStopsWhenAnotherRunOptsOut(t *testing.T) {
	_, srv := newSink(t)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	running := New(cfg)
	if !running.Transmits() {
		t.Fatal("a fresh run with the notice shown must transmit")
	}
	if err := New(cfg).SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if running.Transmits() {
		t.Error("a running client still transmits after another run's `telemetry off`")
	}
}

// Runs flushing at the same moment share one day's allowance: together they
// send exactly MaxEventsPerDay, never one allowance each.
func TestConcurrentRunsShareTheDailyCap(t *testing.T) {
	s, srv := newSink(t)
	cfg := testConfig(t, srv.URL, nil, &bytes.Buffer{})
	// One fixed day: reservations made either side of UTC midnight would
	// rightly draw on two days' allowances.
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	cfg.Now = func() time.Time { return now }
	const runs = 4
	clients := make([]*Client, runs)
	for i := range clients {
		clients[i] = New(cfg)
		for j := 0; j < MaxEventsPerDay; j++ {
			clients[i].Track(context.Background(), "ev_"+string(rune('a'+j%26))+string(rune('a'+j/26))+string(rune('a'+i)))
		}
	}
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Flush(context.Background())
		}()
	}
	wg.Wait()
	// The reservations are exact; what was delivered can only be fewer (a post
	// that ran out of FlushTimeout on a loaded machine still spent its share).
	if got := newState(clients[0].state.path).load().SentToday; got != MaxEventsPerDay {
		t.Errorf("%d concurrent runs reserved %d events, want the one daily cap %d", runs, got, MaxEventsPerDay)
	}
	if n := len(s.records()); n > MaxEventsPerDay {
		t.Errorf("%d concurrent runs sent %d events, more than the daily cap %d", runs, n, MaxEventsPerDay)
	}
}
