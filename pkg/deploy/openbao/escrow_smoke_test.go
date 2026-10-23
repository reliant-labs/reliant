package openbao

// The escrow sink, run for real: the embedded converge.sh and escrow.sh in the
// pinned kubectl image, against a real OpenBao 2.6.2 and a fake Google (the
// metadata server that hands out an access token, and the Secret Manager
// addVersion endpoint, as a Go server on the host).
//
// It settles what the unit tests cannot:
//
//   - the init output reaches the escrow, byte for byte, exactly once, after the
//     in-cluster recovery Secret exists and before anything is converged;
//   - a re-run does not add another version (the ledger answers "already
//     escrowed" with no Google call), and losing the ledger costs one identical
//     version, not an error;
//   - a refusal from Secret Manager fails the run BEFORE the store is converged,
//     and the next run, finding the store initialised and the escrow empty,
//     copies the existing recovery output and carries on (the design's
//     "already-initialised store whose escrow is empty" case);
//   - the script prints no token and no recovery material.
//
// Gated by -short (it boots containers).

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const fakeAccessToken = "ya29.fake-workload-identity-access-token"

// fakeGoogle is the metadata server and Secret Manager's addVersion, on one port.
type fakeGoogle struct {
	mu        sync.Mutex
	srv       *httptest.Server
	project   string
	secret    string
	exists    bool
	refuse    int // when non-zero, addVersion answers this status
	versions  [][]byte
	tokenHits int
	badAuth   int
}

func newFakeGoogle(t *testing.T, project, secret string) *fakeGoogle {
	t.Helper()
	g := &fakeGoogle{project: project, secret: secret, exists: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/computeMetadata/v1/instance/service-accounts/default/token", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
			return
		}
		g.tokenHits++
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fakeAccessToken, "expires_in": 3599, "token_type": "Bearer"})
	})
	mux.HandleFunc("/v1/projects/", func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		want := fmt.Sprintf("/v1/projects/%s/secrets/%s:addVersion", g.project, g.secret)
		if r.Method != http.MethodPost || r.URL.Path != want {
			// A principal holding versions.add only: anything else is refused.
			g.reply(w, http.StatusForbidden, "PERMISSION_DENIED", "Permission denied on "+r.URL.Path)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+fakeAccessToken {
			g.badAuth++
			g.reply(w, http.StatusUnauthorized, "UNAUTHENTICATED", "no valid credential")
			return
		}
		if g.refuse != 0 {
			g.reply(w, g.refuse, "PERMISSION_DENIED", "Permission 'secretmanager.versions.add' denied")
			return
		}
		if !g.exists {
			g.reply(w, http.StatusNotFound, "NOT_FOUND", "Secret not found")
			return
		}
		var body struct {
			Payload struct {
				Data []byte `json:"data"`
			} `json:"payload"`
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil || len(body.Payload.Data) == 0 {
			g.reply(w, http.StatusBadRequest, "INVALID_ARGUMENT", "payload.data is required")
			return
		}
		g.versions = append(g.versions, body.Payload.Data)
		_ = json.NewEncoder(w).Encode(map[string]any{"name": fmt.Sprintf("projects/123/secrets/%s/versions/%d", g.secret, len(g.versions)), "state": "ENABLED"})
	})
	l, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	g.srv = httptest.NewUnstartedServer(mux)
	g.srv.Listener = l
	g.srv.Start()
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGoogle) reply(w http.ResponseWriter, code int, status, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "status": status, "message": msg}})
}

func (g *fakeGoogle) port() int { return g.srv.Listener.Addr().(*net.TCPAddr).Port }

func (g *fakeGoogle) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.versions)
}

func (g *fakeGoogle) version(n int) []byte {
	g.mu.Lock()
	defer g.mu.Unlock()
	if n < 1 || n > len(g.versions) {
		return nil
	}
	return g.versions[n-1]
}

func (g *fakeGoogle) set(f func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f()
}

