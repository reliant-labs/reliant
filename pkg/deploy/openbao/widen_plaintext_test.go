package openbao

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// The SC7 rehearsal (defect D2). A roll-forward after a rollback, and a first
// adoption of a live store, run the pre-rollout `widen` Job while the store
// that answers on the component's address is still the previous, plaintext one.
// The Job dialled https:// against it, failed with "wrong version number" after
// WAIT_SECONDS, and, being pre-rollout, held the very rollout that would have
// replaced the store with one that speaks TLS.
//
// These tests run the real embedded converge.sh in widen mode against a store
// that is plaintext, one that is TLS, and one that is down, with a kubectl shim
// that answers the two questions the widen phase asks.

type storeRequests struct {
	mu   sync.Mutex
	list []string
}

func (r *storeRequests) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := req.Method + " " + req.URL.Path
	if req.Header.Get("X-Vault-Token") != "" {
		line += " [with token]"
	}
	r.list = append(r.list, line)
}

func (r *storeRequests) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.list, "\n")
}

func healthHandler(rec *storeRequests) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		rec.add(req)
		switch req.URL.Path {
		case "/v1/sys/health":
			_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"standby":false}`))
		default:
			http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
		}
	})
}

type widenRun struct {
	out  string
	code int
}

func runWiden(t *testing.T, addr, caFile string, extraEnv ...string) widenRun {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("converge.sh runs in the kubectl image's bash")
	}
	// The child gets a built environment, not the test process's: the library
	// reads no ambient variables (see internal/pkgguard), and a script run
	// against fixed inputs should not depend on the developer's shell either.
	// PATH is the shim dir, the directories the tools were found in, and the
	// system dirs for coreutils.
	pathDirs := []string{""}
	for _, tool := range []string{"bash", "curl", "jq"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not available", tool)
		}
		pathDirs = append(pathDirs, filepath.Dir(p))
	}
	pathDirs = append(pathDirs, "/usr/local/bin", "/usr/bin", "/bin")
	dir := t.TempDir()
	pathDirs[0] = dir
	script := filepath.Join(dir, "converge.sh")
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("converge.sh", ConvergeScript, 0o755)
	doc := write("access.json", `{"kv":{"mount":"secret"},"mounts":[],"policies":[],"roles":[]}`, 0o644)
	applied := write("applied.json", `{"kv":{"mount":"secret"},"mounts":[],"policies":[],"roles":[]}`, 0o644)
	token := write("token", "a.projected.token", 0o644)
	// A store that was already converged once has an applied-document ConfigMap
	// and no recovery Secret to fall back on.
	write("kubectl", `#!/bin/bash
case "$*" in
  *"get configmap"*"-o name"*) echo configmap/applied; exit 0 ;;
  *"get configmap"*jsonpath*) cat "$FAKE_APPLIED"; exit 0 ;;
  *"get secret"*) echo 'Error from server (NotFound): secrets "recovery" not found' >&2; exit 1 ;;
