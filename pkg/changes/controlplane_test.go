package changes

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "fct_secret-token-value"

// fakeControlPlane serves GetChangesSnapshot from whatever reply holds, and
// records every request it saw.
type fakeControlPlane struct {
	*httptest.Server

	mu       sync.Mutex
	status   int
	body     string
	requests []*http.Request
	bodies   []string
}

func newFakeControlPlane(t *testing.T) *fakeControlPlane {
	t.Helper()
	f := &fakeControlPlane{status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r)
		f.bodies = append(f.bodies, string(b))
		status, body := f.status, f.body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.Close)
	return f
}

// serve makes the next responses a 200 carrying snapshot under etag.
func (f *fakeControlPlane) serve(snapshot, etag string) {
	b, _ := json.Marshal(map[string]any{"snapshotJson": snapshot, "revision": "1", "etag": etag, "problems": []any{}})
	f.reply(http.StatusOK, string(b))
}

func (f *fakeControlPlane) reply(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body = status, body
}

func (f *fakeControlPlane) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// cpStore is a started Store over f whose poll loop never ticks on its own
// (tests call onTick), recording nothing, with its log in the returned
// buffer and a jitter that always waits the full backoff.
func cpStore(t *testing.T, f *fakeControlPlane, interval time.Duration) (*Store, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	s, ok := newControlPlaneStore(context.Background(), ControlPlaneSource{BaseURL: f.URL + "/", EnvironmentID: "env-1", Token: testToken}, Options{
		PollInterval: interval,
		OnEvaluate:   func(context.Context, string, Result) {},
		Logger:       slog.New(slog.NewTextHandler(logs, nil)),
	})
	if !ok {
		t.Fatal("newControlPlaneStore refused a complete source")
	}
	s.jitter = func(w int) int { return w }
	s.start(context.Background(), make(chan time.Time), func() {})
	t.Cleanup(func() { _ = s.Close() })
	return s, logs
}

func tick(s *Store) { s.onTick(context.Background()) }

func noToken(t *testing.T, logs *syncBuffer) {
	t.Helper()
	if strings.Contains(logs.String(), testToken) {
		t.Fatalf("the token reached the log:\n%s", logs.String())
	}
}

func TestControlPlaneFetch(t *testing.T) {
	f := newFakeControlPlane(t)
	f.serve(everyoneSnapshot(1, "blue"), "e1")
	s, logs := cpStore(t, f, 0)
	if banner(s) != "blue" || s.Revision() != 1 {
		t.Fatalf("opened: %q at revision %d, want blue at 1 (the first fetch happens before Open returns)", banner(s), s.Revision())
	}

	f.mu.Lock()
	r, body := f.requests[0], f.bodies[0]
	f.mu.Unlock()
	if r.Method != http.MethodPost || r.URL.Path != "/controlplane.v1.ChangesService/GetChangesSnapshot" {
		t.Errorf("request %s %s, want POST /controlplane.v1.ChangesService/GetChangesSnapshot", r.Method, r.URL.Path)
	}
	for h, want := range map[string]string{
		"Authorization":            "Bearer " + testToken,
		"Content-Type":             "application/json",
		"Connect-Protocol-Version": "1",
	} {
		if got := r.Header.Get(h); got != want {
			t.Errorf("header %s = %q, want %q", h, got, want)
		}
	}
	if body != `{"environmentId":"env-1"}` {
		t.Errorf("body %s, want {\"environmentId\":\"env-1\"}", body)
	}

	f.serve(everyoneSnapshot(2, "green"), "e2")
	tick(s)
	if banner(s) != "green" || s.Revision() != 2 {
		t.Fatalf("after a new revision: %q at revision %d, want green at 2", banner(s), s.Revision())
	}
	if s.interval != DefaultFetchInterval {
		t.Errorf("default interval %s, want %s", s.interval, DefaultFetchInterval)
	}
	noToken(t, logs)
}

func TestControlPlaneEtagUnchangedIsNotParsed(t *testing.T) {
	f := newFakeControlPlane(t)
	f.serve(everyoneSnapshot(1, "blue"), "same")
	s, _ := cpStore(t, f, 0)
	// A different document under the same etag: the store trusts the etag
	// and does not parse it again.
	f.serve(everyoneSnapshot(2, "green"), "same")
	tick(s)
	if banner(s) != "blue" || s.Revision() != 1 {
		t.Fatalf("same etag: %q at revision %d, want blue at 1 (not re-parsed)", banner(s), s.Revision())
	}
	if f.count() != 2 {
		t.Fatalf("%d requests, want 2", f.count())
	}
}

