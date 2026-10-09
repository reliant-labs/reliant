// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

// repoMemoryBudgetFraction is the share of a model's context window the
// injected per-repo memories may occupy.
//
// Repo memories are re-sent on every request and no compaction can remove
// them, so they are part of the fixed base a thread can never shrink. Their
// number grows with the project, not with the conversation: one entry per
// nested git repository the daemon discovers. Prod incident 2026-10-09: ~30
// hand-made `git worktree add ../<repo>-<feature>` checkouts inside one
// project each carried a copy of the forge framework guide and their repo's
// reliant.md — 523KB of memory on every request. A fresh sub-agent's first turn
// reported 246.8k tokens against a 231.2k compaction threshold and compacted
// after every tool call (#648 collapsed linked worktrees into their main
// checkout; this bound is what keeps N distinct clones from doing the same).
//
// 10% leaves the conversation the bulk of the window below the 85% compaction
// threshold, and comfortably holds a multi-repo project's real memories
// (measured: reliant-labs' three repos are ~64KB, 6% of a 272k window).
const repoMemoryBudgetFraction = 0.10

// repoMemoryBudgetChars is the character budget for repo memories on a model
// with the given REAL context window (<= 0: unknown, assume a 200k window).
func repoMemoryBudgetChars(contextWindow int64) int {
	if contextWindow <= 0 {
		contextWindow = message.MaxContextTokens
	}
	return int(float64(contextWindow) * repoMemoryBudgetFraction * message.CharsPerToken)
}

// formatRepoMemoryMessages converts the config's repo memories map into
// system messages, one per repo, sorted by repo name for prefix-cache
// stability across turns.
//
// Memories are admitted in that order while they fit budgetChars; one that
// does not fit is skipped and the next is tried. Skipped repos are not
// silently dropped: a final message names each one and where its memory lives,
// so the model reads it when it works there. The second return value lists
// the skipped repos.
func formatRepoMemoryMessages(repoMemories map[string]string, budgetChars int) ([]message.Message, []string) {
	if len(repoMemories) == 0 {
		return nil, nil
	}

	// Sort keys for deterministic ordering (prefix-cache stability).
	keys := make([]string, 0, len(repoMemories))
	for k := range repoMemories {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var msgs []message.Message
	var omitted []string
	omittedSizes := map[string]int{}
	remaining := budgetChars
	for _, repo := range keys {
		content := strings.TrimSpace(repoMemories[repo])
		if content == "" {
			continue
		}
		body := fmt.Sprintf("<system-memory repo=%s>\n%s\n</system-memory>", repo, content)
		if len(body) > remaining {
			omitted = append(omitted, repo)
			omittedSizes[repo] = len(content)
			continue
		}
		remaining -= len(body)
		msgs = append(msgs, systemText(body))
	}

	if len(omitted) > 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "<system-memory-omitted>\nThese nested repos have memory files that were NOT included above: "+
			"together the repo memories exceed %.0f%% of this model's context window, which every request "+
			"carries and no compaction can reclaim.\n", repoMemoryBudgetFraction*100)
		for _, repo := range omitted {
			fmt.Fprintf(&sb, "- %s (%d KB): read %s/reliant.md (and %s/reliant.local.md if present) before working in it\n",
				repo, (omittedSizes[repo]+1023)/1024, repo, repo)
		}
		sb.WriteString("</system-memory-omitted>")
		msgs = append(msgs, systemText(sb.String()))
	}
	return msgs, omitted
}

func systemText(text string) message.Message {
	return message.Message{
		Role:  message.System,
		Parts: []message.ContentPart{message.TextContent{Text: text}},
	}
}

// repoMemoryOmissionNotices warns once per project and omitted set: the same
// project omits the same repos on every request until its memories change.
type repoMemoryOmissionNotices struct {
	lastWarned sync.Map // projectID -> omitted set, joined
}

var repoMemoryOmissions = &repoMemoryOmissionNotices{}

func (n *repoMemoryOmissionNotices) warn(projectID string, omitted []string, budgetChars int) {
	set := strings.Join(omitted, ",")
	if previous, seen := n.lastWarned.Swap(projectID, set); seen && previous.(string) == set {
		return
	}
	logging.Warn("[CallLLM] Repo memories exceed their share of the context window; omitting some",
		"projectID", projectID,
		"omitted", omitted,
		"budgetChars", budgetChars,
		"budgetFraction", repoMemoryBudgetFraction)
}
