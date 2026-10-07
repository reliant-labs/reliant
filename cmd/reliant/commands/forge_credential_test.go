// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/spf13/cobra"

	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/grpc/services"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// ── forge reuses the Reliant session, end to end through the CLI ─────────
//
// The server half (ExchangeToken's rules) is pinned in internal/grpc; these
// pin the CLIENT half the owner's report was about: a machine signed in to
// Reliant — through the app (an Electron-minted daemon credential), on a
// managed cloud daemon (a read-only mounted Secret), or with `reliant auth
// login` — gives forge a credential with no `forge login`, and never hands
// forge the session credential itself.

const testControlPlane = "https://admin.example.com"

// isolateForgeCredHome points HOME (so ~/.reliant/daemon.json and the token
// cache) and FORGE_HOME (forge's credentials.json) at a temp dir, and clears
// any helper the developer's own shell exported. Returns forge's file path.
func isolateForgeCredHome(t *testing.T) (credPath, home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	t.Setenv("FORGE_HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv(envServerURL, "")
	t.Setenv(envGatewayURL, "")
	t.Setenv(envToken, "")
	t.Setenv(cloudcred.HelperEnv, "")
	if err := os.MkdirAll(filepath.Join(home, ".reliant"), 0o700); err != nil {
		t.Fatal(err)
	}
	credPath = filepath.Join(home, credentials.FileName)
	if p, err := cliauth.CredentialsPath(); err != nil || p != credPath {
		t.Fatalf("credentials path %q escaped temp HOME %q — aborting to protect the real file", p, home)
	}
	return credPath, home
}

// reliantAPI serves reliant's real TokenService over HTTP for one control
// plane, backed by tokenauthority.Memory (forge/pkg/accesstoken's real grant
// rules). ExchangeToken authenticates its own bearer, so no interceptor is
// needed for it; internal/grpc pins it behind the real one.
type reliantAPI struct {
	url       string
	authority *tokenauthority.Memory
}

func serveReliantAPI(t *testing.T, controlPlane string) *reliantAPI {
	t.Helper()
	authority := tokenauthority.NewMemory()
	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewTokenServiceHandler(
		services.NewTokenService(authority, services.TokenControlPlane{Issuer: controlPlane}))
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &reliantAPI{url: srv.URL, authority: authority}
}

// mintSession mints a session credential for userID at the API's authority.
func (api *reliantAPI) mintSession(t *testing.T, userID string, scopes ...fat.Scope) string {
	t.Helper()
	m, err := api.authority.MintForUser(context.Background(), tokenauthority.MintRequest{
		UserID: userID, Name: "session", Scopes: scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m.Plaintext
}

func (api *reliantAPI) liveTokens(t *testing.T, userID string) int {
	t.Helper()
	infos, err := api.authority.ListForUser(context.Background(), userID, "")
	if err != nil {
		t.Fatal(err)
	}
	return len(infos)
}

// daemonCeiling is what a daemon credential holds once its user's
// control-plane authority is clipped onto it.
var daemonCeiling = []fat.Scope{
	fat.ScopeDaemonConnect,
	fat.ScopeDeployRead, fat.ScopeDeployWrite, fat.ScopeSecretRead, fat.ScopeSecretWrite,
	fat.ScopeDomainRead, fat.ScopeDomainWrite,
}

// runHelper runs `reliant <args>` in-process with req on stdin and decodes the
// one protocol response from stdout. stderr is returned separately: it must
// never carry a token.
func runHelper(t *testing.T, endpoint string, args ...string) (cloudcred.Response, string) {
	t.Helper()
	in, err := json.Marshal(cloudcred.Request{Version: cloudcred.ProtocolVersion, Endpoint: endpoint,
		Scopes: []string{"deploy:read", "deploy:write", "secret:read", "secret:write", "domain:read", "domain:write"}})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	root := NewRootCmd()
	root.SetIn(bytes.NewReader(in))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"auth", "forge-credential"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("auth forge-credential: %v\nstderr: %s", err, stderr.String())
	}
	var resp cloudcred.Response
	dec := json.NewDecoder(&stdout)
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("stdout is not one protocol response: %v", err)
	}
	if rest, _ := io.ReadAll(dec.Buffered()); strings.TrimSpace(string(rest))+strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("stdout carries more than the protocol response: %q", string(rest)+stdout.String())
	}
	return resp, stderr.String()
}

