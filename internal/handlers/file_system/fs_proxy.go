// Copyright (c) 2025 Reliant Labs
package file_system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/daemonerr"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/daemonpolicy"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/filepreview"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/machinewake"
	"github.com/reliant-labs/reliant/internal/ospath"
	"github.com/reliant-labs/reliant/internal/projectcheckout"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// The service forwards every request to a daemon via DaemonCommand
// (request/response): the machine that holds the workspace a request names
// (see workspaceDaemonID).

// wake is the step a command takes after failing to reach its machine. The
// router resumes an asleep machine when it can (machinewake.ResumerOf's rule:
// only the NATS router can), and Repo checks the machine is the caller's.
func (s *Service) wake() machinewake.Service {
	resumer, _ := s.deps.Router.(machinewake.Resumer)
	return machinewake.NewInstrumented(machinewake.Deps{Resumer: resumer, Owners: s.deps.Placement})
}

func (s *Service) getUserID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok {
		return "", svcerr.Unauthenticated("user ID not found in context")
	}
	return userID, nil
}

// requireProjectBase resolves the workspace root a request is scoped to, and
// refuses the request when there isn't one.
//
// Failing closed here is the point. This service is mounted only in hosted,
// multi-tenant deployments (see internal/grpc/server.go), where the daemon on
// the far end runs as the user and every path this service emits is executed
// there with no further scoping. Without a base there is nothing to confine
// against, so the previous behaviour — forward the caller's raw path — handed
// the daemon whatever the client asked for, including "/" and "..". A request
// that names no workspace is refused instead of being run against the
// daemon's filesystem root or its working directory.
//
// Every client reaches these RPCs with a project id (frontends/web/src/api/fileSystem.ts
// returns early when there is no current project), so nothing legitimate
// depended on the passthrough.
//
// The base is a branch worktree's path, or else the project's main checkout ON
// THE MACHINE the request runs on (target): projects.path is the creating
// machine's path. A machine with no checkout is projectcheckout's refusal,
// whose ProjectCheckoutMissing detail the Files tab renders with its action.
//
// resolvedOn is the machine the main checkout was resolved on, so a request
// left to default resolution is pinned to the machine whose path it carries;
// "" when a branch worktree scoped it.
func (s *Service) requireProjectBase(ctx context.Context, userID, projectID string, worktreeID *string, chatID *string, target machinewake.Target) (basePath, resolvedOn string, err error) {
	if strings.TrimSpace(projectID) == "" {
		return "", "", svcerr.InvalidArgument("project_id is required to scope a filesystem request")
	}
	// The project is the caller's to work in before anything about it is
	// resolved: a refusal's detail names the project and its remote, and a
	// request no chat or worktree placed reaches here with nothing else
	// checking the project id.
	project, err := s.deps.Placement.GetProject(ctx, projectID)
	if err != nil && !errors.Is(err, svcerr.ErrNotFound) {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to load the project"))
	}
	if !authz.CanWriteProject(ctx, userID, project) {
		return "", "", svcerr.NotFound("project")
	}
	mainCheckout := func(ctx context.Context) (string, error) {
		// A request no chat or workspace placed (target.DaemonID "") is
		// resolved among the machines that HAVE the project — the default
		// when it is one of them, else another of the user's — never on
		// project-blind default resolution, which refused the Files tab
		// with "<project> isn't on <machine>" whenever the default machine
		// lacked a checkout another machine had. A chat or workspace pinned
		// to a machine without one is still refused: its files are there.
		machines, _ := s.deps.Router.(daemonplacement.Machines)
		checkout, err := projectcheckout.ForMachine(ctx, s.deps.Placement, machines, userID, project, target.DaemonID, fsCheckoutPurpose)
		if err != nil && target.DaemonID == "" && !target.NoMachine {
			err = s.noCheckoutAnywhere(ctx, userID, project, err)
		}
		if err != nil {
			return "", err
		}
		resolvedOn = checkout.DaemonID
		return checkout.Path, nil
	}
	basePath, err = filepreview.ResolveBasePath(ctx, s.workspaces(), projectID, worktreeID, chatID, mainCheckout)
	if err != nil {
		return "", "", svcerr.WrapOr(err, svcerr.Internal("failed to resolve project path"))
	}
	if strings.TrimSpace(basePath) == "" {
		return "", "", svcerr.FailedPrecondition(fmt.Sprintf("project %q has no workspace path to scope this request to", projectID))
	}
	return basePath, resolvedOn, nil
}

