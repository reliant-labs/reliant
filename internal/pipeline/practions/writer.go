// Copyright (c) 2025 Reliant Labs
package practions

import (
	"context"
	"fmt"

	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// Write is one GitHub write an action made, or, in shadow mode, would have
// made. The action's output lists them under `writes`, so the run's own
// history is the record of what shadow mode held back.
type Write struct {
	// Kind is comment or close.
	Kind string `json:"kind"`
	// Target is the PR, owner/name#n.
	Target string `json:"target"`
	// Body is a comment's text.
	Body string `json:"body,omitempty"`
	// Suppressed is true when shadow mode recorded the write instead of
	// making it.
	Suppressed bool `json:"suppressed,omitempty"`
}

// The write kinds.
const (
	writeComment = "comment"
	writeClose   = "close"
)

// githubWriter is the one path every pr.* GitHub write takes. In a shadow
// repository it writes nothing: it logs and counts the write it would have
// made and returns it marked Suppressed. In mode on it writes as the
// Reliant GitHub App's bot (GitHub.Comment and GitHub.Close). No action
// calls GitHub's write methods any other way.
type githubWriter struct {
	gh     GitHub
	shadow bool
	as     Caller
	repo   string
	number int
	// writes are what this writer did, in order.
	writes []Write
}

func (s *Service) writer(p *prCall) *githubWriter {
	return &githubWriter{gh: s.deps.GitHub, shadow: p.shadow(), as: p.as, repo: p.rec.Repository, number: p.rec.Number}
}

// Comment posts body on the PR's conversation.
func (w *githubWriter) Comment(ctx context.Context, body string) error {
	return w.do(ctx, Write{Kind: writeComment, Body: body}, func() error {
		return w.gh.Comment(ctx, w.as, w.repo, w.number, body)
	})
}

// Close closes the PR.
func (w *githubWriter) Close(ctx context.Context) error {
	return w.do(ctx, Write{Kind: writeClose}, func() error {
		return w.gh.Close(ctx, w.as, w.repo, w.number)
	})
}

func (w *githubWriter) do(ctx context.Context, write Write, send func() error) error {
	write.Target = fmt.Sprintf("%s#%d", w.repo, w.number)
	if w.shadow {
		write.Suppressed = true
		w.writes = append(w.writes, write)
		observability.PipelineGitHubWrites.WithLabelValues(write.Kind, "suppressed").Inc()
		logging.InfoContext(ctx, "[practions] shadow mode: GitHub write recorded, not made",
			"kind", write.Kind, "target", write.Target, "run", w.as.RunID, "node", w.as.NodeID)
		return nil
	}
	if err := send(); err != nil {
		observability.PipelineGitHubWrites.WithLabelValues(write.Kind, "failed").Inc()
		return err
	}
	observability.PipelineGitHubWrites.WithLabelValues(write.Kind, "written").Inc()
	w.writes = append(w.writes, write)
	return nil
}

// Writes are this writer's writes, JSON-shaped for an action's output.
func (w *githubWriter) Writes() []any {
	v, _ := jsonValue(w.writes).([]any)
	if v == nil {
		return []any{}
	}
	return v
}
