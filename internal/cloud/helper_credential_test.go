package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// The fake credential helper is this test binary, re-executed with a marker
// in argv. It speaks the real protocol (cloudcred.Serve), so these tests
// cover forge's half of the exchange end to end: the env var, the subprocess,
// the JSON on both pipes.
const cloudFakeHelperMarker = "forge-cloud-fake-helper"

func fakeHelperArgv(args ...string) []string {
	return append([]string{os.Args[0], "-test.run=^TestCloudFakeHelperProcess$", "--", cloudFakeHelperMarker}, args...)
}

// useFakeHelper points $FORGE_CREDENTIAL_HELPER at the fake for this test and
// forgets any token an earlier test's helper left in the process memo.
func useFakeHelper(t *testing.T, args ...string) {
	t.Helper()
	t.Setenv(cloudcred.HelperEnv, cloudcred.FormatCommand(fakeHelperArgv(args...)))
	resetHelperMemo()
	t.Cleanup(resetHelperMemo)
}

func resetHelperMemo() {
	helperMemo.Lock()
	defer helperMemo.Unlock()
	helperMemo.entries = map[string]Credential{}
}

// TestCloudFakeHelperProcess is not a test: re-executed by the tests below it
// plays the helper. In an ordinary run there is no marker and it returns.
func TestCloudFakeHelperProcess(t *testing.T) {
	var args []string
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) && os.Args[i+1] == cloudFakeHelperMarker {
			args = os.Args[i+2:]
		}
	}
	if len(args) == 0 {
		return
	}
	os.Exit(runCloudFakeHelper(args[0], args[1:]))
}

func runCloudFakeHelper(mode string, args []string) int {
	switch mode {
	case "mint": // mint <token> <ttl-seconds, 0 = none> [count-file]
		if len(args) >= 3 {
			f, err := os.OpenFile(args[2], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				return 4
			}
			_, _ = f.WriteString("called\n")
			_ = f.Close()
		}
		ttl, _ := strconv.Atoi(args[1])
		_ = cloudcred.Serve(context.Background(), os.Stdin, os.Stdout, func(_ context.Context, req cloudcred.Request) (cloudcred.Token, error) {
			tok := cloudcred.Token{Token: args[0], Scopes: req.Scopes, Source: "Fake Host session for " + req.Endpoint}
			if ttl > 0 {
				exp := time.Now().Add(time.Duration(ttl) * time.Second)
				tok.ExpiresAt = &exp
			}
			return tok, nil
		})
		return 0
	case "echo": // the token spells out the request it received
		_ = cloudcred.Serve(context.Background(), os.Stdin, os.Stdout, func(_ context.Context, req cloudcred.Request) (cloudcred.Token, error) {
			return cloudcred.Token{Token: req.Endpoint + "|" + strings.Join(req.Scopes, " ")}, nil
		})
		return 0
	case "refuse": // refuse <code> <message>
		_ = cloudcred.Serve(context.Background(), os.Stdin, os.Stdout, func(context.Context, cloudcred.Request) (cloudcred.Token, error) {
			return cloudcred.Token{}, &cloudcred.HelperError{Code: cloudcred.ErrorCode(args[0]), Message: args[1]}
		})
		return 0
	case "crash":
		fmt.Fprint(os.Stderr, "helper exploded")
		return 2
	}
	return 9
}

// TestResolveCredential_HelperIsTheLastSource is the feature: with no flag, no
// env var and no `forge login`, a configured helper — a host application the
// user is signed in to — supplies the credential. Before the helper existed
// this was ErrNoCredential, and the user had to log in a second time.
func TestResolveCredential_HelperIsTheLastSource(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	useFakeHelper(t, "mint", "rlat_from-helper", "3600")
	ep := endpointFor(t, "prod", "https://cp.example.com", "")

	got, err := ResolveCredential("", ep)
	if err != nil {
		t.Fatalf("a signed-in host must supply the credential: %v", err)
	}
	if got.Token != "rlat_from-helper" || got.Source != SourceHelper {
		t.Fatalf("got %+v, want the helper's token from SourceHelper", got)
	}
	if got.From != "Fake Host session for https://cp.example.com" {
		t.Errorf("From must be the helper's own description of its session; got %q", got.From)
	}
	if got.ExpiresAt == nil {
		t.Error("the helper's expiry must be carried")
	}
}

