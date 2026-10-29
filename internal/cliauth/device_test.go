package cliauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/oauth2"
)

// fakeDeviceServer is an authorization server that offers device pairing and
// answers the polls in `replies` order (the last one repeats).
func fakeDeviceServer(t *testing.T, offerDevice bool, replies []map[string]any) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var polls atomic.Int32
	var gotForm atomic.Value
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc(oauth2.AuthorizationServerMetadataPath, func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/oauth/authorize",
			"token_endpoint":         srv.URL + "/oauth/token",
		}
		if offerDevice {
			doc["device_authorization_endpoint"] = srv.URL + "/oauth/device_authorization"
		}
		_ = json.NewEncoder(w).Encode(doc)
	})
	mux.HandleFunc("/oauth/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm.Store(r.PostForm)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dc-secret", "user_code": "BDFG-HJKL",
			"verification_uri":          "https://app.example.test/oauth/device",
			"verification_uri_complete": "https://app.example.test/oauth/device?user_code=BDFG-HJKL",
			"expires_in":                600, "interval": 5,
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("grant_type") != DeviceGrantType || r.PostForm.Get("device_code") != "dc-secret" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
			return
		}
		n := int(polls.Add(1)) - 1
		reply := replies[min(n, len(replies)-1)]
		if _, ok := reply["access_token"]; !ok {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = json.NewEncoder(w).Encode(reply)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &polls, &gotForm
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestDeviceLogin_PrintsTheCodeAndPollsUntilApproved(t *testing.T) {
	srv, polls, form := fakeDeviceServer(t, true, []map[string]any{
		{"error": "authorization_pending"},
		{"error": "slow_down"},
		{"access_token": "rlat_0123456789abcdef", "scope": "daemon:connect"},
	})
	var out bytes.Buffer
	var opened string
	var waits []time.Duration
	cred, err := DeviceLogin{
		Server: srv.URL, Scopes: []string{ScopeDaemon}, Name: "daemon-box",
		Hostname: "build-box", Platform: "linux/amd64", Out: &out,
		OpenURL: func(u string) error { opened = u; return nil },
		Sleep: func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)
			return nil
		},
	}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cred.Token != "rlat_0123456789abcdef" || cred.ExpiresAt != nil || cred.Issuer != srv.URL {
		t.Fatalf("credential = %+v", cred)
	}
	if polls.Load() != 3 {
		t.Fatalf("polled %d times, want 3", polls.Load())
	}
	// slow_down adds five seconds to every later wait (RFC 8628 §3.5).
	if len(waits) != 3 || waits[0] != 5*time.Second || waits[2] != 10*time.Second {
		t.Fatalf("waits = %v", waits)
	}
	if !strings.Contains(out.String(), "BDFG-HJKL") || !strings.Contains(out.String(), "user_code=BDFG-HJKL") {
		t.Fatalf("output does not show the code and the approval link:\n%s", out.String())
	}
	if opened != "https://app.example.test/oauth/device?user_code=BDFG-HJKL" {
		t.Fatalf("opened %q", opened)
	}
	f := form.Load().(interface{ Get(string) string })
	if f.Get("client_id") != ClientID || f.Get("hostname") != "build-box" || f.Get("device") != "daemon-box" {
		t.Fatalf("authorization request form: %v", f)
	}
}

// The terminal names the same request the approval page does: daemon:connect
// pairs a machine, anything else signs the CLI in on this host.
func TestDeviceLogin_WordingFollowsTheRequestedAuthority(t *testing.T) {
	for _, tc := range []struct {
		scopes      []string
		want, never string
	}{
		{[]string{ScopeDaemon, "deploy:read"}, "Pair this machine", "Sign in the Reliant CLI"},
		{[]string{ScopeAPI, "deploy:read"}, "Sign in the Reliant CLI on build-box.", "Pair this machine"},
	} {
		srv, _, _ := fakeDeviceServer(t, true, []map[string]any{{"access_token": "rlat_0123456789abcdef", "scope": "x"}})
		var out bytes.Buffer
		if _, err := (DeviceLogin{Server: srv.URL, Scopes: tc.scopes, Hostname: "build-box", Out: &out, Sleep: noSleep}).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), tc.never) {
			t.Errorf("scopes %v: output\n%s\nwant %q and never %q", tc.scopes, out.String(), tc.want, tc.never)
		}
	}
	srv, _, _ := fakeDeviceServer(t, true, []map[string]any{{"error": "access_denied"}})
	_, err := DeviceLogin{Server: srv.URL, Scopes: []string{ScopeAPI}, Sleep: noSleep}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sign-in was declined") {
		t.Fatalf("a declined CLI sign-in: err = %v", err)
	}
}

func TestDeviceLogin_DeclinedAndExpiredAreErrors(t *testing.T) {
	for reply, want := range map[string]string{"access_denied": "declined", "expired_token": "expired"} {
		srv, _, _ := fakeDeviceServer(t, true, []map[string]any{{"error": reply}})
		_, err := DeviceLogin{Server: srv.URL, Scopes: []string{ScopeDaemon}, Sleep: noSleep}.Run(context.Background())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: err = %v, want it to say %q", reply, err, want)
		}
	}
}

func TestDeviceLogin_ServerWithoutDeviceFlow(t *testing.T) {
	srv, _, _ := fakeDeviceServer(t, false, nil)
	_, err := DeviceLogin{Server: srv.URL, Scopes: []string{ScopeDaemon}, Sleep: noSleep}.Run(context.Background())
	if !errors.Is(err, ErrNoDeviceFlow) {
		t.Fatalf("err = %v, want ErrNoDeviceFlow so the caller can fall back to a browser login", err)
	}
}

func TestDeviceEndpoint_MustBeOnTheIssuersOrigin(t *testing.T) {
	meta := &oauth2.ProviderMetadata{Issuer: "https://admin.example.com",
		Raw: map[string]any{"device_authorization_endpoint": "https://evil.example.com/oauth/device_authorization"}}
	if _, err := deviceEndpoint(meta); err == nil {
		t.Fatal("accepted a device endpoint off the issuer's origin")
	}
}

func TestMetadataHandler_AdvertisesDevicePairing(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle(oauth2.AuthorizationServerMetadataPath, MetadataHandler("https://admin.example.com"))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	meta, err := oauth2.DiscoverAuthorizationServer(context.Background(), nil, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := deviceEndpoint(meta)
	if err != nil || ep != "https://admin.example.com/oauth/device_authorization" {
		t.Fatalf("device endpoint = %q, %v", ep, err)
	}
}
