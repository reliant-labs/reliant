// Copyright (c) 2025 Reliant Labs
package forge

import (
	"context"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
)

// -----------------------------------------------------------------------------
// A READ ABOUT THE FORGE PROJECT IS NOT A READ ABOUT ONE CHECKOUT.
//
// forgeTarget picks one machine: the one the Reliant project's work is placed
// on. For a write (plan, deploy, promote) that is the only right answer — what
// it builds and ships is that machine's working tree. But topology describes
// the FORGE project — its declared environments, its release ledger, its live
// clusters — and any checkout of it can answer it.
//
// Prod, 2026-11-03..05: every GetTopology of the owner's control-plane console
// answered 503 "no daemon connected" (157 of 157). The console had selected
// the Reliant project created on their laptop, checked out only on two
// laptops, both asleep, while three connected cloud machines held a checkout
// of the same forge project under a second Reliant project.
//
// So when the chosen machine is not reachable, a topology read is asked of the
// next machine holding a checkout of the same forge project: the project's own
// other checkouts first, then those of the caller's other projects that are the
// same repository (the same git remote) AND declare the same forge.yaml name,
// most recently active first. A forge name alone is not an identity: a fork or
// an unrelated repo can declare it while targeting other clusters. Only "not
// reachable" (daemonerr.Unreachable) moves on; an answer, or any other
// failure, is the answer. Each machine is asked at most once.
//
// Env status and verify never move: `forge env status` carries a RUNTIME half
// (host services, listeners, logs, local sessions) that is the answering
// machine's own, so another machine's answer is not this one's.
// -----------------------------------------------------------------------------

// forgeReadDispatch is forgeDispatch for a read about the forge project.
// payload builds the command's payload for a checkout's path. It returns the
// target that answered: target itself, unless its machine was not reachable
// and another checkout answered instead.
func (s *Service) forgeReadDispatch(
	ctx context.Context,
	userID string,
	target projectcheckout.Target,
	commandType string,
	payload func(projectPath string) any,
	timeoutMs int32,
	readsCluster bool,
) (forgeReportReply, string, projectcheckout.Target, error) {
	reply, unreachable, err := s.forgeDispatch(ctx, userID, target, commandType, payload(target.Path), timeoutMs, readsCluster)
	if err == nil || !daemonerr.Unreachable(err) {
		return reply, unreachable, target, err
	}
	asked := map[string]bool{target.DaemonID: true}
	for _, alt := range s.forgeFallbackTargets(ctx, userID, target.Project) {
		if asked[alt.DaemonID] {
			continue
		}
		asked[alt.DaemonID] = true
		altReply, altUnreachable, altErr := s.forgeDispatch(ctx, userID, alt, commandType, payload(alt.Path), timeoutMs, readsCluster)
		if altErr != nil && daemonerr.Unreachable(altErr) {
			continue
		}
		logging.InfoContext(ctx, "forge: the checkout's machine is not reachable; another checkout of the forge project answered",
			"command", commandType,
			"project_id", target.Project.ID, "daemon_id", target.DaemonID,
			"answered_project_id", alt.Project.ID, "answered_daemon_id", alt.DaemonID)
		return altReply, altUnreachable, alt, altErr
	}
	// No other machine could be asked: the chosen machine's answer stands.
	return reply, unreachable, target, err
}

