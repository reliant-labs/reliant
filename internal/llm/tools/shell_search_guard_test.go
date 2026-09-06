// Copyright (c) 2025 Reliant Labs
package tools

import (
	"strings"
	"testing"
)

// The refused cases are transcribed from forge-one-shot exec 7bcf233a, where
// three fan-out units independently ran a full-disk find. Those calls averaged
// 268.7s each and five hit the hard timeout at 5m0s.
func TestUnscopedSearchRefusal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
		refuse  bool
	}{
		// --- measured offenders ---
		{"find / for a go package", `find / -path "*forge/pkg/svcerr*" -type d 2>/dev/null`, true},
		{"find / for crud", `find / -path "*forge/pkg/crud*" 2>/dev/null | head -20`, true},

		// --- other filesystem-wide roots ---
		{"find home via tilde", `find ~ -name "*.go"`, true},
		{"find home via $HOME", `find $HOME -name go.mod`, true},
		{"find literal home dir", `find /Users/user -name go.mod`, true},
		{"find /Users", `find /Users -name "*.proto"`, true},
		{"find /usr", `find /usr -name "libfoo*"`, true},
		{"grep -r at root", `grep -rn "svcerr" /`, true},
		{"grep bundled flags at root", `grep -rln "pattern" /var`, true},
		{"grep --recursive at root", `grep --recursive "x" /etc`, true},
		{"rg at root", `rg "svcerr" /`, true},
		{"fd at home", `fd -e go . ~`, true},
		{"ls -R at root", `ls -R /`, true},
		{"root scan behind sudo", `sudo find / -name x`, true},
		{"root scan behind env prefix", `FOO=bar find / -name x`, true},
		{"root scan in second segment", `echo hi && find / -name x`, true},
		{"root scan after cd /", `cd / && find . -name "*.go"`, true},
		{"root scan after cd $HOME", `cd $HOME && rg "pattern"`, true},
		{"root scan in a pipeline", `find / -name "*.go" | head`, true},
		{"root scan in command substitution", `echo $(find / -name x)`, true},

		// --- legitimate, must NOT be refused ---
		{"find in cwd", `find . -name "*.go"`, false},
		{"find in a subdir", `find internal/llm -name "*.go"`, false},
		{"find under an absolute project path", `find /Users/user/src/reliant-labs/reliant -name go.mod`, false},
		{"find under a home subdir", `find ~/src -name go.mod`, false},
		{"find under $HOME subdir", `find $HOME/src -name go.mod`, false},
		{"grep -r in cwd", `grep -rn "svcerr" .`, false},
		{"grep non-recursive on a system file", `grep root /etc/passwd`, false},
		{"rg in cwd", `rg "svcerr"`, false},
		{"ls non-recursive at root", `ls /`, false},
		{"ls -R in a subdir", `ls -R internal`, false},
		{"root path only inside a quoted string of another command", `echo "find / -name x"`, false},
		{"go list instead of scanning", `go list -m -f '{{.Dir}}' github.com/reliant-labs/forge`, false},
		{"relative cd then find", `cd internal && find . -name "*.go"`, false},
		{"git command mentioning slash", `git log --oneline -- /`, false},
		{"build in cwd", `go build ./...`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unscopedSearchRefusal(tt.command)
			if tt.refuse && got == "" {
				t.Fatalf("expected refusal for %q, got none", tt.command)
			}
			if !tt.refuse && got != "" {
				t.Fatalf("expected %q to be allowed, got refusal:\n%s", tt.command, got)
			}
		})
	}
}

// The refusal is only useful if it tells the agent what to do instead. Without
// the alternative the agent has no way to answer the question it was asking.
func TestUnscopedSearchRefusalNamesTheScopedAlternative(t *testing.T) {
	t.Parallel()
	msg := unscopedSearchRefusal(`find / -path "*forge/pkg/svcerr*"`)
	if msg == "" {
		t.Fatal("expected a refusal")
	}
	// With the scoped grep/glob tools removed, the alternative the refusal has to
	// name is a SCOPED shell search — a relative search root — plus the
	// package-manager route for dependency source outside the worktree.
	for _, want := range []string{"rg", "go list -m", "GOMODCACHE"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q; message:\n%s", want, msg)
		}
	}
}

