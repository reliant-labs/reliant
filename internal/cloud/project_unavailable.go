package cloud

import (
	"errors"
	"fmt"
)

// ReasonHostedProjectNotFound is the reason a control plane gives (in
// ReasonHeader, on a NotFound) when the hosted project a request names by id
// is not one the caller can see: it does not exist, or it belongs to an
// organization the caller is not in. The control plane gives both the same
// answer on purpose, so a project id never reveals that another tenant's
// project exists (control-plane#3323 did the same for ledger resources).
const ReasonHostedProjectNotFound = "hosted_project_not_found"

// ErrHostedProjectUnavailable matches (errors.Is) a *ProjectUnavailableError:
// the hosted project this request named by id is not one the caller can see.
var ErrHostedProjectUnavailable = errors.New("hosted project not available")

// legacyProjectRefusals are the messages an older control plane answered,
// as PermissionDenied, for a project id of an organization the caller is not
// in. That control plane sends no reason for it, so these fixed sentences are
// the only way to recognize the answer; this is the one place forge reads a
// control plane's message for control flow, and only on a request that named
// a project by id, where they mean nothing else.
var legacyProjectRefusals = map[string]bool{
	// A person who is not a member of the project's organization.
	"project not available to this caller": true,
	// A machine credential of another organization.
	"capability does not authorize this project": true,
}

// ProjectUnavailableError is a control plane saying that the hosted project
// a request named by id (its Forge-Project-ID, or ResolveHostedProject's
// projectId) is not the caller's to use. Whichever answer the control plane
// gave — NotFound with ReasonHostedProjectNotFound today, PermissionDenied
// before — forge treats it the same: it cannot tell a deleted project from
// one in an organization the caller left, and neither can the person.
//
// It deliberately does NOT unwrap to *Error. Several callers read a NotFound
// as an answer about the thing they asked for ("no such environment", "an
// older control plane without this procedure"); a project the caller cannot
// see is neither, and must never be mistaken for one. Wire holds the answer
// as it arrived.
type ProjectUnavailableError struct {
	// ProjectID is the hosted project id the request named.
	ProjectID string
	// Wire is the control plane's answer.
	Wire *Error
}

// Error is the same sentence whichever answer the control plane gave: the
// server's own text is deliberately left out (it is in Wire, for structured
// diagnostics), because an older control plane's PermissionDenied wording
// would make the two answers read differently, and the message would then
// tell a person something the new answer never does.
func (e *ProjectUnavailableError) Error() string {
	endpoint := ""
	if e.Wire != nil && e.Wire.Endpoint != "" {
		endpoint = " at " + e.Wire.Endpoint
	}
	return fmt.Sprintf(
		"hosted project %s is not available to this credential%s: no project with that id exists there, "+
			"or it belongs to an organization this credential does not act for\n"+
			"  the id is this checkout's binding to the control plane (.forge/hosted-projects.json)\n"+
			"fix: if you were removed from the project's organization, ask one of its owners to add you back;\n"+
			"     if the project was deleted, or the binding came from another checkout, remove its entry from\n"+
			"     .forge/hosted-projects.json and deploy again to bind this checkout afresh",
		e.ProjectID, endpoint)
}

// Is makes errors.Is(err, ErrHostedProjectUnavailable) true.
func (e *ProjectUnavailableError) Is(target error) bool { return target == ErrHostedProjectUnavailable }

// asProjectUnavailable turns a control plane's "that project is not yours to
// see" answer, to a request that named projectID, into a
// *ProjectUnavailableError, and returns any other error unchanged.
//
// resolving is true when the request was ResolveHostedProject asking for
// projectID itself: it resolves nothing else, so a plain NotFound (which every
// control plane gives for an id that never existed) is about the project. On
// any other request a NotFound may be about the resource it addressed, so only
// the reason counts there.
func asProjectUnavailable(err error, projectID string, resolving bool) error {
	var wire *Error
	if projectID == "" || !errors.As(err, &wire) {
		return err
	}
	switch {
	case wire.HasReason(ReasonHostedProjectNotFound),
		wire.HasCode(CodePermissionDenied) && legacyProjectRefusals[wire.Message],
		resolving && wire.HasCode(CodeNotFound):
		return &ProjectUnavailableError{ProjectID: projectID, Wire: wire}
	}
	return err
}
