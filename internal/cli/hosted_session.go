package cli

// ReportLocalSession (doc §6.3, §7.4): one PRESENCE row per `forge env up`
// stack, so Live can show what is running where without asking a daemon.
//
// A SESSION IS NOT A LEDGER ENTRY, and the design enforces that rather than
// relying on discipline. It names no promotion and no release, it is mutable
// (heartbeats overwrite it), it is garbage-collected after 24 h, the schema's
// trigger confines it to LOCAL envs, and the policy evaluator never reads it.
// Nothing authorizes or bills on a session (owner decision O-8).
//
// IT NEVER BLOCKS `forge env up`. A dev stack that would not start because a
// presence row could not be written would be a hosted dependency inserted into
// the one workflow that has no business having one. ReportSession therefore
// returns its error for the CALLER to log and continue past — and
// ReportSessionBestEffort is the spelling that says so at the call site.
//
// WHY ONE SESSION PER WORKTREE, NEVER PER USER. A LOCAL env has no single
// "what runs": it runs whatever each checkout runs, on each machine. Identity
// is (env, host, worktree_key), and worktree_key is forge's existing devstack
// key — the same key the checkout's port block is held under — so a session
// matches the stack that actually holds those ports.

import (
	"context"
	"fmt"
	"time"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/pkg/release"
)

const procReportLocalSession = "controlplane.v1.DeployService/ReportLocalSession"

// Session states, as ReportLocalSessionRequest.state spells them. Closed.
const (
	// sessionRunning is reported at start and on every heartbeat.
	sessionRunning = "running"
	// sessionStopped is reported at teardown and sets stopped_at: a clean
	// exit, distinct from a session that simply went quiet. A crashed stack
	// reports nothing and is reaped by the GC.
	sessionStopped = "stopped"
)

// hostedSessionClient reports local sessions to one control plane.
type hostedSessionClient struct {
	client cloudCaller
}

// ReportSession upserts the presence row. project is the forge project; the
// env is named rather than addressed by id, because a session is reported
// from a dev stack that has not necessarily resolved an env id and never
// needs one.
//
// stopped reports teardown. Heartbeats and the initial report are both
// `running` — the server distinguishes them by whether the row existed, which
// is one less thing the client can get wrong.
func (c hostedSessionClient) ReportSession(ctx context.Context, project string, s release.LocalSession, stopped bool) (release.LocalSession, error) {
	if err := s.Validate(); err != nil {
		return release.LocalSession{}, err
	}
	state := sessionRunning
	if stopped {
		state = sessionStopped
	}
	req := map[string]any{
		"sessionId":   s.ID,
		"project":     project,
		"environment": s.Env,
		"state":       state,
	}
	if worktree := worktreeWireFields(s.Worktree); len(worktree) > 0 {
		req["worktree"] = worktree
	}
	// The provenance of what the stack is RUNNING: "feat-x@def5678, dirty"
	// is the line Live shows beside the worktree label, and it is a claim
	// recorded as such. ForHosted (inside ProvenanceWireFields) drops the
	// checkout path, which names a user's home directory.
	req["provenance"] = deploytarget.ProvenanceWireFields(s.Provenance)
	if s.BundleDigest != "" {
		req["bundleDigest"] = s.BundleDigest
	}
	var resp struct {
		Session wireLocalSession `json:"session"`
	}
	if err := c.client.Call(ctx, procReportLocalSession, req, &resp); err != nil {
		return release.LocalSession{}, bundlesUnsupported(err)
	}
	return localSessionFromWire(s.Env, resp.Session)
}

// ReportSessionBestEffort is ReportSession with the failure swallowed into a
// returned error the caller is EXPECTED to log and ignore.
//
// It exists so the call site reads as what §7.4 requires: presence is a
// nice-to-have, and `forge env up` must start whether or not the control
// plane is reachable. A caller that wrote `if err := ReportSession(...); err
// != nil { return err }` — the reflex — would have made every dev stack
// depend on a hosted write, and the name is the only thing that reliably
// stops that.
func (c hostedSessionClient) ReportSessionBestEffort(ctx context.Context, project string, s release.LocalSession, stopped bool) error {
	if _, err := c.ReportSession(ctx, project, s, stopped); err != nil {
		return fmt.Errorf("report local session for %s (presence only; the stack is unaffected): %w", s.Env, err)
	}
	return nil
}

// sessionHeartbeat is how often a supervising `forge env up` re-reports.
// Live greys a session out after release.SessionStaleAfter (3 min), so this
// has to be comfortably under it: three missed heartbeats, not one, before a
// healthy stack looks stale.
const sessionHeartbeat = 60 * time.Second
