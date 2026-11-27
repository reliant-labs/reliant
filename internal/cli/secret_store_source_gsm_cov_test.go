package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// covAGSMServer is a scripted Secret Manager: each route answers with the
// status and body registered for "METHOD path" (path without the query), and
// every request is recorded.
type covAGSMServer struct {
	mu     sync.Mutex
	routes map[string][]covAGSMReply
	seen   []covAGSMReq
	srv    *httptest.Server
}

type covAGSMReply struct {
	status int
	body   string
}

type covAGSMReq struct {
	method, path, query, body, contentType string
}

func covANewGSM(t *testing.T, routes map[string][]covAGSMReply) (*gsmREST, *covAGSMServer) {
	t.Helper()
	s := &covAGSMServer{routes: routes}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.seen = append(s.seen, covAGSMReq{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(b), r.Header.Get("Content-Type")})
		key := r.Method + " " + r.URL.EscapedPath()
		rs := s.routes[key]
		if len(rs) == 0 {
			http.Error(w, `{"error":{"message":"no route `+key+`","status":"UNSCRIPTED"}}`, http.StatusTeapot)
			return
		}
		rep := rs[0]
		if len(rs) > 1 {
			s.routes[key] = rs[1:]
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(s.srv.Close)
	return &gsmREST{client: s.srv.Client(), base: s.srv.URL}, s
}

func TestGSMRESTLatest(t *testing.T) {
	const path = "/projects/p/secrets/db-pass"
	tests := []struct {
		name                  string
		routes                map[string][]covAGSMReply
		wantVer               uint32
		wantUsable, wantExist bool
		wantErr               string
		wantRequests          int
	}{
		{
			name:       "enabled version",
			routes:     map[string][]covAGSMReply{"GET " + path + "/versions/latest": {{200, `{"name":"projects/1/secrets/db-pass/versions/7","state":"ENABLED"}`}}},
			wantVer:    7,
			wantUsable: true, wantExist: true, wantRequests: 1,
		},
		{
			name:    "disabled version exists but is not usable",
			routes:  map[string][]covAGSMReply{"GET " + path + "/versions/latest": {{200, `{"name":"projects/1/secrets/db-pass/versions/3","state":"DISABLED"}`}}},
			wantVer: 3, wantExist: true, wantRequests: 1,
		},
		{
			name: "no version yet but the container exists",
			routes: map[string][]covAGSMReply{
				"GET " + path + "/versions/latest": {{404, `{}`}},
				"GET " + path:                      {{200, `{}`}},
			},
			wantExist: true, wantRequests: 2,
		},
		{
			name: "no such secret",
			routes: map[string][]covAGSMReply{
				"GET " + path + "/versions/latest": {{404, `{}`}},
				"GET " + path:                      {{404, `{}`}},
			},
			wantRequests: 2,
		},
		{
			name: "container probe fails",
			routes: map[string][]covAGSMReply{
				"GET " + path + "/versions/latest": {{404, `{}`}},
				"GET " + path:                      {{403, `{"error":{"message":"denied","status":"PERMISSION_DENIED"}}`}},
			},
			wantErr: "secret manager get secret: HTTP 403 PERMISSION_DENIED: denied", wantRequests: 2,
		},
		{
			name:    "other status on latest",
			routes:  map[string][]covAGSMReply{"GET " + path + "/versions/latest": {{500, `not json`}}},
			wantErr: "secret manager get latest version: HTTP 500 : Internal Server Error", wantRequests: 1,
		},
		{
			name:    "undecodable 200",
			routes:  map[string][]covAGSMReply{"GET " + path + "/versions/latest": {{200, `<html>`}}},
			wantErr: "invalid character", wantRequests: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, srv := covANewGSM(t, tc.routes)
			ver, usable, exists, err := g.Latest(context.Background(), "p", "db-pass")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if ver != tc.wantVer || usable != tc.wantUsable || exists != tc.wantExist {
				t.Errorf("got (%d,%v,%v), want (%d,%v,%v)", ver, usable, exists, tc.wantVer, tc.wantUsable, tc.wantExist)
			}
			if len(srv.seen) != tc.wantRequests {
				t.Errorf("requests = %+v, want %d", srv.seen, tc.wantRequests)
			}
		})
	}
}

func TestGSMRESTLatestTransportError(t *testing.T) {
	g, srv := covANewGSM(t, nil)
	srv.srv.Close()
	if _, _, _, err := g.Latest(context.Background(), "p", "x"); err == nil {
		t.Fatal("a dead server must be an error, not 'absent'")
	}
}