func TestControlPlaneRefusedTokenKeepsLastGood(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		f := newFakeControlPlane(t)
		f.serve(everyoneSnapshot(1, "blue"), "e1")
		s, logs := cpStore(t, f, 0)

		f.reply(status, `{"code":"permission_denied","message":"missing scope changes:read"}`)
		tick(s) // refused
		tick(s) // refused again: the same warning
		tick(s) // let pass by the backoff
		if banner(s) != "blue" || s.Revision() != 1 {
			t.Fatalf("HTTP %d: %q at revision %d, want the last good blue at 1", status, banner(s), s.Revision())
		}
		if n := strings.Count(logs.String(), "refused the token"); n != 1 {
			t.Fatalf("HTTP %d: one refusal fetched twice warned %d times, want once:\n%s", status, n, logs.String())
		}
		if !strings.Contains(logs.String(), "missing scope changes:read") {
			t.Errorf("HTTP %d: the warning dropped the server's message:\n%s", status, logs.String())
		}
		noToken(t, logs)
	}
}

func TestControlPlaneBacksOff(t *testing.T) {
	f := newFakeControlPlane(t)
	f.reply(http.StatusServiceUnavailable, `{"code":"unavailable","message":"down"}`)
	// One-minute polls cap the backoff at five ticks (about five minutes).
	s, logs := cpStore(t, f, time.Minute)
	if s.Revision() != 0 || f.count() != 1 {
		t.Fatalf("opened against a failing server: revision %d after %d requests, want 0 after 1", s.Revision(), f.count())
	}
	// Failure n waits min(2^(n-1), 5) ticks: request, then 1, 2, 4, 5, 5
	// ticks between requests.
	var at []int
	for i := 1; i <= 25; i++ {
		before := f.count()
		tick(s)
		if f.count() > before {
			at = append(at, i)
		}
	}
	want := []int{1, 3, 7, 12, 17, 22}
	if len(at) != len(want) {
		t.Fatalf("requests on ticks %v, want %v", at, want)
	}
	for i := range want {
		if at[i] != want[i] {
			t.Fatalf("requests on ticks %v, want %v", at, want)
		}
	}
	if n := strings.Count(logs.String(), "did not serve the snapshot"); n != 1 {
		t.Fatalf("one error repeated warned %d times, want once:\n%s", n, logs.String())
	}

	// Recovery resets the backoff: the next request after it succeeds, and
	// every tick after that fetches.
	f.serve(everyoneSnapshot(1, "blue"), "e1")
	for i := 0; i < 5 && s.Revision() == 0; i++ {
		tick(s)
	}
	if banner(s) != "blue" {
		t.Fatalf("after the server recovered: %q, want blue", banner(s))
	}
	before := f.count()
	tick(s)
	tick(s)
	if f.count() != before+2 {
		t.Fatalf("after recovery %d requests in two ticks, want 2", f.count()-before)
	}
}

func TestControlPlaneUnreachable(t *testing.T) {
	f := newFakeControlPlane(t)
	f.serve(everyoneSnapshot(1, "blue"), "e1")
	s, logs := cpStore(t, f, 0)
	f.Close()
	tick(s)
	if banner(s) != "blue" {
		t.Fatalf("unreachable: %q, want the last good blue", banner(s))
	}
	if !strings.Contains(logs.String(), "cannot reach the control plane") {
		t.Fatalf("no warning for an unreachable control plane:\n%s", logs.String())
	}
	if s.skip != 0 || s.failures != 1 {
		t.Fatalf("after one failure: skip %d, failures %d, want 0 and 1", s.skip, s.failures)
	}
}

func TestControlPlaneTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	logs := &syncBuffer{}
	s := OpenControlPlane(context.Background(), ControlPlaneSource{
		BaseURL: srv.URL, EnvironmentID: "env-1", Token: testToken, Timeout: 50 * time.Millisecond,
	}, Options{OnEvaluate: func(context.Context, string, Result) {}, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	defer s.Close()
	if s.Revision() != 0 || !strings.Contains(logs.String(), "cannot reach the control plane") {
		t.Fatalf("a hung control plane: revision %d, log:\n%s", s.Revision(), logs.String())
	}
}

func TestControlPlaneUnknownSchemaKeepsLastGood(t *testing.T) {
	f := newFakeControlPlane(t)
	f.serve(everyoneSnapshot(1, "blue"), "e1")
	s, logs := cpStore(t, f, 0)
	f.serve(`{"schema":"forge.changes/v9","revision":2,"changes":{}}`, "e2")
	tick(s)
	tick(s)
	if banner(s) != "blue" || s.Revision() != 1 {
		t.Fatalf("unknown schema: %q at revision %d, want the last good blue at 1", banner(s), s.Revision())
	}
	if n := strings.Count(logs.String(), "cannot parse the control plane's snapshot"); n != 1 {
		t.Fatalf("one bad snapshot fetched twice warned %d times, want once:\n%s", n, logs.String())
	}
	// A parse failure is the snapshot's, not the source's: no backoff.
	if s.skip != 0 {
		t.Fatalf("a bad snapshot backed off %d ticks, want 0", s.skip)
	}
}

func TestControlPlaneRevisionOnlyMovesForward(t *testing.T) {
	f := newFakeControlPlane(t)
	f.serve(everyoneSnapshot(5, "blue"), "e5")
	s, logs := cpStore(t, f, 0)

	f.serve(everyoneSnapshot(3, "green"), "e3")
	tick(s)
	if banner(s) != "blue" || s.Revision() != 5 {
		t.Fatalf("an older revision: %q at revision %d, want the last good blue at 5", banner(s), s.Revision())
	}
	if !strings.Contains(logs.String(), "older than the last good snapshot") {
		t.Fatalf("no warning for an older revision:\n%s", logs.String())
	}

	f.serve(everyoneSnapshot(5, "blue"), "e5")
	tick(s)
	f.serve(everyoneSnapshot(6, "green"), "e6")
	tick(s)
	if banner(s) != "green" || s.Revision() != 6 {
		t.Fatalf("a newer revision: %q at revision %d, want green at 6", banner(s), s.Revision())
	}
}

func TestControlPlaneNoSnapshotIsEmpty(t *testing.T) {
	f := newFakeControlPlane(t)
	f.serve(everyoneSnapshot(1, "blue"), "e1")
	s, _ := cpStore(t, f, 0)
	f.serve("", "")
	tick(s)
	if banner(s) != "code-default" || s.Revision() != 0 {
		t.Fatalf("no snapshot delivered: %q at revision %d, want the code default at 0", banner(s), s.Revision())
	}
}

func TestOpenControlPlaneIncompleteSourceIsEmpty(t *testing.T) {
	for name, cp := range map[string]ControlPlaneSource{
		"no token":    {BaseURL: "https://cp.example", EnvironmentID: "env-1"},
		"no env":      {BaseURL: "https://cp.example", Token: testToken},
		"no url":      {EnvironmentID: "env-1", Token: testToken},
		"not a url":   {BaseURL: "cp.example", EnvironmentID: "env-1", Token: testToken},
		"not http(s)": {BaseURL: "ftp://cp.example", EnvironmentID: "env-1", Token: testToken},
	} {
		logs := &syncBuffer{}
		s := OpenControlPlane(context.Background(), cp, Options{
			OnEvaluate: func(context.Context, string, Result) {},
			Logger:     slog.New(slog.NewTextHandler(logs, nil)),
		})
		if banner(s) != "code-default" || s.Revision() != 0 {
			t.Errorf("%s: %q at revision %d, want the code default at 0", name, banner(s), s.Revision())
		}
		if !strings.Contains(logs.String(), "cannot fetch the snapshot") {
			t.Errorf("%s: no warning:\n%s", name, logs.String())
		}
		noToken(t, logs)
		if err := s.Close(); err != nil {
			t.Errorf("%s: Close: %v", name, err)
		}
	}
}

func TestOpenFromEnv(t *testing.T) {
	f := newFakeControlPlane(t)
	f.serve(everyoneSnapshot(7, "green"), "e7")
	path := filepath.Join(t.TempDir(), "snapshot.json")
	writeFile(t, path, everyoneSnapshot(3, "blue"))
	fetch := map[string]string{EnvURL: f.URL, EnvEnvironmentID: "env-1", EnvToken: testToken}
	with := func(base map[string]string, extra map[string]string) func(string) string {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return func(k string) string { return env[k] }
	}

	cases := []struct {
		name     string
		getenv   func(string) string
		revision int64
		requests int
		warn     string
	}{
		{name: "the file wins", getenv: with(fetch, map[string]string{EnvFile: path}), revision: 3},
		{name: "fetch", getenv: with(fetch, nil), revision: 7, requests: 1},
		{name: "nothing set", getenv: with(nil, nil)},
		{name: "nil getenv"},
		{name: "half configured", getenv: with(map[string]string{EnvURL: f.URL}, nil), warn: EnvEnvironmentID + ", " + EnvToken},
	}
	for _, c := range cases {
		before := f.count()
		logs := &syncBuffer{}
		s := OpenFromEnv(context.Background(), c.getenv, Options{
			OnEvaluate: func(context.Context, string, Result) {},
			Logger:     slog.New(slog.NewTextHandler(logs, nil)),
		})
		if s.Revision() != c.revision {
			t.Errorf("%s: revision %d, want %d", c.name, s.Revision(), c.revision)
		}
		if got := f.count() - before; got != c.requests {
			t.Errorf("%s: %d requests to the control plane, want %d", c.name, got, c.requests)
		}
		if c.warn != "" && !strings.Contains(logs.String(), c.warn) {
			t.Errorf("%s: the warning does not name %s:\n%s", c.name, c.warn, logs.String())
		}
		noToken(t, logs)
		_ = s.Close()
	}
}