// TestForgeCredential_ElectronDaemonSessionEndToEnd is the owner's scenario:
// signed in through the app (the daemon credential is on disk, Electron wrote
// it), an agent runs forge in a shell the daemon spawned. The daemon exports a
// helper pinned to itself, and forge gets an exchanged deploy token — not the
// daemon's credential — with no `forge login`.
func TestForgeCredential_ElectronDaemonSessionEndToEnd(t *testing.T) {
	credPath, _ := isolateForgeCredHome(t)
	api := serveReliantAPI(t, testControlPlane)
	const userID = "user-app"
	daemonPAT := api.mintSession(t, userID, daemonCeiling...)
	if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{PAT: daemonPAT, ServerURL: api.url}); err != nil {
		t.Fatal(err)
	}
	// An older release's deposit of the same session sits in forge's file.
	if err := credentials.Store(credPath, testControlPlane, "host-app", credentials.Credential{Token: daemonPAT}); err != nil {
		t.Fatal(err)
	}

	// The daemon starts.
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if _, err := resolveOrAwaitCredentials(context.Background(), cmd, &connection{ServerURL: api.url}, "", t.TempDir(), true); err != nil {
		t.Fatalf("daemon start: %v", err)
	}

	// Its children inherit a helper PINNED to it.
	argv, err := cloudcred.ParseCommand(os.Getenv(cloudcred.HelperEnv))
	if err != nil || len(argv) < 5 {
		t.Fatalf("the daemon did not export a usable %s: %q %v", cloudcred.HelperEnv, os.Getenv(cloudcred.HelperEnv), err)
	}
	if strings.Join(argv[1:5], " ") != "auth forge-credential --daemon-server "+api.url {
		t.Fatalf("helper argv = %q, want it pinned to this daemon's server", argv)
	}
	// …and the legacy copy of the session token is gone from forge's file.
	if _, err := credentials.Lookup(credPath, testControlPlane, "host-app"); err == nil {
		t.Error("the daemon left an older release's deposited session token in forge's credentials file")
	}

	// forge asks the helper, exactly as the agent's forge would.
	resp, stderr := runHelper(t, testControlPlane, argv[3:]...)
	if resp.Error != nil {
		t.Fatalf("helper refused: %+v", resp.Error)
	}
	if resp.Token == "" || resp.Token == daemonPAT {
		t.Fatalf("forge must get an EXCHANGED token, never the daemon's own credential")
	}
	if strings.Contains(stderr, resp.Token) || strings.Contains(stderr, daemonPAT) {
		t.Fatalf("a token reached stderr: %s", stderr)
	}
	p, err := api.authority.Introspect(context.Background(), resp.Token)
	if err != nil {
		t.Fatal(err)
	}
	if p.ActingUserID != userID || p.Scopes.Has(fat.ScopeDaemonConnect) || !p.Scopes.Has(fat.ScopeDeployWrite) {
		t.Fatalf("exchanged principal = %+v; want the same user with deploy authority and no session authority", p)
	}
	if resp.ExpiresAt == nil || resp.ExpiresAt.After(time.Now().Add(time.Hour+time.Minute)) {
		t.Fatalf("expires_at = %v, want within the hour", resp.ExpiresAt)
	}
	if !strings.Contains(resp.Source, "Reliant session (daemon at ") {
		t.Errorf("source = %q, want the session named for forge to print", resp.Source)
	}

	// The next forge command reuses it: no second mint.
	before := api.liveTokens(t, userID)
	again, _ := runHelper(t, testControlPlane, argv[3:]...)
	if again.Token != resp.Token {
		t.Error("a fresh cached token must be reused, not re-minted")
	}
	if after := api.liveTokens(t, userID); after != before {
		t.Errorf("the second request minted (%d → %d live tokens)", before, after)
	}
	info, err := os.Stat(forgeTokenCachePath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("token cache mode = %v, want 0600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(forgeTokenCachePath())
	if strings.Contains(string(raw), daemonPAT) {
		t.Error("the cache must hold a fingerprint of the session, never the session credential")
	}
}

// TestForgeCredential_ManagedCloudDaemon is the hosted case: a managed pod's
// credential is a READ-ONLY mounted Secret that nothing in reliant wrote, the
// pod is non-interactive by force, and an agent there must still deploy with
// no login.
func TestForgeCredential_ManagedCloudDaemon(t *testing.T) {
	_, home := isolateForgeCredHome(t)
	t.Setenv(daemonruntime.DaemonTypeEnvVar, "managed")
	api := serveReliantAPI(t, testControlPlane)
	const userID = "user-cloud"
	pat := api.mintSession(t, userID, append([]fat.Scope{fat.ScopeReliantAPI}, daemonCeiling...)...)

	// Exactly the shape control-plane's daemonpat renders into the Secret,
	// mounted read-only.
	store := map[string]any{
		"origins": map[string]any{api.url: map[string]any{"_default": map[string]any{
			"pat": pat, "server_url": api.url, "registered_at": "2026-10-01T00:00:00Z",
		}}},
		"default_accounts": map[string]string{api.url: "_default"},
	}
	data, _ := json.Marshal(store)
	daemonFile := filepath.Join(home, ".reliant", "daemon.json")
	if err := os.WriteFile(daemonFile, data, 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(daemonFile, 0o600) })

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if _, err := resolveOrAwaitCredentials(context.Background(), cmd, &connection{ServerURL: api.url}, "",
		t.TempDir(), daemonNonInteractiveDefault()); err != nil {
		t.Fatalf("a managed daemon failed to start on its mounted credential: %v", err)
	}
	argv, err := cloudcred.ParseCommand(os.Getenv(cloudcred.HelperEnv))
	if err != nil || len(argv) < 3 {
		t.Fatalf("no helper exported on a cloud daemon: %v", err)
	}
	resp, _ := runHelper(t, testControlPlane, argv[3:]...)
	if resp.Error != nil || resp.Token == "" {
		t.Fatalf("a cloud daemon's agent could not get forge a credential: %+v", resp.Error)
	}
	if p, err := api.authority.Introspect(context.Background(), resp.Token); err != nil || p.Scopes.Has(fat.ScopeReliantAPI) {
		t.Fatalf("exchanged token must carry no session authority: %+v %v", p, err)
	}
}

