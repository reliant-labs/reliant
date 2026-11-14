package doctor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The split, pinned at the check sets.
//
// `forge doctor` straddled "is this project well-formed" and "is some
// environment running". The second half is not answerable without a resolved
// address, and doctor had none — it guessed :8080. Every runtime check now
// lives in the set `forge env status <env>` runs, and NONE of them may leak
// back into the project set: the moment one does, doctor takes an --env
// again and starts reporting on an environment nobody named.
func TestProjectChecksCarryNoRuntimeChecks(t *testing.T) {
	runtimeNames := map[string]bool{}
	for _, c := range runtimeSignals()[""] {
		runtimeNames[c.name] = true
	}
	if len(runtimeNames) == 0 {
		t.Fatal("the runtime check set is empty — the env-runtime question has no home")
	}

	for _, c := range projectChecks() {
		if runtimeNames[c.name] {
			t.Errorf("project check %q is an env-runtime check — it needs a resolved "+
				"address doctor does not have; it belongs in runtimeSignals()", c.name)
		}
	}

	// The runtime set must actually contain the checks that moved.
	for _, want := range []string{
		composeCheckName, "App Health", "pprof",
		"Metrics (ClickStack)", "Traces (ClickStack)", "Logs (ClickStack)", "Delve",
	} {
		if !runtimeNames[want] {
			t.Errorf("runtime check set is missing %q", want)
		}
	}

	// And the project set must keep the checks that stayed.
	projectNames := map[string]bool{}
	for _, c := range projectChecks() {
		projectNames[c.name] = true
	}
	for _, want := range []string{
		"Deploy Manifests", "Deploy Probes", "Deploy Resources", "Deploy Secrets",
		"Deploy SA Binding", "Deploy Migrations", "Payload Limits", "Disowned Files",
	} {
		if !projectNames[want] {
			t.Errorf("project check set is missing %q", want)
		}
	}
}

// UNDETERMINED IS NOT A PASS, and it must not render as "not applicable".
//
// Both used to be StatusSkip, so `App Health  app port 8080 not discovered`
// printed the same gray dash as `tool: mkcert  not required for this
// project`. One of those is a hole in the report.
func TestUndeterminedIsDistinctFromNotApplicableAndNotAPass(t *testing.T) {
	if statusIcon(StatusUnknown) == statusIcon(StatusSkip) {
		t.Errorf("StatusUnknown and StatusSkip render identically (%q) — "+
			"a check that could not determine its answer is indistinguishable from one that does not apply",
			statusIcon(StatusSkip))
	}

	d := newDoctor("p", t.TempDir())
	d.register("undetermined", func(context.Context, *Environment) CheckResult {
		return CheckResult{Status: StatusUnknown, Message: "could not look"}
	})
	d.register("fine", func(context.Context, *Environment) CheckResult {
		return CheckResult{Status: StatusPass, Message: "ok"}
	})
	report := d.run(context.Background(), nil)
	if report.Overall == StatusPass {
		t.Error("a report containing an UNDETERMINED check rolled up to pass")
	}
	if report.Overall != StatusWarn {
		t.Errorf("Overall = %q, want %q (undetermined is not a failure either)", report.Overall, StatusWarn)
	}

	var buf bytes.Buffer
	printReport(&buf, report, false)
	if !strings.Contains(buf.String(), "UNDETERMINED") {
		t.Errorf("the trailer does not name the undetermined check; it reads as an ordinary warning:\n%s", buf.String())
	}
}

// App Health with no resolved address is UNDETERMINED, never a skip.
func TestCheckAppHealthWithoutAnAddressIsUndetermined(t *testing.T) {
	got := CheckAppHealth(context.Background(), &Environment{ProjectDir: t.TempDir()})
	if got.Status != StatusUnknown {
		t.Errorf("status = %q, want %q — %q reads as \"nothing to check here\"", got.Status, StatusUnknown, got.Status)
	}
}

