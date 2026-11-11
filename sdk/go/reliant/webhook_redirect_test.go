// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A webhook delivery carries the trigger's credential: a bearer token, a path
// token in the URL, or an HMAC signature over the body. Go's client follows
// redirects by default and re-sends some of that (Authorization to the same
// host even over http, any custom signature header to any host, the signed
// body on 307/308), so a redirect would carry the credential past the https
// origin NewWebhook validated. Deliveries refuse every redirect.

func TestWebhookRefusesRedirects(t *testing.T) {
	creds := map[string]WebhookCredential{
		"bearer":             WebhookBearer("whk_SECRET"),
		"path token":         WebhookPathToken("tok_SECRET"),
		"hmac":               WebhookHMAC([]byte("SECRET"), WebhookHMACConfig{}),
		"hmac custom header": WebhookHMAC([]byte("SECRET"), WebhookHMACConfig{Header: "X-Hub-Signature", Prefix: "sha256="}),
	}
	for name, cred := range creds {
		for _, code := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			for _, target := range []string{"another origin", "http downgrade"} {
				t.Run(fmt.Sprintf("%s/%d/%s", name, code, target), func(t *testing.T) {
					var reached atomic.Int32
					elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						reached.Add(1)
						w.WriteHeader(http.StatusAccepted)
					}))
					t.Cleanup(elsewhere.Close)

					// The receiver is https. The redirect points at another host, or
					// at plain http on the receiver's own host and port: followed,
					// that sends the credential in cleartext, as a second
					// connection to the listener.
					var conns atomic.Int32
					origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						to := elsewhere.URL + r.URL.Path
						if target == "http downgrade" {
							to = "http://" + r.Host + r.URL.Path
						}
						http.Redirect(w, r, to, code)
					}))
					origin.Config.ConnState = func(_ net.Conn, st http.ConnState) {
						if st == http.StateNew {
							conns.Add(1)
						}
					}
					origin.StartTLS()
					t.Cleanup(origin.Close)

					supplied := origin.Client()
					before := supplied.CheckRedirect
					w, err := NewWebhook(origin.URL, "trig-1", cred, WithWebhookHTTPClient(supplied))
					if err != nil {
						t.Fatal(err)
					}
					_, err = w.SendJSON(context.Background(), map[string]any{"k": "v"})
					if !errors.Is(err, ErrFailedPrecondition) {
						t.Fatalf("err = %v, want ErrFailedPrecondition for a refused redirect", err)
					}
					if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), elsewhere.URL) {
						t.Fatalf("the error leaks the credential or the redirect target: %v", err)
					}
					if n := reached.Load(); n != 0 {
						t.Fatalf("the redirect was followed: the other server got %d request(s)", n)
					}
					if n := conns.Load(); n != 1 {
						t.Fatalf("the client opened %d connections to the receiver, want 1: the redirect was followed", n)
					}
					if fmt.Sprintf("%p", supplied.CheckRedirect) != fmt.Sprintf("%p", before) {
						t.Fatal("NewWebhook changed the caller's http.Client")
					}
				})
			}
		}
	}
}

// Same-origin too: the client sends exactly one request, the delivery itself.
func TestWebhookSendsExactlyOneRequestOnARedirect(t *testing.T) {
	var sent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.Add(1)
		if r.URL.Path == "/hooks/trig-1" {
			http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	w, err := NewWebhook(srv.URL, "trig-1", WebhookBearer("whk_SECRET"), WithWebhookInsecureLoopback())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Send(context.Background(), []byte("{}")); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("err = %v, want ErrFailedPrecondition", err)
	}
	if n := sent.Load(); n != 1 {
		t.Fatalf("the client sent %d requests, want 1: the redirect was followed", n)
	}
}
