// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/handlers/terminal"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/vendorcli"
)

// fakeScripts records the scripts the command runs on this computer and
// answers the status script with status.
type fakeScripts struct {
	mu          sync.Mutex
	status      string
	statusRuns  []string
	interactive []string
	exitCode    int
}

func (f *fakeScripts) Output(_ context.Context, script string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusRuns = append(f.statusRuns, script)
	return f.status, nil
}

func (f *fakeScripts) Interactive(_ context.Context, script string, _ io.Reader, stdout, _ io.Writer) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interactive = append(f.interactive, script)
	_, _ = io.WriteString(stdout, "vendor prompt\n")
	return f.exitCode, nil
}

// fakeTerminal is a terminal with a fixed size that cannot change.
type fakeTerminal struct{ raw, restored int }

func (f *fakeTerminal) MakeRaw() (func(), error) {
	f.raw++
	return func() { f.restored++ }, nil
}
func (f *fakeTerminal) Size() (int, int)              { return 100, 30 }
func (f *fakeTerminal) OnResize(func()) (stop func()) { return func() {} }

// loginFixture isolates the CLI, gives this computer a daemon "d-local" (its
// data directory passed as --data-dir), and fakes the shell and the TTY.
type loginFixture struct {
	dataDir string
	scripts *fakeScripts
	term    *fakeTerminal
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	isolateCLI(t)
	t.Setenv(daemonpolicy.AgentShellEnv, "")
	t.Setenv("DAEMON_DATA_DIR", "")
	f := &loginFixture{dataDir: t.TempDir(), scripts: &fakeScripts{}, term: &fakeTerminal{}}
	require.NoError(t, bootstrap.WriteDaemonID(f.dataDir, "d-local"))

	prevScripts, prevTTY, prevTerm := localScripts, stdinIsTerminal, loginTerminal
	localScripts, stdinIsTerminal, loginTerminal = f.scripts, func() bool { return true }, f.term
	t.Cleanup(func() { localScripts, stdinIsTerminal, loginTerminal = prevScripts, prevTTY, prevTerm })
	return f
}

func (f *loginFixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return executeRoot(t, append(append([]string{"machine", "login"}, args...), "--data-dir", f.dataDir)...)
}

func TestMachineLogin_RefusedInAnAgentShellButStatusIsNot(t *testing.T) {
	f := newLoginFixture(t)
	f.scripts.status = "RELIANT_CLI\tclaude\tsigned_out\n"
	t.Setenv(daemonpolicy.AgentShellEnv, "1")

	_, err := f.run(t, "claude")
	require.ErrorIs(t, err, errAgentCannotSignInCLIs)
	assert.Empty(t, f.scripts.interactive, "a refused sign-in ran")

	out, err := f.run(t, "--status", "claude")
	require.NoError(t, err)
	assert.Contains(t, out, "Claude Code (claude)")
	assert.Contains(t, out, "signed out")
}

func TestMachineLogin_ArgumentErrors(t *testing.T) {
	f := newLoginFixture(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "name one vendor CLI"},
		{[]string{"claude", "codex"}, "name one vendor CLI"},
		{[]string{"bash"}, `unknown vendor CLI "bash": want one of claude, codex, gemini, cursor, copilot`},
		{[]string{"--status", "claude", "nope"}, `unknown vendor CLI "nope"`},
		{[]string{"claude", "--json"}, "--json goes with --status"},
	} {
		_, err := f.run(t, tc.args...)
		require.Error(t, err, tc.args)
		assert.Contains(t, err.Error(), tc.want, tc.args)
	}
	assert.Empty(t, f.scripts.interactive)
	assert.Empty(t, f.scripts.statusRuns)
}

func TestMachineLogin_NeedsATerminal(t *testing.T) {
	f := newLoginFixture(t)
	stdinIsTerminal = func() bool { return false }
	_, err := f.run(t, "claude")
	require.ErrorContains(t, err, "signing in is interactive")
	assert.Empty(t, f.scripts.interactive)
}

