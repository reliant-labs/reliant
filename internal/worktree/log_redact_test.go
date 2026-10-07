package worktree

import (
	"strings"
	"testing"
)

func TestGitOutputForLog_StripsURLCredentials(t *testing.T) {
	t.Parallel()
	out := []byte("remote: Permission denied\nfatal: unable to access 'https://x-access-token:ghs_SECRET123@github.com/acme/repo.git/': 403\n")
	got := gitOutputForLog(out)
	if strings.Contains(got, "ghs_SECRET123") || strings.Contains(got, "x-access-token") {
		t.Fatalf("credential leaked into log output: %q", got)
	}
	if !strings.Contains(got, "https://github.com/acme/repo.git/") {
		t.Fatalf("host/path should survive redaction: %q", got)
	}
}

func TestGitOutputForLog_IsBounded(t *testing.T) {
	t.Parallel()
	got := gitOutputForLog([]byte(strings.Repeat("a", maxGitOutputForLog*3)))
	if len(got) != maxGitOutputForLog {
		t.Fatalf("len = %d, want %d", len(got), maxGitOutputForLog)
	}
}
