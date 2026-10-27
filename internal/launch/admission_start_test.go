package launch

import (
	"context"
	"errors"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

func TestAdmissionAcceptedStartCallbackFailureRetriesSameIdentity(t *testing.T) {
	repo, ctx, project, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	attempts := 0
	bound := ""
	spec := Spec{OwnerUserID: launchTestUserID, ProjectID: project, Workflow: "builtin://agent", Params: mockModelParams(t), Messages: userSeed("review"), NewChatID: "admission-callback-root"}
	spec.OnStarted = func(_ context.Context, eventID, runID string) error {
		attempts++
		if attempts == 1 {
			return errors.New("database unavailable after accepted start")
		}
		bound = runID
		return nil
	}
	ev := Event{Kind: core.TriggerEventKindIntegration, DedupeKey: "admission-callback"}
	if _, err := launcher.Launch(ctx, ev, spec); err == nil {
		t.Fatal("callback failure hidden")
	}
	result, err := launcher.Launch(ctx, ev, spec)
	if err != nil {
		t.Fatal(err)
	}
	if bound != "run-admission-callback-root" || result.RunID != bound {
		t.Fatalf("wrong recovered execution: %q %+v", bound, result)
	}
	first, _ := starter.rootRun(t)
	if first.options.Memo["reliant_admission_event_id"] != result.EventID {
		t.Fatal("recovery memo does not identify immutable event")
	}
	for _, id := range starter.startedIDs() {
		if id != "admission-callback-root" {
			t.Fatal("retry created another root")
		}
	}
	if attempts != 2 {
		t.Fatalf("callback attempts %d", attempts)
	}
}
