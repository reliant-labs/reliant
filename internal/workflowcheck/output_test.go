// Copyright (c) 2025 Reliant Labs
package workflowcheck

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestOutputForUsesTheRunsOutputOnlyWhenItEnded(t *testing.T) {
	run := map[string]any{"title": "3 lint findings", "summary": "see annotations", "text": "more",
		"annotations": []any{map[string]any{"path": "a.go", "start_line": float64(3), "message": "unused", "level": "warning"}}}

	ended := outputFor(state{Title: "Failed", Summary: "The run failed.", RunOutput: true}, run, "", "https://x/run")
	require.Equal(t, "3 lint findings", ended.Title)
	require.Equal(t, "see annotations", ended.Summary)
	require.Equal(t, "more", ended.Text)
	require.Len(t, ended.Annotations, 1)
	require.Equal(t, map[string]any{"path": "a.go", "start_line": 3, "end_line": 3, "annotation_level": "warning", "message": "unused"}, ended.Annotations[0])

	queued := outputFor(state{Title: "Queued", Summary: "Waiting."}, run, "", "https://x/run")
	require.Equal(t, "Queued", queued.Title)
	require.Contains(t, queued.Summary, "[Open the run in Reliant](https://x/run)")
	require.Empty(t, queued.Annotations)
}

func TestOutputForFallsBackAndSaysWhy(t *testing.T) {
	s := state{Title: "Passed", Summary: "The run completed.", RunOutput: true}

	noOutput := outputFor(s, nil, "check.output did not evaluate: no such key", "")
	require.Equal(t, "Passed", noOutput.Title)
	require.Contains(t, noOutput.Summary, "no such key")

	badShape := outputFor(s, map[string]any{"title": "only a title"}, "", "")
	require.Equal(t, "Passed", badShape.Title)
	require.Contains(t, badShape.Summary, "title and summary")

	badAnnotation := outputFor(s, map[string]any{"title": "t", "summary": "s", "annotations": []any{map[string]any{"path": "a.go"}}}, "", "")
	require.Equal(t, "Passed", badAnnotation.Title)
	require.Contains(t, badAnnotation.Summary, "annotations[0]")
}

func TestAnnotationRules(t *testing.T) {
	for name, a := range map[string]map[string]any{
		"no path":           {"start_line": 1, "message": "m"},
		"no line":           {"path": "a", "message": "m"},
		"zero line":         {"path": "a", "start_line": 0, "message": "m"},
		"end before start":  {"path": "a", "start_line": 5, "end_line": 4, "message": "m"},
		"unknown level":     {"path": "a", "start_line": 1, "message": "m", "annotation_level": "error"},
		"fractional line":   {"path": "a", "start_line": 1.5, "message": "m"},
		"not even a string": {"path": 7, "start_line": 1, "message": "m"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := annotation(a)
			require.Error(t, err)
		})
	}
	got, err := annotation(map[string]any{"path": "a", "start_line": 2, "end_line": 4, "message": "m", "start_column": 1, "title": "t"})
	require.NoError(t, err)
	require.Equal(t, "failure", got["annotation_level"], "the default level fails loudest")
	require.NotContains(t, got, "start_column", "columns only on a one-line annotation")
	require.Equal(t, "t", got["title"])
}

func TestTruncateKeepsRunes(t *testing.T) {
	s := strings.Repeat("é", 200)
	cut := truncate(s, 255)
	require.LessOrEqual(t, len(cut), 255)
	require.True(t, utf8.ValidString(cut))
	require.Equal(t, "abc", truncate("abc", 255))
}

func TestDigestChangesWithWhatIsWritten(t *testing.T) {
	o := Output{Title: "t", Summary: "s"}
	require.Equal(t, digest("queued", "", o), digest("queued", "", o))
	require.NotEqual(t, digest("queued", "", o), digest("in_progress", "", o))
	require.NotEqual(t, digest("completed", "success", o), digest("completed", "failure", o))
	require.NotEqual(t, digest("queued", "", o), digest("queued", "", Output{Title: "t", Summary: "s2"}))
}
