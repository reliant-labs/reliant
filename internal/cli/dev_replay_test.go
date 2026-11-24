package cli

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

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/devtelemetry"
)

type replayMintCaller struct {
	ack      *bool
	requests []map[string]any
}

func (f *replayMintCaller) Call(_ context.Context, proc string, req, out any) error {
	if proc != procMintLocalTelemetryToken {
		return fmt.Errorf("unexpected procedure %s", proc)
	}
	b, _ := json.Marshal(req)
	var body map[string]any
	if err := json.Unmarshal(b, &body); err != nil {
		return err
	}
	f.requests = append(f.requests, body)
	response := map[string]any{"processToken": "p", "browserToken": "b", "expiresAt": devTelNow.Add(time.Hour).Format(time.RFC3339), "endpoint": "https://otel.example", "instance": map[string]string{"id": "instance"}}
	if f.ack != nil {
		response["sessionReplay"] = *f.ack
	}
	b, _ = json.Marshal(response)
	return json.Unmarshal(b, out)
}

func TestDevReplayRequiresRequestedAndAcknowledged(t *testing.T) {
	for _, request := range []bool{false, true} {
		for _, ack := range []*bool{nil, boolPtr(false), boolPtr(true)} {
			name := fmt.Sprintf("request=%v/ack=%v", request, ack)
			t.Run(name, func(t *testing.T) {
				caller := &replayMintCaller{ack: ack}
				g, err := mintDevTelemetry(t.Context(), caller, "s", "shop", "dev", nil, devTelNow, request)
				if err != nil {
					t.Fatal(err)
				}
				want := request && ack != nil && *ack
				if g.SessionReplay != want {
					t.Fatalf("grant=%v want=%v", g.SessionReplay, want)
				}
				if caller.requests[0]["sessionReplay"] != request {
					t.Fatal("request lost explicit opt-out")
				}
				declared := map[string]string{"OTEL_REPLAY": "true", "APP_NAME": "shop"}
				e := &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Type: "vite", RuntimeConfig: declared}}}
				applyHostedDevTelemetry(e, t.TempDir(), g, nil)
				got := e.Frontends[0].RuntimeConfig["OTEL_REPLAY"]
				if (got == "true") != want {
					t.Fatalf("capture=%q grant=%v", got, want)
				}
				if declared["OTEL_REPLAY"] != "true" || declared["APP_NAME"] != "shop" {
					t.Fatal("owned declaration mutated")
				}
				var notice strings.Builder
				printDevTelemetryNotice(&notice, "dev", g)
				if strings.Contains(notice.String(), "masked session replay") != want {
					t.Fatal("notice misrepresents grant")
				}
			})
		}
	}
}

func boolPtr(v bool) *bool { return &v }

func TestDevReplayRenewalAndKeeperPreserveExplicitChoice(t *testing.T) {
	for _, replay := range []bool{false, true} {
		caller := &replayMintCaller{ack: boolPtr(true)}
		r := &devTelemetryRun{projectDir: t.TempDir(), env: "dev", project: "shop", sessionID: "s", client: caller, sessionReplay: replay}
		if err := r.renewNow(t.Context(), devTelNow); err != nil {
			t.Fatal(err)
		}
		if caller.requests[0]["sessionReplay"] != replay || r.grant.SessionReplay != replay {
			t.Fatal("renewal changed replay choice")
		}
		spec := r.keeperSpec()
		b, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		var roundtrip devTelemetryKeeperSpec
		if err := json.Unmarshal(b, &roundtrip); err != nil {
			t.Fatal(err)
		}
		if roundtrip.SessionReplay != replay {
			t.Fatal("detached keeper lost replay choice")
		}
	}
}

