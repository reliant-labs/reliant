// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/instanceid"
)

// fakeCP plays the control plane's CLI login: RFC 8414 metadata, an
// /oauth/authorize that approves at once (as if the user clicked Approve), and
// an /oauth/token that checks the PKCE verifier and mints a token. It records
// every authorize request.
type fakeCP struct {
	srv       *httptest.Server
	mu        sync.Mutex
	requests  []url.Values
	challenge string
	token     string
	// offerDevice advertises RFC 8628 device authorization; the request is
	// approved at once (as if the person approved on another device) and
	// recorded in deviceForm.
	offerDevice bool
	deviceForm  url.Values
}

func newFakeCP(t *testing.T, token string) *fakeCP {
	t.Helper()
	cp := &fakeCP{token: token}
	cp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server":
			doc := map[string]string{
				"issuer":                 cp.srv.URL,
				"authorization_endpoint": cp.srv.URL + "/oauth/authorize",
				"token_endpoint":         cp.srv.URL + "/oauth/token",
			}
			if cp.offerDevice {
				doc["device_authorization_endpoint"] = cp.srv.URL + "/oauth/device_authorization"
			}
			_ = json.NewEncoder(w).Encode(doc)
		case "/oauth/device_authorization":
			_ = r.ParseForm()
			cp.mu.Lock()
			cp.deviceForm = r.PostForm
			cp.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"device_code": "dc-secret", "user_code": "BDFG-HJKL",
				"verification_uri":          "https://app.example.test/oauth/device",
				"verification_uri_complete": "https://app.example.test/oauth/device?user_code=BDFG-HJKL",
				"expires_in":                600, "interval": 5,
			})
		case "/oauth/token":
			_ = r.ParseForm()
			if r.PostForm.Get("grant_type") == cliauth.DeviceGrantType {
				cp.mu.Lock()
				ok := cp.deviceForm != nil && r.PostForm.Get("device_code") == "dc-secret" && r.PostForm.Get("client_id") == cliauth.ClientID
				scope := cp.deviceForm.Get("scope")
				cp.mu.Unlock()
				if !ok {
					w.WriteHeader(400)
					_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": cp.token, "token_type": "Bearer", "expires_in": 7776000, "scope": scope,
				})
				return
			}
			cp.browserToken(w, r)
		case "/oauth/authorize":
			q := r.URL.Query()
			cp.mu.Lock()
			cp.requests = append(cp.requests, q)
			cp.challenge = q.Get("code_challenge")
			cp.mu.Unlock()
			back, _ := url.Parse(q.Get("redirect_uri"))
			back.RawQuery = url.Values{"code": {"c"}, "state": {q.Get("state")}}.Encode()
			http.Redirect(w, r, back.String(), http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cp.srv.Close)
	return cp
}

// browserToken redeems the authorization code: the PKCE verifier must match
// the last authorize request's challenge.
func (cp *fakeCP) browserToken(w http.ResponseWriter, r *http.Request) {
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	cp.mu.Lock()
	ok := len(cp.requests) > 0 && base64.RawURLEncoding.EncodeToString(sum[:]) == cp.challenge && r.PostForm.Get("client_id") == cliauth.ClientID
	var scope string
	if ok {
		scope = cp.requests[len(cp.requests)-1].Get("scope")
	}
	cp.mu.Unlock()
	if !ok {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": cp.token, "token_type": "Bearer", "expires_in": 7776000, "scope": scope,
	})
}

func (cp *fakeCP) lastRequest() url.Values {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.requests[len(cp.requests)-1]
}

// presentDesktop makes this process look like a desktop session to
// chooseLoginFlow, so a test run on a cloud machine, over ssh or on a headless
// CI runner gets the same default login flow as one on a laptop. A test of the
// device default sets the one variable it is about afterwards.
func presentDesktop(t *testing.T) {
	t.Helper()
	for _, name := range []string{"RELIANT_DAEMON_TYPE", "RELIANT_AGENT_SHELL", "SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY", "WAYLAND_DISPLAY"} {
		t.Setenv(name, "")
	}
	t.Setenv("DISPLAY", ":0")
}

