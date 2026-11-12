package doctor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// App Health holds a service to the probes its workload DECLARES. An app
// forge did not write (adopted by its Dockerfile) declares a tcp probe and
// serves no /healthz, so judging it by forge's contract reported every
// healthy one as `healthz failed: status 404`.
func TestCheckAppHealthHonoursTheDeclaredProbe(t *testing.T) {
	// An app with no forge endpoints: 404 on everything but /up.
	mux := http.NewServeMux()
	mux.HandleFunc("/up", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("fine")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	check := func(p AppProbe) CheckResult {
		return CheckAppHealth(context.Background(), probedEnv(addr, p))
	}
	if got := check(AppProbe{}); got.Status != StatusFail {
		t.Errorf("forge's /healthz contract against an app without it = %q (%s), want fail", got.Status, got.Message)
	}
	if got := check(AppProbe{TCP: true}); got.Status != StatusPass || !strings.Contains(got.Message, "tcp probe") {
		t.Errorf("declared tcp probe = %q (%s), want pass naming the tcp probe", got.Status, got.Message)
	}
	if got := check(AppProbe{ReadinessPath: "/up", LivenessPath: "/up"}); got.Status != StatusPass {
		t.Errorf("declared /up probe = %q (%s), want pass (any 200; only forge's /healthz promises \"ok\")", got.Status, got.Message)
	}
	if got := check(AppProbe{ReadinessPath: "/up"}); got.Status != StatusFail || !strings.Contains(got.Message, "/healthz") {
		t.Errorf("readiness /up with liveness defaulted = %q (%s), want fail on the defaulted /healthz", got.Status, got.Message)
	}
	// A declared startup probe is checked first: while it fails the workload
	// has not started, even though readiness and liveness answer.
	if got := check(AppProbe{ReadinessPath: "/up", LivenessPath: "/up", StartupPath: "/started"}); got.Status != StatusFail || !strings.Contains(got.Message, "/started") {
		t.Errorf("failing startup probe = %q (%s), want fail naming /started", got.Status, got.Message)
	}
	if got := check(AppProbe{ReadinessPath: "/up", LivenessPath: "/up", StartupPath: "/up"}); got.Status != StatusPass || !strings.HasPrefix(got.Message, "app "+addr+": /up=200 /up=200 /up=200") {
		t.Errorf("answering startup probe = %q (%s), want pass checking all three", got.Status, got.Message)
	}
	// Startup alone is a declared probe: it is checked, and the unset
	// readiness and liveness fall back to forge's own paths.
	if got := check(AppProbe{StartupPath: "/up"}); got.Status != StatusFail || !strings.Contains(got.Message, "/healthz") {
		t.Errorf("startup-only /up = %q (%s), want fail on the defaulted /healthz after /up answered", got.Status, got.Message)
	}
	if got := check(AppProbe{StartupPath: "/started"}); got.Status != StatusFail || !strings.Contains(got.Message, "/started") {
		t.Errorf("startup-only failing = %q (%s), want fail naming /started", got.Status, got.Message)
	}

	// A tcp probe against a port nothing listens on fails, naming it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()
	got := CheckAppHealth(context.Background(), probedEnv(closed, AppProbe{TCP: true}))
	if got.Status != StatusFail || !strings.Contains(got.Message, "not accepting connections") {
		t.Errorf("tcp probe on a closed port = %q (%s), want fail", got.Status, got.Message)
	}
}

// probedEnv is the Environment RunRuntime builds for one resolved address.
func probedEnv(addr string, p AppProbe) *Environment {
	env := &Environment{Target: RuntimeTarget{Service: "app", HTTP: addr, Probe: p}}
	env.SetPort("app", 8080, addr)
	return env
}
