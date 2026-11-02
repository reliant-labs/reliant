// Copyright (c) 2026 Reliant Labs
package shell

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/reliant/internal/logging"
)

// The idle reaper stops background processes that the chats which started
// them have left behind.
//
// Nothing else ever stops one: a background process is workspace-scoped, so
// any chat may read or kill it, and it ends only when it exits, is killed, or
// the daemon restarts. On 2026-11-02 a shared machine held ~10 `vite preview`
// / `serve` trees (node + esbuild, ~250 threads together), 2-24 hours old,
// started by chats that had long finished.
//
// A process is reaped when ALL of these hold:
//
//   - this daemon started it for an agent (a chat id is recorded), and it is
//     not keep_alive;
//   - it has been idle for the window (DefaultIdleReap): no output, and no
//     agent touched it (shell_output, shell_wait, shell_list showing it,
//     shell_kill — Touch);
//   - the server said, within VerdictTTL, that the chat which started it has
//     no run in progress.
//
// The chat's state lives on the server, not here. It arrives on exec.bg_status,
// the server reconciler's 30-second poll of every backgrounded tool call's
// process (RecordChatVerdicts), so knowing it costs no extra round trip. No
// fresh verdict — the server is unreachable, the daemon is offline, the
// process has no tool call behind it — means no reap: unknown is not finished.
//
// A process whose workspace was archived is reaped at once, idle or not and
// keep_alive or not: its checkout is gone or going. That word comes the same
// way (ChatVerdict.WorkspaceArchived), and the daemon's own worktree removal
// stops what runs in the directory it removes (StopProcessesUnder).
//
// Reaping is KillProcess: SIGTERM to the whole process group, SIGKILL after
// KillGraceTimeout, then the command's cgroup. A note in the process's output
// says why, so shell_output explains the kill.

// DefaultIdleReap is how long a finished chat's background process may sit
// idle before the reaper stops it.
const DefaultIdleReap = 4 * time.Hour

const (
	// defaultReapInterval is how often the reaper looks.
	defaultReapInterval = 5 * time.Minute
	// defaultVerdictTTL is how long the server's word on a chat stays good.
	// The reconciler repeats it every 30 seconds; a verdict this old means
	// the server stopped asking, and what it last said may no longer hold.
	defaultVerdictTTL = 10 * time.Minute
)

// ReaperConfig is the idle reaper's policy.
type ReaperConfig struct {
	// IdleAfter is the idle window; zero or negative turns idle reaping off
	// (an archived workspace's processes are still stopped).
	IdleAfter time.Duration
	// Interval is how often the reaper looks; zero is defaultReapInterval.
	Interval time.Duration
	// VerdictTTL bounds the age of a chat verdict the reaper acts on; zero
	// is defaultVerdictTTL.
	VerdictTTL time.Duration
}

func (c ReaperConfig) withDefaults() ReaperConfig {
	if c.Interval <= 0 {
		c.Interval = defaultReapInterval
	}
	if c.VerdictTTL <= 0 {
		c.VerdictTTL = defaultVerdictTTL
	}
	if c.IdleAfter < 0 {
		c.IdleAfter = 0
	}
	return c
}

// ChatVerdict is the server's word on the chat that started a process.
type ChatVerdict struct {
	// Running: the chat has a run in progress (pending or active).
	Running bool
	// WorkspaceArchived: the workspace the process was started in is
	// archived or deleted.
	WorkspaceArchived bool
}

func (m *BackgroundManager) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

// OutputActivity is when a command last showed life: wrote any output, or an
// agent used it. A foreground command's output writers stamp one from the
// start (Writer), so a command detached into the background
// (AdoptRunningProcess) keeps the clock its output has been setting. Safe for
// concurrent use; the zero of a nil *OutputActivity is "never active".
//
// Its lock is also the reaper's claim (claimReap): a touch or a write lands
// either before the reaper's last look, which then sees it, or after the
// process is committed to being stopped.
type OutputActivity struct {
	clock func() time.Time
	mu    sync.Mutex
	nanos int64 // guarded by mu
	// reaping: the reaper has committed to stopping the process. Guarded by mu.
	reaping bool
}

