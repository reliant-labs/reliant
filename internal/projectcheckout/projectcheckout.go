// Copyright (c) 2025 Reliant Labs

// Package projectcheckout is how a server-side handler reaches a project's
// files: the machine, and the project's path ON THAT MACHINE, resolved
// together from one project_daemons row, with every command pinned to that
// machine.
//
// projects.path is where the project was created, on whichever machine
// created it — a Mac path for a project made on the owner's laptop. It is not
// a path on any other machine, so it is never sent to a daemon. A handler that
// sent it to whichever daemon default resolution picked asked a cloud machine
// for "/Users/<owner>/src/control-plane" (the forge console, PR #2016). The
// architecture guard TestProjectPathNeverReachesADaemon fails any new read of
// projects.path outside its reviewed list, and points here.
//
// The API server never looks at a filesystem: which machine holds which
// checkout is the record those machines reported (create, clone, and the
// reconcile every daemon runs on connect).
//
//forge:exclude-contract: resolution over the Reader and Machines its caller passes; it holds no state
package projectcheckout

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/daemonplacement"
	"github.com/reliant-labs/reliant/internal/db"
)

// Reader is what resolution reads: each machine's checkout of the project
// (project_daemons) and who owns each machine, whose name an error uses.
type Reader interface {
	daemonplacement.Installs
}

// Sender runs a command on one named machine. *toolexec.NATSDaemonRouter
// implements it.
type Sender interface {
	SendDaemonCommandToDaemon(ctx context.Context, userID, daemonID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
}

// Target is a project's main checkout on the machine chosen to act on it.
type Target struct {
	Project *db.Project
	// DaemonID is the machine holding the checkout. Never empty.
	DaemonID string
	// Path is where the checkout is ON THAT MACHINE.
	Path string
}

// Join is rel under the checkout, on the target's machine. "" and "." are
// the checkout itself.
func (t Target) Join(rel string) string {
	if rel == "" || rel == "." {
		return t.Path
	}
	return filepath.Join(t.Path, rel)
}

// Send runs one command on the target's machine.
func (t Target) Send(ctx context.Context, s Sender, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return s.SendDaemonCommandToDaemon(ctx, userID, t.DaemonID, commandType, payload, timeoutMs)
}

// Pinned is s bound to the target's machine, for code written against a
// plain SendDaemonCommand (daemoncmd.Sender): every command it sends goes to
// the machine holding the checkout, never to default resolution.
func (t Target) Pinned(s Sender) PinnedSender {
	return PinnedSender{sender: s, daemonID: t.DaemonID}
}

// PinnedSender sends every command to one machine.
type PinnedSender struct {
	sender   Sender
	daemonID string
}

// SendDaemonCommand sends to the pinned machine.
func (p PinnedSender) SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error) {
	return p.sender.SendDaemonCommandToDaemon(ctx, userID, p.daemonID, commandType, payload, timeoutMs)
}

// Resolve returns the machine a command about project should run on and the
// project's checkout there (daemonplacement.ProjectCheckout: the machine the
// project's chats and package commands are placed on, among those holding a
// checkout).
//
// With no checkout on any of the user's machines it returns MissingError's
// answer, never a path: there is no machine on which any path would be true.
// purpose finishes the sentence telling the user what to do ("clone it onto a
// machine to <purpose>"), e.g. "use forge with it".
//
// machines may be nil (a composition with no daemon router): the oldest
// install answers, which is the record a caller with no router can still
// read or name, though it cannot send to it.
func Resolve(ctx context.Context, r Reader, machines daemonplacement.Machines, userID string, project *db.Project, purpose string) (Target, error) {
	if machines == nil {
		machines = unplaced{}
	}
	checkout, err := daemonplacement.ProjectCheckout(ctx, r, machines, userID, project.ID)
	var missing *daemonplacement.NoCheckoutError
	switch {
	case errors.As(err, &missing):
		return Target{}, MissingError(ctx, r, project, missing, purpose)
	case err != nil:
		return Target{}, svcerr.WithCause(svcerr.Internal("failed to resolve the machine holding the project"), err)
	}
	return Target{Project: project, DaemonID: checkout.DaemonID, Path: checkout.Path}, nil
}

