// Copyright (c) 2026 Reliant Labs
//go:build linux

package credsandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The deprecated-readable tier in a real sandbox: the sign-ins stay readable
// to a credentials-none command, its stderr names the ones that exist (and
// only those) before it starts, and its stdout, which a step may parse, carries
// nothing but its own output. Claude's sign-in, in the hidden tier, is absent.
func TestSandbox_DeprecatedReadableSignInsStayReadableWithANotice(t *testing.T) {
	requireSandbox(t)
	home := fakeHome(t)
	write := func(rel, content string) {
		p := filepath.Join(home, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	write(".gemini/oauth_creds.json", "gemini-signed-in")
	write(".config/cursor/auth.json", "cursor-signed-in")
	write(".claude/.credentials.json", "PAT-claude")

	plan := planFor(home)
	plan.Hide = append(plan.Hide, filepath.Join(home, ".claude"))
	gemini, cursor := filepath.Join(home, ".gemini"), filepath.Join(home, ".config", "cursor")
	plan.DeprecatedReadable = []string{gemini, filepath.Join(home, ".cursor"), cursor, filepath.Join(home, ".copilot")}

	cmd := exec.Command("bash", "-c", `
cat "$HOME/.gemini/oauth_creds.json"; echo
cat "$HOME/.config/cursor/auth.json"; echo
cat "$HOME/.claude/.credentials.json" 2>/dev/null && echo LEAK
echo done`)
	cmd.Dir = home
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home} //nolint:forbidigo // the test's own PATH for the command under test
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.Empty(t, Apply(cmd, selfPath(t), plan))
	require.NoError(t, cmd.Run(), "stdout: %s\nstderr: %s", stdout.String(), stderr.String())

	assert.Equal(t, "gemini-signed-in\ncursor-signed-in\ndone\n", stdout.String(),
		"readable this release, and stdout is the command's alone")
	errOut := strings.TrimSuffix(stderr.String(), coverageWarning)
	assert.Equal(t, DeprecatedReadableNotice([]string{gemini, cursor}), errOut,
		"one line, naming only the paths that exist")
}