// TestResolveCredential_HelperIsAskedForTheEndpointAndForgeScopes: the
// request names the normalized endpoint — the helper must refuse one its
// session does not belong to — and the scopes forge's commands need.
func TestResolveCredential_HelperIsAskedForTheEndpointAndForgeScopes(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	useFakeHelper(t, "echo")

	got, err := ResolveCredential("", endpointFor(t, "prod", "https://CP.Example.com:443/", ""))
	if err != nil {
		t.Fatal(err)
	}
	want := "https://cp.example.com|" + strings.Join(LoginScopes(), " ")
	if got.Token != want {
		t.Fatalf("helper saw %q, want %q", got.Token, want)
	}
}

// TestResolveCredential_FullPrecedence has all four sources present at once:
// flag > env > `forge login` > helper. A human's deliberate login must beat a
// host signed in as someone else, and CI's variable must beat both.
func TestResolveCredential_FullPrecedence(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	useFakeHelper(t, "mint", "from-helper", "3600")
	ep := endpointFor(t, "prod", "https://cp.example.com", "ACME_DEPLOY_TOKEN")
	storeFor(t, ep.URL, "from-file")
	t.Setenv("ACME_DEPLOY_TOKEN", "from-env")

	steps := []struct {
		flag   string
		want   string
		source CredentialSource
		then   func()
	}{
		{"from-flag", "from-flag", SourceFlag, func() {}},
		{"", "from-env", SourceEnv, func() { t.Setenv("ACME_DEPLOY_TOKEN", "") }},
		{"", "from-file", SourceLogin, func() {
			path, _ := CredentialsPath()
			if _, err := credentials.Remove(path, ep.URL, ClientID); err != nil {
				t.Fatal(err)
			}
		}},
		{"", "from-helper", SourceHelper, func() {}},
	}
	for _, s := range steps {
		got, err := ResolveCredential(s.flag, ep)
		if err != nil || got.Token != s.want || got.Source != s.source {
			t.Fatalf("want %q from %s; got %+v %v", s.want, s.source, got, err)
		}
		s.then()
	}
}

