package cloudcred

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
)

// The fake helper is this test binary, re-executed. The marker travels in
// argv rather than the environment: pkg/ never reads the environment, tests
// included (pkg/.golangci.yml), and argv is all a helper needs anyway.
const fakeHelperMarker = "cloudcred-fake-helper"

func fakeHelper(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestFakeHelperProcess$", "--", fakeHelperMarker, mode}
}

// TestFakeHelperProcess is not a test. Re-executed by fakeHelper, it plays a
// credential helper in the mode named after the marker; in an ordinary test
// run there is no marker and it returns at once.
func TestFakeHelperProcess(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--" && i+2 < len(os.Args) && os.Args[i+1] == fakeHelperMarker {
			mode = os.Args[i+2]
		}
	}
	if mode == "" {
		return
	}
	os.Exit(runFakeHelper(mode))
}

func runFakeHelper(mode string) int {
	ctx := context.Background()
	switch mode {
	case "echo":
		// Answers with a token that encodes what it was asked, so the test
		// can see the request crossed the boundary intact.
		err := Serve(ctx, os.Stdin, os.Stdout, func(_ context.Context, req Request) (Token, error) {
			exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
			return Token{
				Token:     "rlat_echo:" + req.Endpoint + ":" + strings.Join(req.Scopes, ","),
				ExpiresAt: &exp,
				Scopes:    req.Scopes,
				Source:    "fake session",
			}, nil
		})
		if err != nil {
			return 3
		}
		return 0
	case "denied":
		_ = Serve(ctx, os.Stdin, os.Stdout, func(context.Context, Request) (Token, error) {
			return Token{}, &HelperError{Code: CodeDenied, Message: "sign in to the host again"}
		})
		return 0
	case "no-session":
		_ = Serve(ctx, os.Stdin, os.Stdout, func(context.Context, Request) (Token, error) {
			return Token{}, &HelperError{Code: CodeNoSession, Message: "not signed in to the host"}
		})
		return 0
	case "garbage-with-secret":
		fmt.Fprint(os.Stdout, `{"version":1,"token":"rlat_SUPERSECRET"`) // truncated JSON
		return 0
	case "fail":
		fmt.Fprint(os.Stderr, "the host is on fire")
		return 7
	case "wrong-version":
		fmt.Fprint(os.Stdout, `{"version":99,"token":"rlat_x"}`)
		return 0
	case "hang":
		time.Sleep(time.Minute)
		return 0
	}
	return 9
}

func TestParseCommand(t *testing.T) {
	cases := []struct {
		in      string
		want    []string
		wantErr bool
	}{
		{in: "", want: nil},
		{in: "   ", want: nil},
		// A plain value is ONE executable, never split: this path has spaces.
		{in: "/Applications/My Host.app/bin/helper", want: []string{"/Applications/My Host.app/bin/helper"}},
		{in: `["/opt/host/bin/host","auth","forge-credential","--server","https://api.example.com"]`,
			want: []string{"/opt/host/bin/host", "auth", "forge-credential", "--server", "https://api.example.com"}},
		{in: `[]`, wantErr: true},
		{in: `[""]`, wantErr: true},
		{in: `["unterminated`, wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseCommand(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseCommand(%q) = %v, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseCommand(%q): %v", tc.in, err)
			continue
		}
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") || len(got) != len(tc.want) {
			t.Errorf("ParseCommand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatCommand_RoundTrips(t *testing.T) {
	argv := []string{`C:\Program Files\Host\host.exe`, "auth", "forge-credential", `--account`, `a "quoted" one`}
	got, err := ParseCommand(FormatCommand(argv))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\x00") != strings.Join(argv, "\x00") {
		t.Fatalf("round trip = %q, want %q", got, argv)
	}
}

func TestExec_ServeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tok, err := Exec(ctx, fakeHelper("echo"), Request{
		// Serve normalizes, so a sloppy spelling still reaches the helper as
		// the one key the host compares against.
		Endpoint: "HTTPS://Admin.Example.com:443/",
		Scopes:   []string{"deploy:read", "secret:write"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if want := "rlat_echo:https://admin.example.com:deploy:read,secret:write"; tok.Token != want {
		t.Errorf("token = %q, want %q", tok.Token, want)
	}
	if tok.Source != "fake session" {
		t.Errorf("source = %q", tok.Source)
	}
	if tok.ExpiresAt == nil || !tok.ExpiresAt.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("expires_at = %v", tok.ExpiresAt)
	}
	if strings.Join(tok.Scopes, ",") != "deploy:read,secret:write" {
		t.Errorf("scopes = %v", tok.Scopes)
	}
}

func TestExec_RefusalIsAHelperError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := Exec(ctx, fakeHelper("denied"), Request{Endpoint: "https://cp.example.com"})
	var he *HelperError
	if !errors.As(err, &he) {
		t.Fatalf("err = %v, want a *HelperError", err)
	}
	if he.Code != CodeDenied || he.Message != "sign in to the host again" {
		t.Errorf("refusal = %+v", he)
	}
	if errors.Is(err, ErrNoSession) {
		t.Error("a denial must not read as no session")
	}

	_, err = Exec(ctx, fakeHelper("no-session"), Request{Endpoint: "https://cp.example.com"})
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("err = %v, want ErrNoSession", err)
	}
	if !strings.Contains(err.Error(), "not signed in to the host") {
		t.Errorf("the host's own advice must be the message; got %q", err)
	}
}

// TestExec_MalformedStdoutNeverLeaks: a helper that dies mid-write may leave a
// token on stdout. The error forge prints must not carry it.
func TestExec_MalformedStdoutNeverLeaks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := Exec(ctx, fakeHelper("garbage-with-secret"), Request{Endpoint: "https://cp.example.com"})
	if err == nil {
		t.Fatal("a truncated response must be an error")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("the error leaked stdout: %v", err)
	}
}

