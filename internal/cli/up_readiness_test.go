package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestHostReadinessWaitsForDelayedStartup(t *testing.T) {
	var out bytes.Buffer
	probes := 0
	err := waitHostReadiness(t.Context(), "dev", nil, time.Second, time.Millisecond, &out, func() []hostReadyResult {
		probes++
		state := portReadyNobody
		if probes == 4 {
			state = portReadyOurs
		}
		return []hostReadyResult{{name: "api", port: 8080, state: state}}
	})
	if err != nil || probes != 4 {
		t.Fatalf("delayed startup: probes=%d, err=%v", probes, err)
	}
	if !strings.Contains(out.String(), "compilation + startup") || !strings.Contains(out.String(), "api :8080") {
		t.Fatalf("waiting should explain what is pending: %s", &out)
	}
}

func TestHostReadinessTimeoutDoesNotClaimTheProcessFailed(t *testing.T) {
	err := waitHostReadiness(t.Context(), "dev", nil, time.Millisecond, time.Hour, io.Discard, func() []hostReadyResult {
		return []hostReadyResult{{name: "api", port: 8080, state: portReadyNobody}}
	})
	if err == nil {
		t.Fatal("unbound port passed readiness")
	}
	for _, want := range []string{"timed out", "may still be starting", "--host-ready-timeout", "api.log"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "failed to bind") {
		t.Errorf("a timeout is not proof of a failed process: %v", err)
	}
}

func TestHostReadinessFailsImmediatelyOnObservedFailure(t *testing.T) {
	for _, tc := range []hostReadyResult{
		{name: "api", port: 8080, state: portReadyNobody, exited: "exit status 2"},
		{name: "api", port: 8080, state: portReadyNobody, exited: "exit status 0"},
		{name: "api", port: 8080, state: portReadyForeign, holderPID: 42, holderCmd: "other-server"},
	} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		err := waitHostReadiness(ctx, "dev", nil, time.Hour, time.Hour, io.Discard, func() []hostReadyResult { return []hostReadyResult{tc} })
		cancel()
		if err == nil || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "timed out") {
			t.Fatalf("failure must return before the timer: %v", err)
		}
		if tc.exited != "" && !strings.Contains(err.Error(), tc.exited) {
			t.Errorf("missing observed exit: %v", err)
		}
	}
}

func TestHostReadinessCancellationInterruptsPolling(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	err := waitHostReadiness(ctx, "dev", nil, time.Hour, time.Hour, io.Discard, func() []hostReadyResult {
		cancel()
		return []hostReadyResult{{name: "api", port: 8080, state: portReadyNobody}}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want cancellation", err)
	}
}

func TestHostReadinessTimeoutFlags(t *testing.T) {
	// One command owns the host-ready timeout now: `forge env up` absorbed
	// `forge env up`, so there is no second copy of this flag to keep in step.
	for _, newCmd := range []func() *cobra.Command{newEnvUpCmd} {
		cmd := newCmd()
		if d, err := cmd.Flags().GetDuration("host-ready-timeout"); err != nil || d < time.Minute {
			t.Fatalf("default does not allow cold builds: %s, %v", d, err)
		}
		if err := cmd.ParseFlags([]string{"--host-ready-timeout=5m"}); err != nil {
			t.Fatal(err)
		}
		if d, _ := cmd.Flags().GetDuration("host-ready-timeout"); d != 5*time.Minute {
			t.Fatalf("custom timeout = %s", d)
		}
		for _, value := range []string{"0s", "-1s"} {
			cmd = newCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			args := []string{"--host-ready-timeout=" + value}
			if cmd.Name() == "up" {
				args = append(args, "dev")
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "must be positive") {
				t.Fatalf("invalid timeout was not rejected before startup: %v", err)
			}
		}
	}
}