func TestSmoke_EscrowSinkAgainstARealOpenBao(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: boots OpenBao and the converge image; runs in task test")
	}
	if exec.Command("docker", "version").Run() != nil {
		t.Skip("docker is not available")
	}
	s := newSmoke(t)

	spec := s.spec
	spec.Bootstrap = &Bootstrap{
		Project: "acme-prod", ReaderServiceAccount: "openbao-bootstrap-reader",
		ReaderGSA:    "openbao-bootstrap-reader@acme-prod.iam.gserviceaccount.com",
		EscrowGSA:    "openbao-escrow-writer@acme-prod.iam.gserviceaccount.com",
		ESONamespace: "external-secrets", SealSecretID: "openbao-unseal-prod", EscrowSecretID: "openbao-recovery-prod",
	}
	s.setSpec(spec)

	// The hook the Job mounts, exactly as rendered.
	hookDir := filepath.Join(s.dir, "escrow")
	mustMkdir(t, hookDir)
	objs, err := Render(s.spec)
	if err != nil {
		t.Fatal(err)
	}
	var hook bool
	for _, o := range objs {
		if o.GetKind() == "ConfigMap" && o.GetName() == EscrowConfigMap(spec.Name) {
			data, _ := o.Object["data"].(map[string]any)
			mustWrite(t, filepath.Join(hookDir, "escrow.sh"), data["escrow.sh"].(string), 0o755)
			hook = true
		}
	}
	if !hook {
		t.Fatal("the escrow hook ConfigMap was not rendered")
	}

	g := newFakeGoogle(t, "acme-prod", "openbao-recovery-prod")
	base := fmt.Sprintf("http://host.docker.internal:%d", g.port())
	s.env["OPENBAO_ESCROW_GSM_BASE"] = base
	s.env["OPENBAO_ESCROW_METADATA_URL"] = base
	s.dockerArgs = []string{"--add-host", "host.docker.internal:host-gateway", "-v", hookDir + ":/escrow:ro"}

	s.start()
	out := s.converge(PhaseWiden, 0)
	mustContain(t, out, "first install")

	// ---- 1. Secret Manager refuses: the run fails AFTER the recovery Secret is
	// written and BEFORE anything is converged.
	g.set(func() { g.refuse = http.StatusForbidden })
	out = s.converge(PhaseConverge, 1)
	mustContain(t, out, "persisted to Secret openbao-recovery")
	mustContain(t, out, "Secret Manager refused")
	mustContain(t, out, "roles/secretmanager.secretVersionAdder")
	mustContain(t, out, "the escrow hook failed")
	for _, bad := range []string{"enabling auth mount", "applied ", "recorded the applied access document"} {
		if strings.Contains(out, bad) {
			t.Errorf("a store whose recovery output is not escrowed outside the cluster was converged (%q):\n%s", bad, out)
		}
	}
	if g.count() != 0 {
		t.Fatalf("a refused add stored %d version(s)", g.count())
	}
	if s.stateFile("openbao", "configmap-openbao-escrow-ledger", "escrowed-secret-uid") != "" {
		t.Error("the ledger claims an escrow that did not happen")
	}
	// The kubectl shim stores a Secret's data as the base64 the API carries; the
	// recovery output itself is what that decodes to.
	recB64 := s.stateFile("openbao", "secret-openbao-recovery", "init.json")
	if recB64 == "" {
		t.Fatal("the recovery Secret must exist: the init output may not be lost")
	}
	recRaw, err := base64.StdEncoding.DecodeString(recB64)
	if err != nil {
		t.Fatalf("the recovery Secret does not hold base64: %v", err)
	}
	rec := string(recRaw)

	// ---- 2. The store is initialised and the escrow is empty: the next run
	// copies the EXISTING recovery output, then converges.
	g.set(func() { g.refuse = 0 })
	out = s.converge(PhaseConverge, 0)
	if strings.Contains(out, "initialising") {
		t.Errorf("a re-run re-initialised the store:\n%s", out)
	}
	mustContain(t, out, "the escrow was empty: the init output is now version 1")
	if g.count() != 1 {
		t.Fatalf("want exactly one escrowed version, got %d", g.count())
	}
	if got := string(g.version(1)); got != rec {
		t.Errorf("the escrowed version is not the recovery Secret's init output, byte for byte: escrowed %d bytes, stored %d bytes (equal after trimming: %v)",
			len(got), len(rec), strings.TrimSpace(got) == strings.TrimSpace(rec))
	}
	var init struct {
		RootToken string   `json:"root_token"`
		Recovery  []string `json:"recovery_keys"`
	}
	if err := json.Unmarshal(g.version(1), &init); err != nil || init.RootToken == "" || len(init.Recovery) != 5 {
		t.Fatalf("the escrowed payload is not the init output: %v", err)
	}
	if i, a := strings.Index(out, "the escrow was empty"), strings.Index(out, "applied "); !(i >= 0 && a > i) {
		t.Errorf("the escrow must precede the converge:\n%s", out)
	}
	uid := s.stateFile("openbao", "secret-openbao-recovery", ".uid")
	if got := s.stateFile("openbao", "configmap-openbao-escrow-ledger", "escrowed-secret-uid"); got == "" || got+"\n" != uid+"\n" && got != strings.TrimSpace(uid) {
		t.Errorf("the ledger records %q, want the recovery Secret's uid %q", got, strings.TrimSpace(uid))
	}

	// ---- 3. A re-run asks Google nothing and adds nothing.
	hits := func() int { g.mu.Lock(); defer g.mu.Unlock(); return g.tokenHits }
	before := hits()
	out = s.converge(PhaseConverge, 0)
	mustContain(t, out, "already escrowed")
	if g.count() != 1 || hits() != before {
		t.Errorf("a re-run called Google (versions %d, token fetches %d -> %d)", g.count(), before, hits())
	}

	// ---- 4. Losing the ledger costs one identical version, never an error.
	if err := os.RemoveAll(filepath.Join(s.stateDi, "openbao", "configmap-openbao-escrow-ledger")); err != nil {
		t.Fatal(err)
	}
	out = s.converge(PhaseConverge, 0)
	mustContain(t, out, "added version 2")
	if g.count() != 2 || string(g.version(2)) != rec {
		t.Errorf("a lost ledger should add one identical version, got %d", g.count())
	}

	// ---- 5. A container that does not exist is named, not retried blindly.
	_ = os.RemoveAll(filepath.Join(s.stateDi, "openbao", "configmap-openbao-escrow-ledger"))
	g.set(func() { g.exists = false })
	out = s.converge(PhaseConverge, 1)
	mustContain(t, out, "does not exist: create the container first")

	// ---- 6. Nothing the run printed carries a token or recovery material.
	for _, secret := range append([]string{fakeAccessToken, init.RootToken}, init.Recovery...) {
		if strings.Contains(out, secret) {
			t.Errorf("the escrow printed a credential or recovery key")
		}
	}
}
