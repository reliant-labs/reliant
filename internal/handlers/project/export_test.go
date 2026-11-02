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