// fsCheckoutPurpose finishes projectcheckout's refusal sentence ("clone it
// onto <machine> to <purpose>").
const fsCheckoutPurpose = "browse its files"

// noCheckoutAnywhere is the refusal for a request no machine was named for,
// when none of the user's machines has an installed checkout and none is
// cloning or failed (err is Resolve's). There is then no machine the files
// are on, so the Files tab describes the machine default resolution names —
// the one "Clone it here" clones onto — with that machine's own state (a
// clone of a machine the registry does not list yet, still landing there, is
// CLONING). Any other err is returned as it is.
func (s *Service) noCheckoutAnywhere(ctx context.Context, userID string, project *db.Project, err error) error {
	var missing *daemonplacement.NoCheckoutError
	if s.deps.Router == nil || !errors.As(err, &missing) || len(missing.Cloning) > 0 || len(missing.Failed) > 0 {
		return err
	}
	machine, rerr := s.deps.Router.ResolveDaemonID(ctx, userID)
	if rerr != nil || machine == "" {
		return err
	}
	if _, onErr := projectcheckout.On(ctx, s.deps.Placement, userID, project, machine, fsCheckoutPurpose); onErr != nil {
		return onErr
	}
	// Installed there after all (a row written since Resolve read): the
	// original refusal stands; the next read resolves it.
	return err
}

// workspaceScope is where a workspace-scoped request acts: the directory it
// is confined to, and the machine that directory is on.
type workspaceScope struct {
	basePath string
	// target is the machine holding basePath (daemonID "" when default
	// resolution picks it), and whether the request was made for a chat with
	// no machine. See workspaceDaemonID.
	target machinewake.Target
}

// resolveScope resolves the workspace root a request is scoped to and the
// machine it must run on. Every request that names a project goes through
// here, so a file tree, a preview and a save of the same file cannot land on
// different machines.
func (s *Service) resolveScope(ctx context.Context, userID, projectID string, worktreeID, chatID *string) (workspaceScope, error) {
	target, err := s.workspaceDaemonID(ctx, userID, projectID, worktreeID, chatID)
	if err != nil {
		return workspaceScope{}, err
	}
	basePath, resolvedOn, err := s.requireProjectBase(ctx, userID, projectID, worktreeID, chatID, target)
	if err != nil {
		return workspaceScope{}, err
	}
	if target.DaemonID == "" && !target.NoMachine && resolvedOn != "" {
		// The path is that machine's: every command of the request goes
		// there, not to a second default resolution that could pick another
		// (the tree, a preview and a delete all scope through here).
		target.DaemonID = resolvedOn
	}
	return workspaceScope{basePath: basePath, target: target}, nil
}