esac
exit 0
`, 0o755)
	if caFile == "" {
		caFile = write("ca.crt", "", 0o644)
	}

	cmd := exec.Command("bash", script)
	cmd.Env = append([]string{
		"PATH=" + strings.Join(pathDirs, string(os.PathListSeparator)),
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"FAKE_APPLIED=" + applied,
		"OPENBAO_PHASE=widen",
		"OPENBAO_ADDR=" + addr,
		"OPENBAO_CA_FILE=" + caFile,
		"OPENBAO_NAMESPACE=ns",
		"OPENBAO_RECOVERY_SECRET=recovery",
		"OPENBAO_APPLIED_CONFIGMAP=applied",
		"OPENBAO_SELF_MOUNT=jwt-self",
		"OPENBAO_CONVERGE_ROLE=openbao-converge",
		"OPENBAO_KV_MOUNT=secret",
		"OPENBAO_DOC_FILE=" + doc,
		"OPENBAO_TOKEN_FILE=" + token,
	}, extraEnv...)
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running converge.sh: %v", err)
	}
	return widenRun{out: string(out), code: code}
}

// someCA writes a valid PEM certificate the Job can load as its CA bundle.
func someCA(t *testing.T) string {
	t.Helper()
	tls := httptest.NewTLSServer(http.NotFoundHandler())
	defer tls.Close()
	p := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tls.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestWiden_SkipsAStoreThatStillAnswersInPlaintext is the rehearsal failure.
//
// The rule: widen exists to put a name a release adds in force before anything
// reads it, through THIS component's TLS listener. A store that answers in
// plaintext where the component's TLS listener belongs is the previous
// incarnation. It has none of the component's grants in force to widen (the
// first-install case, which already exits 0), nothing of the component's can
// read from it yet (a SecretStore dials the same https address and fails
// until the rollout lands), and the rollout that replaces it waits on this
// Job. Failing is a deadlock, so the Job skips; the converge phase applies the
// whole document after the rollout. And it must not fall back to authenticating
// over the plaintext listener: that would put the Job's service-account token
// on the wire, which is what a transitional listener is only excused from for
// existing clients.
//
// Mutation that fails it: delete the serves_plaintext branch from the widen
// phase of converge.sh: the Job then dies on the TLS handshake.
func TestWiden_SkipsAStoreThatStillAnswersInPlaintext(t *testing.T) {
	rec := &storeRequests{}
	srv := httptest.NewServer(healthHandler(rec))
	defer srv.Close()

	// A real CA, as the Job has: the failure must be the TLS handshake against
	// a plaintext listener ("wrong version number"), not a missing CA file.
	got := runWiden(t, "https://"+strings.TrimPrefix(srv.URL, "http://"), someCA(t))
	if got.code != 0 {
		t.Fatalf("widen against a plaintext (previous) store must exit 0, got %d:\n%s", got.code, got.out)
	}
	if !strings.Contains(got.out, "plaintext") || !strings.Contains(got.out, "Skipping") {
		t.Errorf("the skip must say why:\n%s", got.out)
	}
	for _, line := range strings.Split(rec.all(), "\n") {
		if strings.Contains(line, "with token") || strings.Contains(line, "login") || strings.HasPrefix(line, "POST") || strings.HasPrefix(line, "PUT") {
			t.Errorf("the skip must send no credential and write nothing; the store saw %q\nall requests:\n%s", line, rec.all())
		}
	}
}

// TestWiden_StillUsesTheTLSListenerWhenTheStoreHasOne pins the other half: a
// store that does speak TLS is widened as before, through a login on its TLS
// listener. The skip must be a property of "answers in plaintext", never of
// "anything went wrong".
func TestWiden_StillUsesTheTLSListenerWhenTheStoreHasOne(t *testing.T) {
	rec := &storeRequests{}
	srv := httptest.NewTLSServer(healthHandler(rec))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}

	got := runWiden(t, srv.URL, ca)
	if strings.Contains(got.out, "Skipping") {
		t.Fatalf("a TLS store must not be skipped:\n%s", got.out)
	}
	if !strings.Contains(rec.all(), "POST /v1/auth/jwt-self/login") {
		t.Errorf("widen must log in on the TLS listener; the store saw:\n%s\noutput:\n%s", rec.all(), got.out)
	}
	if got.code != exitRefused {
		t.Errorf("with the login refused and no recovery Secret the Job refuses (%d), got %d:\n%s", exitRefused, got.code, got.out)
	}
}

// TestWiden_AnUnreachableStoreStillFails: a store that answers nothing is not
// the previous incarnation, it is a store that is down or a wrong address, and
// that must still fail the Job.
func TestWiden_AnUnreachableStoreStillFails(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out the Job's poll interval; runs in task test")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := "https://" + strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	got := runWiden(t, addr, someCA(t), "OPENBAO_WAIT_SECONDS=1")
	if got.code == 0 {
		t.Fatalf("a store nothing answers on must fail widen:\n%s", got.out)
	}
	if strings.Contains(got.out, "Skipping") {
		t.Errorf("an unreachable store was mistaken for a plaintext one:\n%s", got.out)
	}
}