// forgeFallbackTargets is every checkout of project's forge project on the
// caller's machines (its own, then those of the caller's projects with the
// same git remote and forge name), in the order forgeReadDispatch asks them. Best effort: a
// failed read leaves fewer candidates, never an error, because the first
// machine's failure is still the answer then.
func (s *Service) forgeFallbackTargets(ctx context.Context, userID string, project *db.Project) []projectcheckout.Target {
	targets, err := projectcheckout.Checkouts(ctx, s.deps.Repo, userID, project)
	if err != nil {
		logging.WarnContext(ctx, "forge: failed to list the project's checkouts", "error", err, "project_id", project.ID)
	}
	name, remote := forgeNameOf(project), remoteIdentity(project.RemoteURL)
	if name == "" || remote == "" {
		return targets
	}
	projects, err := s.deps.Repo.ListProjects(ctx, db.ProjectFilters{UserID: userID})
	if err != nil {
		logging.WarnContext(ctx, "forge: failed to list the caller's projects", "error", err, "project_id", project.ID)
		return targets
	}
	for _, other := range projects {
		if other == nil || other.ID == project.ID || forgeNameOf(other) != name || remoteIdentity(other.RemoteURL) != remote {
			continue
		}
		more, err := projectcheckout.Checkouts(ctx, s.deps.Repo, userID, other)
		if err != nil {
			logging.WarnContext(ctx, "forge: failed to list the project's checkouts", "error", err, "project_id", other.ID)
			continue
		}
		targets = append(targets, more...)
	}
	return targets
}

// forgeNameOf is forge's name for project (forge.yaml `name`), "" when it is
// not known to be a forge project.
func forgeNameOf(project *db.Project) string {
	if project == nil || !project.IsForge || project.ForgeProjectName == nil {
		return ""
	}
	return strings.TrimSpace(*project.ForgeProjectName)
}

// remoteIdentity is the repository a git remote URL names, in one form for
// every spelling of it: "git@github.com:Org/Repo.git",
// "ssh://git@github.com:22/org/repo" and "https://user:tok@github.com/org/repo/"
// are all "github.com/org/repo". "" when there is no remote, so a project
// without one never matches another.
//
// Only proven equivalences are folded: credentials, a trailing "/" or ".git",
// the host's case, a scheme's default port, the scp-like and ssh:// spellings
// of one ssh remote, and, on GitHub (githubHosts) alone, the transport and the
// path's case. Anything else can name a different repository (another server
// on another port or transport, or a case-sensitive host serving Team/App
// beside team/app), so it is kept and such remotes never match: on any other
// host, ssh://host/team/app and https://host/team/app stay distinct.
func remoteIdentity(raw *string) string {
	if raw == nil {
		return ""
	}
	s := strings.TrimSpace(*raw)
	if s == "" {
		return ""
	}
	scheme := ""
	if i := strings.Index(s, "://"); i >= 0 {
		scheme, s = strings.ToLower(s[:i]), s[i+3:]
	} else if i := strings.Index(s, ":"); i >= 0 && !strings.Contains(s[:i], "/") {
		// scp-like: [user@]host:path is ssh. Its ":" never introduces a port.
		scheme, s = "ssh", s[:i]+"/"+s[i+1:]
	}
	if scheme == "git+ssh" || scheme == "ssh+git" {
		scheme = "ssh"
	}
	host, path, _ := strings.Cut(s, "/")
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	host = strings.ToLower(host)
	if h, port, ok := strings.Cut(host, ":"); ok && port == defaultGitPorts[scheme] {
		host = h
	}
	path = strings.TrimSuffix(strings.TrimRight(path, "/"), ".git")
	path = strings.TrimRight(path, "/")
	if host == "" || path == "" {
		return ""
	}
	if githubHosts[host] {
		return host + "/" + strings.ToLower(path)
	}
	// Elsewhere the transport is part of the identity: one hostname can run
	// independent ssh and http(s) services, each with its own repositories.
	return scheme + "://" + host + "/" + path
}

// defaultGitPorts is the port each git transport uses when a URL names none,
// so "ssh://host:22/x" is the same remote as "ssh://host/x".
var defaultGitPorts = map[string]string{"ssh": "22", "https": "443", "http": "80", "git": "9418"}

// githubHosts are GitHub's hosts, known to serve one repository namespace on
// every transport and to resolve a repository path regardless of case.
var githubHosts = map[string]bool{"github.com": true, "www.github.com": true}