// TestResolveCredential_ExpiredLoginFallsThroughToHelper: an expired `forge
// login` used to be a hard stop ("log in again"). With the user signed in to
// the host, that is a second login for nothing — the helper answers instead.
func TestResolveCredential_ExpiredLoginFallsThroughToHelper(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	useFakeHelper(t, "mint", "from-helper", "3600")
	ep := endpointFor(t, "prod", "https://cp.example.com", "")
	past := time.Now().Add(-time.Hour)
	path, _ := CredentialsPath()
	if err := credentials.Store(path, ep.URL, ClientID, credentials.Credential{Token: "old", ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveCredential("", ep)
	if err != nil || got.Token != "from-helper" {
		t.Fatalf("an expired login must fall through to the helper; got %+v %v", got, err)
	}
}

// TestResolveCredential_HelperRefusalIsTheHostsAdvice: when the host has no
// session that fits, its own message ("sign in to …") is the remedy. Under a
// host, `forge login` is the wrong advice and must not appear; CI's route
// (the env var) still must.
func TestResolveCredential_HelperRefusalIsTheHostsAdvice(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	const tokenEnv = "ACME_DEPLOY_TOKEN"
	t.Setenv(tokenEnv, "")
	useFakeHelper(t, "refuse", "no_session", "not signed in to Fake Host — sign in to Fake Host (`fakehost login` / the app)")
	ep := endpointFor(t, "prod", "https://cp.example.com", tokenEnv)

	_, err := ResolveCredential("", ep)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("want ErrNoCredential; got %v", err)
	}
	var refusal *cloudcred.HelperError
	if !errors.As(err, &refusal) || refusal.Code != cloudcred.CodeNoSession {
		t.Errorf("the helper's refusal must stay reachable for its code; got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"sign in to Fake Host (`fakehost login` / the app)", tokenEnv, ep.URL} {
		if !strings.Contains(msg, want) {
			t.Errorf("message must contain %q; got:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "forge login") {
		t.Errorf("under a host, the remedy is the host's sign-in, never `forge login`; got:\n%s", msg)
	}
}

// TestResolveCredential_LegacyHostDepositIsNeverPresented: the retired deposit
// seam copied a host's own session token into forge's file under "host-app",
// and forge presented it to deploy endpoints that — correctly — refused it. A
// leftover entry must never be sent again.
func TestResolveCredential_LegacyHostDepositIsNeverPresented(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	t.Setenv(cloudcred.HelperEnv, "")
	ep := endpointFor(t, "prod", "https://cp.example.com", "")
	path, _ := CredentialsPath()
	if err := credentials.Store(path, ep.URL, "host-app", credentials.Credential{Token: "rlat_daemon_session", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveCredential("", ep)
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("a legacy deposit must not resolve; got %+v %v", got, err)
	}
	if strings.Contains(err.Error(), "rlat_daemon_session") {
		t.Fatalf("the error leaked the deposited token:\n%s", err)
	}
}

// TestResolveCredential_HelperRunsOncePerProcess: a deploy resolves its
// credential in several places; the helper runs once and the memo answers the
// rest — until the token is about to expire, when the helper is asked again.
func TestResolveCredential_HelperRunsOncePerProcess(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	ep := endpointFor(t, "prod", "https://cp.example.com", "")

	calls := filepath.Join(t.TempDir(), "calls")
	useFakeHelper(t, "mint", "long-lived", "3600", calls)
	for i := 0; i < 3; i++ {
		if _, err := ResolveCredential("", ep); err != nil {
			t.Fatal(err)
		}
	}
	if n := countLines(t, calls); n != 1 {
		t.Fatalf("helper ran %d times for one endpoint in one process, want 1", n)
	}

	// A token inside the reuse margin is not reused.
	nearExpiry := filepath.Join(t.TempDir(), "calls")
	useFakeHelper(t, "mint", "short-lived", "30", nearExpiry)
	for i := 0; i < 2; i++ {
		if _, err := ResolveCredential("", ep); err != nil {
			t.Fatal(err)
		}
	}
	if n := countLines(t, nearExpiry); n != 2 {
		t.Fatalf("a token about to expire must be re-requested; helper ran %d times, want 2", n)
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "\n")
}

func TestResolveCredential_HelperCrashIsErrNoCredential(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	useFakeHelper(t, "crash")

	_, err := ResolveCredential("", endpointFor(t, "prod", "https://cp.example.com", ""))
	if !errors.Is(err, ErrNoCredential) {
		t.Fatalf("want ErrNoCredential; got %v", err)
	}
	for _, want := range []string{cloudcred.HelperEnv, "helper exploded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message must contain %q; got:\n%s", want, err)
		}
	}
}

func TestResolveCredential_MalformedHelperValue(t *testing.T) {
	t.Setenv("FORGE_HOME", t.TempDir())
	t.Setenv(DefaultTokenEnv, "")
	t.Setenv(cloudcred.HelperEnv, `["unterminated`)

	_, err := ResolveCredential("", endpointFor(t, "prod", "https://cp.example.com", ""))
	if !errors.Is(err, ErrNoCredential) || !strings.Contains(err.Error(), cloudcred.HelperEnv) {
		t.Fatalf("a malformed helper value must say which variable is wrong; got %v", err)
	}
}

// TestCall_HelperCredentialRejectionNeverSaysForgeLogin: a helper-minted token
// the control plane refuses is the host session's problem, so the remedy is
// the host — not a `forge login` the user was promised they would not need.
func TestCall_HelperCredentialRejectionNeverSaysForgeLogin(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"plain", map[string]any{"code": "unauthenticated", "message": "token revoked"}, "sign in again in the application that provided this credential (Fake Host session)"},
		{"missing scope", map[string]any{"code": "permission_denied", "message": "Promote rejected: token does not carry the deploy:write scope"},
			"the session it was minted from (Fake Host session) does not hold deploy:write"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := connectErrorServer(t, http.StatusForbidden, tc.body, "")
			ep, err := ResolveEndpoint("prod", &Declaration{Endpoint: srv.URL, TokenEnv: "ACME_TOKEN"})
			if err != nil {
				t.Fatal(err)
			}
			client := NewClient(ep, Credential{Token: "t", Source: SourceHelper, From: "Fake Host session"})
			got := client.Call(context.Background(), "controlplane.v1.DeployService/Promote", map[string]any{}, &struct{}{}).Error()
			if !strings.Contains(got, tc.want) || !strings.Contains(got, "ACME_TOKEN") {
				t.Errorf("hint missing %q / the CI variable:\n%s", tc.want, got)
			}
			if strings.Contains(got, "forge login") {
				t.Errorf("a helper-minted credential must never be told to `forge login`:\n%s", got)
			}
		})
	}
}