// workspaceDaemonID returns the machine a workspace-scoped request must run
// on (daemonID "" leaves it to default resolution), marked noMachine when the
// request was made for a chat that has no machine: such a request must never
// wake one (see machinewake).
//
// The files exist on one machine, and daemonplacement.WorkspaceDaemon names
// it: an owned worktree's owner, else the chat's machine (the precedence
// ExecuteTools routes the chat's tools by), else default resolution. The Files
// tab, the preview and the terminal share that one rule, so they cannot show
// two different disks.
func (s *Service) workspaceDaemonID(ctx context.Context, userID, projectID string, worktreeID, chatID *string) (machinewake.Target, error) {
	ws, err := daemonplacement.WorkspaceDaemon(ctx, s.workspaces(), userID, projectID, deref(worktreeID), deref(chatID))
	switch {
	case errors.Is(err, daemonplacement.ErrForeignChat):
		return machinewake.Target{}, svcerr.NotFound("chat")
	case errors.Is(err, daemonplacement.ErrForeignWorkspace):
		return machinewake.Target{}, svcerr.NotFound("worktree")
	case err != nil:
		return machinewake.Target{}, svcerr.WrapOr(err, svcerr.Internal("failed to resolve the workspace's machine"))
	}
	return machinewake.Target{DaemonID: ws.DaemonID, NoMachine: ws.NoMachine}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// resolve turns a client path into the absolute, confined path the daemon
// will act on.
//
// The "" | "/" == workspace root rule and the confinement check are NOT
// reimplemented here: both come from filepreview.ValidatePathScoped via
// validateWorkspacePath. ScopeBaseOnly is deliberate — every rpc that lists,
// writes, creates, deletes, copies or searches stays inside the workspace, so
// an absolute path outside it is refused rather than honoured. The read-only
// viewer rpcs use resolveForView instead.
//
// The returned path is always absolute, so "" and "/" never cross the wire to
// the daemon.
//
// Errors are already svcerr kinds — PermissionDenied for an escape,
// InvalidArgument for a malformed request — so callers return them unchanged.
// Re-wrapping them as NotFound (which every call site used to do) reported a
// refused traversal as a missing file.
func (w workspaceScope) resolve(requestedPath string) (string, error) {
	return validateWorkspacePath(w.basePath, requestedPath, filepreview.ScopeBaseOnly)
}

// resolveForView is resolve for the read-only viewer rpcs (GetFileContent,
// GetFileMetadata, GetFilePreviewInfo, GetFilePreview): it also honours an
// absolute path outside the workspace.
//
// The daemon those rpcs reach is the requesting user's own machine (or cloud
// machine), whose files their agents already read with a shell, so viewing a
// file they named there grants nothing new — and a path an agent mentioned
// outside the workspace (a sibling repo, the module cache, ~/.config) is
// exactly what the viewer is asked to open. A RELATIVE path that climbs out
// of the workspace is still refused: it is resolved against a base the user
// did not choose, so escaping it can only be a traversal.
//
// What the viewer never opens is a credential store (~/.ssh, ~/.aws, browser
// profiles, …), wherever a path or its symlinks lead. Only the daemon can
// resolve links on its disk, so each viewer rpc sends the workspace root as
// "viewer_root" and the daemon refuses the read (daemonpolicy.DenyViewerRead);
// fsProxyDaemonError reports that as PermissionDenied.
func (w workspaceScope) resolveForView(requestedPath string) (string, error) {
	return validateWorkspacePath(w.basePath, requestedPath, filepreview.ScopeAllowAbsolute)
}

// resolveForView is scope.resolveForView for a path as a person or an agent
// writes it, "~" included: "~/notes/post.md" names the file in the home
// directory of the machine that holds it, and is an absolute path there.
//
// The contract (filepreview.ValidatePathScoped) knows nothing of "~", so such
// a path read as relative and was joined onto the workspace root:
// "/home/workspace/projects/x/~/notes/post.md", a file that does not exist —
// prod, 2026-11-05, every click on a "~/..." path a chat mentioned (issues
// b52cdbdb, 584d1a9a). Only the machine knows its home directory, so it is
// asked (skills.get_home_dir, which every daemon serves) and only for such a
// path. The expanded path is then an absolute path outside the workspace,
// confined and credential-guarded exactly like one the client sent.
func (s *Service) resolveForView(ctx context.Context, userID string, scope workspaceScope, requestedPath string) (string, error) {
	rest, ok := homeRelative(requestedPath)
	if !ok {
		return scope.resolveForView(requestedPath)
	}
	var resp struct {
		HomeDir string `json:"home_dir"`
		Error   string `json:"error,omitempty"`
	}
	if err := s.sendCommand(ctx, userID, scope.target, "skills.get_home_dir", map[string]any{}, &resp, homeDirTimeoutMs); err != nil {
		return "", err
	}
	if resp.HomeDir == "" || !ospath.IsAbs(resp.HomeDir) {
		return "", svcerr.FailedPrecondition("this machine has no home directory to resolve " + requestedPath + " against")
	}
	if rest == "" {
		return scope.resolveForView(ospath.Clean(resp.HomeDir))
	}
	return scope.resolveForView(ospath.Join(resp.HomeDir, rest))
}

// homeRelative reports whether p is "~" or begins "~/" (or "~\" for a
// Windows machine), and returns what follows the separator. "~user/..." is
// not this: it names another account's home, which the viewer does not open.
func homeRelative(p string) (string, bool) {
	if p == "~" {
		return "", true
	}
	if len(p) >= 2 && p[0] == '~' && (p[1] == '/' || p[1] == '\\') {
		return p[2:], true
	}
	return "", false
}

// requireViewerGuard refuses to show a file unless the daemon's reply says it
// applied the credential guard (viewer_guarded, which cmd_fs.go sets on every
// reply to a viewer_root request). A daemon from before the guard ignores
// viewer_root and would return ~/.ssh/id_rsa like any other file, so a reply
// without the mark is never shown: FailedPrecondition, naming the fix.
func requireViewerGuard(guarded bool) error {
	return requireViewerGuardTo(guarded, viewFilesAction)
}

// viewFilesAction is what an outdated daemon stops the viewer rpcs doing.
const viewFilesAction = "view files"

// requireViewerGuardTo is requireViewerGuard for a read that serves something
// other than the viewer, named by action ("download files").
func requireViewerGuardTo(guarded bool, action string) error {
	if guarded {
		return nil
	}
	return svcerr.FailedPrecondition("this machine's daemon is outdated: update it to " + action)
}

// validateWorkspacePath is the single place this package applies the shared
// path contract.
//
// The contract itself is filepreview.ValidatePathScoped and lives there alone:
// "" and "/" name the workspace root, a relative path is joined onto the base
// and must stay beneath it, and an absolute path means the location it names.
// Every path-scoped rpc resolves through this function, so none can drift from
// that rule or report it with a different code. The code is the contract's
// own: its refusals are PermissionDenied kinds (filepreview.ErrPathOutsideBase,
// ErrAbsolutePathOutsideBase), and anything else it fails with is a malformed
// request.
func validateWorkspacePath(basePath, requestedPath string, scope filepreview.PathScope) (string, error) {
	absFullPath, err := filepreview.ValidatePathScoped(basePath, requestedPath, scope)
	if err != nil {
		// The refusal alone cannot be diagnosed: "outside the workspace" names
		// neither the path the client sent nor the base it was compared
		// against, and the pair is what distinguishes a traversal attempt from
		// a client holding paths for a DIFFERENT workspace than the one the
		// request resolved to (a chat on a worktree whose base is not under
		// the project root, say). Both are needed, so both are logged here,
		// once, at the single place the refusal is produced.
		logging.Warn("Refused a workspace path",
			"requested_path", requestedPath, "base_path", basePath, "scope", scope, "error", err)
		return "", svcerr.WrapOr(err, svcerr.InvalidArgument("malformed workspace path"))
	}
	return absFullPath, nil
}

// sendCommand is a helper that marshals the request, sends the daemon command
// to the target machine (daemonID "" = default resolution), and unmarshals the
// response. A machine found asleep is woken (machinewake) and the request fails
// as waking, for the client to retry.
func (s *Service) sendCommand(ctx context.Context, userID string, target machinewake.Target, commandType string, req any, resp any, timeoutMs int32) error {
	payload, err := json.Marshal(req)
	if err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("marshal request"))
	}

	var respBytes []byte
	if target.DaemonID == "" {
		respBytes, err = s.deps.Router.SendDaemonCommand(ctx, userID, commandType, payload, timeoutMs)
	} else {
		respBytes, err = s.deps.Router.SendDaemonCommandToDaemon(ctx, userID, target.DaemonID, commandType, payload, timeoutMs)
	}
	if err != nil {
		// An asleep machine is woken here. The error then carries the wake,
		// which machinewake.NewInterceptor turns into Unavailable + DaemonWaking
		// whatever code is chosen below.
		err = s.wake().AfterFailure(ctx, userID, target, err)
		return fsProxyDaemonError(commandType, err)
	}

	if err := json.Unmarshal(respBytes, resp); err != nil {
		return svcerr.WrapOr(err, svcerr.Internal("unmarshal response"))
	}
	return nil
}