// IsMissing reports whether err is Resolve's "no machine holds a checkout"
// answer: for a caller whose daemon read is best effort (a cached flag it
// refreshes when it can) and which carries on without it.
func IsMissing(err error) bool {
	return errors.Is(err, daemonplacement.ErrNoCheckout)
}

// MissingError is the answer when none of the user's machines holds the
// project: FailedPrecondition (retrying cannot put a checkout on a machine),
// a sentence saying what to do, and a ProjectCheckoutMissing detail the web
// renders with its own action — the same detail the Files tab uses for a
// project that is not on the machine it asked.
//
// A clone still landing is said as such (it fixes itself), a failed one with
// its reason, and otherwise the user is told to put the project on a machine.
func MissingError(ctx context.Context, names daemonplacement.DaemonReader, project *db.Project, missing *daemonplacement.NoCheckoutError, purpose string) error {
	detail := &reliantv1.ProjectCheckoutMissing{
		ProjectId: project.ID,
		State:     reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT,
		RemoteUrl: remoteURL(project),
	}
	name := strconv.Quote(project.Name)

	var msg string
	switch {
	case len(missing.Cloning) > 0:
		row := missing.Cloning[0]
		detail.State = reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONING
		detail.DaemonId, detail.Path = row.DaemonID, row.Path
		msg = fmt.Sprintf("%s is still being cloned onto %s; try again once the clone finishes",
			name, daemonplacement.MachineName(ctx, names, row.DaemonID))
	case len(missing.Failed) > 0:
		row := missing.Failed[0]
		detail.State = reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_CLONE_FAILED
		detail.DaemonId, detail.Path, detail.InstallError = row.DaemonID, row.Path, row.InstallError
		msg = fmt.Sprintf("cloning %s onto %s failed", name, daemonplacement.MachineName(ctx, names, row.DaemonID))
		if reason := strings.TrimSpace(row.InstallError); reason != "" {
			msg += " (" + reason + ")"
		}
		msg += ": clone it again to " + purpose
	case detail.RemoteUrl != "":
		msg = fmt.Sprintf("%s is not checked out on any of your machines: clone it onto a machine to %s", name, purpose)
	default:
		msg = fmt.Sprintf("%s is not on any of your connected machines: connect the machine that holds its folder to %s", name, purpose)
	}
	return svcerr.WithDetail(svcerr.WithCause(svcerr.FailedPrecondition(msg), missing), detail)
}

// GoneError is the answer when the machine that reported a checkout no
// longer has its directory: moved or deleted since it last reconciled.
// NotFound naming the machine and the path ON IT, with the
// ProjectCheckoutMissing detail — rather than the daemon's bare "does not
// exist: <path>", which reads as a server bug and does not say which machine
// looked.
func GoneError(ctx context.Context, names daemonplacement.DaemonReader, t Target, cause error) error {
	detail := &reliantv1.ProjectCheckoutMissing{
		ProjectId: t.Project.ID,
		DaemonId:  t.DaemonID,
		Path:      t.Path,
		State:     reliantv1.ProjectCheckoutState_PROJECT_CHECKOUT_STATE_ABSENT,
		RemoteUrl: remoteURL(t.Project),
	}
	msg := fmt.Sprintf("%s's checkout at %s is missing on %s (moved or deleted?): clone it there again, or open it on a machine that has it",
		strconv.Quote(t.Project.Name), t.Path, daemonplacement.MachineName(ctx, names, t.DaemonID))
	return svcerr.WithDetail(svcerr.WithCause(svcerr.WithMessage(svcerr.ErrNotFound, msg), cause), detail)
}

// unplaced places no machine, so the oldest install answers.
type unplaced struct{}

func (unplaced) PlacementDaemonID(context.Context, string, string) (string, error) {
	return "", errors.New("no daemon router")
}

func remoteURL(project *db.Project) string {
	if project.RemoteURL == nil {
		return ""
	}
	return strings.TrimSpace(*project.RemoteURL)
}