func TestGSMRESTPathEscapesProjectAndID(t *testing.T) {
	if got := gsmSecretPath("my proj", "a/b"); got != "/projects/my%20proj/secrets/a%2Fb" {
		t.Errorf("path = %q", got)
	}
}

func TestGSMRESTAddVersion(t *testing.T) {
	const path = "/projects/p/secrets/tok"
	value := []byte("s3cr3t\x00bytes")
	enc := base64.StdEncoding.EncodeToString(value)

	t.Run("adds a version to an existing secret", func(t *testing.T) {
		g, srv := covANewGSM(t, map[string][]covAGSMReply{
			"POST " + path + ":addVersion": {{200, `{"name":"projects/1/secrets/tok/versions/4"}`}},
		})
		v, err := g.AddVersion(context.Background(), "p", "tok", value, false)
		if err != nil || v != 4 {
			t.Fatalf("v=%d err=%v", v, err)
		}
		if len(srv.seen) != 1 || srv.seen[0].contentType != "application/json" {
			t.Fatalf("requests = %+v", srv.seen)
		}
		var body struct{ Payload struct{ Data string } }
		if err := json.Unmarshal([]byte(srv.seen[0].body), &body); err != nil || body.Payload.Data != enc {
			t.Errorf("payload must be the base64 of the value: %q (%v)", srv.seen[0].body, err)
		}
	})

	t.Run("creates the container when asked and the secret is missing", func(t *testing.T) {
		g, srv := covANewGSM(t, map[string][]covAGSMReply{
			"POST " + path + ":addVersion": {{404, `{}`}, {200, `{"name":"projects/1/secrets/tok/versions/1"}`}},
			"POST /projects/p/secrets":     {{200, `{}`}},
		})
		v, err := g.AddVersion(context.Background(), "p", "tok", value, true)
		if err != nil || v != 1 {
			t.Fatalf("v=%d err=%v", v, err)
		}
		if len(srv.seen) != 3 || srv.seen[1].path != "/projects/p/secrets" || srv.seen[1].query != "secretId=tok" {
			t.Fatalf("requests = %+v", srv.seen)
		}
		if !strings.Contains(srv.seen[1].body, `"automatic"`) {
			t.Errorf("secret must be created with automatic replication: %s", srv.seen[1].body)
		}
	})

	t.Run("a create race (409) is not an error", func(t *testing.T) {
		g, _ := covANewGSM(t, map[string][]covAGSMReply{
			"POST " + path + ":addVersion": {{404, `{}`}, {200, `{"name":"projects/1/secrets/tok/versions/2"}`}},
			"POST /projects/p/secrets":     {{409, `{}`}},
		})
		if v, err := g.AddVersion(context.Background(), "p", "tok", value, true); err != nil || v != 2 {
			t.Fatalf("v=%d err=%v", v, err)
		}
	})

	t.Run("without create a missing secret is reported, not created", func(t *testing.T) {
		g, srv := covANewGSM(t, map[string][]covAGSMReply{
			"POST " + path + ":addVersion": {{404, `{"error":{"message":"Secret not found","status":"NOT_FOUND"}}`}},
		})
		_, err := g.AddVersion(context.Background(), "p", "tok", value, false)
		if err == nil || !strings.Contains(err.Error(), "secret manager add version: HTTP 404 NOT_FOUND: Secret not found") {
			t.Fatalf("err = %v", err)
		}
		if len(srv.seen) != 1 {
			t.Errorf("must not try to create: %+v", srv.seen)
		}
	})

	t.Run("create failure", func(t *testing.T) {
		g, _ := covANewGSM(t, map[string][]covAGSMReply{
			"POST " + path + ":addVersion": {{404, `{}`}},
			"POST /projects/p/secrets":     {{403, `{"error":{"message":"no","status":"PERMISSION_DENIED"}}`}},
		})
		_, err := g.AddVersion(context.Background(), "p", "tok", value, true)
		if err == nil || !strings.Contains(err.Error(), "secret manager create secret: HTTP 403") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("second add after create fails", func(t *testing.T) {
		g, _ := covANewGSM(t, map[string][]covAGSMReply{
			"POST " + path + ":addVersion": {{404, `{}`}, {500, `{}`}},
			"POST /projects/p/secrets":     {{200, `{}`}},
		})
		if _, err := g.AddVersion(context.Background(), "p", "tok", value, true); err == nil || !strings.Contains(err.Error(), "add version: HTTP 500") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an unparseable success body", func(t *testing.T) {
		g, _ := covANewGSM(t, map[string][]covAGSMReply{"POST " + path + ":addVersion": {{200, `nope`}}})
		if _, err := g.AddVersion(context.Background(), "p", "tok", value, false); err == nil {
			t.Fatal("want a decode error")
		}
	})

	t.Run("transport error", func(t *testing.T) {
		g, srv := covANewGSM(t, nil)
		srv.srv.Close()
		if _, err := g.AddVersion(context.Background(), "p", "tok", value, true); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestGSMRESTAccess(t *testing.T) {
	const path = "GET /projects/p/secrets/tok/versions/latest:access"
	t.Run("decodes the base64 payload", func(t *testing.T) {
		enc := base64.StdEncoding.EncodeToString([]byte("hunter2"))
		g, _ := covANewGSM(t, map[string][]covAGSMReply{path: {{200, `{"payload":{"data":"` + enc + `"}}`}}})
		got, err := g.Access(context.Background(), "p", "tok")
		if err != nil || string(got) != "hunter2" {
			t.Fatalf("got %q err %v", got, err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		g, _ := covANewGSM(t, map[string][]covAGSMReply{path: {{404, `{"error":{"message":"gone","status":"NOT_FOUND"}}`}}})
		if _, err := g.Access(context.Background(), "p", "tok"); err == nil || !strings.Contains(err.Error(), "secret manager access: HTTP 404 NOT_FOUND: gone") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a payload that is not base64", func(t *testing.T) {
		g, _ := covANewGSM(t, map[string][]covAGSMReply{path: {{200, `{"payload":{"data":"!!!"}}`}}})
		if _, err := g.Access(context.Background(), "p", "tok"); err == nil {
			t.Fatal("want a decode error")
		}
	})
	t.Run("bad json", func(t *testing.T) {
		g, _ := covANewGSM(t, map[string][]covAGSMReply{path: {{200, `{`}}})
		if _, err := g.Access(context.Background(), "p", "tok"); err == nil {
			t.Fatal("want a decode error")
		}
	})
	t.Run("transport error", func(t *testing.T) {
		g, srv := covANewGSM(t, nil)
		srv.srv.Close()
		if _, err := g.Access(context.Background(), "p", "tok"); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestGSMRESTDisable(t *testing.T) {
	const path = "POST /projects/p/secrets/tok/versions/9:disable"
	g, srv := covANewGSM(t, map[string][]covAGSMReply{path: {{200, `{}`}}})
	if err := g.Disable(context.Background(), "p", "tok", 9); err != nil {
		t.Fatal(err)
	}
	if len(srv.seen) != 1 || srv.seen[0].body != "{}" {
		t.Errorf("requests = %+v", srv.seen)
	}
	g2, _ := covANewGSM(t, map[string][]covAGSMReply{path: {{403, `{"error":{"message":"no","status":"PERMISSION_DENIED"}}`}}})
	if err := g2.Disable(context.Background(), "p", "tok", 9); err == nil || !strings.Contains(err.Error(), "disable version: HTTP 403 PERMISSION_DENIED") {
		t.Fatalf("err = %v", err)
	}
	g3, s3 := covANewGSM(t, nil)
	s3.srv.Close()
	if err := g3.Disable(context.Background(), "p", "tok", 9); err == nil {
		t.Fatal("want a transport error")
	}
}

func TestGSMRESTListFollowsPages(t *testing.T) {
	g, srv := covANewGSM(t, map[string][]covAGSMReply{
		"GET /projects/p/secrets": {
			{200, `{"secrets":[{"name":"projects/1/secrets/a"},{"name":"projects/1/secrets/b"}],"nextPageToken":"t2"}`},
			{200, `{"secrets":[{"name":"projects/1/secrets/c"}]}`},
		},
	})
	ids, err := g.List(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("ids = %v, want %v", ids, want)
	}
	if len(srv.seen) != 2 || srv.seen[0].query != "pageSize=250" || srv.seen[1].query != "pageSize=250&pageToken=t2" {
		t.Errorf("paging requests = %+v", srv.seen)
	}

	for name, rep := range map[string]covAGSMReply{
		"http error": {500, `{}`},
		"bad body":   {200, `[`},
	} {
		t.Run(name, func(t *testing.T) {
			g, _ := covANewGSM(t, map[string][]covAGSMReply{"GET /projects/p/secrets": {rep}})
			if _, err := g.List(context.Background(), "p"); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	t.Run("transport error", func(t *testing.T) {
		g, s := covANewGSM(t, nil)
		s.srv.Close()
		if _, err := g.List(context.Background(), "p"); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestGSMRESTCallRejectsUnmarshalableBody(t *testing.T) {
	g, _ := covANewGSM(t, nil)
	if _, _, err := g.call(context.Background(), "POST", "/x", nil, make(chan int)); err == nil {
		t.Fatal("a body json cannot encode must fail before any request")
	}
	if _, _, err := g.call(context.Background(), "BAD METHOD", "/x", nil, nil); err == nil {
		t.Fatal("an invalid method must fail request construction")
	}
}

func TestGSMErrorFallsBackToStatusText(t *testing.T) {
	err := gsmError("op", 503, []byte("<html>"))
	if got := err.Error(); got != "secret manager op: HTTP 503 : Service Unavailable" {
		t.Errorf("got %q", got)
	}
}

func TestGSMStoreSourcePutCreateOnlyRefusesAnExistingValue(t *testing.T) {
	ctx := context.Background()
	g := newFakeGSM()
	if _, err := g.AddVersion(ctx, "p", "TOKEN", []byte("old"), true); err != nil {
		t.Fatal(err)
	}
	src := gsmStoreSource{api: g, project: "p", create: true}
	if got := src.describe(); got != "Google Secret Manager project p" {
		t.Errorf("describe = %q", got)
	}
	if _, err := src.put(ctx, "TOKEN", "new", true); !errors.Is(err, errStoreValueExists) {
		t.Fatalf("create-only over a live value: %v", err)
	}
	if v, _ := g.Access(ctx, "p", "TOKEN"); string(v) != "old" {
		t.Errorf("the refused write must not add a version: latest = %q", v)
	}

	// A secret whose only version is disabled is not a value: create-only proceeds.
	g.disabled["p/TOKEN"] = true
	if v, err := src.put(ctx, "TOKEN", "new", true); err != nil || v != 2 {
		t.Errorf("version=%d err=%v", v, err)
	}
	// An empty container (exists, no version) is also writable.
	g.secrets["p/EMPTY"] = nil
	if v, err := src.put(ctx, "EMPTY", "v", true); err != nil || v != 1 {
		t.Errorf("version=%d err=%v", v, err)
	}
}

func TestGSMStoreSourcePutCreatesOnlyWhenAllowed(t *testing.T) {
	ctx := context.Background()
	g := newFakeGSM()
	if _, err := (gsmStoreSource{api: g, project: "p"}).put(ctx, "NEW", "v", false); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("without create a missing container is an error: %v", err)
	}
	v, err := (gsmStoreSource{api: g, project: "p", create: true}).put(ctx, "NEW", "v", false)
	if err != nil || v != 1 || len(g.created) != 1 {
		t.Fatalf("v=%d err=%v created=%v", v, err, g.created)
	}
}

func TestGSMStoreSourcePutNeverLeaksTheValueInAnError(t *testing.T) {
	g := newFakeGSM()
	g.fail = errors.New("denied for payload hunter2-the-secret")
	_, err := (gsmStoreSource{api: g, project: "p", create: true}).put(context.Background(), "TOKEN", "hunter2-the-secret", false)
	if err == nil || strings.Contains(err.Error(), "hunter2-the-secret") {
		t.Fatalf("err = %v: the value must be redacted", err)
	}
	// A failing existence probe surfaces untouched.
	if _, err := (gsmStoreSource{api: g, project: "p"}).put(context.Background(), "TOKEN", "v", true); err == nil {
		t.Fatal("want the probe's error")
	}
}

func TestGSMStoreSourcePresenceReportsOnlyUsableVersions(t *testing.T) {
	ctx := context.Background()
	g := newFakeGSM()
	for _, n := range []string{"A", "B", "C"} {
		if _, err := g.AddVersion(ctx, "p", n, []byte(n), true); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := g.AddVersion(ctx, "p", "A", []byte("a2"), false); err != nil {
		t.Fatal(err)
	}
	g.disabled["p/B"] = true
	src := gsmStoreSource{api: g, project: "p"}
	got, err := src.presence(ctx, []string{"A", "B", "C", "ABSENT"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (map[string]uint32{"A": 2, "C": 1}); !reflect.DeepEqual(got, want) {
		t.Errorf("presence = %v, want %v (a disabled version and a missing secret are not present)", got, want)
	}
	if g.accessed != 0 {
		t.Error("presence is metadata only: no payload may be read")
	}
	g.fail = errors.New("quota")
	if _, err := src.presence(ctx, []string{"A"}); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Errorf("err = %v", err)
	}
}