// fsProxyDaemonError maps a failed daemon command to the code that says what
// happened. The machine's STATE is not a server failure:
//
//   - not reachable right now — starting, asleep (and possibly just woken), or
//     not connected (NATS had no responder) — is Unavailable: retryable, what
//     the web's daemon-wait machinery expects, and its message keeps the
//     "no daemon connected" marker isDaemonConnectingError keys on;
//   - no machine at all is FailedPrecondition: nothing to wait for, and the
//     file tree says "no machine" instead of an error;
//   - connected but too busy to answer in time is DeadlineExceeded (see
//     daemonerr.MachineState).
//
// The path the request named not being on the machine — a folder the user
// deleted, a worktree that was removed, a checkout never cloned there — is the
// state of the user's disk too: NotFound. As Internal it reached Sentry from
// both the server and the browser (ELECTRON-AW, ELECTRON-9Y).
//
// A daemon older than this server is FailedPrecondition in the daemon's own
// words (daemonerr.Outdated): retrying cannot help, updating the machine does.
//
// Only what is left is Internal. Mapping the not-connected case there (it used
// to fall through) logged an ERROR "rpc failed" for every file-tree poll while
// a machine restarted — 117 in five hours for one user's crash-looping machine
// on 2026-10-09 — and handed the UI a 500-class error for a machine that was
// merely starting.
//
// What the client reads: the daemon's own words (the path it could not find,
// the read that failed) reach the user, whose machine it is; the router's and
// the transport's stay on the error as its logged cause.
func fsProxyDaemonError(commandType string, err error) error {
	if daemonerr.IsOutdated(err) {
		return daemonerr.Outdated(err)
	}
	if serr, ok := daemonerr.MachineState("", err); ok {
		return serr
	}
	text, reported := toolexec.DaemonReportedText(err)
	if isMissingPathError(err) {
		if !reported {
			text = "path not found on this machine"
		}
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, text), err)
	}
	// A credential store the daemon refused to open (DenyViewerRead,
	// DenyCredentialFile) is a refusal, not a server failure.
	if reported && (strings.Contains(text, daemonpolicy.ErrViewerCredential.Error()) ||
		strings.Contains(text, daemonpolicy.ErrCredentialFile.Error())) {
		return svcerr.WithCause(svcerr.WithMessage(svcerr.ErrPermissionDenied, text), err)
	}
	msg := "daemon command " + commandType + " failed"
	if reported {
		msg += ": " + text
	}
	return svcerr.WithCause(svcerr.Internal(msg), err)
}