// NewOutputActivity starts an activity clock reading clock (nil: time.Now),
// active now.
func NewOutputActivity(clock func() time.Time) *OutputActivity {
	if clock == nil {
		clock = time.Now
	}
	a := &OutputActivity{clock: clock}
	a.Touch()
	return a
}

// Touch records activity now.
func (a *OutputActivity) Touch() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.nanos = a.clock().UnixNano()
	a.mu.Unlock()
}

// At is when the activity was last recorded.
func (a *OutputActivity) At() time.Time {
	if a == nil {
		return time.Time{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.atLocked()
}

func (a *OutputActivity) atLocked() time.Time {
	return time.Unix(0, a.nanos).UTC()
}

// Writer wraps w so that every non-empty write — a whole line or a progress
// dot — records activity.
func (a *OutputActivity) Writer(w io.Writer) io.Writer {
	return activityWriter{w: w, activity: a}
}

type activityWriter struct {
	w        io.Writer
	activity *OutputActivity
}

func (aw activityWriter) Write(p []byte) (int, error) {
	if len(p) > 0 {
		aw.activity.Touch()
	}
	return aw.w.Write(p)
}

// Touch records that an agent used these processes (read their output,
// waited on them, listed them, killed them), resetting their idle time.
// Unknown ids are ignored. Never call it for a poll nobody asked for — the
// reconciler's, a UI's — or nothing would ever be idle.
func (m *BackgroundManager) Touch(processIDs ...string) {
	m.TouchForGrant("", processIDs...)
}

// TouchForGrant is Touch for a caller confined to grantID: it reaches only
// that grant's processes (see GetProcessForGrant; "" reaches all). It reads
// the registry alone — no port scan, unlike GetProcess — so touching every
// process a listing showed stays cheap.
func (m *BackgroundManager) TouchForGrant(grantID string, processIDs ...string) {
	m.mu.RLock()
	touched := make([]*BackgroundProcess, 0, len(processIDs))
	for _, id := range processIDs {
		if p, ok := m.processes[id]; ok && (grantID == "" || p.GrantID == grantID) {
			touched = append(touched, p)
		}
	}
	m.mu.RUnlock()
	for _, p := range touched {
		p.activity.Touch()
	}
}

// RecordChatVerdicts records the server's word on the chats that started
// these processes (keyed by process id). A verdict that a workspace was
// archived wakes the reaper at once.
func (m *BackgroundManager) RecordChatVerdicts(verdicts map[string]ChatVerdict) {
	now := m.now()
	archived := false
	m.mu.RLock()
	for id, v := range verdicts {
		p, ok := m.processes[id]
		if !ok {
			continue
		}
		p.outputMu.Lock()
		p.chat, p.chatAt = v, now
		archived = archived || (v.WorkspaceArchived && p.Status == "running")
		p.outputMu.Unlock()
	}
	m.mu.RUnlock()
	if archived {
		m.kickReaper()
	}
}

func (m *BackgroundManager) kickReaper() {
	select {
	case m.reapKick <- struct{}{}:
	default:
	}
}

// IdleReapWindow is the idle window the running reaper uses; zero when idle
// reaping is off or no reaper runs.
func (m *BackgroundManager) IdleReapWindow() time.Duration {
	return time.Duration(m.idleReap.Load())
}

// ReapOutlook says how the idle reaper sees one process, for listings.
type ReapOutlook struct {
	// LastActive is when it last wrote output or an agent touched it.
	LastActive time.Time
	// StopsAfter is the earliest the reaper may stop it if nothing touches
	// it: LastActive plus the idle window, and only once its chat has no run
	// in progress. Nil when idle reaping never applies; Exempt says why.
	StopsAfter *time.Time
	Exempt     string
	// ChatRunning is the server's latest fresh word on its chat; nil when
	// there is none.
	ChatRunning *bool
}

// ReapOutlook reports how the idle reaper sees process.
func (m *BackgroundManager) ReapOutlook(process *BackgroundProcess) ReapOutlook {
	window := m.IdleReapWindow()
	now := m.now()
	// Activity before outputMu: a reap claim takes them in that order.
	out := ReapOutlook{LastActive: process.activity.At()}
	process.outputMu.RLock()
	defer process.outputMu.RUnlock()
	if !process.chatAt.IsZero() && now.Sub(process.chatAt) <= defaultVerdictTTL {
		running := process.chat.Running
		out.ChatRunning = &running
	}
	switch {
	case process.activity == nil || process.cmd == nil:
		out.Exempt = "not started by this daemon"
	case process.ChatID == "":
		out.Exempt = "not started by a chat"
	case process.KeepAlive:
		out.Exempt = "keep_alive"
	case window <= 0:
		out.Exempt = "idle reaping is off"
	default:
		at := out.LastActive.Add(window)
		out.StopsAfter = &at
	}
	return out
}

// reapReason says why process should be reaped now, or "" to keep it.
func (m *BackgroundManager) reapReason(process *BackgroundProcess, now time.Time, cfg ReaperConfig) string {
	lastActive := process.activity.At()
	process.outputMu.RLock()
	defer process.outputMu.RUnlock()
	return reapReasonLocked(process, now, cfg, lastActive)
}

// reapReasonLocked is reapReason for a caller holding process.outputMu, given
// when the process was last active.
func reapReasonLocked(process *BackgroundProcess, now time.Time, cfg ReaperConfig, lastActive time.Time) string {
	// Only what this daemon started and still controls: a process recovered
	// from the database, or one that has ended, is not the reaper's.
	if process.Status != "running" || process.cmd == nil || process.cancelFunc == nil || process.activity == nil {
		return ""
	}
	fresh := !process.chatAt.IsZero() && now.Sub(process.chatAt) <= cfg.VerdictTTL
	if fresh && process.chat.WorkspaceArchived {
		return "its workspace was archived"
	}
	if process.ChatID == "" || process.KeepAlive || cfg.IdleAfter <= 0 {
		return ""
	}
	idle := now.Sub(lastActive)
	if idle < cfg.IdleAfter || !fresh || process.chat.Running {
		return ""
	}
	return fmt.Sprintf(idleReasonPrefix+"%s (no output, and no agent read, waited on, listed or killed it) and chat %s has no run in progress",
		idle.Round(time.Minute), process.ChatID)
}

// idleReasonPrefix starts every idle reap's reason.
const idleReasonPrefix = "idle for "

// ReapIdle runs one reaper pass and returns the ids it stopped.
func (m *BackgroundManager) ReapIdle(cfg ReaperConfig) []string {
	cfg = cfg.withDefaults()
	now := m.now()
	m.mu.RLock()
	reasons := map[*BackgroundProcess]string{}
	for _, p := range m.processes {
		if reason := m.reapReason(p, now, cfg); reason != "" {
			reasons[p] = reason
		}
	}
	m.mu.RUnlock()
	// Each candidate is judged again as it is claimed: output, a touch or a
	// "running" verdict that landed since the scan above must win.
	return m.reapAll(reasons, func(p *BackgroundProcess, lastActive time.Time) string {
		return reapReasonLocked(p, m.now(), cfg, lastActive)
	})
}

// StopProcessesUnder stops every running process this daemon started whose
// working directory is dir or inside it — the daemon is about to remove dir
// (a workspace being archived or deleted). Returns how many it stopped.
func (m *BackgroundManager) StopProcessesUnder(dir, why string) int {
	if dir == "" {
		return 0
	}
	root := filepath.Clean(dir)
	m.mu.RLock()
	reasons := map[*BackgroundProcess]string{}
	for _, p := range m.processes {
		p.outputMu.RLock()
		mine := p.Status == "running" && p.cmd != nil && p.cancelFunc != nil && within(root, p.WorkingDir)
		p.outputMu.RUnlock()
		if mine {
			reasons[p] = why
		}
	}
	m.mu.RUnlock()
	// Whatever the process did since, the directory is going: only one
	// that has ended meanwhile is spared.
	return len(m.reapAll(reasons, func(p *BackgroundProcess, _ time.Time) string {
		if p.Status != "running" {
			return ""
		}
		return why
	}))
}

// within reports whether path is root or below it.
func within(root, path string) bool {
	if path == "" {
		return false
	}
	rel, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Test hooks, nil in production. beforeReap runs for each candidate between
// the scan that chose it and its claim; duringReapClaim runs inside a claim
// that committed, with the claim's locks held.
var (
	beforeReap      func(*BackgroundProcess)
	duringReapClaim func(*BackgroundProcess)
)

// claimReap commits process to being reaped if decide — run while activity
// (output, touches) and the chat verdict are held still — still gives a
// reason, and returns it ("" spares the process). So an update lands either
// before the claim, and decide sees it, or after the process is committed to
// being stopped; and a process is claimed once, whichever path claims it.
func (p *BackgroundProcess) claimReap(decide func(p *BackgroundProcess, lastActive time.Time) string) string {
	a := p.activity
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p.outputMu.Lock()
	defer p.outputMu.Unlock()
	if a.reaping {
		return ""
	}
	reason := decide(p, a.atLocked())
	if reason != "" {
		a.reaping = true
		if duringReapClaim != nil {
			duringReapClaim(p)
		}
	}
	return reason
}

// reapAll stops each process, concurrently: each kill may wait out
// KillGraceTimeout, and the processes are independent. Each is claimed first
// (claimReap with decide), which spares a process whose circumstances changed
// since the scan that chose it.
func (m *BackgroundManager) reapAll(candidates map[*BackgroundProcess]string, decide func(*BackgroundProcess, time.Time) string) []string {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		reaped []string
	)
	for p := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if beforeReap != nil {
				beforeReap(p)
			}
			reason := p.claimReap(decide)
			if reason == "" {
				logging.Debug("Background process no longer reapable; sparing it", "id", p.ID)
				return
			}
			if m.reap(p, reason) {
				mu.Lock()
				reaped = append(reaped, p.ID)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return reaped
}

// reap stops one claimed process, saying why in the daemon log and, once
// it has stopped, in its output.
func (m *BackgroundManager) reap(process *BackgroundProcess, reason string) bool {
	idle := m.now().Sub(process.activity.At())
	logging.Info("Reaping background process",
		"id", process.ID, "chat_id", process.ChatID, "thread_id", process.ThreadID,
		"command", process.Command, "working_dir", process.WorkingDir, "pid", process.GetPID(),
		"idle", idle.Round(time.Second).String(), "reason", reason)
	if err := m.KillProcess(process.ID); err != nil {
		logging.Warn("Failed to reap background process", "id", process.ID, "error", err)
		return false
	}

	note := "[reliant] Stopped this background process: " + reason + "."
	if strings.HasPrefix(reason, idleReasonPrefix) {
		note += " Start a server that must outlive its chat with keep_alive: true."
	}
	process.outputMu.Lock()
	defer process.outputMu.Unlock()
	process.outputSeq++
	process.combinedOutput = append(process.combinedOutput, OutputLineWithSeq{Type: "stderr", Text: note, Sequence: process.outputSeq})
	// The stderr buffer only once the command has stopped writing to it: an
	// adopted command's own writers fill it without outputMu.
	select {
	case <-process.done:
		process.stderr.WriteString(note + "\n")
	default:
	}
	return true
}

// RunReaper reaps on cfg.Interval, and at once when a workspace is archived,
// until ctx ends.
func (m *BackgroundManager) RunReaper(ctx context.Context, cfg ReaperConfig) {
	cfg = cfg.withDefaults()
	m.idleReap.Store(int64(cfg.IdleAfter))
	defer m.idleReap.Store(0)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-m.reapKick:
		}
		m.ReapIdle(cfg)
	}
}