// `rg -r` is --replace, not --recursive. Verified against ripgrep 14.1.1 on a
// file containing "hello world": `rg -rn hello .` prints "./a.txt:n world" and
// exits 0 — right file, right line, matched text replaced by the "n" that -r
// consumed as its argument. Nothing downstream can tell that from a real result,
// which is why this is a refusal and not a documentation note.
func TestRipgrepReplaceRefusal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		command string
		refuse  bool
	}{
		// --- the habit, in the spellings it actually appears in ---
		{"bundled -rn", `rg -rn 'RenderProjectMemory' .`, true},
		{"bundled -rl", `rg -rl 'pattern' internal/`, true},
		{"bundled -rni", `rg -rni 'pattern' .`, true},
		{"separate -r", `rg -r 'pattern' .`, true},
		{"-r after another flag", `rg -n -r 'pattern' .`, true},
		{"trailing -r in a bundle", `rg -nr 'pattern' .`, true},
		{"rg in the second segment", `echo hi && rg -rn 'pattern' .`, true},
		{"rg in a pipeline", `rg -rn 'pattern' . | head`, true},
		{"rg behind an env prefix", `FOO=bar rg -rn 'pattern' .`, true},
		{"rg by absolute path", `/opt/homebrew/bin/rg -rn 'pattern' .`, true},

		// --- deliberate substitution: the escape hatch must stay open ---
		{"long --replace", `rg -o --replace '$1' 'func (\w+)' .`, false},
		{"long --replace with =", `rg --replace='$1' 'func (\w+)' .`, false},

		// --- ordinary searches, must NOT be refused ---
		{"plain search", `rg 'pattern' .`, false},
		{"with line numbers", `rg -n 'pattern' internal/`, false},
		{"files only", `rg -l 'pattern' .`, false},
		{"type filter", `rg -t go 'pattern' .`, false},
		{"case insensitive bundle", `rg -ni 'pattern' .`, false},
		// -e takes the rest of the bundle as its value, so this 'r' is pattern
		// text, not a flag. A substring test for 'r' would refuse it wrongly.
		{"r inside an -e pattern", `rg -erecursive .`, false},
		{"r inside a -t value", `rg -trust 'pattern' .`, false},
		{"pattern containing r after --", `rg -n -- -rn .`, false},
		// grep's -r IS recursive; this guard is about rg only.
		{"grep -rn is untouched", `grep -rn 'pattern' .`, false},
		{"a word ending in rg", `cargo build`, false},
		{"pattern mentioning rg -rn in a string", `echo "rg -rn 'x' ."`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ripgrepReplaceRefusal(tt.command)
			if tt.refuse && got == "" {
				t.Fatalf("expected refusal for %q, got none", tt.command)
			}
			if !tt.refuse && got != "" {
				t.Fatalf("expected %q to be allowed, got refusal:\n%s", tt.command, got)
			}
		})
	}
}

// A refusal that only says "no" costs a turn and teaches nothing. This one has
// to name the correct search AND the long spelling, so an agent that genuinely
// wanted substitution is redirected rather than blocked.
func TestRipgrepReplaceRefusalNamesBothWaysOut(t *testing.T) {
	t.Parallel()
	msg := ripgrepReplaceRefusal(`rg -rn 'RenderProjectMemory' .`)
	if msg == "" {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"--replace", "rg 'pattern' .", "recurses"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q; message:\n%s", want, msg)
		}
	}
}

// The guard must be reachable from the shell tool itself, not merely exist.
// Execute returns the refusal as a tool error before touching the daemon, which
// is why a nil-daemon context still has to be rejected first.
func TestShellToolRefusesFilesystemWideScan(t *testing.T) {
	t.Parallel()
	if unscopedSearchRefusal(`find / -name "*.go"`) == "" {
		t.Fatal("guard did not fire on the canonical offender")
	}
	desc := shellDescriptionCommon()
	if !strings.Contains(desc, "NEVER SCAN THE FILESYSTEM") {
		t.Error("shell description does not warn against filesystem scans")
	}
	if strings.Contains(desc, `grep -rn "pattern" .`) {
		t.Error("shell description still instructs the model to search via shell grep")
	}
}
