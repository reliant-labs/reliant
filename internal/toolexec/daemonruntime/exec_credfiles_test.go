// Copyright (c) 2025 Reliant Labs
//go:build linux

package daemonruntime

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/credsandbox"
	"github.com/reliant-labs/reliant/internal/daemon"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/llm/tools/shell"
)

// The 2026-10-25 review (H2): `credentials: none` removed credential variables and
// left the daemon's credential FILES readable by the command. These are the
// files, through the daemon's real exec.run and fs.* handlers.

const (
	credDaemonPAT = "PAT-daemon-json-secret"
	credForgePAT  = "PAT-forge-credentials-secret"
)

// credHome points HOME at a directory laid out like a daemon's and returns it.
func credHome(t *testing.T) string {
	t.Helper()
	if !credsandbox.Enabled() {
		t.Skip("the credentials sandbox is off in this test binary (this host does not allow it)")
	}
	home := t.TempDir()
	for rel, content := range map[string]string{
		".reliant/daemon.json":            credDaemonPAT,
		".reliant/forge-token-cache.json": "PAT-cache-secret",
		".config/forge/credentials.json":  credForgePAT,
		".git-credentials":                "https://x:PAT-git-secret@github.com",
		".config/forge/storage.json":      "storage",
		"project/main.go":                 "package main",
	} {
		p := filepath.Join(home, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("FORGE_HOME", "")
	return home
}

func TestExecRun_CredentialsNone_CannotReadTheDaemonsCredentialFiles(t *testing.T) {
	home := credHome(t)

	script := `
cd "$HOME/project"
for f in "$HOME/.reliant/daemon.json" "$HOME/.reliant/forge-token-cache.json" "$HOME/.config/forge/credentials.json" "$HOME/.git-credentials"; do
  cat "$f" 2>&1
done
grep -r PAT- "$HOME" 2>/dev/null
find "$HOME" -name 'daemon.json' -o -name 'credentials.json' 2>/dev/null
echo "can still read: $(cat $HOME/project/main.go) $(cat $HOME/.config/forge/storage.json)"`

	res := runExec(t, daemon.RunCommandRequest{Command: script, WorkingDir: filepath.Join(home, "project"), Credentials: "none"})
	require.Equal(t, 0, res.ExitCode, res.Combined)
	assert.NotContains(t, res.Combined, "PAT-", "no credential content reaches a credentials-none command")
	assert.Equal(t, 4, strings.Count(res.Combined, "No such file or directory"), "each credential file is absent: %s", res.Combined)
	assert.NotContains(t, res.Combined, "Permission denied", "absent, not merely unreadable: a chmod would not hide a file from the daemon's own user")
	assert.Contains(t, res.Combined, "can still read: package main storage")

	// And the default is unchanged: an ordinary command is the user's own agent.
	res = runExec(t, daemon.RunCommandRequest{Command: `cat "$HOME/.reliant/daemon.json"`, WorkingDir: home})
	assert.Equal(t, credDaemonPAT, res.Stdout, "a command without credentials: none still sees the daemon's own files")
}

func TestExecRun_CredentialsNone_CannotReachTheContainerEngine(t *testing.T) {
	credHome(t)
	sock := "/var/run/docker.sock"
	if _, err := os.Lstat(sock); err != nil {
		t.Skip("no container engine socket on this machine")
	}
	res := runExec(t, daemon.RunCommandRequest{
		Command: `python3 - <<'EOF'
import socket
s = socket.socket(socket.AF_UNIX)
try:
    s.connect("/var/run/docker.sock"); print("CONNECTED")
except OSError as e:
    print("refused:", e.strerror)
EOF`,
		WorkingDir:  t.TempDir(),
		Credentials: "none",
	})
	assert.NotContains(t, res.Combined, "CONNECTED", res.Combined)
}

func TestExecBGStart_CredentialsNone_CannotReadTheDaemonsCredentialFiles(t *testing.T) {
	home := credHome(t)

	payload, _ := json.Marshal(daemon.RunCommandRequest{
		Command: `cat "$HOME/.reliant/daemon.json" "$HOME/.config/forge/credentials.json" 2>&1; echo done`, WorkingDir: home, Credentials: "none"})
	raw, err := handleExecBGStart(context.Background(), payload)
	require.NoError(t, err)
	var started struct {
		ProcessID string `json:"process_id"`
	}
	require.NoError(t, json.Unmarshal(raw, &started))

	out := waitForBGOutput(t, started.ProcessID)
	assert.NotContains(t, out, "PAT-")
	assert.Contains(t, out, "done")
}

// A sandbox that cannot start must not fall back to running the command bare: the
// command is not run, and the result says why.
func TestExecRun_CredentialsNone_RefusesRatherThanRunUnsandboxed(t *testing.T) {
	home := credHome(t)
	marker := filepath.Join(home, "project", "ran")

	self, err := os.Executable()
	require.NoError(t, err)
	broken := filepath.Join(t.TempDir(), "reliant-helper-moved-away")
	require.NoError(t, os.Symlink(self, broken))
	require.NoError(t, os.Remove(broken)) // a dangling link: the helper binary is gone

	cmd := exec.Command("bash", "-c", "touch "+marker)
	refusal := credsandbox.Apply(cmd, broken, credsandbox.DefaultPlan())
	assert.Contains(t, refusal, "not run")
	assert.Contains(t, refusal, "credential files")
	assert.Nil(t, cmd.Process, "Apply never starts the command; the caller must see the refusal and not start it")
	_, statErr := os.Stat(marker)
	assert.True(t, os.IsNotExist(statErr), "the command did not run")
}

func TestFileTools_RefuseTheDaemonsCredentialFiles(t *testing.T) {
	home := credHome(t)
	ctx := context.Background()

	for _, rel := range []string{".reliant/daemon.json", ".reliant/forge-token-cache.json", ".config/forge/credentials.json"} {
		path := filepath.Join(home, rel)

		_, err := handleFSReadFile(ctx, mustJSON(t, map[string]any{"path": path}))
		require.ErrorIs(t, err, daemonpolicy.ErrCredentialFile, "fs.read_file %s", rel)

		_, err = handleFSReadBinaryFile(ctx, mustJSON(t, map[string]any{"path": path}))
		require.ErrorIs(t, err, daemonpolicy.ErrCredentialFile, "fs.read_binary_file %s", rel)

		_, err = handleFSWriteFile(ctx, mustJSON(t, map[string]any{"path": path, "content": "x"}))
		require.ErrorIs(t, err, daemonpolicy.ErrCredentialFile, "fs.write_file %s", rel)

		_, err = handleFSDelete(ctx, mustJSON(t, map[string]any{"path": path}))
		require.ErrorIs(t, err, daemonpolicy.ErrCredentialFile, "fs.delete %s", rel)

		_, err = handleFSCopy(ctx, mustJSON(t, map[string]any{"source": path, "destination": filepath.Join(home, "project", "stolen")}))
		require.ErrorIs(t, err, daemonpolicy.ErrCredentialFile, "fs.copy from %s", rel)
	}
	got, err := os.ReadFile(filepath.Join(home, ".reliant/daemon.json"))
	require.NoError(t, err)
	assert.Equal(t, credDaemonPAT, string(got), "the refused write left the file alone")

	// A symlink in the workspace to the credential file is the credential file.
	link := filepath.Join(home, "project", "innocent.txt")
	require.NoError(t, os.Symlink(filepath.Join(home, ".reliant/daemon.json"), link))
	_, err = handleFSReadFile(ctx, mustJSON(t, map[string]any{"path": link}))
	require.ErrorIs(t, err, daemonpolicy.ErrCredentialFile, "a link to it")

	// Ordinary files are unaffected.
	raw, err := handleFSReadFile(ctx, mustJSON(t, map[string]any{"path": filepath.Join(home, "project/main.go")}))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "package main")
}

func TestFileTools_SearchAndGlobNeverListTheDaemonsCredentialFiles(t *testing.T) {
	home := credHome(t)
	ctx := context.Background()

	raw, err := handleFSSearch(ctx, mustJSON(t, map[string]any{
		"pattern": "PAT-",
		"opts":    map[string]any{"base_dir": home, "output_mode": "content"},
	}))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), credDaemonPAT)
	assert.NotContains(t, string(raw), credForgePAT)
	assert.NotContains(t, string(raw), "daemon.json")

	raw, err = handleFSGlob(ctx, mustJSON(t, map[string]any{
		"pattern": "**/*.json",
		"opts":    map[string]any{"base_dir": home, "include_ignored": true},
	}))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "daemon.json")
	assert.NotContains(t, string(raw), "credentials.json")
	assert.True(t, strings.Contains(string(raw), "storage.json"), "other files are still found: %s", raw)
}

// waitForBGOutput returns a background process's output once it has finished.
func waitForBGOutput(t *testing.T, id string) string {
	t.Helper()
	mgr := shell.GetBackgroundManager()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, complete, _, err := mgr.GetProcessStatus(id)
		require.NoError(t, err)
		if complete {
			break
		}
		require.True(t, time.Now().Before(deadline), "process %s still running after 30s", id)
		time.Sleep(20 * time.Millisecond)
	}
	lines, err := mgr.GetCombinedOutput(id)
	require.NoError(t, err)
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l.Text + "\n")
	}
	return sb.String()
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
