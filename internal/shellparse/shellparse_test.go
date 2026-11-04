// Copyright (c) 2025 Reliant Labs
package shellparse

import (
	"reflect"
	"testing"
)

func TestCommands(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in string
		want     []Command
	}{
		{"one", "go test ./...", []Command{{Words: []string{"go", "test", "./..."}}}},
		{"quotes are removed", `git commit -m "a; b && c"`, []Command{{Words: []string{"git", "commit", "-m", "a; b && c"}}}},
		{"operators split", "a && b || c; d", []Command{{Words: []string{"a"}}, {Words: []string{"b"}}, {Words: []string{"c"}}, {Words: []string{"d"}}}},
		{"pipe marks stdin", "a | b | c", []Command{{Words: []string{"a"}}, {Words: []string{"b"}, PipeIn: true}, {Words: []string{"c"}, PipeIn: true}}},
		{"a pipe continued on the next line", "a |\nb", []Command{{Words: []string{"a"}}, {Words: []string{"b"}, PipeIn: true}}},
		{"or is not a pipe", "a || b", []Command{{Words: []string{"a"}}, {Words: []string{"b"}}}},
		{"redirect ampersand stays in the word", "a 2>&1 | b", []Command{{Words: []string{"a", "2>&1"}}, {Words: []string{"b"}, PipeIn: true}}},
		{"comment", "ls # go clean -cache\npwd", []Command{{Words: []string{"ls"}}, {Words: []string{"pwd"}}}},
		{"hash inside a word", "echo a#b", []Command{{Words: []string{"echo", "a#b"}}}},
		{"escaped semicolon", `find . -exec rm {} \;`, []Command{{Words: []string{"find", ".", "-exec", "rm", "{}", ";"}}}},
		{"continuation", "go \\\n test", []Command{{Words: []string{"go", "test"}}}},
		{"substitution stays one word", "rm -rf $(go env GOCACHE)", []Command{{Words: []string{"rm", "-rf", "$(go env GOCACHE)"}}}},
		{"substitution holding quotes", `echo "$(echo "a b")" x`, []Command{{Words: []string{"echo", `$(echo "a b")`, "x"}}}},
		{"backticks stay one word", "rm -rf `go env GOMODCACHE`", []Command{{Words: []string{"rm", "-rf", "`go env GOMODCACHE`"}}}},
		{"subshell parentheses", "(cd x && ls)", []Command{{Words: []string{"cd", "x"}}, {Words: []string{"ls"}}}},
		{"newline inside quotes", "git commit -m \"a\nb\"", []Command{{Words: []string{"git", "commit", "-m", "a\nb"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Commands(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Commands(%q)\n got %#v\nwant %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestSingleQuotedSubstitutionIsData(t *testing.T) {
	t.Parallel()
	cmds := Commands(`echo '$(rm -rf /x)' "$(rm -rf /y)"`)
	if len(cmds) != 1 || len(cmds[0].Words) != 3 {
		t.Fatalf("got %#v", cmds)
	}
	if got := Substitutions(cmds[0].Words[1]); len(got) != 0 {
		t.Errorf("single-quoted text ran as a substitution: %v", got)
	}
	if got := Restore(cmds[0].Words[1]); got != "$(rm -rf /x)" {
		t.Errorf("Restore = %q", got)
	}
	if got := Substitutions(cmds[0].Words[2]); !reflect.DeepEqual(got, []string{"rm -rf /y"}) {
		t.Errorf("double-quoted substitution: %v", got)
	}
}

func TestSubstitutions(t *testing.T) {
	t.Parallel()
	cases := map[string][]string{
		"plain":               nil,
		"$(go env GOCACHE)":   {"go env GOCACHE"},
		"`go env GOMODCACHE`": {"go env GOMODCACHE"},
		"a$(b)c$(d $(e))":     {"b", "d $(e)"},
		"$((1+2))":            nil,
		"$(echo ')') tail":    {"echo ')'"},
		"${HOME}":             nil,
	}
	for in, want := range cases {
		if got := Substitutions(in); !reflect.DeepEqual(got, want) {
			t.Errorf("Substitutions(%q) = %q, want %q", in, got, want)
		}
	}
}
