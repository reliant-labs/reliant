//go:build e2e

package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/pgtest"
)

// TestE2ENativeSignInSessionAuthenticatesRPCs is the gate for the half of
// native sign-in that happens AFTER the sign-in form: the RPCs.
//
// The broker answers /auth/login with an HttpOnly session cookie, which is
// the browser's only credential — no script can read it, so nothing can copy
// it into an Authorization header. The scaffolded auth interceptor read ONLY
// that header, so a fresh project signed in successfully and then answered
// every RPC with 401 "missing Authorization header". The browser half and
// the server half each passed their own tests; only a call that carries the
// cookie through the real interceptor shows whether they agree.
//
// So this walks a fresh project to a booted server and calls a generated,
// auth-gated RPC three ways: with the session cookie (must pass), with
// nothing (must 401 — the cookie channel must not open the door), and with
// another app's cookie (must 401).
func TestE2ENativeSignInSessionAuthenticatesRPCs(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "cookieapp", "--mod", "example.com/cookieapp", "--service", "widgets")
	projectDir := filepath.Join(dir, "cookieapp")
	addCorpusForgePkgReplace(t, projectDir)

	protoPath := filepath.Join(projectDir, "proto", "services", "widgets", "v1", "widgets.proto")
	proto := readFileE2E(t, protoPath) + `
// forge:entity
message Widget {
  string id = 1;
  string name = 2;
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author widgets proto: %v", err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	port := freePortE2E(t)
	serverBin := filepath.Join(projectDir, "cookie-server")
	buildCorpusServer(t, projectDir, serverBin)

	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("provision postgres: %v", err)
	}
	defer cleanup()
	applyProjectMigrationsPostgres(t, projectDir, dsn)

	pubPEM, bearer := mintDevJWT(t)
	token := strings.TrimPrefix(bearer, "Bearer ")

	cmd := exec.Command(serverBin, "server")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DATABASE_URL="+dsn,
		"ENVIRONMENT=development",
		"JWT_SECRET="+pubPEM,
	)
	var serverOut strings.Builder
	cmd.Stdout = &serverOut
	cmd.Stderr = &serverOut
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
		}
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitForServing(t, base, 30*time.Second) {
		t.Fatalf("server did not become ready\nserver output:\n%s", serverOut.String())
	}

	list := func(cookie string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+"/services.widgets.v1.WidgetsService/ListWidgets", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("ListWidgets: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, body := list("cookieapp_session=" + token); code != http.StatusOK {
		t.Errorf("an RPC carrying the native sign-in session cookie = HTTP %d, want 200 — the browser is signed in and every call still fails:\n%s\nserver output:\n%s",
			code, body, serverOut.String())
	}
	if code, body := list(""); code != http.StatusUnauthorized {
		t.Errorf("an RPC with no credential = HTTP %d, want 401:\n%s", code, body)
	}
	if code, body := list("otherapp_session=" + token); code != http.StatusUnauthorized {
		t.Errorf("another app's session cookie authenticated this API (HTTP %d, want 401):\n%s", code, body)
	}
}