// withFakeLoginBrowser is a browser on this machine that follows the login
// URL at once, as if the person approved. A browser here means a desktop.
func withFakeLoginBrowser(t *testing.T) {
	t.Helper()
	presentDesktop(t)
	prev := loginOpener
	loginOpener = func(u string) error {
		go func() {
			if resp, err := http.Get(u); err == nil {
				_ = resp.Body.Close()
			}
		}()
		return nil
	}
	t.Cleanup(func() { loginOpener = prev })
}

func executeRoot(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// TestAuthLogin_WritesTheSharedFile is the contract of `reliant auth login`:
// PKCE against the control plane found by discovery from --server, as client
// reliant-cli asking reliant:api, stored in forge's shared file under THAT
// server, beside (not over) forge's own entry for it.
func TestAuthLogin_WritesTheSharedFile(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_RELIANTCLI00000000")

	// forge already logged in to the same origin: it must survive.
	path, _ := cliauth.CredentialsPath()
	if err := credentials.Store(path, cp.srv.URL, "forge-cli", credentials.Credential{Token: "rlat_FORGE"}); err != nil {
		t.Fatal(err)
	}

	out, err := executeRoot(t, "auth", "login", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("auth login: %v\n%s", err, out)
	}
	req := cp.lastRequest()
	wantScope := strings.Join(append([]string{cliauth.ScopeAPI}, cliauth.ForgeScopes()...), " ")
	if req.Get("client_id") != cliauth.ClientID || req.Get("scope") != wantScope || req.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize request: %v", req)
	}
	got, err := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID)
	if err != nil || got.Token != "rlat_RELIANTCLI00000000" || got.ExpiresAt == nil {
		t.Fatalf("stored credential: %+v %v", got, err)
	}
	if f, _ := credentials.Lookup(path, cp.srv.URL, "forge-cli"); f.Token != "rlat_FORGE" {
		t.Fatal("reliant's login clobbered forge's entry for the same server")
	}

	// A later command against that server uses it; status reports it.
	if out, err := executeRoot(t, "auth", "status", "--server", cp.srv.URL); err != nil || !strings.Contains(out, "rlat_RELIANTC") {
		t.Fatalf("status after login: %v\n%s", err, out)
	}
	// logout forgets reliant's entry only.
	if _, err := executeRoot(t, "auth", "logout", "--server", cp.srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID); err == nil {
		t.Fatal("logout must remove reliant's entry")
	}
	if f, _ := credentials.Lookup(path, cp.srv.URL, "forge-cli"); f.Token != "rlat_FORGE" {
		t.Fatal("reliant logout removed forge's entry")
	}
}

// TestAuthTokenCreate_MintsANamedTokenAndDoesNotStoreIt: `auth token create`
// is a consented login under the given name, printed once, never saved over
// the CLI's own login.
func TestAuthTokenCreate_MintsANamedTokenAndDoesNotStoreIt(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_CITOKEN0000000000")
	loginFor(t, cp.srv.URL, "rlat_MYLOGIN")
	path, _ := cliauth.CredentialsPath()

	out, err := executeRoot(t, "auth", "token", "create", "--name", "ci-deploy", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("token create: %v\n%s", err, out)
	}
	if !strings.Contains(out, "rlat_CITOKEN0000000000") {
		t.Fatalf("the token must be printed once:\n%s", out)
	}
	if req := cp.lastRequest(); req.Get("device") != "ci-deploy" || req.Get("scope") != cliauth.ScopeAPI {
		t.Errorf("authorize request: %v", req)
	}
	if c, _ := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID); c.Token != "rlat_MYLOGIN" {
		t.Fatalf("token create must not replace the CLI's own login; got %q", c.Token)
	}
}

