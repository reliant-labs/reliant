// Copyright (c) 2025 Reliant Labs
//go:build !windows

package terminal

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"

	"github.com/reliant-labs/reliant/internal/credsandbox"
)

// TestMain lets the credential sandbox's helper run from this test binary
// (credsandbox.Dispatch), and turns the sandbox off on a host that does not
// allow it, where a credentials-none terminal keeps the environment half.
func TestMain(m *testing.M) {
	credsandbox.DispatchForTest()
	os.Exit(m.Run())
}

// runInShell types line into a new session's shell and returns its output
// once it contains want, failing after a deadline: a shell that cannot read
// its terminal (stopped in a background process group) never answers.
func runInShell(t *testing.T, opts SessionOptions, line, want string) string {
	t.Helper()
	m := NewManager()
	t.Cleanup(m.Cleanup)
	s, err := m.CreateSessionWith(t.TempDir(), "", opts)
	if err != nil {
		t.Fatalf("CreateSessionWith: %v", err)
	}
	if err := m.Write(s.ID, []byte(line+"\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := make(chan string, 1)
	go func() {
		var b strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := s.PTY.Read(buf)
			b.Write(buf[:n])
			if strings.Contains(b.String(), want) || err != nil {
				out <- b.String()
				return
			}
		}
	}()
	select {
	case got := <-out:
		return got
	case <-time.After(20 * time.Second):
		t.Fatalf("the shell never printed %q", want)
	}
	return ""
}

// A terminal an org's forced `credentials: none` stamps runs its shell with
// no credential in its environment, interactively, in the sandbox where the
// host allows it; an unstamped one is today's.
func TestCreateSessionWith_CredentialsNone(t *testing.T) {
	testkit.Slow(t, "starts interactive shells on PTYs, one in the credential sandbox")
	t.Setenv("GH_TOKEN", "ghp_terminal_test_token_value")
	const line = `echo "MARK=[${GH_TOKEN}]-$((40+2))"`

	forced := runInShell(t, SessionOptions{CredentialsNone: true}, line, "MARK=[]-42")
	if strings.Contains(forced, "ghp_terminal_test_token_value") {
		t.Fatalf("the credentials-none shell saw the token: %q", forced)
	}
	// Where the sandbox is in force the shell is in a PID namespace of its
	// own, the sandbox's init its parent (pid 1): it answered, so it runs in
	// the terminal's foreground rather than stopped on SIGTTIN.
	if self, err := os.Executable(); err == nil && credsandbox.Enabled() && credsandbox.Available(self) == nil {
		runInShell(t, SessionOptions{CredentialsNone: true}, `echo "PPID=[$PPID]"`, "PPID=[1]")
	}

	today := runInShell(t, SessionOptions{}, line, "MARK=[ghp_terminal_test_token_value]-42")
	if !strings.Contains(today, "MARK=[ghp_terminal_test_token_value]-42") {
		t.Fatalf("an unstamped shell must keep today's environment: %q", today)
	}
}

func TestCredentialsNoneShellEnv(t *testing.T) {
	env := credentialsNoneShellEnv([]string{"PATH=/bin", "HOME=/h", "GH_TOKEN=x", "OPENAI_API_KEY=sk-x", "TERM=xterm-256color"})
	joined := strings.Join(env, "\n")
	for _, gone := range []string{"GH_TOKEN=x", "OPENAI_API_KEY"} {
		if strings.Contains(joined, gone) {
			t.Errorf("%s survived: %v", gone, env)
		}
	}
	for _, kept := range []string{"PATH=/bin", "HOME=/h", "TERM=xterm-256color", "PROMPT_EOL_MARK="} {
		if !strings.Contains(joined, kept) {
			t.Errorf("%s missing: %v", kept, env)
		}
	}
}