// TestForgeCredential_ExecInterop crosses the real process boundary with
// forge's OWN client (cloudcred.Exec — what forge's credential resolution
// runs): this test binary is re-executed as `reliant auth forge-credential`.
func TestForgeCredential_ExecInterop(t *testing.T) {
	isolateForgeCredHome(t)
	api := serveReliantAPI(t, testControlPlane)
	loginFor(t, api.url, api.mintSession(t, "user-cli", append([]fat.Scope{fat.ScopeReliantAPI}, daemonCeiling[1:]...)...))
	t.Setenv(envServerURL, api.url)

	argv := []string{os.Args[0], "-test.run=^TestReliantAsHelperProcess$", "--", reliantHelperMarker, "auth", "forge-credential"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tok, err := cloudcred.Exec(ctx, argv, cloudcred.Request{Endpoint: testControlPlane, Scopes: []string{"deploy:write"}})
	if err != nil {
		t.Fatalf("forge's helper client could not get a token from reliant: %v", err)
	}
	if strings.Join(tok.Scopes, ",") != "deploy:write" {
		t.Errorf("scopes = %v, want exactly what forge asked for", tok.Scopes)
	}
	if !strings.Contains(tok.Source, "CLI login") {
		t.Errorf("source = %q", tok.Source)
	}
}

const reliantHelperMarker = "reliant-as-forge-credential-helper"

// TestReliantAsHelperProcess is not a test: re-executed by
// TestForgeCredential_ExecInterop, it runs the reliant command line after the
// marker and exits.
func TestReliantAsHelperProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == reliantHelperMarker {
			root := NewRootCmd()
			root.SetArgs(os.Args[i+2:])
			if err := root.Execute(); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

// TestForgeCredential_Refusals: every way of NOT getting a token says what to
// do — and under Reliant that is signing in to Reliant, never `forge login`.
func TestForgeCredential_Refusals(t *testing.T) {
	t.Run("not signed in at all", func(t *testing.T) {
		isolateForgeCredHome(t)
		resp, _ := runHelper(t, testControlPlane)
		if resp.Error == nil || resp.Error.Code != cloudcred.CodeNoSession ||
			!strings.Contains(resp.Error.Message, "sign in to Reliant (`reliant auth login` / the app)") {
			t.Fatalf("refusal = %+v", resp.Error)
		}
	})
	t.Run("a session without deploy permission is not escalated", func(t *testing.T) {
		isolateForgeCredHome(t)
		api := serveReliantAPI(t, testControlPlane)
		narrow := api.mintSession(t, "user-narrow", fat.ScopeDaemonConnect)
		if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{PAT: narrow, ServerURL: api.url}); err != nil {
			t.Fatal(err)
		}
		before := api.liveTokens(t, "user-narrow")
		resp, _ := runHelper(t, testControlPlane, "--daemon-server", api.url)
		if resp.Error == nil || resp.Error.Code != cloudcred.CodeDenied || !strings.Contains(resp.Error.Message, "reliant auth login") {
			t.Fatalf("refusal = %+v", resp.Error)
		}
		if strings.Contains(resp.Error.Message, "forge login") {
			t.Errorf("under Reliant the remedy is Reliant's sign-in: %s", resp.Error.Message)
		}
		if after := api.liveTokens(t, "user-narrow"); after != before {
			t.Error("a refused exchange minted a token")
		}
	})
	t.Run("a session for another control plane", func(t *testing.T) {
		isolateForgeCredHome(t)
		api := serveReliantAPI(t, "https://admin.other.example")
		pat := api.mintSession(t, "user-elsewhere", daemonCeiling...)
		if err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{PAT: pat, ServerURL: api.url}); err != nil {
			t.Fatal(err)
		}
		before := api.liveTokens(t, "user-elsewhere")
		resp, _ := runHelper(t, testControlPlane)
		if resp.Error == nil || resp.Error.Code != cloudcred.CodeNoSession || !strings.Contains(resp.Error.Message, "admin.other.example") {
			t.Fatalf("refusal = %+v", resp.Error)
		}
		if after := api.liveTokens(t, "user-elsewhere"); after != before {
			t.Error("asking a server for another control plane's token minted one")
		}
	})
}

