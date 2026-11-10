// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"

	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/instanceid"
)

// executeRootSplit runs the CLI with stdout and stderr kept apart, for the
// commands whose stdout must carry only their result.
func executeRootSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := NewRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// deviceRequests returns what the fake control plane saw: the device
// authorization form (nil if none) and the number of browser logins.
func (cp *fakeCP) deviceRequests() (form map[string][]string, browserLogins int) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	return cp.deviceForm, len(cp.requests)
}

// TestAuthTokenCreate_DeviceFlagMintsTheRequestedToken: --device on a desktop
// asks for a device code with exactly the --name and --scope the browser
// flow would send, prints the code on stderr and the token alone on stdout,
// and leaves the CLI's own login alone. The browser is only a convenience
// opened on the approval URL, never a loopback login.
func TestAuthTokenCreate_DeviceFlagMintsTheRequestedToken(t *testing.T) {
	isolateCLI(t)
	presentDesktop(t)
	var opened []string
	prevSleep, prevOpen := deviceLoginSleep, loginOpener
	deviceLoginSleep = func(context.Context, time.Duration) error { return nil }
	loginOpener = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { deviceLoginSleep, loginOpener = prevSleep, prevOpen })

	cp := newFakeCP(t, "rlat_DEVICETOKEN00000")
	cp.offerDevice = true
	loginFor(t, cp.srv.URL, "rlat_MYLOGIN")
	path, _ := cliauth.CredentialsPath()

	stdout, stderr, err := executeRootSplit(t, "auth", "token", "create", "--device", "--name", "ci-deploy",
		"--scope", "runs:read,triggers:fire", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("token create --device: %v\n%s%s", err, stdout, stderr)
	}
	form, browserLogins := cp.deviceRequests()
	if got := form["client_id"]; len(got) != 1 || got[0] != cliauth.ClientID {
		t.Errorf("device request client_id = %v", got)
	}
	if got := form["scope"]; len(got) != 1 || got[0] != "runs:read triggers:fire" {
		t.Errorf("device request scope = %v, want exactly runs:read triggers:fire", got)
	}
	if got := form["device"]; len(got) != 1 || got[0] != "ci-deploy" {
		t.Errorf("device request name = %v, want ci-deploy", got)
	}
	if got := form["hostname"]; len(got) != 1 || got[0] != instanceid.Hostname() {
		t.Errorf("device request hostname = %v", got)
	}
	if browserLogins != 0 {
		t.Fatal("--device must not also start a browser login")
	}
	if len(opened) != 1 || !strings.Contains(opened[0], "user_code=BDFG-HJKL") {
		t.Errorf("on a desktop the approval URL is opened once as a convenience; opened %v", opened)
	}
	for _, want := range []string{"BDFG-HJKL", `"reliant-cli@ci-deploy"`, "does not sign this CLI in"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if !strings.Contains(stdout, "rlat_DEVICETOKEN00000") || !strings.Contains(stdout, "Scopes: runs:read triggers:fire") {
		t.Errorf("stdout must show the token and its scopes:\n%s", stdout)
	}
	if strings.Contains(stdout, "BDFG-HJKL") {
		t.Errorf("the device prompt must stay off stdout:\n%s", stdout)
	}
	if c, _ := credentials.Lookup(path, cp.srv.URL, cliauth.ClientID); c.Token != "rlat_MYLOGIN" {
		t.Fatalf("token create must not replace the CLI's own login; got %q", c.Token)
	}
}

