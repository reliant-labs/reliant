// Copyright (c) 2025 Reliant Labs
package gitport

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Trailer keys the queue writes into every commit it builds.
const (
	// TrailerPR is "Reliant-PR: <owner/repo>#<n> head=<sha>": the PR and the
	// exact head a squash commit carries.
	TrailerPR = "Reliant-PR"
	// TrailerBatch is "Reliant-Batch: <id>": the batch whose gate tested the
	// batch's final tree (and only that one; see the design's §3.2 step 5).
	TrailerBatch = "Reliant-Batch"
)

// Trailer is one "Key: value" line of a commit message's trailer block.
type Trailer struct {
	Key   string
	Value string
}

// PRTrailer is the Reliant-PR trailer for PR number n of repo (owner/name) at
// head.
func PRTrailer(repo string, n int, head string) Trailer {
	return Trailer{Key: TrailerPR, Value: fmt.Sprintf("%s#%d head=%s", repo, n, head)}
}

// BatchTrailer is the Reliant-Batch trailer.
func BatchTrailer(id string) Trailer { return Trailer{Key: TrailerBatch, Value: id} }

// PRRef is a parsed Reliant-PR trailer.
type PRRef struct {
	Repo   string
	Number int
	Head   string
}

// ParsePRTrailer parses a Reliant-PR trailer's value.
func ParsePRTrailer(value string) (PRRef, error) {
	ref, head, ok := strings.Cut(value, " head=")
	repo, num, ok2 := strings.Cut(ref, "#")
	n, err := strconv.Atoi(num)
	if !ok || !ok2 || err != nil || repo == "" || n <= 0 || !isSHA(head) {
		return PRRef{}, fmt.Errorf("gitport: %q is not a %s trailer value (<owner/repo>#<n> head=<sha>)", value, TrailerPR)
	}
	return PRRef{Repo: repo, Number: n, Head: head}, nil
}

// FormatMessage builds a commit message: title, a blank line and body (when
// set), then a blank line and the trailer block. A trailer key may not hold
// whitespace or a colon, and no field may hold a newline where it would break
// the block.
func FormatMessage(title, body string, trailers []Trailer) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" || strings.Contains(title, "\n") {
		return "", errors.New("gitport: a commit title must be one non-empty line")
	}
	var b strings.Builder
	b.WriteString(title)
	b.WriteString("\n")
	if body = strings.TrimSpace(body); body != "" {
		b.WriteString("\n")
		b.WriteString(body)
		b.WriteString("\n")
	}
	if len(trailers) > 0 {
		b.WriteString("\n")
		for _, t := range trailers {
			if t.Key == "" || strings.ContainsAny(t.Key, " \t\r\n:") {
				return "", fmt.Errorf("gitport: trailer key %q is not a single token", t.Key)
			}
			if strings.ContainsAny(t.Value, "\r\n") || strings.TrimSpace(t.Value) == "" {
				return "", fmt.Errorf("gitport: trailer %s needs a one-line value", t.Key)
			}
			b.WriteString(t.Key)
			b.WriteString(": ")
			b.WriteString(strings.TrimSpace(t.Value))
			b.WriteString("\n")
		}
	}
	return b.String(), nil
}

// ParseTrailers returns the trailer block of a commit message: the "Key:
// value" lines of its last paragraph, in order. A last paragraph that is not
// entirely trailers (or the title itself) yields none.
func ParseTrailers(message string) []Trailer {
	paras := strings.Split(strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n")), "\n\n")
	if len(paras) < 2 {
		return nil
	}
	var out []Trailer
	for _, line := range strings.Split(paras[len(paras)-1], "\n") {
		k, v, ok := strings.Cut(line, ": ")
		if !ok || k == "" || strings.ContainsAny(k, " \t") {
			return nil
		}
		out = append(out, Trailer{Key: k, Value: strings.TrimSpace(v)})
	}
	return out
}
