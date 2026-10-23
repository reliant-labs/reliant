// Copyright (c) 2025 Reliant Labs
package forge

import (
	"encoding/json"
	"fmt"
	"strings"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/errreason"
)

// A forge command that could not AUTHENTICATE to the control plane did not
// answer the question it was asked: it never got to ask. The deploy panel has
// to say who can fix that — the user, by signing in (no_control_plane_credential),
// or an org admin, by granting a scope (scope_missing) — so the refusal comes
// back as this rpc's error, carrying forge's reason and scopes
// (13-p9-frontend-plan.md §7.1 #7), rather than as a report document the panel
// would render as a deploy that failed.
//
// Only PlanDeploy reads it: the deploy panel's pre-plan call
// (frontends/web/src/services/forge/deployAuthz.ts). The other forge rpcs keep
// returning forge's document whatever it says.
//
// forge reports it in its --json document as error_reason / error_scopes
// beside `error` (forge's json_error_facts.go). This is the only place this
// layer reads forge's document, and it reads only those fields: everything
// else stays opaque (see forgeReportReply).

// forgeAuthzReasons are the reasons that mean forge could not ask.
var forgeAuthzReasons = map[string]bool{
	errreason.ScopeMissing:             true,
	errreason.NoControlPlaneCredential: true,
}

// forgeErrorFacts is the slice of a forge --json document this layer reads.
type forgeErrorFacts struct {
	OK          *bool    `json:"ok"`
	Error       string   `json:"error"`
	ErrorReason string   `json:"error_reason"`
	ErrorScopes []string `json:"error_scopes"`
}

// forgeAuthzRefusal returns the rpc error for a report whose forge run was
// refused authentication or authority by the control plane, and nil for every
// other report — including every other failure, which stays data.
//
// The message keeps the shape a client read before forge emitted a document for
// this failure: forge exited without one, and the daemon relayed its stderr as
// "forge command failed: exit N: …", which the shared mapper prefixed with the
// command. forge's own error text now stands where its stderr excerpt did, and
// it carries the words desktop builds from before the reason match on ("no
// control-plane credential", "permissions do not include").
//
// The code is never Unauthenticated, whatever the control plane said: on a
// reliant rpc that code means "your Reliant session is invalid", and the web
// would re-authenticate the user over a credential forge holds.
func forgeAuthzRefusal(commandType string, reply forgeReportReply) error {
	if len(reply.Report) == 0 {
		return nil
	}
	var facts forgeErrorFacts
	if json.Unmarshal(reply.Report, &facts) != nil || facts.OK == nil || *facts.OK {
		return nil
	}
	reason := strings.TrimSpace(facts.ErrorReason)
	if !forgeAuthzReasons[reason] {
		return nil
	}
	msg := fmt.Sprintf("daemon command %s failed: %s: exit %d: %s",
		commandType, forgeErrCommandFailedMarker, reply.ExitCode, flattenForgeError(facts.Error))
	base := svcerr.FailedPrecondition(msg)
	if reason == errreason.ScopeMissing {
		base = svcerr.PermissionDenied(msg)
	}
	return svcerr.WithMetadata(svcerr.WithReason(base, reason),
		fat.ScopesHeader, fat.ScopesHeaderValue(facts.ErrorScopes))
}

// flattenForgeError renders forge's multi-line error the way the daemon
// renders forge's stderr for a message: one line, " | " between them.
func flattenForgeError(text string) string {
	return strings.ReplaceAll(strings.TrimSpace(text), "\n", " | ")
}
