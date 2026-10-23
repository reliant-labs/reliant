package serverkit_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// TestRun_AmbientEnvironmentCannotGateOperators pins the single gate on the
// controller manager: the caller populated Operators, so the manager runs.
//
// serverkit used to consult a RUN_OPERATORS environment variable as a second
// gate over that decision. Two processes with the same binary and the same
// config then supervised different components depending on the shell that
// started them, and the one place the difference was visible — an env var
// declared in no config proto — is the one place nobody looks when a
// reconcile silently stops happening. Which components a process supervises
// is a composition-root decision: the command that must not run a manager is
// the command that populates no operators.
//
// The assertion is behavioural rather than a lookup of the removed helper: an
// operator whose manager returns an error takes the process down, so a Run
// that keeps serving is a Run that never started the manager at all.
func TestRun_AmbientEnvironmentCannotGateOperators(t *testing.T) {
	// Not parallel: mutates the process environment.
	for _, v := range []string{"false", "0", "no", "off"} {
		t.Setenv("RUN_OPERATORS", v)
		addr := freeAddr(t)
		opErr := errors.New("manager started anyway")
		srv := serverkit.Server{
			Handler:   emptyHandler(),
			Operators: []serverkit.Operator{&stubOperator{name: "op"}},
			RunOperators: func(context.Context, *slog.Logger, string) error {
				return opErr
			},
		}
		errCh, _ := runInBackground(t, baseConfig(addr), srv)
		select {
		case err := <-errCh:
			if err == nil || !contains(err.Error(), "manager started anyway") {
				t.Fatalf("RUN_OPERATORS=%s: Run should return the operator error, got %v", v, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("RUN_OPERATORS=%s in the environment suppressed the controller manager the "+
				"caller populated — nothing outside the composition root may decide which "+
				"components a process supervises", v)
		}
	}
}
