//go:build e2e

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// echoProtoRPC replaces the scaffolded service's empty body with one public
// rpc the test can call without a token.
const echoProtoRPC = `  rpc Echo(EchoRequest) returns (EchoResponse) {
    option (forge.v1.method) = {auth_required: false};
  }
}

message EchoRequest {
  string text = 1;
}

message EchoResponse {
  string text = 1;
}`

const echoHandler = `package tasks

import (
	"context"

	"connectrpc.com/connect"

	pb "example.com/dbgdemo/gen/services/tasks/v1"
)

// Echo implements the Echo RPC.
func (s *Service) Echo(
	ctx context.Context,
	req *connect.Request[pb.EchoRequest],
) (*connect.Response[pb.EchoResponse], error) {
	reply := "echo: " + req.Msg.GetText()
	return connect.NewResponse(&pb.EchoResponse{Text: reply}), nil
}
`

// TestE2EDebugEnvUpAttachAndStart is the owner-visible `forge debug` flow
// against a scaffolded project brought up by `forge env up dev`:
//
//   - attach to the running api (air-built, so -trimpath'd): break on an rpc
//     by short name and by file:line, send a request, `continue --timeout`
//     returns the hit, `eval` reads the request and a local, and `stop` with
//     both breakpoints still set leaves the api ALIVE and serving — its next
//     requests pass over both breakpoint addresses. (The old stop left the
//     int3s behind and the next request killed the api with SIGTRAP.)
//   - `start api` builds its own debug binary and runs it with env dev's
//     configuration (it would die without its database_url), on --app-port
//     because env up holds the configured port; break, request, eval, and
//     `stop` kills only that process.
func TestE2EDebugEnvUpAttachAndStart(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	if runtime.GOOS == "windows" {
		t.Skip("attach/signal model is unix-only")
	}
	requireTool(t, "go", "dlv")
	bin := buildforgeBinary(t)
	root := t.TempDir()

	run := func(dir string, timeout time.Duration, args ...string) (string, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(dir, 5*time.Minute, args...)
		if err != nil {
			t.Fatalf("forge %v: %v\n%s", args, err, out)
		}
		return out
	}
	mustContain := func(out, want, what string) {
		t.Helper()
		if !strings.Contains(out, want) {
			t.Fatalf("%s: output lacks %q:\n%s", what, want, out)
		}
	}

	must(root, "project", "new", "dbgdemo", "--mod", "example.com/dbgdemo", "--service", "tasks", "--skip-tools")
	project := filepath.Join(root, "dbgdemo")
	protoPath := filepath.Join(project, "proto", "services", "tasks", "v1", "tasks.proto")
	proto := readFileE2E(t, protoPath)
	if !strings.Contains(proto, "  // TODO: Add your RPC methods here.\n}") {
		t.Fatalf("scaffolded proto changed shape:\n%s", proto)
	}
	writeFileE2E(t, protoPath, strings.Replace(proto, "  // TODO: Add your RPC methods here.\n}", echoProtoRPC, 1))
	must(project, "scaffold", "rpc", "tasks", "Echo")
	handlerPath := filepath.Join(project, "internal", "handlers", "tasks", "rpc_echo.go")
	writeFileE2E(t, handlerPath, echoHandler)
	returnLine := 0
	for i, line := range strings.Split(echoHandler, "\n") {
		if strings.Contains(line, "return connect.NewResponse") {
			returnLine = i + 1
		}
	}

	t.Cleanup(func() {
		// Whatever the test left: a debug session first (it may hold the api
		// halted), then the stack.
		_, _ = run(project, time.Minute, "debug", "stop")
		if out, err := run(project, 2*time.Minute, "env", "down", "dev"); err != nil {
			t.Errorf("cleanup own dev stack: %v\n%s", err, out)
		}
	})
	// --target api: the api (and the database and migrations it needs) as a
	// host process under air, without the local observability containers.
	must(project, "env", "up", "dev", "--background", "--no-seed", "--target", "api")

	// The api's port and pid, as `forge env status` reports them.
	var status struct {
		Runtime struct {
			Services []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
				PID  int    `json:"pid"`
			} `json:"services"`
		} `json:"runtime"`
	}
	statusOut := must(project, "env", "status", "dev", "--json")
	if err := json.Unmarshal([]byte(statusOut[strings.Index(statusOut, "{"):]), &status); err != nil {
		t.Fatalf("env status --json: %v\n%s", err, statusOut)
	}
	apiPort, apiPID := 0, 0
	for _, s := range status.Runtime.Services {
		if s.Name == "api" {
			apiPort, apiPID = s.Port, s.PID
		}
	}
	if apiPort == 0 || apiPID == 0 {
		t.Fatalf("env status reports no api port/pid:\n%s", statusOut)
	}

	echo := func(port int, text string, timeout time.Duration) (string, error) {
		client := &http.Client{Timeout: timeout}
		resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/services.tasks.v1.TasksService/Echo", port),
			"application/json", bytes.NewBufferString(`{"text":"`+text+`"}`))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("status %d: %s", resp.StatusCode, body)
		}
		return string(body), err
	}
	waitEcho := func(port int) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for {
			body, err := echo(port, "ready", 5*time.Second)
			if err == nil && strings.Contains(body, "echo: ready") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("Echo on :%d never answered: %v %s", port, err, body)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	type result struct {
		body string
		err  error
	}
	echoAsync := func(port int, text string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			body, err := echo(port, text, 2*time.Minute)
			ch <- result{body, err}
		}()
		return ch
	}
	waitEcho(apiPort)

	// --- attach to env up's api -------------------------------------------
	out := must(project, "debug", "start", "--attach", strconv.Itoa(apiPID), "--run")
	mustContain(out, "Target is RUNNING", "attach")
	waitEcho(apiPort) // attached, and still serving

	mustContain(must(project, "debug", "break", "Echo"), "internal/handlers/tasks.(*Service).Echo", "break by short name")
	mustContain(must(project, "debug", "break", fmt.Sprintf("internal/handlers/tasks/rpc_echo.go:%d", returnLine)), fmt.Sprintf("rpc_echo.go:%d", returnLine), "break by file:line on a -trimpath binary")

	pending := echoAsync(apiPort, "dbg")
	out = must(project, "debug", "continue", "--timeout", "90s")
	mustContain(out, "Reason: breakpoint", "continue to the rpc entry")
	mustContain(out, "Echo", "continue to the rpc entry")
	mustContain(must(project, "debug", "eval", "req.Msg.Text"), "dbg", "eval request field")
	out = must(project, "debug", "continue", "--timeout", "90s")
	mustContain(out, fmt.Sprintf("rpc_echo.go:%d", returnLine), "continue to the line breakpoint")
	mustContain(must(project, "debug", "eval", "reply"), "echo: dbg", "eval local")

	// Stop with both breakpoints set and the api halted on one.
	mustContain(must(project, "debug", "stop"), "detached", "stop an attach session")
	select {
	case r := <-pending:
		if r.err != nil || !strings.Contains(r.body, "echo: dbg") {
			t.Fatalf("the request held at the breakpoint did not complete after stop: %v %s", r.err, r.body)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the request held at the breakpoint never completed after stop")
	}
	for i := range 3 {
		if body, err := echo(apiPort, "after"+strconv.Itoa(i), 10*time.Second); err != nil || !strings.Contains(body, "echo: after") {
			t.Fatalf("REGRESSION: api stopped serving after `debug stop` (request %d): %v %s", i, err, body)
		}
	}
	if p, err := os.FindProcess(apiPID); err != nil || p.Signal(syscall.Signal(0)) != nil {
		t.Fatalf("REGRESSION: `debug stop` of an attach session killed the api (pid %d)", apiPID)
	}

	// --- start a debug build with env dev's configuration ----------------
	appPort := freePortE2E(t)
	out = must(project, "debug", "start", "api", "--run", "--app-port", strconv.Itoa(appPort))
	mustContain(out, `workload "api" of env "dev"`, "start resolves env config")
	mustContain(out, "Target is RUNNING", "start")
	mustContain(out, filepath.Join(".forge", "logs", "dev", "debug_api.log"), "start names its program log")
	waitEcho(appPort)
	must(project, "debug", "break", "Echo")
	pending = echoAsync(appPort, "own")
	mustContain(must(project, "debug", "continue", "--timeout", "90s"), "Reason: breakpoint", "continue on the started service")
	mustContain(must(project, "debug", "eval", "req.Msg.Text"), "own", "eval on the started service")
	mustContain(must(project, "debug", "stop"), "killed", "stop a started session")
	select {
	case <-pending:
	case <-time.After(30 * time.Second):
		t.Fatal("request to the started service still pending after stop")
	}
	if _, err := echo(appPort, "gone", 3*time.Second); err == nil {
		t.Fatalf("the started service still serves on :%d after stop", appPort)
	}
	if body, err := echo(apiPort, "still", 10*time.Second); err != nil || !strings.Contains(body, "echo: still") {
		t.Fatalf("stopping the started service disturbed env up's api: %v %s", err, body)
	}
}