func TestExec_FailureCarriesStderr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := Exec(ctx, fakeHelper("fail"), Request{Endpoint: "https://cp.example.com"})
	if err == nil || !strings.Contains(err.Error(), "the host is on fire") {
		t.Fatalf("err = %v, want the helper's stderr", err)
	}
	var he *HelperError
	if errors.As(err, &he) {
		t.Fatal("a crashed helper is not a refusal")
	}
}

func TestExec_RefusesAnotherProtocolVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := Exec(ctx, fakeHelper("wrong-version"), Request{Endpoint: "https://cp.example.com"})
	if err == nil || !strings.Contains(err.Error(), "version 99") {
		t.Fatalf("err = %v, want a version refusal", err)
	}
}

func TestExec_DeadlineBoundsAHungHelper(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Exec(ctx, fakeHelper("hang"), Request{Endpoint: "https://cp.example.com"})
	if err == nil {
		t.Fatal("a hung helper must fail")
	}
	if time.Since(start) > 20*time.Second {
		t.Fatalf("Exec waited %s for a helper past its deadline", time.Since(start))
	}
}

func TestServe_PlainErrorIsUnavailable(t *testing.T) {
	var out strings.Builder
	in := strings.NewReader(`{"version":1,"endpoint":"https://cp.example.com"}`)
	err := Serve(context.Background(), in, &out, func(context.Context, Request) (Token, error) {
		return Token{}, errors.New("control plane unreachable")
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeUnavailable || resp.Error.Message != "control plane unreachable" {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Token != "" {
		t.Fatal("a refusal carries no token")
	}
}

func TestServe_RefusesAnotherRequestVersion(t *testing.T) {
	var out strings.Builder
	called := false
	err := Serve(context.Background(), strings.NewReader(`{"version":2,"endpoint":"https://cp.example.com"}`), &out,
		func(context.Context, Request) (Token, error) { called = true; return Token{Token: "x"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("a request of another version must not reach mint")
	}
	if !strings.Contains(out.String(), "protocol version 2") {
		t.Fatalf("response = %s", out.String())
	}
}

func TestToken_Expired(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	soon := now.Add(30 * time.Second)
	later := now.Add(time.Hour)
	if (Token{}).Expired(now, time.Minute) {
		t.Error("a token with no expiry never expires")
	}
	if !(Token{ExpiresAt: &soon}).Expired(now, time.Minute) {
		t.Error("a token inside the margin counts as expired")
	}
	if (Token{ExpiresAt: &later}).Expired(now, time.Minute) {
		t.Error("a token well outside the margin is live")
	}
}

func TestRemoveLegacyHostDeposits(t *testing.T) {
	path := filepath.Join(t.TempDir(), credentials.FileName)
	created := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, e := range []struct{ endpoint, client string }{
		{"https://admin.example.com", legacyHostClientID},
		{"https://admin.example.com", "forge-cli"},
		{"http://localhost:8090", legacyHostClientID},
		{"https://api.example.com", "reliant-cli"},
	} {
		if err := credentials.Store(path, e.endpoint, e.client, credentials.Credential{Token: "t-" + e.client, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}

	n, err := RemoveLegacyHostDeposits(path)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed %d, want 2", n)
	}
	f, err := credentials.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range f.Endpoints() {
		if _, err := f.Get(endpoint, legacyHostClientID); err == nil {
			t.Errorf("a legacy deposit survived at %s", endpoint)
		}
	}
	if _, err := f.Get("https://admin.example.com", "forge-cli"); err != nil {
		t.Errorf("a human's forge login must survive: %v", err)
	}
	if _, err := f.Get("https://api.example.com", "reliant-cli"); err != nil {
		t.Errorf("another client's entry must survive: %v", err)
	}

	again, err := RemoveLegacyHostDeposits(path)
	if err != nil || again != 0 {
		t.Fatalf("second purge = %d, %v; want 0, nil", again, err)
	}
}

func TestRemoveLegacyHostDeposits_MissingFileIsANoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", credentials.FileName)
	n, err := RemoveLegacyHostDeposits(path)
	if err != nil || n != 0 {
		t.Fatalf("purge of a missing file = %d, %v; want 0, nil", n, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a purge with nothing to remove must not create the file (stat: %v)", err)
	}
}

// TestServeAndToken_CarryTheServersReason: a host's denial can say WHY in
// structure (scope_missing, and which scopes), and both halves of the
// protocol keep it, so forge's --json document can report it.
func TestServeAndToken_CarryTheServersReason(t *testing.T) {
	var out strings.Builder
	in := strings.NewReader(`{"version":1,"endpoint":"https://cp.example.com"}`)
	err := Serve(context.Background(), in, &out, func(context.Context, Request) (Token, error) {
		return Token{}, &HelperError{Code: CodeDenied, Message: "ask an admin",
			Reason: "scope_missing", Scopes: []string{"deploy:write", "secret:read"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatal(err)
	}
	_, err = resp.token("helper")
	var he *HelperError
	if !errors.As(err, &he) {
		t.Fatalf("err = %v, want a *HelperError", err)
	}
	if he.Code != CodeDenied || he.Message != "ask an admin" || he.Reason != "scope_missing" ||
		strings.Join(he.Scopes, ",") != "deploy:write,secret:read" {
		t.Fatalf("refusal = %+v", he)
	}
	if !errors.Is(err, &HelperError{Code: CodeDenied}) {
		t.Error("Is still matches on the code alone")
	}
}
