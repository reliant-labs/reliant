// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/db"
)

// liveBackgroundSpawnLister is the one repository read the coarse fresh
// restart needs to find the spawns its dead predecessor left in flight.
type liveBackgroundSpawnLister interface {
	ListLiveBackgroundSpawns(ctx context.Context, rootWorkflowID string) ([]*db.LiveBackgroundSpawn, error)
}

// ResumableSpawnsFromDurableState derives the background spawns a coarse
// fresh restart of rootWorkflowID must relaunch, from durable rows alone.
//
// A continue-as-new handoff carries its spawns in the continuation input with
// exact loop positions. A run TERMINATED by Temporal (the history-limit death
// this exists for) hands nothing off: its spawns were goroutines in the dead
// execution. What survives them is durable — the backgrounded tool_calls row
// (tool call id, issuing thread, preset and title in its input) joined to the
// child workflow row (child thread, issuing workflow). That is everything a
// relaunch needs except the loop position, which relaunch recovers from the
// child thread's history; the spawn resumes at iteration 0 of its agent loop,
// exactly as a transient-error retry of a live spawn does.
//
// Returned parents-before-children so an issuing spawn is registered before
// the spawns it issued.
func ResumableSpawnsFromDurableState(ctx context.Context, repo liveBackgroundSpawnLister, rootWorkflowID string) ([]SpawnHandoff, error) {
	rows, err := repo.ListLiveBackgroundSpawns(ctx, rootWorkflowID)
	if err != nil {
		return nil, fmt.Errorf("list live background spawns for %s: %w", rootWorkflowID, err)
	}
	handoffs := make([]SpawnHandoff, 0, len(rows))
	for _, row := range rows {
		h := SpawnHandoff{
			ToolCallID:       row.ToolCallID,
			ParentThread:     row.ParentThreadID,
			ChildThread:      row.ChildThreadID,
			ChildWorkflowID:  row.ChildWorkflowID,
			ParentWorkflowID: row.IssuingWorkflowID,
			SpawnDepth:       row.Depth + 1,
		}
		// The input column is the spawn's unwrapped tool input as the LLM
		// sent it. An unparseable one still relaunches, under the default
		// preset — losing the preset is better than losing the spawn.
		if len(row.ToolInput) > 0 {
			if parsed, parseErr := parseSpawnToolInput(string(row.ToolInput)); parseErr == nil || parsed.preset != "" {
				h.Preset = parsed.preset
				h.Title = parsed.title
			}
		}
		handoffs = append(handoffs, h)
	}
	return handoffs, nil
}

// stoppedSpawnReader is the one repository read resuming a stopped sub-agent
// from durable state needs.
type stoppedSpawnReader interface {
	GetStoppedSpawnAgent(ctx context.Context, chatID, threadID string) (*db.StoppedSpawnAgent, error)
}

// StoppedSpawnFromDurableState returns the relaunch record of the sub-agent on
// thread if it stopped on a failure, nil if it did not (it is running,
// completed, was stopped by the user, or is not a sub-agent of chatID).
//
// It is the durable form of the record the execution that saw the agent stop
// keeps in memory (ChildWorkflowTracker.stoppedSpawns), and what makes "a
// message resumes a stopped agent" hold beyond that execution's life: in a
// successor started after it completed, in a fresh restart after it died, and
// for an agent that stopped before executions kept such a record at all.
func StoppedSpawnFromDurableState(ctx context.Context, repo stoppedSpawnReader, chatID, thread string) (*SpawnHandoff, error) {
	agent, err := repo.GetStoppedSpawnAgent(ctx, chatID, thread)
	if err != nil {
		return nil, fmt.Errorf("read stopped sub-agent %s: %w", thread, err)
	}
	if agent == nil {
		return nil, nil
	}
	h := stoppedSpawnHandoff(agent)
	return &h, nil
}

// stoppedSpawnHandoff is a stopped sub-agent's durable record as the relaunch
// record a resume works from (resumeStoppedSpawn). Like a live spawn derived
// from durable state (ResumableSpawnsFromDurableState) it carries no child
// inputs — the resume rebuilds them from the run's own — and resumes its agent
// loop at iteration 0; the thread's history is where it left off.
//
// Pure: the workflow converts a LookupStoppedSubAgent result with it, so it
// must give the same answer on replay.
func stoppedSpawnHandoff(agent *db.StoppedSpawnAgent) SpawnHandoff {
	h := SpawnHandoff{
		ToolCallID:       agent.ToolCallID,
		ParentThread:     agent.ParentThreadID,
		ChildThread:      agent.ChildThreadID,
		ChildWorkflowID:  agent.ChildWorkflowID,
		ParentWorkflowID: agent.IssuingWorkflowID,
		ProjectPath:      agent.WorktreePath,
		SpawnDepth:       agent.SpawnDepth,
		Resumes:          agent.Resumes,
		LastFailure:      agent.LastFailure,
	}
	// As for a live spawn: an unparseable input still resumes, under the
	// default preset.
	if agent.ToolInput != "" {
		if parsed, parseErr := parseSpawnToolInput(agent.ToolInput); parseErr == nil || parsed.preset != "" {
			h.Preset = parsed.preset
			h.Title = parsed.title
		}
	}
	return h
}
