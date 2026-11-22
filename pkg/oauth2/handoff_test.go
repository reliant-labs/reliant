package oauth2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBroker is the broker half of a handoff: start records the request,
// /consent (the "page") redirects the browser to the loopback with a code,
// redeem checks the verifier against the RECORDED challenge itself.
type fakeBroker struct {
	mu                         sync.Mutex
	challenge, redirect, state string
	method                     string
	stateOverride              string
}

func (b *fakeBroker) server(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch r.URL.Path {
		case "/auth/handoff/start":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			b.challenge, b.redirect, b.state, b.method = body["code_challenge"], body["redirect_uri"], body["state"], body["code_challenge_method"]
			_ = json.NewEncoder(w).Encode(map[string]string{"authorize_url": srv.URL + "/consent?req=r1"})
		case "/consent":
			state := b.state
			if b.stateOverride != "" {
				state = b.stateOverride
			}
			http.Redirect(w, r, b.redirect+"?"+url.Values{"code": {"fhc_1"}, "state": {state}}.Encode(), http.StatusSeeOther)
		case "/auth/handoff/redeem":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			sum := sha256.Sum256([]byte(body["code_verifier"]))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != b.challenge || body["redirect_uri"] != b.redirect || body["code"] != "fhc_1" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"code":"handoff_invalid","error":"The code is not valid."}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"kind":"native","session":"fs_new","token_type":"Bearer","expires_at":%q}`,
				time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHandoffLoginRoundTrip(t *testing.T) {
	b := &fakeBroker{}
	srv := b.server(t)
	var out strings.Builder
	got, err := HandoffLogin{Base: srv.URL + "/", OpenURL: browser, Out: &out, Timeout: 10 * time.Second}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Session != "fs_new" || !got.ExpiresAt.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("session %#v %v", got.Session, got.ExpiresAt)
	}
	if b.method != "S256" || !strings.HasPrefix(b.redirect, "http://127.0.0.1:") || !strings.HasSuffix(b.redirect, "/callback") {
		t.Errorf("start sent method %q redirect %q", b.method, b.redirect)
	}
	if strings.Contains(fmt.Sprint(*got), "fs_new") || strings.Contains(fmt.Sprintf("%#v", *got), "fs_new") {
		t.Error("the session credential does not redact itself")
	}
	if !strings.Contains(out.String(), "/consent?req=r1") {
		t.Errorf("the notice does not name the URL: %q", out.String())
	}
}

func TestHandoffLoginRefusesAForeignState(t *testing.T) {
	b := &fakeBroker{stateOverride: "someone-elses"}
	srv := b.server(t)
	_, err := HandoffLogin{Base: srv.URL, OpenURL: browser, Timeout: 10 * time.Second}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("a callback with another state was accepted: %v", err)
	}
}

func TestHandoffLoginReportsTheBrokersRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":"handoff_invalid","error":"redirect_uri is not allowed"}`))
	}))
	t.Cleanup(srv.Close)
	_, err := HandoffLogin{Base: srv.URL, Timeout: time.Second}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "redirect_uri is not allowed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := (HandoffLogin{}).Run(context.Background()); err == nil {
		t.Fatal("a handoff with no base ran")
	}
}
