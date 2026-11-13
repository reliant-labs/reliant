package project

import (
	"testing"
	"time"
)

// SetGitCheckBudget shrinks GetProject's daemon git-check budget for one
// test. The test must not run in parallel: the budget is package state.
func SetGitCheckBudget(t *testing.T, d time.Duration) {
	t.Helper()
	prev := gitCheckBudget
	gitCheckBudget = d
	t.Cleanup(func() { gitCheckBudget = prev })
}

// SetMoveWait shrinks how long MoveProjectToOrg waits for a move's saga, and
// how often it looks, for one test. Not parallel-safe: package state.
func SetMoveWait(t *testing.T, wait, poll time.Duration) {
	t.Helper()
	prevWait, prevPoll := moveWait, movePoll
	moveWait, movePoll = wait, poll
	t.Cleanup(func() { moveWait, movePoll = prevWait, prevPoll })
}