// On this computer's own daemon the vendor's recipe runs right here, and the
// verdict decides the exit.
func TestMachineLogin_LocalRunsTheRecipeHereThenChecks(t *testing.T) {
	f := newLoginFixture(t)
	f.scripts.status = "RELIANT_CLI\tcodex\tsigned_in\n"

	out, err := f.run(t, "codex")
	require.NoError(t, err)
	want, err := vendorcli.LoginScript("codex")
	require.NoError(t, err)
	require.Equal(t, []string{want}, f.scripts.interactive, "the vendorcli recipe, on this terminal")
	require.Equal(t, []string{vendorcli.StatusScript([]string{"codex"})}, f.scripts.statusRuns)
	assert.Contains(t, out, "vendor prompt")
	assert.Contains(t, out, "Codex (codex)")
	assert.Contains(t, out, "signed in")

	f.scripts.status = "RELIANT_CLI\tcodex\tsigned_out\n"
	_, err = f.run(t, "codex")
	require.ErrorContains(t, err, "Codex is signed out on d-local")
}

func TestMachineLogin_LocalStatusJSON(t *testing.T) {
	f := newLoginFixture(t)
	f.scripts.status = "RELIANT_CLI\tclaude\tsigned_in\nRELIANT_CLI\tgemini\tmissing\nRELIANT_CLI\tcursor\texpired\tlocal_bin\n"

	out, err := f.run(t, "--status", "--json", "claude", "gemini", "cursor", "copilot")
	require.NoError(t, err)
	var report struct {
		Machine struct{ ID string } `json:"machine"`
		Logins  []vendorLogin       `json:"logins"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &report), out)
	assert.Equal(t, "d-local", report.Machine.ID)
	assert.Equal(t, []vendorLogin{
		{Vendor: "claude", Name: "Claude Code", State: loginSignedIn},
		{Vendor: "gemini", Name: "Gemini CLI", State: loginNotInstalled},
		{Vendor: "cursor", Name: "Cursor Agent", State: loginExpired, OffPath: true},
		{Vendor: "copilot", Name: "GitHub Copilot CLI", State: loginUnknown},
	}, report.Logins)
}

// loginRegistry is the Reliant API: the registry (machines and the status
// rpc) and the sign-in websocket.
type loginRegistry struct {
	reliantv1connect.UnimplementedDaemonRegistryServiceHandler
	checked []string
	// wsFirst is the websocket's first frame; init carries on with output
	// and an exit.
	wsFirst map[string]any
	wsAuth  string
	wsQuery string
}

func (r *loginRegistry) ListDaemons(context.Context, *connect.Request[reliantv1.ListDaemonsRequest]) (*connect.Response[reliantv1.ListDaemonsResponse], error) {
	return connect.NewResponse(&reliantv1.ListDaemonsResponse{Daemons: []*reliantv1.DaemonInfo{
		{DaemonId: "d-local", Name: "laptop"},
		{DaemonId: "d-cloud", Name: "cloud-box", Pools: []string{"pipeline"}},
	}}), nil
}

func (r *loginRegistry) CheckMachineCLILogins(_ context.Context, req *connect.Request[reliantv1.CheckMachineCLILoginsRequest]) (*connect.Response[reliantv1.CheckMachineCLILoginsResponse], error) {
	r.checked = append(r.checked, req.Msg.GetDaemonId()+":"+strings.Join(req.Msg.GetVendors(), ","))
	return connect.NewResponse(&reliantv1.CheckMachineCLILoginsResponse{Logins: []*reliantv1.MachineCLILogin{
		{Vendor: "claude", Name: "Claude Code", State: reliantv1.CLILoginState_CLI_LOGIN_STATE_SIGNED_IN, OffPath: true},
	}}), nil
}

func (r *loginRegistry) serveWS(w http.ResponseWriter, req *http.Request) {
	r.wsAuth, r.wsQuery = req.Header.Get("Authorization"), req.URL.RawQuery
	conn, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.WriteJSON(r.wsFirst)
	if r.wsFirst["type"] != "init" {
		return
	}
	_ = conn.WriteJSON(map[string]any{"type": "output", "data": "Open https://example.test and enter ABCD\r\n"})
	_ = conn.WriteJSON(map[string]any{"type": "exit", "data": "Process exited with code 0"})
}

func startLoginRegistry(t *testing.T, r *loginRegistry) {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := reliantv1connect.NewDaemonRegistryServiceHandler(r)
	mux.Handle(path, handler)
	mux.HandleFunc(terminal.TerminalCLILoginWSPath, r.serveWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv(envServerURL, srv.URL)
	t.Setenv(envToken, "rlat_login")
}

// --machine naming this computer's own daemon still runs here.
func TestMachineLogin_MachineThatIsThisComputerRunsHere(t *testing.T) {
	f := newLoginFixture(t)
	reg := &loginRegistry{}
	startLoginRegistry(t, reg)
	f.scripts.status = "RELIANT_CLI\tclaude\tsigned_out\n"

	out, err := f.run(t, "--status", "--machine", "laptop")
	require.NoError(t, err)
	assert.Contains(t, out, "Agent CLIs on laptop")
	assert.Len(t, f.scripts.statusRuns, 1)
	assert.Empty(t, reg.checked, "this computer's status needs no server round trip")
}

func TestMachineLogin_RemoteStatusGoesThroughTheAPI(t *testing.T) {
	f := newLoginFixture(t)
	reg := &loginRegistry{}
	startLoginRegistry(t, reg)

	out, err := f.run(t, "--status", "claude", "--machine", "cloud-box")
	require.NoError(t, err)
	assert.Equal(t, []string{"d-cloud:claude"}, reg.checked)
	assert.Empty(t, f.scripts.statusRuns, "another machine's status is never run here")
	assert.Contains(t, out, "Claude Code (claude)")
	assert.Contains(t, out, "signed in")
	assert.Contains(t, out, "not on the PATH the machine's daemon gives run steps: update Reliant on cloud-box")
}

// Another machine's sign-in is a session through the API: the login rides
// the Authorization header (never the URL), the terminal is raw for the
// session and restored after, and the verdict is checked through the API.
func TestMachineLogin_RemoteSessionThenVerdict(t *testing.T) {
	f := newLoginFixture(t)
	reg := &loginRegistry{wsFirst: map[string]any{"type": "init", "session_id": "s1"}}
	startLoginRegistry(t, reg)

	out, err := f.run(t, "claude", "--machine", "cloud-box")
	require.NoError(t, err)
	assert.Equal(t, "Bearer rlat_login", reg.wsAuth)
	assert.NotContains(t, reg.wsQuery, "rlat_login")
	assert.Contains(t, reg.wsQuery, "daemonId=d-cloud")
	assert.Contains(t, reg.wsQuery, "vendor=claude")
	assert.Contains(t, reg.wsQuery, "cols=100")
	assert.Contains(t, out, "ABCD")
	assert.Contains(t, out, "machine pool pipeline", "a pooled machine is warned about")
	assert.Equal(t, 1, f.term.raw)
	assert.GreaterOrEqual(t, f.term.restored, 1, "the terminal must be restored")
	assert.Equal(t, []string{"d-cloud:claude"}, reg.checked)
	assert.Empty(t, f.scripts.interactive)
}

// An old daemon is told to update, by name: never a shell.
func TestMachineLogin_RemoteOldDaemonSaysUpdate(t *testing.T) {
	f := newLoginFixture(t)
	reg := &loginRegistry{wsFirst: map[string]any{"type": "error", "code": terminal.WSErrorDaemonOutdated, "machine_name": "cloud-box"}}
	startLoginRegistry(t, reg)

	_, err := f.run(t, "claude", "--machine", "cloud-box")
	require.ErrorContains(t, err, "machine cloud-box runs a Reliant too old for 'reliant machine login': update Reliant on it")
	assert.Equal(t, 0, f.term.raw, "the terminal is untouched when no session opened")
	assert.Empty(t, reg.checked)
}

func TestMachineLogin_NoLocalDaemonAsksForMachine(t *testing.T) {
	newLoginFixture(t)
	_, err := executeRoot(t, "machine", "login", "--status")
	require.ErrorContains(t, err, "no Reliant machine runs on this computer: name the machine with --machine")
}