// TestAuthLogin_ServerWithoutDiscoveryFailsClearly: a self-hosted server with
// no control plane offers no browser login and the CLI says what to do.
func TestAuthLogin_ServerWithoutDiscoveryFailsClearly(t *testing.T) {
	isolateCLI(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	for _, flow := range []string{"--browser", "--device"} {
		_, err := executeRoot(t, "auth", "login", flow, "--server", srv.URL)
		if err == nil || !strings.Contains(err.Error(), "RELIANT_TOKEN") {
			t.Fatalf("%s: want a no-login error pointing at RELIANT_TOKEN; got %v", flow, err)
		}
	}
}

// TestChooseLoginFlow: the browser flow wherever a browser can reach this
// machine's loopback listener; the device flow wherever the environment says
// none can; the flags override either way, and Remote is kept so a server
// without device sign-in falls back to the browser only where it can work.
func TestChooseLoginFlow(t *testing.T) {
	desktop := map[string]string{"DISPLAY": ":0"}
	for _, tc := range []struct {
		name               string
		env                map[string]string
		goos               string
		device, browser    bool
		wantDevice, remote bool
		wantForced         bool
	}{
		{name: "linux desktop", env: desktop, goos: "linux"},
		{name: "wayland desktop", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, goos: "linux"},
		{name: "mac", goos: "darwin"},
		{name: "windows", goos: "windows"},
		{name: "cloud machine", env: map[string]string{"RELIANT_DAEMON_TYPE": "managed", "DISPLAY": ":0"}, goos: "linux", wantDevice: true, remote: true},
		{name: "self-hosted daemon type is not remote", env: map[string]string{"RELIANT_DAEMON_TYPE": "self_hosted"}, goos: "darwin"},
		{name: "agent shell", env: map[string]string{"RELIANT_AGENT_SHELL": "1"}, goos: "darwin", wantDevice: true, remote: true},
		{name: "ssh into a mac", env: map[string]string{"SSH_CONNECTION": "10.0.0.2 51000 10.0.0.1 22"}, goos: "darwin", wantDevice: true, remote: true},
		{name: "ssh tty", env: map[string]string{"SSH_TTY": "/dev/pts/0", "DISPLAY": ":0"}, goos: "linux", wantDevice: true, remote: true},
		{name: "headless linux", goos: "linux", wantDevice: true, remote: true},
		{name: "--browser on a cloud machine", env: map[string]string{"RELIANT_DAEMON_TYPE": "managed"}, goos: "linux", browser: true, remote: true, wantForced: true},
		{name: "--device on a desktop", env: desktop, goos: "linux", device: true, wantDevice: true, wantForced: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseLoginFlow(tc.device, tc.browser, func(k string) string { return tc.env[k] }, tc.goos)
			if got.Device != tc.wantDevice || (got.Remote != "") != tc.remote || got.Forced != tc.wantForced {
				t.Fatalf("chooseLoginFlow = %+v; want device=%v remote=%v forced=%v", got, tc.wantDevice, tc.remote, tc.wantForced)
			}
		})
	}
}

// withNoDeviceWait makes the device flow's polls free and fails the test if
// anything tries to open a browser.
func withNoDeviceWait(t *testing.T) {
	t.Helper()
	prevSleep, prevOpen := deviceLoginSleep, loginOpener
	deviceLoginSleep = func(context.Context, time.Duration) error { return nil }
	loginOpener = func(u string) error {
		t.Errorf("opened a browser at %s on a machine no browser can reach", u)
		return nil
	}
	t.Cleanup(func() { deviceLoginSleep, loginOpener = prevSleep, prevOpen })
}