func TestDevReplayRenewalLossWithdrawsFutureCaptureWithoutLateEnable(t *testing.T) {
	dir := t.TempDir()
	fe := FrontendEntity{Name: "web", Type: "vite", Path: "frontends/web"}
	cfg := runtimeDocInventory(&config.ProjectConfig{}, &KCLEntities{Frontends: []FrontendEntity{fe}})
	body := codegen.GenerateFrontendConfigJS("web", "dev", `{"OTEL_REPLAY":"true","API_URL":"/api","CUSTOM":"kept"}`)
	if _, err := writeFrontendRuntimeDocs(cfg, dir, map[string]string{"web": body}); err != nil {
		t.Fatal(err)
	}
	caller := &replayMintCaller{ack: boolPtr(false)}
	var log strings.Builder
	r := &devTelemetryRun{projectDir: dir, env: "dev", project: "shop", sessionID: "s", client: caller, sessionReplay: true, frontends: []FrontendEntity{fe}, grant: devTelemetryGrant{SessionReplay: true}, log: &log}
	if err := r.renewNow(t.Context(), devTelNow); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "frontends/web/public/config.js")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"OTEL_REPLAY": "false"`) || !strings.Contains(string(b), `"CUSTOM": "kept"`) {
		t.Fatalf("downgrade config: %s", b)
	}
	if !strings.Contains(log.String(), "reload/restart") {
		t.Fatal("missing accurate capture notice")
	}
	caller.ack = boolPtr(true)
	if err := r.renewNow(t.Context(), devTelNow); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(b) {
		t.Fatal("late ack silently enabled capture")
	}
}

func TestDevReplayWithdrawalFailureKeepsCommittedGrant(t *testing.T) {
	prev := stopReplayFrontends
	var stopped []string
	stopReplayFrontends = func(_ string, _ string, names []string) error { stopped = names; return nil }
	t.Cleanup(func() { stopReplayFrontends = prev })
	dir := t.TempDir()
	path := filepath.Join(dir, "frontends/web/public/config.js")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	} // deterministic read/write failure, even as root
	caller := &replayMintCaller{ack: boolPtr(false)}
	var log strings.Builder
	old := devTelemetryGrant{SessionReplay: true, mintedAt: devTelNow.Add(-time.Hour), expires: devTelNow.Add(time.Minute)}
	r := &devTelemetryRun{projectDir: dir, env: "dev", project: "shop", sessionID: "s", client: caller, sessionReplay: true, frontends: []FrontendEntity{{Name: "web", Type: "vite", Path: "frontends/web"}}, grant: old, log: &log}
	err := r.renewNow(t.Context(), devTelNow)
	if err == nil {
		t.Fatal("withdrawal failure must not claim capture disabled")
	}
	if r.grant.SessionReplay || !r.grant.expires.Equal(devTelNow.Add(time.Hour)) {
		t.Fatalf("new token persisted but memory holds old grant: %+v", r.grant)
	}
	st, ok, readErr := devtelemetry.ReadState(dir, "dev")
	if readErr != nil || !ok {
		t.Fatalf("state: %v %v", ok, readErr)
	}
	if !st.ExpiresAt.Equal(r.grant.expires) {
		t.Fatal("persisted state rolled back to old expiry")
	}
	if !strings.Contains(st.RenewError, "capture") || strings.Contains(st.RenewError, "unreachable") {
		t.Fatalf("misclassified committed renewal: %q", st.RenewError)
	}
	if !strings.Contains(log.String(), "cannot confirm capture is off") {
		t.Fatalf("inaccurate warning: %s", log.String())
	}
	if !errors.Is(err, errDevReplayWithdrawal) {
		t.Fatalf("must be terminal withdrawal error, not retry mint: %v", err)
	}
	if strings.Join(stopped, ",") != "frontend:web" {
		t.Fatalf("failed policy must stop only this stack frontend: %v", stopped)
	}
	spec := r.keeperSpec()
	if spec.GrantedReplay || !spec.ExpiresAt.Equal(r.grant.expires) {
		t.Fatal("restart snapshot retained old grant")
	}
	if caller.requests[0]["sessionReplay"] != true {
		t.Fatal("renewal changed owned capture intent")
	}
}

func TestDevReplayKeeperStopsAfterCommittedWithdrawalFailure(t *testing.T) {
	prev := stopReplayFrontends
	stopReplayFrontends = func(string, string, []string) error { return nil }
	t.Cleanup(func() { stopReplayFrontends = prev })
	dir := t.TempDir()
	path := filepath.Join(dir, "frontends/web/public/config.js")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	spec := keeperSpecForTest(dir)
	spec.SessionReplay = true
	spec.GrantedReplay = true
	spec.Frontends = []FrontendEntity{{Name: "web", Type: "vite", Path: "frontends/web"}}
	clock := newFakeDevClock(devTelNow)
	caller := &fakeMintCaller{clock: clock.Now, minted: make(chan struct{}, 1)} // absent ack denies replay
	done := make(chan struct{})
	go func() {
		defer close(done)
		runDevTelemetryKeeper(t.Context(), spec, caller, &fakeSessionReporter{}, time.Hour, clock, &strings.Builder{})
	}()
	recv(t, "renewal timer", clock.armed)
	clock.Advance(30 * time.Minute)
	recv(t, "mint committed", caller.minted)
	recv(t, "keeper exits instead of retrying mint", done)
	if caller.count() != 1 {
		t.Fatalf("mint retried after commit: %d", caller.count())
	}
	st, ok, err := devtelemetry.ReadState(dir, "dev")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if !st.ExpiresAt.Equal(devTelNow.Add(90 * time.Minute)) {
		t.Fatalf("wrong committed expiry: %v", st.ExpiresAt)
	}
}

func TestDevReplayLocalDecisionDoesNotDependOnNotice(t *testing.T) {
	for _, cp := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			e := &KCLEntities{Observability: &ObservabilityEntity{Enabled: enabled, Signals: ObservabilitySignalsEntity{Browser: true, Replay: true}}, Frontends: []FrontendEntity{{Name: "web", Type: "vite", RuntimeConfig: map[string]string{"OTEL_REPLAY": "true"}}}}
			var log strings.Builder
			printLocalReplayAvailability(e, devTelemetryFacts{ControlPlane: cp, TelemetryOn: enabled, SessionReplay: enabled}, devTelemetryMode{Value: devTelemetryLocal}, "local", &log)
			if e.Frontends[0].RuntimeConfig["OTEL_REPLAY"] != "false" {
				t.Fatalf("no collector but capture remains on cp=%v enabled=%v", cp, enabled)
			}
		}
	}
}

func TestDevReplayNeedsEnabledBrowserPolicy(t *testing.T) {
	e := &KCLEntities{Observability: &ObservabilityEntity{Enabled: true, Signals: ObservabilitySignalsEntity{Browser: true, Replay: true}}}
	if !devReplayRequested(e) {
		t.Fatal("explicit opt-in not requested")
	}
	e.Observability.Signals.Browser = false
	if devReplayRequested(e) {
		t.Fatal("browser opt-out ignored")
	}
	e.Observability.Signals.Browser = true
	e.Observability.Signals.Replay = false
	if devReplayRequested(e) {
		t.Fatal("replay opt-out ignored")
	}
	e.Observability.Signals.Replay = true
	e.Observability.Enabled = false
	if devReplayRequested(e) {
		t.Fatal("all-off ignored")
	}
	if devReplayRequested(nil) || devReplayRequested(&KCLEntities{}) {
		t.Fatal("legacy env opted in")
	}
}
