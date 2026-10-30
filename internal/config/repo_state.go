package config

import (
	"encoding/json"
	"fmt"
)

// SyncedRepoState is the git state one repository's .reliant files were
// synced from, as a machine's daemon reported it with its snapshot.
type SyncedRepoState struct {
	// Repo is the repo's path relative to the project root, "" for the root.
	Repo string `json:"repo"`
	// Branch is the checked-out branch, "" when HEAD is detached.
	Branch string `json:"branch,omitempty"`
	// Commit is HEAD's SHA.
	Commit string `json:"commit,omitempty"`
	// Dirty is true when .reliant had uncommitted changes.
	Dirty bool `json:"dirty,omitempty"`
	// DefaultBranch is origin's default branch, "" when unknown.
	DefaultBranch string `json:"default_branch,omitempty"`
	// Error is why the state could not be read in full.
	Error string `json:"error,omitempty"`
}

// OffDefaultBranch reports a checkout whose branch is known not to be the
// default one: a detached HEAD, or another branch than origin's default.
func (s SyncedRepoState) OffDefaultBranch() bool {
	if s.DefaultBranch == "" {
		return false
	}
	return s.Branch != s.DefaultBranch
}

// ShortCommit is the commit's first 7 characters.
func (s SyncedRepoState) ShortCommit() string {
	if len(s.Commit) > 7 {
		return s.Commit[:7]
	}
	return s.Commit
}

// Describe is the state for display: "main 1a2b3c4, clean",
// "feat/x 1a2b3c4, uncommitted changes", "detached 1a2b3c4, clean", and
// "main 1a2b3c4, state unknown" when the daemon could not read it in full:
// a failed read is never shown as clean.
func (s SyncedRepoState) Describe() string {
	branch := s.Branch
	if branch == "" {
		branch = "detached"
	}
	state := "clean"
	switch {
	case s.Dirty:
		state = "uncommitted changes"
	case s.Error != "":
		state = "state unknown"
	}
	if s.Commit == "" {
		if s.Error != "" {
			return "git state unknown"
		}
		return fmt.Sprintf("%s (no commits), %s", branch, state)
	}
	return fmt.Sprintf("%s %s, %s", branch, s.ShortCommit(), state)
}

// RepoStateFor is the state of the repo a project-relative file is in: the
// longest repo path that prefixes it, else the project root's. nil when the
// machine reported none.
func RepoStateFor(states []SyncedRepoState, relativePath string) *SyncedRepoState {
	var best *SyncedRepoState
	for i := range states {
		s := &states[i]
		if s.Repo == "" {
			if best == nil {
				best = s
			}
			continue
		}
		if len(relativePath) > len(s.Repo) && relativePath[:len(s.Repo)] == s.Repo && relativePath[len(s.Repo)] == '/' {
			if best == nil || len(s.Repo) > len(best.Repo) {
				best = s
			}
		}
	}
	return best
}

// ParseSyncedRepoStates deserializes project_machine_catalogs.repo_states_json.
func ParseSyncedRepoStates(jsonStr *string) ([]SyncedRepoState, error) {
	if jsonStr == nil || *jsonStr == "" {
		return nil, nil
	}
	var out []SyncedRepoState
	if err := json.Unmarshal([]byte(*jsonStr), &out); err != nil {
		return nil, fmt.Errorf("failed to parse synced repo states JSON: %w", err)
	}
	return out, nil
}

// WorkflowProvenance is where one machine's copy of a project workflow comes
// from: the machine, the file, and the git state it was synced at.
type WorkflowProvenance struct {
	Name         string
	RelativePath string
	DaemonID     string
	// Machine is the machine's display name.
	Machine string
	// Repo is the git state of the repo the file is in, nil when the machine
	// did not report one (an older daemon, or not a git checkout).
	Repo *SyncedRepoState
}

// Describe is the provenance for display: "machine cicd at
// control-plane/.reliant/workflows/release.yaml @ main 1a2b3c4, clean".
func (p WorkflowProvenance) Describe() string {
	s := fmt.Sprintf("machine %s at %s", p.Machine, p.RelativePath)
	if p.Repo != nil {
		s += " @ " + p.Repo.Describe()
	}
	return s
}

// Warnings are what about this source an automation's owner should act on:
// a definition synced from a feature branch, or from uncommitted changes.
func (p WorkflowProvenance) Warnings() []string {
	if p.Repo == nil {
		return nil
	}
	var out []string
	where := "the project root"
	if p.Repo.Repo != "" {
		where = p.Repo.Repo
	}
	if p.Repo.OffDefaultBranch() {
		branch := p.Repo.Branch
		if branch == "" {
			branch = "a detached HEAD (" + p.Repo.ShortCommit() + ")"
		}
		out = append(out, fmt.Sprintf("machine %s has %s checked out on %s, not %s: this runs that branch's %s",
			p.Machine, where, branch, p.Repo.DefaultBranch, p.RelativePath))
	}
	if p.Repo.Dirty {
		out = append(out, fmt.Sprintf("machine %s has uncommitted changes under %s/.reliant: this runs the edited files, not what is committed",
			p.Machine, where))
	}
	if p.Repo.Error != "" {
		out = append(out, fmt.Sprintf("machine %s could not read the git state of %s (%s): which commit this runs is not known",
			p.Machine, where, p.Repo.Error))
	}
	return out
}