// TestAuthLogin_DeviceFlowOnACloudMachine is request A's contract: on a
// Reliant cloud machine `reliant auth login` asks for a device code with the
// browser login's scopes and name, says "Sign in the Reliant CLI", opens no
// browser, and stores the token exactly as the browser flow does.
func TestAuthLogin_DeviceFlowOnACloudMachine(t *testing.T) {
	path := isolateCLI(t)
	t.Setenv("RELIANT_DAEMON_TYPE", "managed")
	withNoDeviceWait(t)
	cp := newFakeCP(t, "rlat_DEVICELOGIN000000")
	cp.offerDevice = true

	out, err := executeRoot(t, "auth", "login", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("auth login: %v\n%s", err, out)
	}
	cp.mu.Lock()
	form, browserLogins := cp.deviceForm, len(cp.requests)
	cp.mu.Unlock()
	wantScope := strings.Join(append([]string{cliauth.ScopeAPI}, cliauth.ForgeScopes()...), " ")
	if form.Get("client_id") != cliauth.ClientID || form.Get("scope") != wantScope ||
		form.Get("device") != cliLoginName() || form.Get("hostname") != instanceid.Hostname() || form.Get("platform") == "" {
		t.Errorf("device authorization request: %v", form)
	}
	for _, want := range []string{"Reliant cloud machine", "Sign in the Reliant CLI on", "BDFG-HJKL", "user_code=BDFG-HJKL", "Logged in to"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Pair this machine") {
		t.Errorf("a CLI sign-in must not read as a machine pairing:\n%s", out)
	}
	got, err := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID)
	if err != nil || got.Token != "rlat_DEVICELOGIN000000" || got.ExpiresAt == nil {
		t.Fatalf("stored credential: %+v %v", got, err)
	}
	if browserLogins != 0 {
		t.Fatal("the device flow must not also start a browser login")
	}
}

// TestAuthLogin_NoDeviceFlowWhereNoBrowserCanWork: a server without device
// sign-in, on a host no browser can reach, is a clear error naming the way
// out — never a browser login that waits five minutes for a callback that
// cannot arrive.
func TestAuthLogin_NoDeviceFlowWhereNoBrowserCanWork(t *testing.T) {
	isolateCLI(t)
	t.Setenv("SSH_CONNECTION", "10.0.0.2 51000 10.0.0.1 22")
	withNoDeviceWait(t)
	cp := newFakeCP(t, "rlat_X")

	_, err := executeRoot(t, "auth", "login", "--server", cp.srv.URL)
	if err == nil || !strings.Contains(err.Error(), "does not offer device-code sign-in") ||
		!strings.Contains(err.Error(), "ssh session") || !strings.Contains(err.Error(), "--browser") {
		t.Fatalf("want a clear no-device-sign-in error; got %v", err)
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if len(cp.requests) != 0 {
		t.Fatal("fell back to a browser login on a host no browser can reach")
	}
}

// TestAuthLogin_ForcedDeviceFallsBackToTheBrowserWhereOneWorks: --device on a
// desktop, against a server without device sign-in, says so and approves in
// the browser instead.
func TestAuthLogin_ForcedDeviceFallsBackToTheBrowserWhereOneWorks(t *testing.T) {
	path := isolateCLI(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_FALLBACK00000000")

	out, err := executeRoot(t, "auth", "login", "--device", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("auth login --device: %v\n%s", err, out)
	}
	if !strings.Contains(out, "does not offer device-code sign-in; approving in your browser instead") {
		t.Errorf("output does not explain the fallback:\n%s", out)
	}
	if got, _ := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID); got.Token != "rlat_FALLBACK00000000" {
		t.Fatalf("stored token = %q", got.Token)
	}
}

func TestAuthLogin_DeviceAndBrowserAreExclusive(t *testing.T) {
	isolateCLI(t)
	if _, err := executeRoot(t, "auth", "login", "--device", "--browser"); err == nil {
		t.Fatal("--device and --browser together must be refused")
	}
}

// TestContextCommandIsGone: `reliant context` and --context are deleted, with
// no compatibility shim.
func TestContextCommandIsGone(t *testing.T) {
	isolateCLI(t)
	root := NewRootCmd()
	for _, c := range root.Commands() {
		if c.Name() == "context" {
			t.Fatal("`reliant context` must not exist")
		}
	}
	if root.PersistentFlags().Lookup("context") != nil {
		t.Fatal("--context must not exist")
	}
	if _, err := executeRoot(t, "project", "list", "--context", "prod"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("--context must be an unknown flag; got %v", err)
	}
}
