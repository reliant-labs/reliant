package deploystate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeBackend is a minimal in-memory server speaking the Remote
// protocol. It exists to prove the seam is real: if Remote and Local
// disagree about anything a caller can observe, the interface is not one
// interface.
func fakeBackend(t *testing.T) (*Remote, *httptest.Server, map[string]Record, map[string]Policy) {
	t.Helper()
	records := map[string]Record{}
	policies := map[string]Policy{}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/state/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/state/")
		parts := strings.Split(rest, "/")

		switch {
		case r.Method == http.MethodGet && len(parts) == 1:
			out := listResponse{}
			for _, rec := range records {
				if rec.Key.Env == parts[0] {
					out.Records = append(out.Records, rec)
				}
			}
			_ = json.NewEncoder(w).Encode(out)

		case r.Method == http.MethodGet && len(parts) == 3:
			rec, ok := records[rest]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(rec)

		case r.Method == http.MethodPut && len(parts) == 3:
			var rec Record
			if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			records[rest] = rec
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	mux.HandleFunc("/v1/policy/", func(w http.ResponseWriter, r *http.Request) {
		env := strings.TrimPrefix(r.URL.Path, "/v1/policy/")
		switch r.Method {
		case http.MethodGet:
			p, ok := policies[env]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(policyFile{Policy: p})
		case http.MethodPut:
			var pf policyFile
			if err := json.NewDecoder(r.Body).Decode(&pf); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			policies[env] = pf.Policy
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewRemote(srv.URL), srv, records, policies
}

func TestRemotePutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := fakeBackend(t)

	rec := Record{
		Key:      Key{Env: "prod", Provider: "k8s", Service: "api"},
		Desired:  Desired{Digest: "sha256:aaa", Replicas: intp(3)},
		Observed: Observation{Digest: "sha256:aaa", Measured: true, Serving: true},
		Owned:    []Field{FieldDigest},
	}
	if err := store.Put(ctx, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, rec.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Desired.Digest != "sha256:aaa" || !got.Observed.Measured || !got.Owns(FieldDigest) {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

func TestRemoteMissingIsErrNotFound(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := fakeBackend(t)

	_, err := store.Get(ctx, Key{Env: "prod", Provider: "k8s", Service: "nope"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound — callers must branch on ONE sentinel regardless of backend", err)
	}
}

// TestRemoteMissingPolicyIsObserveNotError pins the SAME direction Local
// pins: a backend that has never heard of this environment must not be
// able to produce a converge verdict.
func TestRemoteMissingPolicyIsObserveNotError(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := fakeBackend(t)

	p, err := store.Policy(ctx, "prod")
	if err != nil {
		t.Fatalf("Policy on an unknown environment: %v", err)
	}
	if p != PolicyObserve {
		t.Fatalf("Policy = %v, want PolicyObserve", p)
	}
}

func TestRemotePolicyRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := fakeBackend(t)

	for _, want := range []Policy{PolicyConverge, PolicyPinned, PolicyObserve} {
		if err := store.SetPolicy(ctx, "prod", want); err != nil {
			t.Fatalf("SetPolicy(%v): %v", want, err)
		}
		got, err := store.Policy(ctx, "prod")
		if err != nil {
			t.Fatalf("Policy: %v", err)
		}
		if got != want {
			t.Fatalf("Policy = %v, want %v", got, want)
		}
	}
}

func TestRemoteList(t *testing.T) {
	ctx := context.Background()
	store, _, _, _ := fakeBackend(t)

	for _, svc := range []string{"api", "web"} {
		if err := store.Put(ctx, Record{Key: Key{Env: "prod", Provider: "k8s", Service: svc}}); err != nil {
			t.Fatalf("Put %s: %v", svc, err)
		}
	}
	if err := store.Put(ctx, Record{Key: Key{Env: "staging", Provider: "k8s", Service: "api"}}); err != nil {
		t.Fatalf("Put staging: %v", err)
	}

	got, err := store.List(ctx, "prod")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List(prod) = %d records, want 2", len(got))
	}
	for _, rec := range got {
		if rec.Key.Env != "prod" {
			t.Fatalf("List(prod) leaked %s", rec.Key)
		}
	}
}

// TestRemoteServerErrorIsAnError: a 500 must not be mistaken for
// "nothing here". Only a 404 means absent.
func TestRemoteServerErrorIsAnError(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	store := NewRemote(srv.URL)

	if _, err := store.Get(ctx, Key{Env: "prod", Provider: "k8s", Service: "api"}); err == nil {
		t.Fatal("Get swallowed a 500")
	} else if errors.Is(err, ErrNotFound) {
		t.Fatalf("a 500 was reported as ErrNotFound: %v", err)
	}

	p, err := store.Policy(ctx, "prod")
	if err == nil {
		t.Fatal("Policy swallowed a 500")
	}
	if p.AllowsConverge() {
		t.Fatal("a failing policy read yielded a converge-permitting policy")
	}
}

// TestRemoteEscapesPathSegments: a service name with a slash must not be
// able to address a sibling route.
func TestRemoteEscapesPathSegments(t *testing.T) {
	ctx := context.Background()
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	store := NewRemote(srv.URL)

	_, _ = store.Get(ctx, Key{Env: "prod", Provider: "k8s", Service: "a/../../policy/prod"})

	if strings.Contains(seen, "/policy/") {
		t.Fatalf("escaped path %q reached a sibling route", seen)
	}
	if !strings.HasPrefix(seen, "/v1/state/prod/k8s/") {
		t.Fatalf("escaped path %q left the state route", seen)
	}
}

func TestRemoteSendsBearerToken(t *testing.T) {
	ctx := context.Background()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	store := NewRemote(srv.URL, WithBearerToken("sekrit"))
	_, _ = store.Policy(ctx, "prod")

	if auth != "Bearer sekrit" {
		t.Fatalf("Authorization = %q, want %q", auth, "Bearer sekrit")
	}
}

// TestBothBackendsAgree runs the same sequence against Local and Remote
// and requires identical observable behaviour. This is what makes the
// interface a seam rather than a type declaration: if the two disagree,
// a caller's correctness depends on which backend it was handed.
func TestBothBackendsAgree(t *testing.T) {
	ctx := context.Background()
	remote, _, _, _ := fakeBackend(t)

	backends := map[string]Store{
		"local":  NewLocal(t.TempDir()),
		"remote": remote,
	}
	key := Key{Env: "prod", Provider: "k8s", Service: "api"}

	for name, store := range backends {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Get(ctx, key); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Get on empty backend: err = %v, want ErrNotFound", err)
			}
			p, err := store.Policy(ctx, "prod")
			if err != nil {
				t.Fatalf("Policy on empty backend: %v", err)
			}
			if p != PolicyObserve {
				t.Fatalf("Policy on empty backend = %v, want PolicyObserve", p)
			}

			rec := drifting()
			if err := store.Put(ctx, rec); err != nil {
				t.Fatalf("Put: %v", err)
			}
			if err := store.SetPolicy(ctx, "prod", PolicyConverge); err != nil {
				t.Fatalf("SetPolicy: %v", err)
			}

			decisions, err := DecideEnv(ctx, store, "prod", testNow, DefaultStabilityWindow)
			if err != nil {
				t.Fatalf("DecideEnv: %v", err)
			}
			if len(decisions) != 1 {
				t.Fatalf("DecideEnv returned %d decisions, want 1", len(decisions))
			}
			if decisions[0].Action != ActionConverge || decisions[0].State != StateDiverged {
				t.Fatalf("decision = %v/%v, want diverged/converge", decisions[0].State, decisions[0].Action)
			}

			if err := store.SetPolicy(ctx, "prod", PolicyPinned); err != nil {
				t.Fatalf("SetPolicy pinned: %v", err)
			}
			pinned, err := DecideEnv(ctx, store, "prod", testNow, DefaultStabilityWindow)
			if err != nil {
				t.Fatalf("DecideEnv after pin: %v", err)
			}
			if pinned[0].Action != ActionReport {
				t.Fatalf("after pin: Action = %v, want ActionReport", pinned[0].Action)
			}
		})
	}
}