// TestAuthTokenCreate_DeviceFlowWhereNoBrowserCanReach: with no flag, an ssh
// session gets the device flow, as `reliant auth login` does, and no browser
// is opened.
func TestAuthTokenCreate_DeviceFlowWhereNoBrowserCanReach(t *testing.T) {
	isolateCLI(t)
	t.Setenv("SSH_CONNECTION", "10.0.0.2 51000 10.0.0.1 22")
	withNoDeviceWait(t)
	cp := newFakeCP(t, "rlat_SSHTOKEN0000000")
	cp.offerDevice = true

	out, err := executeRoot(t, "auth", "token", "create", "--name", "ci", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("token create: %v\n%s", err, out)
	}
	form, browserLogins := cp.deviceRequests()
	if got := form["scope"]; len(got) != 1 || got[0] != cliauth.ScopeAPI {
		t.Errorf("device request scope = %v, want %s", got, cliauth.ScopeAPI)
	}
	if browserLogins != 0 {
		t.Fatal("an ssh session must not start a browser login")
	}
	for _, want := range []string{"ssh session", "BDFG-HJKL", "rlat_SSHTOKEN0000000"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestAuthTokenCreate_DeviceGrantIsHeldToTheRequest: a device approval that
// grants more than the --scope asked for is refused exactly as a browser
// one is, and the token is not printed.
func TestAuthTokenCreate_DeviceGrantIsHeldToTheRequest(t *testing.T) {
	isolateCLI(t)
	t.Setenv("RELIANT_DAEMON_TYPE", "managed")
	withNoDeviceWait(t)
	cp := newFakeCP(t, "rlat_DEVBROADER000000")
	cp.offerDevice = true
	cp.grantScope = "runs:read reliant:api"

	out, err := executeRoot(t, "auth", "token", "create", "--name", "ci", "--scope", "runs:read", "--server", cp.srv.URL)
	if err == nil || !strings.Contains(err.Error(), "also granted reliant:api") {
		t.Fatalf("want a refusal naming the extra scope; got %v\n%s", err, out)
	}
	if strings.Contains(out, "rlat_DEVBROADER") {
		t.Errorf("the broader token must not be printed:\n%s", out)
	}
}

// TestAuthTokenCreate_NoDeviceFlowWhereNoBrowserCanWork: a server without
// device sign-in, on a host no browser can reach, is a clear error rather
// than a browser login waiting for a callback that cannot arrive.
func TestAuthTokenCreate_NoDeviceFlowWhereNoBrowserCanWork(t *testing.T) {
	isolateCLI(t)
	t.Setenv("SSH_CONNECTION", "10.0.0.2 51000 10.0.0.1 22")
	withNoDeviceWait(t)
	cp := newFakeCP(t, "rlat_X")

	_, err := executeRoot(t, "auth", "token", "create", "--name", "ci", "--server", cp.srv.URL)
	if err == nil || !strings.Contains(err.Error(), "does not offer device-code sign-in") || !strings.Contains(err.Error(), "--browser") {
		t.Fatalf("want a clear no-device-sign-in error; got %v", err)
	}
	if _, browserLogins := cp.deviceRequests(); browserLogins != 0 {
		t.Fatal("fell back to a browser login on a host no browser can reach")
	}
}

// TestAuthTokenCreate_BrowserFlagOverridesTheEnvironment: --browser keeps the
// loopback flow even where the environment says no browser can reach it.
func TestAuthTokenCreate_BrowserFlagOverridesTheEnvironment(t *testing.T) {
	isolateCLI(t)
	withFakeLoginBrowser(t)
	t.Setenv("SSH_CONNECTION", "10.0.0.2 51000 10.0.0.1 22")
	cp := newFakeCP(t, "rlat_FORCEDBROWSER0000")
	cp.offerDevice = true

	out, err := executeRoot(t, "auth", "token", "create", "--browser", "--name", "ci", "--server", cp.srv.URL)
	if err != nil {
		t.Fatalf("token create --browser: %v\n%s", err, out)
	}
	if form, browserLogins := cp.deviceRequests(); form != nil || browserLogins != 1 {
		t.Fatalf("--browser must use the browser login only (device form %v, browser logins %d)", form, browserLogins)
	}
	if !strings.Contains(out, "rlat_FORCEDBROWSER0000") {
		t.Errorf("the token must be printed:\n%s", out)
	}
}

func TestAuthTokenCreate_DeviceAndBrowserAreExclusive(t *testing.T) {
	isolateCLI(t)
	if _, err := executeRoot(t, "auth", "token", "create", "--name", "ci", "--device", "--browser"); err == nil ||
		!strings.Contains(err.Error(), "none of the others can be") {
		t.Fatalf("--device and --browser together must be refused; got %v", err)
	}
}
