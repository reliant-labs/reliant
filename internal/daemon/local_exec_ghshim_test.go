// Copyright (c) 2025 Reliant Labs
package daemon

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/ghshim"
)

// A chat's `gh pr create` names the chat in the pull request body on every
// credentials path RunCommand has. credentials: none rebuilds the environment
// from an allowlist and runs the command in the credential sandbox (where the
// machine allows it), and the shim must still be first on PATH after both, or
// the pull request goes out unmarked.
func TestRunCommand_GhShimMarksThePullRequest(t *testing.T) {
	testkit.Slow(t, "runs real shell commands, and the credential sandbox, through the gh shim")
	if runtime.GOOS == "windows" {
		t.Skip("the gh shim is a POSIX shell script")
	}
	const chat = "11111111-2222-4333-8444-555555555555"

	// The shim execs this test binary (TestMain calls ghshim.Dispatch). A
	// link or copy under this test's temp directory, which a credentials-none
	// command's private /tmp keeps; the go build directory is not kept.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "reliant")
	if err := os.Link(self, bin); err != nil {
		copyExecutable(t, self, bin)
	}
	s, err := ghshim.Install(t.TempDir(), bin)
	if err != nil {
		t.Fatal(err)
	}
	prev := ghshim.Default()
	ghshim.SetDefault(s)
	t.Cleanup(func() { ghshim.SetDefault(prev) })

	// A gh that prints its arguments and the --body-file it was given.
	ghDir := t.TempDir()
	fakeGh := `#!/bin/sh
printf 'ARGS:%s\n' "$*"
prev=
for a in "$@"; do
  if [ "$prev" = "--body-file" ]; then printf 'BODY:'; cat "$a"; printf '\n'; fi
  prev=$a
done
`
	if err := os.WriteFile(filepath.Join(ghDir, "gh"), []byte(fakeGh), 0o755); err != nil {
		t.Fatal(err)
	}

	// Under this test's temp directory too: a credentials-none command's
	// private /tmp keeps only the top directory holding its working directory,
	// and a subtest's t.TempDir() is another one.
	work := t.TempDir()
	for _, credentials := range []string{"", daemonpolicy.CredentialsNone} {
		t.Run("credentials="+credentials, func(t *testing.T) {
			res, err := NewLocalClient().RunCommand(context.Background(), &RunCommandRequest{
				Command:    `gh pr create --title t --body 'Fixes #1.'`,
				WorkingDir: work,
				Env: map[string]string{
					ghshim.ChatIDEnv: chat,
					"PATH":           ghDir + string(os.PathListSeparator) + os.Getenv("PATH"), //nolint:forbidigo // the test's own PATH for the command under test
				},
				Credentials: credentials,
				TimeoutMs:   60000,
			})
			if err != nil {
				t.Fatalf("RunCommand: %v", err)
			}
			if res.ExitCode != 0 {
				t.Fatalf("exit %d:\n%s", res.ExitCode, res.Combined)
			}
			want := "BODY:Fixes #1.\n\n<!-- reliant-chat: " + chat + " -->"
			if !strings.Contains(res.Combined, want) {
				t.Fatalf("the body gh got must name the chat (%q):\n%s", want, res.Combined)
			}
		})
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	src, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close() //nolint:errcheck // read-only
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