// TestAuthLogin_AsksForForgeCeilingAndRetiresDeposits: `reliant auth login`
// asks for forge's authority as a ceiling (the control plane clips it), and no
// longer copies its token into forge's file — it removes any copy an older
// release left there.
func TestAuthLogin_AsksForForgeCeilingAndRetiresDeposits(t *testing.T) {
	credPath, _ := isolateForgeCredHome(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_ONELOGIN00000000")
	if err := credentials.Store(credPath, cp.srv.URL, "host-app", credentials.Credential{Token: "rlat_OLDDEPOSIT"}); err != nil {
		t.Fatal(err)
	}

	if out, err := runRoot(t, "auth", "login", "--server", cp.srv.URL); err != nil {
		t.Fatalf("auth login: %v\n%s", err, out)
	}
	scope := strings.Fields(cp.lastRequest().Get("scope"))
	for _, want := range append([]string{cliauth.ScopeAPI}, cliauth.ForgeScopes()...) {
		if !containsString(scope, want) {
			t.Errorf("login asked for %v, missing %s", scope, want)
		}
	}
	f, err := credentials.Load(credPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range f.Endpoints() {
		if _, err := f.Get(endpoint, "host-app"); err == nil {
			t.Errorf("a session token copy remains in forge's file at %s", endpoint)
		}
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestAuthLogout_ForgetsExchangedTokensKeepsForgeLogin: logging out of a
// server drops the tokens exchanged from its sessions — none outlives the
// sign-out on this machine — and leaves another server's, and a human's own
// `forge login`, alone.
func TestAuthLogout_ForgetsExchangedTokensKeepsForgeLogin(t *testing.T) {
	credPath, _ := isolateForgeCredHome(t)
	const server, other = "https://api.example.invalid", "https://api.other.invalid"
	loginFor(t, server, "rlat_LOGGEDIN")
	if err := credentials.Store(credPath, testControlPlane, "forge-cli", credentials.Credential{Token: "rlat_HUMAN"}); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour).UTC()
	seed, _ := json.Marshal(map[string]any{"version": 1, "entries": []map[string]any{
		{"endpoint": testControlPlane, "server": server, "subject": "a", "token": "rlat_X1", "expires_at": exp},
		{"endpoint": testControlPlane, "server": other, "subject": "b", "token": "rlat_X2", "expires_at": exp},
	}})
	if err := os.WriteFile(forgeTokenCachePath(), seed, 0o600); err != nil {
		t.Fatal(err)
	}

	if out, err := runRoot(t, "auth", "logout", "--server", server); err != nil {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	raw, _ := os.ReadFile(forgeTokenCachePath())
	if strings.Contains(string(raw), "rlat_X1") {
		t.Error("a token exchanged from the signed-out server's session survived logout")
	}
	if !strings.Contains(string(raw), "rlat_X2") {
		t.Error("logout of one server dropped another server's token")
	}
	if human, _ := credentials.Lookup(credPath, testControlPlane, "forge-cli"); human.Token != "rlat_HUMAN" {
		t.Error("`reliant auth logout` deleted the user's own `forge login`")
	}
}

// TestDaemonRegister_AsksForForgeCeiling: an interactive registration asks for
// daemon:connect plus forge's authority, so the new daemon's credential has
// something to exchange.
func TestDaemonRegister_AsksForForgeCeiling(t *testing.T) {
	isolateForgeCredHome(t)
	withFakeLoginBrowser(t)
	cp := newFakeCP(t, "rlat_NEWDAEMON000000000")
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := registerDaemon(context.Background(), cmd, &connection{ServerURL: cp.srv.URL}, "", false); err != nil {
		t.Fatalf("registerDaemon: %v", err)
	}
	scope := strings.Fields(cp.lastRequest().Get("scope"))
	for _, want := range append([]string{cliauth.ScopeDaemon}, cliauth.ForgeScopes()...) {
		if !containsString(scope, want) {
			t.Errorf("registration asked for %v, missing %s", scope, want)
		}
	}
	if containsString(scope, cliauth.ScopeAPI) {
		t.Error("a daemon credential must not become an API credential")
	}
}

// TestDaemon_NeverOverridesAUsersHelper: a helper the user configured for the
// daemon's environment stays theirs.
func TestDaemon_NeverOverridesAUsersHelper(t *testing.T) {
	isolateForgeCredHome(t)
	t.Setenv(cloudcred.HelperEnv, `["/usr/local/bin/my-helper"]`)
	daemonForgeHelper.Lock()
	daemonForgeHelper.value = ""
	daemonForgeHelper.Unlock()

	exportForgeCredentialHelper("https://api.example.invalid", "")
	if got := os.Getenv(cloudcred.HelperEnv); got != `["/usr/local/bin/my-helper"]` {
		t.Fatalf("the daemon replaced the user's helper with %q", got)
	}
}

// TestReliantForge_PointsForgeAtTheSession: `reliant forge …` sets the helper
// for its own process when nothing did, and only when a forge command runs.
func TestReliantForge_PointsForgeAtTheSession(t *testing.T) {
	isolateForgeCredHome(t)
	root := NewRootCmd()
	if os.Getenv(cloudcred.HelperEnv) != "" {
		t.Fatal("building the command tree must not set the helper (a daemon would inherit it unpinned)")
	}
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"forge", "version"})
	_ = root.Execute()
	argv, err := cloudcred.ParseCommand(os.Getenv(cloudcred.HelperEnv))
	if err != nil || len(argv) != 3 || argv[1] != "auth" || argv[2] != "forge-credential" {
		t.Fatalf("after `reliant forge`, %s = %q (%v), want this binary's unpinned helper", cloudcred.HelperEnv, os.Getenv(cloudcred.HelperEnv), err)
	}
}
