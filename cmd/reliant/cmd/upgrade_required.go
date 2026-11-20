// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
	"github.com/reliant-labs/reliant/internal/version"
)

// ============================================================================
// Client identity and the server's refusal of an old CLI.
//
// Every request the CLI makes names it: `Reliant-Client: cli/<version>`, set by
// bearerAuthTransport. A server whose floor this version is below refuses with
// FailedPrecondition, the reason errreason.UpgradeRequired in
// svcerr.ReasonHeader, and a google.rpc.ErrorInfo detail carrying the floor
// and how to upgrade. cliError, called once by Execute, turns that refusal into
// one line naming the upgrade command, and a distinct exit status, whichever
// command hit it and however it wrapped the error.
// ============================================================================

const (
	// reliantClientHeader names the client kind and version on every request.
	// User-Agent is left as it is.
	reliantClientHeader = transport.ReliantClientHeader
	// cliClientKind is this binary's kind in reliantClientHeader.
	cliClientKind = "cli"

	// upgradeRequiredErrorInfoReason and upgradeRequiredErrorInfoDomain
	// identify the refusal's ErrorInfo detail.
	upgradeRequiredErrorInfoReason = "UPGRADE_REQUIRED"
	upgradeRequiredErrorInfoDomain = "reliantlabs.io"

	// upgradeRequiredExitCode is the CLI's exit status when the server refuses
	// its version. Distinct from 1 (any failure) and from the other statuses a
	// command documents (2 timeout, 3 gate, 4 funding question, 75 daemon
	// upgrade restart), so a script can tell "upgrade, then retry" apart.
	upgradeRequiredExitCode = 10
)

// cliClientIdentity is this binary's Reliant-Client value. A build with no
// version stamp sends "cli/unknown", which the server never forces.
func cliClientIdentity() string {
	return transport.ClientIdentity(cliClientKind)
}

// upgradeRequiredError is the server's refusal of this CLI's version.
type upgradeRequiredError struct {
	// clientVersion is the version the server saw, else this binary's.
	clientVersion string
	// minVersion is the server's floor. Empty when the refusal is of a
	// request that reached the server with no Reliant-Client header at all.
	minVersion     string
	upgradeCommand string
	upgradeURL     string
	cause          error
}

func (e *upgradeRequiredError) Error() string {
	upgrade := e.upgradeCommand
	if upgrade == "" {
		upgrade = e.upgradeURL
	}
	if e.minVersion == "" {
		// The server refuses unidentified clients. This CLI always sends the
		// header, so something on the way dropped it, or an older build of
		// this CLI is the one running.
		msg := fmt.Sprintf("this server refused reliant %s: no Reliant-Client version reached it. Upgrade to the latest version", e.clientVersion)
		if upgrade != "" {
			msg += ": " + upgrade
		}
		return msg
	}
	if upgrade == "" {
		upgrade = "install the latest version"
	}
	return fmt.Sprintf("reliant %s is older than the minimum this server supports (%s). Upgrade: %s",
		e.clientVersion, e.minVersion, upgrade)
}

func (e *upgradeRequiredError) Unwrap() error { return e.cause }

// ExitCode is the process exit code the CLI's main returns.
func (e *upgradeRequiredError) ExitCode() int { return upgradeRequiredExitCode }

// asUpgradeRequired finds the server's upgrade_required refusal anywhere in
// err's chain: by the reason header, or by the ErrorInfo detail alone (a proxy
// may drop the header; the detail rides the body).
func asUpgradeRequired(err error) (*upgradeRequiredError, bool) {
	var already *upgradeRequiredError
	if errors.As(err, &already) {
		return already, true
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil, false
	}
	info := upgradeErrorInfo(ce)
	if info == nil && ce.Meta().Get(svcerr.ReasonHeader) != errreason.UpgradeRequired {
		return nil, false
	}
	meta := info.GetMetadata()
	clientVersion := meta["client_version"]
	if clientVersion == "" {
		clientVersion = strings.TrimPrefix(version.Get().Version, "v")
	}
	return &upgradeRequiredError{
		clientVersion:  clientVersion,
		minVersion:     meta["min_version"],
		upgradeCommand: meta["upgrade_command"],
		upgradeURL:     meta["upgrade_url"],
		cause:          err,
	}, true
}

// upgradeErrorInfo is ce's UPGRADE_REQUIRED ErrorInfo detail, or nil.
func upgradeErrorInfo(ce *connect.Error) *errdetails.ErrorInfo {
	for _, d := range ce.Details() {
		v, err := d.Value()
		if err != nil {
			continue
		}
		if info, ok := v.(*errdetails.ErrorInfo); ok &&
			info.GetReason() == upgradeRequiredErrorInfoReason && info.GetDomain() == upgradeRequiredErrorInfoDomain {
			return info
		}
	}
	return nil
}

// cliError is the error Execute prints and exits with: the upgrade refusal in
// place of whatever wrapped it, else err unchanged.
func cliError(err error) error {
	if upgrade, ok := asUpgradeRequired(err); ok {
		return upgrade
	}
	return err
}