// fsProxyPreviewInfo mirrors the daemon's fs.preview_info response: the file's
// stat plus internal/filepreview's classification of it, made on the daemon
// from the file's name and first bytes.
type fsProxyPreviewInfo struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	Modified   string `json:"modified"`
	ViewerKind string `json:"viewer_kind"`
	MIMEType   string `json:"mime_type"`
	IsBinary   bool   `json:"is_binary"`
	IsEditable bool   `json:"is_editable"`

	ViewerGuarded bool `json:"viewer_guarded"`
}

// previewInfo asks the machine holding resolvedPath to stat and classify it.
// requestedPath is the client's path, used only in the log line.
func (s *Service) previewInfo(ctx context.Context, userID string, scope workspaceScope, resolvedPath, requestedPath string) (*fsProxyPreviewInfo, error) {
	return s.previewInfoTo(ctx, userID, scope, resolvedPath, requestedPath, viewFilesAction)
}

// previewInfoTo is previewInfo for a read that serves action (see
// requireViewerGuardTo).
func (s *Service) previewInfoTo(ctx context.Context, userID string, scope workspaceScope, resolvedPath, requestedPath, action string) (*fsProxyPreviewInfo, error) {
	var info fsProxyPreviewInfo
	cmdReq := map[string]any{"path": resolvedPath, "viewer_root": scope.basePath}
	if err := s.sendCommand(ctx, userID, scope.target, "fs.preview_info", cmdReq, &info, previewInfoTimeoutMs); err != nil {
		// Requesting preview info for a directory is a client-input condition
		// (e.g. the UI asking for a tree folder), not a server failure. Return
		// a typed FailedPrecondition so it stays out of ERROR logs (and issues).
		// The daemon-side error text is the contract
		// here (cmd_fs.go returns "path is a directory: <path>").
		if strings.Contains(err.Error(), "path is a directory") {
			logging.DebugContext(ctx, "[FSProxy] preview requested for a directory", "path", requestedPath)
			return nil, svcerr.FailedPrecondition("path is a directory")
		}
		return nil, err
	}
	if err := requireViewerGuardTo(info.ViewerGuarded, action); err != nil {
		return nil, err
	}
	return &info, nil
}

// pickerMachine is the machine the project picker's ListDirectory and
// CreateDirectory run on: the one the request names, which must be the
// caller's (another user's is NotFound, the same as a missing one), else the
// default machine (the zero Target), as before requests could name one. A
// named machine that is asleep is woken and one that is offline is reported
// by sendCommand, like any pinned command.
func (s *Service) pickerMachine(ctx context.Context, userID, daemonID string) (machinewake.Target, error) {
	if daemonID == "" {
		return machinewake.Target{}, nil
	}
	d, err := s.deps.Placement.GetDaemon(ctx, daemonID)
	if err != nil || !authz.CanWriteMachine(ctx, userID, d) {
		return machinewake.Target{}, svcerr.NotFound("machine")
	}
	return machinewake.Target{DaemonID: daemonID}, nil
}

// pickerEcho is the machine a picker response names: the request's machine,
// or unset for the default machine. The client reads an unset echo on a
// request that named a machine as a server too old to honor it.
func pickerEcho(target machinewake.Target) *string {
	if target.DaemonID == "" {
		return nil
	}
	id := target.DaemonID
	return &id
}
