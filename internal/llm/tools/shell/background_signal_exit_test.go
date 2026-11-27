// Copyright (c) 2026 Reliant Labs
//go:build !windows

package shell

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const strangerSignalNote = "[reliant] This background process "

// runToEnd starts command through a fresh manager, lets act end it, and
// returns the process once it has finished, with its stderr.
func runToEnd(t *testing.T, command string, act func(m *BackgroundManager, p *BackgroundProcess)) (*BackgroundProcess, string) {
	t.Helper()
	m := &BackgroundManager{processes: map[string]*BackgroundProcess{}}
	p, err := m.StartProcess(context.Background(), StartProcessOptions{Command: command, WorkingDir: t.TempDir()})
	require.NoError(t, err)
	pgid := p.cmd.Process.Pid
	t.Cleanup(func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
	if act != nil {
		act(m, p)
	}
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
		t.Fatalf("process %s did not finish", p.ID)
	}
	_, stderr, err := m.GetOutput(p.ID)
	require.NoError(t, err)
	return p, stderr
}

// reliant#2751: a background command something else on the machine killed
// read "failed, exit code -1", its output stopping mid-run, with nothing to
// say it was killed and needs a rerun. Its output now says so.
func TestBackgroundProcess_KilledByAStrangerSaysSo(t *testing.T) {
	p, stderr := runToEnd(t, "sleep 30", func(_ *BackgroundManager, p *BackgroundProcess) {
		require.NoError(t, syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM))
	})
	live := p.Live()
	assert.Equal(t, "failed", live.Status)
	require.NotNil(t, live.ExitCode)
	assert.Equal(t, -1, *live.ExitCode, "the exit code is unchanged; the note is the news")
	assert.Contains(t, stderr, strangerSignalNote+"was ended by signal 15 (terminated)")
	assert.Contains(t, stderr, "re-run it")
}

// The heavy-slot wrapper and a shell report a child's death the shell way,
// 128+N, and that reads as the signal too.
func TestBackgroundProcess_ShellReportedSignalSaysSo(t *testing.T) {
	p, stderr := runToEnd(t, `sh -c 'kill -TERM $$'; exit $?`, nil)
	live := p.Live()
	require.NotNil(t, live.ExitCode)
	assert.Equal(t, 143, *live.ExitCode)
	assert.Contains(t, stderr, strangerSignalNote+"exited with code 143, which is how a shell reports a command ended by signal 15 (terminated)")
}

// A stop this daemon makes is not a stranger's: shell_kill and the reaper
// record their own outcome.
func TestBackgroundProcess_OurOwnKillAddsNoStrangerNote(t *testing.T) {
	p, stderr := runToEnd(t, "sleep 30", func(m *BackgroundManager, p *BackgroundProcess) {
		require.NoError(t, m.KillProcess(p.ID))
	})
	assert.Equal(t, "killed", p.StatusSafe())
	assert.NotContains(t, stderr, strangerSignalNote)
}

// A command's own exit code is its own verdict, a high one included unless it
// is a shell's report of a stopping signal.
func TestBackgroundProcess_OwnExitCodeAddsNoNote(t *testing.T) {
	for _, code := range []string{"3", "200"} {
		_, stderr := runToEnd(t, "exit "+code, nil)
		assert.NotContains(t, stderr, strangerSignalNote, "exit %s", code)
	}
}
