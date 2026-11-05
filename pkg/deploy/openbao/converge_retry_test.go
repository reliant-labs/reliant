package openbao

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// SC7 prod adoption (session SC7P). The converge Job treated one dropped
// connection as fatal, and its failure message quoted whatever the PREVIOUS
// call had left in the response buffer (in practice the login response, which
// carries a client token). These tests run the real embedded converge.sh
// (widen phase, JWT login) against a TLS store that misbehaves on one path.

const retryTestToken = "s.client-token-that-must-never-be-quoted"

// flakyStore serves a store that is healthy except for one behavior: requests
// whose "METHOD path" is in drop are closed without a reply for the first
// dropFor[key] times, and requests in forbid answer 403.
type flakyStore struct {
	mu      sync.Mutex
	seen    []string
	dropFor map[string]int
	forbid  map[string]bool
	mounts  string
}

func (f *flakyStore) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.seen {
		if s == key {
			n++
		}
	}
	return n
}

func (f *flakyStore) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	key := req.Method + " " + req.URL.Path
	f.mu.Lock()
	f.seen = append(f.seen, key)
	drop := f.dropFor[key] > 0
	if drop {
		f.dropFor[key]--
	}
	forbid := f.forbid[key]
	mounts := f.mounts
	f.mu.Unlock()

	if drop {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if forbid {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
		return
	}
	switch key {
	case "GET /v1/sys/health":
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"standby":false}`))
	case "POST /v1/auth/jwt-self/login":
		_, _ = w.Write([]byte(`{"auth":{"client_token":"` + retryTestToken + `"}}`))
	case "GET /v1/sys/mounts":
		_, _ = w.Write([]byte(mounts))
	case "GET /v1/sys/auth":
		_, _ = w.Write([]byte(`{}`))
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func runAgainst(t *testing.T, store *flakyStore, extraEnv ...string) widenRun {
	t.Helper()
	srv := httptest.NewTLSServer(store)
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	return runWiden(t, srv.URL, ca, append([]string{"OPENBAO_RETRY_BASE_SECONDS=0"}, extraEnv...)...)
}

// A dropped connection on a call is retried, and the run still succeeds.
//
// Mutation that fails it: remove the retry loop from api() in converge.sh.
func TestConverge_RetriesADroppedConnection(t *testing.T) {
	store := &flakyStore{
		mounts:  `{"secret/":{}}`,
		dropFor: map[string]int{"POST /v1/auth/jwt-self/login": 2, "POST /v1/secret/config": 2},
	}
	got := runAgainst(t, store)
	if got.code != 0 {
		t.Fatalf("a transient connection drop must not fail the Job, exit %d:\n%s", got.code, got.out)
	}
	if n := store.count("POST /v1/auth/jwt-self/login"); n != 3 {
		t.Errorf("login should have been attempted 3 times (2 dropped, 1 served), got %d", n)
	}
	if n := store.count("POST /v1/secret/config"); n != 3 {
		t.Errorf("the kv config write should have been attempted 3 times, got %d", n)
	}
	if !strings.Contains(got.out, "retry 1/6") {
		t.Errorf("a retry must be logged:\n%s", got.out)
	}
}

// A real API error is the store's answer: one attempt, then fail, quoting that
// answer.
//
// Mutation that fails it: retry on any non-2xx.
func TestConverge_FailsFastOnAnAPIError(t *testing.T) {
	store := &flakyStore{forbid: map[string]bool{"GET /v1/sys/mounts": true}}
	got := runAgainst(t, store)
	if got.code != 1 {
		t.Fatalf("a 403 must fail the Job (exit 1), got %d:\n%s", got.code, got.out)
	}
	if n := store.count("GET /v1/sys/mounts"); n != 1 {
		t.Errorf("an API error must not be retried, the store saw %d attempts", n)
	}
	if !strings.Contains(got.out, "answered 403") || !strings.Contains(got.out, "permission denied") {
		t.Errorf("the failure must quote the store's actual answer:\n%s", got.out)
	}
	if strings.Contains(got.out, "retry ") {
		t.Errorf("nothing should have been retried:\n%s", got.out)
	}
}

// When the retries run out, the message says there was no answer. It must not
// quote the previous call's response (here the login's, which holds a token).
//
// Mutation that fails it: delete the `: >"$TMP/resp"` truncation in api().
func TestConverge_ExhaustedRetriesDoNotQuoteTheLastResponse(t *testing.T) {
	store := &flakyStore{
		mounts:  `{"secret/":{}}`,
		dropFor: map[string]int{"GET /v1/sys/mounts": 100},
	}
	got := runAgainst(t, store, "OPENBAO_API_RETRIES=2")
	if got.code != 1 {
		t.Fatalf("an unreachable store must fail the Job (exit 1), got %d:\n%s", got.code, got.out)
	}
	if n := store.count("GET /v1/sys/mounts"); n != 3 {
		t.Errorf("expected 1 attempt + 2 retries, got %d", n)
	}
	if !strings.Contains(got.out, "no answer from the store") || !strings.Contains(got.out, "curl exit") {
		t.Errorf("the failure must say the store gave no answer, and why:\n%s", got.out)
	}
	if strings.Contains(got.out, retryTestToken) || strings.Contains(got.out, "client_token") {
		t.Errorf("the failure quoted the previous response (a login token):\n%s", got.out)
	}
}

// An unreachable store says nothing about the credential: it is a retryable
// failure (exit 1), never the "no usable credential" verdict (exit 64) that
// fails the Job for good.
//
// Mutation that fails it: delete the unreachable() call in jwt_login().
func TestConverge_AnUnreachableLoginIsNotACredentialVerdict(t *testing.T) {
	store := &flakyStore{dropFor: map[string]int{"POST /v1/auth/jwt-self/login": 100}}
	got := runAgainst(t, store, "OPENBAO_API_RETRIES=1")
	if got.code != 1 {
		t.Fatalf("want exit 1 (retryable), got %d:\n%s", got.code, got.out)
	}
	if strings.Contains(got.out, "no usable credential") {
		t.Errorf("a dropped connection was reported as a credential refusal:\n%s", got.out)
	}
}

// Enabling a mount is not replayed after an ambiguous failure: a repeat would
// answer 400 and hide whether the first one took. The Job fails and its own
// retry starts from a listing.
func TestConverge_DoesNotReplayAnAmbiguousMountEnable(t *testing.T) {
	store := &flakyStore{
		mounts:  `{}`,
		dropFor: map[string]int{"POST /v1/sys/mounts/secret": 100},
	}
	got := runAgainst(t, store)
	if got.code != 1 {
		t.Fatalf("want exit 1, got %d:\n%s", got.code, got.out)
	}
	if n := store.count("POST /v1/sys/mounts/secret"); n != 1 {
		t.Errorf("a mount enable must be sent once after an ambiguous failure, got %d", n)
	}
}
