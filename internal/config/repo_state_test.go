package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A state the daemon could not read in full is never shown as clean, and
// its error is a warning on the provenance.
func TestSyncedRepoStateReadFailureIsNotClean(t *testing.T) {
	sha := "1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b"
	statusFailed := SyncedRepoState{Repo: "control-plane", Branch: "main", Commit: sha, DefaultBranch: "main", Error: "git status .reliant: signal: killed"}
	assert.Equal(t, "main 1a2b3c4, state unknown", statusFailed.Describe())
	headFailed := SyncedRepoState{Repo: "control-plane", Error: "read HEAD: permission denied"}
	assert.Equal(t, "git state unknown", headFailed.Describe())

	p := WorkflowProvenance{Machine: "cicd", RelativePath: "control-plane/.reliant/workflows/release.yaml", Repo: &statusFailed}
	assert.Contains(t, strings.Join(p.Warnings(), "\n"), "machine cicd could not read the git state of control-plane (git status .reliant: signal: killed)")
	assert.Equal(t, "machine cicd at control-plane/.reliant/workflows/release.yaml @ main 1a2b3c4, state unknown", p.Describe())

	// Off the default branch: a detached HEAD, or another branch.
	assert.True(t, SyncedRepoState{Branch: "", Commit: sha, DefaultBranch: "main"}.OffDefaultBranch())
	assert.False(t, SyncedRepoState{Branch: "main", DefaultBranch: "main"}.OffDefaultBranch())
	assert.False(t, SyncedRepoState{Branch: "feat"}.OffDefaultBranch(), "unknown default branch: no claim")
}
