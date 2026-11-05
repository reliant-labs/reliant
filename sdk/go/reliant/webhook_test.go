// Copyright (c) 2025 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type seen struct {
	method, path, auth, ct, idem, sig, ua string
	body                                  []byte
}

func hookServer(t *testing.T, status int, reply string) (*httptest.Server, *seen) {
	t.Helper()
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = seen{
			method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), ct: r.Header.Get("Content-Type"),
			idem: r.Header.Get("Idempotency-Key"), sig: r.Header.Get("X-Signature-256"), ua: r.Header.Get("User-Agent"), body: b,
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestWebhookBearerDelivery(t *testing.T) {
	srv, got := hookServer(t, 202, `{"event_id":"ev-1","outcome":"pending","duplicate":false}`)
	w, err := NewWebhook(srv.URL, "trig-1", WebhookBearer("whk_secret"), WithWebhookInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	d, err := w.SendJSON(context.Background(), map[string]any{"status": "failed"}, WithIdempotencyKey("deploy-42"), WithHeader("X-Source", "ci"))
	if err != nil {
		t.Fatal(err)
	}
	if d.EventID != "ev-1" || d.Outcome != "pending" || d.Duplicate {
		t.Fatalf("receipt = %+v", d)
	}
	if got.method != "POST" || got.path != "/hooks/trig-1" || got.auth != "Bearer whk_secret" || got.ct != "application/json" ||
		got.idem != "deploy-42" || !strings.HasPrefix(got.ua, "reliant-go-sdk/") || string(got.body) != `{"status":"failed"}` {
		t.Fatalf("server saw %+v", got)
	}
}

func TestWebhookPathTokenGoesInTheURLAndNeverInAnError(t *testing.T) {
	srv, got := hookServer(t, 404, "not found")
	w, err := NewWebhook(srv.URL, "trig-1", WebhookPathToken("tok_SECRET"), WithWebhookInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	_, err = w.Send(context.Background(), []byte("hi"))
	if got.path != "/hooks/trig-1/tok_SECRET" || got.auth != "" {
		t.Fatalf("server saw %+v", got)
	}
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if strings.Contains(err.Error(), "tok_SECRET") {
		t.Fatalf("the error leaked the URL credential: %v", err)
	}

	// A transport failure carries the URL in net/http's error; it must not leak
	// the credential either.
	srv.Close()
	_, err = w.Send(context.Background(), []byte("hi"))
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "tok_SECRET") {
		t.Fatalf("transport failure = %v: want ErrUnavailable without the credential", err)
	}
}

func TestWebhookHMACSignsTheExactBodyBytes(t *testing.T) {
	srv, got := hookServer(t, 202, `{}`)
	w, err := NewWebhook(srv.URL, "trig-1", WebhookHMAC([]byte("shh"), WebhookHMACConfig{Prefix: "sha256="}), WithWebhookInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(context.Background(), []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	// Known-answer: HMAC-SHA256("shh", `{"a":1}`).
	const want = "sha256=dfb8cf3fc9778c70386e30f5e0776d37f9ee9c8756d3cbd7df0902150644358d"
	if got.sig != want {
		t.Fatalf("signature header = %q", got.sig)
	}
	if got.auth != "" {
		t.Fatalf("an HMAC delivery also sent a bearer: %q", got.auth)
	}
}

func TestWebhookMapsTheDocumentedAnswers(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{404, ErrNotFound},
		{409, ErrFailedPrecondition}, // the trigger is disabled
		{413, ErrInvalidArgument},
		{503, ErrUnavailable},
	}
	for _, tc := range cases {
		srv, _ := hookServer(t, tc.status, "x")
		w, _ := NewWebhook(srv.URL, "t", WebhookBearer("x"), WithWebhookInsecureLoopback())
		if _, err := w.Send(context.Background(), []byte("b")); !errors.Is(err, tc.want) {
			t.Errorf("HTTP %d: err = %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestWebhookDuplicateReceiptAndUnreadableReceipt(t *testing.T) {
	srv, _ := hookServer(t, 202, `{"event_id":"e","outcome":"pending","duplicate":true}`)
	w, _ := NewWebhook(srv.URL, "t", WebhookBearer("x"), WithWebhookInsecureLoopback())
	if d, err := w.Send(context.Background(), nil); err != nil || !d.Duplicate {
		t.Fatalf("duplicate = %+v, %v", d, err)
	}
	// Accepted is accepted, even if the receipt body is not what we expected.
	srv2, _ := hookServer(t, 202, "accepted")
	w2, _ := NewWebhook(srv2.URL, "t", WebhookBearer("x"), WithWebhookInsecureLoopback())
	if d, err := w2.Send(context.Background(), nil); err != nil || d == nil {
		t.Fatalf("unreadable receipt = %+v, %v", d, err)
	}
}

func TestNewWebhookValidates(t *testing.T) {
	for _, tc := range []struct {
		base, id string
		cred     WebhookCredential
		ok       bool
	}{
		{"https://api.reliantapi.com", "t-1", WebhookBearer("x"), true},
		{"", "t-1", WebhookBearer("x"), true}, // the default server
		{"https://api.reliantapi.com", "", WebhookBearer("x"), false},
		{"https://api.reliantapi.com", "a/b", WebhookBearer("x"), false},
		{"https://api.reliantapi.com", "t-1", nil, false},
		{"http://api.reliantapi.com", "t-1", WebhookBearer("x"), false}, // a token never goes in the clear
	} {
		_, err := NewWebhook(tc.base, tc.id, tc.cred)
		if (err == nil) != tc.ok {
			t.Errorf("NewWebhook(%q,%q,%v) err = %v, want ok=%v", tc.base, tc.id, tc.cred, err, tc.ok)
		}
	}
	// An empty token is refused at send, before it reaches the network.
	srv, got := hookServer(t, 202, `{}`)
	w, _ := NewWebhook(srv.URL, "t", WebhookBearer(""), WithWebhookInsecureLoopback())
	if _, err := w.Send(context.Background(), nil); err == nil || got.method != "" {
		t.Fatalf("an empty token was sent: err=%v saw=%+v", err, got)
	}
}
