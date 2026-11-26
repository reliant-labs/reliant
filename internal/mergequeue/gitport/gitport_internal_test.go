// Copyright (c) 2025 Reliant Labs
package gitport

import (
	"errors"
	"os"
	"slices"
	"testing"
)

func TestParseVersion(t *testing.T) {
	t.Parallel()
	for out, ok := range map[string]bool{
		"git version 2.43.0\n":                  true,
		"git version 2.40.1":                    true,
		"git version 3.0.0":                     true,
		"git version 2.39.5 (Apple Git-154)":    false,
		"git version 2.37.0.windows.1":          false,
		"git version 1.9.9":                     false,
		"not git":                               false,
		"git version 2.45.2.windows.1\nextra\n": true,
	} {
		err := parseVersion(out)
		if ok != (err == nil) {
			t.Errorf("parseVersion(%q) = %v, want ok=%v", out, err, ok)
		}
		if err != nil && !errors.Is(err, ErrGitTooOld) {
			t.Errorf("parseVersion(%q) = %v, not ErrGitTooOld", out, err)
		}
	}
}

func TestFormatMessage(t *testing.T) {
	t.Parallel()
	got, err := FormatMessage(" title ", "", []Trailer{BatchTrailer("b")})
	if err != nil || got != "title\n\nReliant-Batch: b\n" {
		t.Fatalf("FormatMessage = %q, %v", got, err)
	}
	if got, _ := FormatMessage("t", "body", nil); got != "t\n\nbody\n" {
		t.Fatalf("no trailers = %q", got)
	}
	for _, bad := range []struct {
		title string
		tr    []Trailer
	}{
		{"", nil},
		{"two\nlines", nil},
		{"t", []Trailer{{Key: "K:ey", Value: "v"}}},
		{"t", []Trailer{{Key: "Key", Value: "multi\nline"}}},
		{"t", []Trailer{{Key: "Key", Value: " "}}},
	} {
		if _, err := FormatMessage(bad.title, "", bad.tr); err == nil {
			t.Errorf("FormatMessage(%q, %+v) accepted", bad.title, bad.tr)
		}
	}
}

func TestParseTrailers(t *testing.T) {
	t.Parallel()
	if got := ParseTrailers("only a title\n"); got != nil {
		t.Fatalf("title only = %+v", got)
	}
	if got := ParseTrailers("t\n\nprose: with a colon but\nnot a trailer block at all\n"); got != nil {
		t.Fatalf("prose = %+v", got)
	}
	got := ParseTrailers("t\n\nbody\n\nA: 1\nB-C: two\n")
	if !slices.Equal(got, []Trailer{{"A", "1"}, {"B-C", "two"}}) {
		t.Fatalf("trailers = %+v", got)
	}
	for _, bad := range []string{"acme/w#x head=" + sha40, "acme/w#3", "#3 head=" + sha40, "acme/w#3 head=short", "acme/w#0 head=" + sha40} {
		if _, err := ParsePRTrailer(bad); err == nil {
			t.Errorf("ParsePRTrailer(%q) accepted", bad)
		}
	}
}

const sha40 = "0123456789abcdef0123456789abcdef01234567"

func TestHermeticEnvIgnoresHostConfig(t *testing.T) {
	t.Parallel()
	env := HermeticEnv(Identity{Name: "N", Email: "e@x"}, "EXTRA=1")
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_TERMINAL_PROMPT=0", "GIT_COMMITTER_NAME=N", "GIT_AUTHOR_EMAIL=e@x", "EXTRA=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s: %v", want, env)
		}
	}
}
