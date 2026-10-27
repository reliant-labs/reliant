package triggers

import (
	"context"
	"strings"
	"testing"
)

func TestScheduledConcurrencyNeverReachesLauncher(t *testing.T) {
	repo := newFakeRepo()
	trig := scheduleActivation(t)
	repo.triggers[trig.ID] = trig
	launcher := &fakeLauncher{}
	wf := strings.Replace(declaringWorkflow, "name:", "automation_only: true\nconcurrency: {max_running: 6, key: \"'pr'\", supersede: latest}\nname:", 1)
	_, _ = NewFirer(repo, launcher).WithWorkflows(fakeWorkflows{yaml: map[string]string{"triage": wf}}).Fire(context.Background(), fireReq(trig.ID))
	if len(launcher.snapshot()) != 0 {
		t.Fatal("scheduled concurrency bypassed queue")
	}
}
