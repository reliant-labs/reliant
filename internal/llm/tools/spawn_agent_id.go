// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/rctx"
)

// Agent ids are thread UUIDs, and a parent rarely keeps all 36 characters of
// one: a context summary, a report or a listing it skimmed leaves it the
// first 8, the same way a git sha is quoted. The spawn tools therefore accept
// an unambiguous prefix of an agent the caller can address and expand it to
// the full id before any ownership check runs. The prefix is matched only
// against the caller's OWN addressable agents, so it widens nothing: an agent
// that a full id could not reach, a prefix cannot reach either.

// minAgentIDPrefixLen keeps a stray "a" or "1" from silently picking an
// agent. Four hex characters is already 65,536 values; an orchestrator with a
// few hundred children still gets an ambiguity error, not a wrong agent.
const minAgentIDPrefixLen = 4

// spawnAgentCandidateStore is what spawnAgentIDCandidates reads: the caller's
// own spawn children and those it inherited through a branch.
type spawnAgentCandidateStore interface {
	spawnChildLister
	inheritedSpawnLookup
}

// spawnAgentIDCandidates lists every agent id the caller may name: its own
// children, the ones it inherited (read-only, but inspectable), and extra ids
// such as its own parent for spawn_send. Best-effort for the inherited half,
// like findInheritedSpawnChild.
func spawnAgentIDCandidates(rctx *rctx.ToolContext, repo spawnAgentCandidateStore, callerThreadID string, extra ...string) ([]string, error) {
	children, err := repo.ListSpawnChildren(rctx.Context, callerThreadID)
	if err != nil {
		return nil, fmt.Errorf("failed to list sub-agents: %w", err)
	}
	ids := make([]string, 0, len(children)+len(extra))
	for _, child := range children {
		if child.ChildThreadID != nil {
			ids = append(ids, *child.ChildThreadID)
		}
	}
	if inherited, inhErr := repo.ListInheritedSpawnChildren(rctx.Context, callerThreadID); inhErr == nil {
		for _, child := range inherited {
			if child.ChildThreadID != nil {
				ids = append(ids, *child.ChildThreadID)
			}
		}
	}
	for _, id := range extra {
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// resolveSpawnAgentID expands agentID to the full id of an agent the caller
// may address. An exact id, or one that matches nothing, is returned
// unchanged so the caller's own "not a sub-agent" refusal still explains
// itself; only an ambiguous prefix is an error here.
func resolveSpawnAgentID(rctx *rctx.ToolContext, repo spawnAgentCandidateStore, callerThreadID, agentID string, extra ...string) (string, error) {
	agentID = strings.TrimSpace(agentID)
	if len(agentID) < minAgentIDPrefixLen || len(agentID) >= 36 {
		return agentID, nil
	}
	candidates, err := spawnAgentIDCandidates(rctx, repo, callerThreadID, extra...)
	if err != nil {
		return "", err
	}
	return expandAgentIDPrefix(agentID, candidates)
}

// expandAgentIDPrefix is resolveSpawnAgentID's pure core.
func expandAgentIDPrefix(agentID string, candidates []string) (string, error) {
	matches := map[string]bool{}
	for _, id := range candidates {
		if id == agentID {
			return id, nil
		}
		if strings.HasPrefix(id, agentID) {
			matches[id] = true
		}
	}
	switch len(matches) {
	case 0:
		return agentID, nil
	case 1:
		for id := range matches {
			return id, nil
		}
	}
	ids := make([]string, 0, len(matches))
	for id := range matches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return "", fmt.Errorf("agent_id %q is ambiguous: it is a prefix of %d of your agents (%s). Pass more characters, or the full agent_id",
		agentID, len(ids), strings.Join(ids, ", "))
}