// The payoff of the split: the app check probes the address the CALLER
// resolved, on whatever port the stack actually bound.
//
// Doctor used to look up "app:8080" from `docker compose port app 8080`, so
// a project serving on :3091 got `– App Health  app port 8080 not
// discovered` — a miss that rendered as a dash. RunRuntime takes the address
// as input and never guesses.
func TestRunRuntimeProbesTheCallerResolvedAddress(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	d := New(Deps{})
	report, err := d.RunRuntime(context.Background(), RuntimeInput{
		ProjectName: "demo",
		ProjectDir:  t.TempDir(), // no compose file: the infra check must SKIP, not fail
		Env:         "dev",
		Target:      RuntimeTarget{Service: "reliant-api-server", HTTP: addr},
		Signal:      "app",
	})
	if err != nil {
		t.Fatalf("RunRuntime: %v", err)
	}

	var app, infra *CheckResult
	for i := range report.Checks {
		switch report.Checks[i].Name {
		case "App Health":
			app = &report.Checks[i]
		case composeCheckName:
			infra = &report.Checks[i]
		}
	}
	if app == nil {
		t.Fatalf("no App Health check in the report: %+v", report.Checks)
	}
	if app.Status != StatusPass {
		t.Errorf("App Health = %q (%s), want pass against the resolved address %s",
			app.Status, app.Message, addr)
	}
	// The result must name WHICH service answered — a project runs several.
	if !strings.Contains(app.Message, "reliant-api-server") {
		t.Errorf("App Health message does not name the service probed: %q", app.Message)
	}
	if infra == nil {
		t.Fatal("no compose-infra check in the report")
	}
	if infra.Status != StatusSkip {
		t.Errorf("compose infra = %q (%s), want skip — a project with no compose file "+
			"declares no compose stack, and doctor used to FAIL a healthy host-mode stack for it",
			infra.Status, infra.Message)
	}
}

// A REMOTE-ONLY env (every workload on a cluster / hosted) has no subject for
// the machine-local checks. Measured on control-plane's GKE prod: with a
// compose file in the project, `forge env status prod` reported the
// developer's own dev compose stack as "Compose Infra 9/9 healthy" and told
// the reader to `forge env up` prod. Every machine-local check must SKIP and
// say why; the project's compose file must not be consulted at all.
func TestRunRuntimeSkipsMachineLocalChecksForARemoteOnlyEnv(t *testing.T) {
	dir := t.TempDir()
	// A compose file that WOULD be inspected: the skip must not depend on
	// its absence.
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yml"), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	report, err := New(Deps{}).RunRuntime(context.Background(), RuntimeInput{
		ProjectName: "demo", ProjectDir: dir, Env: "prod",
		Target: RuntimeTarget{RemoteOnly: true},
	})
	if err != nil {
		t.Fatalf("RunRuntime: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range report.Checks {
		seen[c.Name] = true
		if !machineLocalChecks[c.Name] {
			continue
		}
		if c.Status != StatusSkip || !strings.Contains(c.Message, `env "prod" runs nothing on this machine`) {
			t.Errorf("%s = %q (%s), want a skip naming the remote-only env", c.Name, c.Status, c.Message)
		}
	}
	for name := range machineLocalChecks {
		if !seen[name] {
			t.Errorf("machine-local check %q missing from the report: a skipped check must still be listed", name)
		}
	}
	if !seen[clusterWorkloadsCheckName] {
		t.Errorf("Cluster Workloads must still run for a remote-only env: %+v", report.Checks)
	}
}

// A mistyped runtime signal is a usage error, not an empty report.
func TestRunRuntimeRejectsUnknownSignals(t *testing.T) {
	d := New(Deps{})
	if _, err := d.RunRuntime(context.Background(), RuntimeInput{Signal: "bogus"}); err == nil {
		t.Error("RunRuntime accepted an unknown signal")
	}
}
