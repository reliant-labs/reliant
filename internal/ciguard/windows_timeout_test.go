package ciguard

import "testing"

// TestWindowsJobIsBounded keeps the Windows job from holding main's CI for
// GitHub's six-hour default. main's CI concurrency group never cancels in
// progress, so while one push run is stuck every later main CI run waits
// `pending` behind it and main-green cannot be recorded: on 2026-11-11 a hung
// Windows job (run 42972145234, 06:27Z) blocked every forge merge for four
// hours. A run takes 11-19 minutes.
func TestWindowsJobIsBounded(t *testing.T) {
	t.Parallel()
	const maxMinutes = 60
	wf := readWorkflow(t)
	j, ok := wf.Jobs["windows"]
	if !ok {
		t.Fatalf("%s has no windows job; update this guard if it was renamed", ciWorkflow)
	}
	if j.TimeoutMinutes <= 0 || j.TimeoutMinutes > maxMinutes {
		t.Fatalf("the windows job's timeout-minutes is %d; it must be set, between 1 and %d, "+
			"or a hang holds main's CI for GitHub's 6-hour default", j.TimeoutMinutes, maxMinutes)
	}
}
