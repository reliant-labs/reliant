// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// GitHub's limits on a check run's output.
const (
	maxTitleBytes = 255
	// maxTextBytes is the limit on summary and text. GitHub counts
	// characters; bytes are never fewer, so a cut in bytes always fits.
	maxTextBytes = 65535
	// AnnotationBatch is how many annotations GitHub takes per request; the
	// rest are sent by later updates, which GitHub appends.
	AnnotationBatch = 50
	// maxAnnotations bounds what one check sends at all.
	maxAnnotations = 1000
)

// Output is what a check run shows: GitHub's output object.
type Output struct {
	Title       string
	Summary     string
	Text        string
	Annotations []map[string]any
}

// params is the output as the check_run actions take it, without
// annotations.
func (o Output) params() map[string]any {
	p := map[string]any{"title": o.Title, "summary": o.Summary}
	if o.Text != "" {
		p["text"] = o.Text
	}
	return p
}

// digest identifies what a write says, so the syncer writes only a change.
func digest(status, conclusion string, o Output) string {
	raw, _ := json.Marshal([]any{status, conclusion, o.Title, o.Summary, o.Text})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// outputFor is what the check shows in state s: the run's own output when the
// run ended and evaluated one, else the engine's words. runURL, when set, is
// linked from the engine's summary. A run output that does not have the shape
// GitHub needs falls back too, and says why.
func outputFor(s state, runOutput map[string]any, outputError, runURL string) Output {
	if s.RunOutput && runOutput != nil {
		if o, err := parseOutput(runOutput); err == nil {
			return o
		} else {
			outputError = err.Error()
		}
	}
	summary := s.Summary
	if s.RunOutput && outputError != "" {
		summary += "\n\nThe workflow's check output could not be used: " + outputError
	}
	if runURL != "" {
		summary += fmt.Sprintf("\n\n[Open the run in Reliant](%s)", runURL)
	}
	return Output{Title: truncate(s.Title, maxTitleBytes), Summary: truncate(summary, maxTextBytes)}
}

// parseOutput reads a run's check output: {title, summary, text?,
// annotations?}. title and summary are required, as GitHub requires them.
func parseOutput(v map[string]any) (Output, error) {
	title, _ := v["title"].(string)
	summary, _ := v["summary"].(string)
	if strings.TrimSpace(title) == "" || strings.TrimSpace(summary) == "" {
		return Output{}, fmt.Errorf("it needs a non-empty title and summary")
	}
	o := Output{Title: truncate(strings.TrimSpace(title), maxTitleBytes), Summary: truncate(summary, maxTextBytes)}
	if text, ok := v["text"].(string); ok {
		o.Text = truncate(text, maxTextBytes)
	}
	if raw, ok := v["annotations"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return Output{}, fmt.Errorf("annotations must be a list")
		}
		for i, item := range list {
			if len(o.Annotations) == maxAnnotations {
				break
			}
			a, err := annotation(item)
			if err != nil {
				return Output{}, fmt.Errorf("annotations[%d]: %w", i, err)
			}
			o.Annotations = append(o.Annotations, a)
		}
	}
	return o, nil
}

var annotationLevels = map[string]bool{"notice": true, "warning": true, "failure": true}

// annotation reads one annotation as GitHub's check run API takes it:
// {path, start_line, end_line?, annotation_level, message, title?,
// raw_details?, start_column?, end_column?}. `level` is accepted for
// annotation_level, and end_line defaults to start_line.
func annotation(item any) (map[string]any, error) {
	m, ok := item.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("must be an object")
	}
	path, _ := m["path"].(string)
	message, _ := m["message"].(string)
	if strings.TrimSpace(path) == "" || strings.TrimSpace(message) == "" {
		return nil, fmt.Errorf("path and message are required")
	}
	start, ok := lineNumber(m["start_line"])
	if !ok {
		return nil, fmt.Errorf("start_line must be a positive integer")
	}
	end := start
	if raw, present := m["end_line"]; present && raw != nil {
		if end, ok = lineNumber(raw); !ok || end < start {
			return nil, fmt.Errorf("end_line must be an integer at or after start_line")
		}
	}
	level, _ := m["annotation_level"].(string)
	if level == "" {
		level, _ = m["level"].(string)
	}
	if level == "" {
		level = "failure"
	}
	if !annotationLevels[level] {
		return nil, fmt.Errorf("annotation_level %q must be notice, warning or failure", level)
	}
	out := map[string]any{
		"path": path, "start_line": start, "end_line": end,
		"annotation_level": level, "message": truncate(message, 64*1024),
	}
	if title, _ := m["title"].(string); title != "" {
		out["title"] = truncate(title, maxTitleBytes)
	}
	if details, _ := m["raw_details"].(string); details != "" {
		out["raw_details"] = truncate(details, 64*1024)
	}
	// Columns only on a one-line annotation, as GitHub requires.
	if start == end {
		if c, ok := lineNumber(m["start_column"]); ok {
			out["start_column"] = c
			if e, ok := lineNumber(m["end_column"]); ok && e >= c {
				out["end_column"] = e
			}
		}
	}
	return out, nil
}

func lineNumber(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, n > 0
	case int64:
		return int(n), n > 0
	case float64:
		return int(n), n > 0 && n == float64(int(n))
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil && i > 0
	}
	return 0, false
}

// truncate cuts s to at most max bytes on a rune boundary, marking the cut.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	const marker = "…"
	cut := max - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}
