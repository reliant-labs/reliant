// Copyright (c) 2026 Reliant Labs
package tools_daemon

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/reliant-labs/forge/pkg/svcerr"
	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
)

// The daemon version floor, from the gateway's side: ClientVersionPolicy's
// daemon floor (ServerProcessConfig.client_versions.daemon_*) held against the
// build a daemon reports when it registers (daemonBuildFromRegister).
//
// A daemon is below the floor when its commit time is before
// min_commit_time_unix: the key that orders builds, which semver cannot (every
// build between two tags is the same X.Y.Z). Only when either side has no
// commit time does semver decide, against min_version. A build with neither
// (it predates the report, or was not stamped) is unknown, and an unknown
// build is never forced.
//
// What the gateway does with a daemon below the floor:
//
//   - One that acts on RestartRequested (drains_on_restart_request) is sent
//     exactly one, right after its registration_ack. A daemon on a person's
//     machine only reports AWAITING_OWNER and prints the upgrade advice: it
//     never drains (owner decision D6), so it is told whether or not the floor
//     is enforced. A managed machine drains when told, refusing new tool calls
//     until its pod is rolled onto a new image, so it is told only once the
//     floor is enforced: the observe phase changes no machine's behaviour.
//   - One that predates the drain protocol cannot be asked. With enforce on,
//     its registration is refused (FailedPrecondition + upgrade_required); its
//     reconnect backoff paces the retries. Such a daemon never reads
//     RegistrationAck.accepted, so accepted=false would be served as a
//     success. With enforce off it is logged and counted.
//
// The gateway's outbound streams (it dials a managed pod the control plane
// names) are never refused: the remedy for an old pod is the workspace
// operator's image roll, and refusing it would only make the machine
// unreachable until then.

// upgradeRequiredReason is the x-forge-error-reason of a refusal for an old
// client: errreason.UpgradeRequired's spelling, the frozen client-upgrade
// contract's ("upgrade_required"), which every first-party client branches on.
const upgradeRequiredReason = "upgrade_required"

// The refusal's google.rpc.ErrorInfo, as the API server's refusal carries it.
const (
	upgradeRequiredInfoReason = "UPGRADE_REQUIRED"
	upgradeRequiredInfoDomain = "reliantlabs.io"
)

// floorRestartReason is RestartRequested.reason for a daemon below the floor.
const floorRestartReason = "below the server's minimum supported version; upgrade to the latest version"

// floorVerdict is judgeDaemonBuild's answer.
type floorVerdict int

const (
	// floorUnknown: no floor, or a build that cannot be compared with it.
	floorUnknown floorVerdict = iota
	// floorMet: at or above the floor.
	floorMet
	// floorBelow: older than the floor.
	floorBelow
)

func (v floorVerdict) String() string {
	switch v {
	case floorMet:
		return "met"
	case floorBelow:
		return "below"
	default:
		return "unknown"
	}
}

// floorAction is what the gateway does about a registering daemon's build.
type floorAction int

const (
	// floorActionNone: at or above the floor, or unknown.
	floorActionNone floorAction = iota
	// floorActionRestart: send one RestartRequested.
	floorActionRestart
	// floorActionRefuse: refuse the registration (upgrade_required).
	floorActionRefuse
	// floorActionObserve: below the floor, but only logged and counted.
	floorActionObserve
)

// The outcome label of reliant_daemon_version_floor_total.
func (a floorAction) outcome() string {
	switch a {
	case floorActionRestart:
		return "restart_requested"
	case floorActionRefuse:
		return "refused"
	case floorActionObserve:
		return "observed"
	default:
		return ""
	}
}

// daemonSemverRe is semver 2.0.0 without a leading v. x/mod/semver alone
// would also take the shorthands "1" and "1.2", which no build reports and no
// floor may name (buf.validate holds min_version to the same pattern).
var daemonSemverRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// daemonSemver is a reported daemon version as x/mod/semver reads it, or ""
// when it is not semver. A `go install` build reports the module version, with
// its leading v; a commit with no release base reports a bare sha.
func daemonSemver(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if !daemonSemverRe.MatchString(v) {
		return ""
	}
	return "v" + v
}

// daemonFloor is ClientVersionPolicy's daemon floor (its daemon_* fields).
// The zero value is no floor.
type daemonFloor struct {
	minVersion        string
	minCommitTimeUnix int64
	upgradeURL        string
	upgradeCommand    string
}

// daemonFloorOf reads the daemon floor from policy; nil is no floor.
func daemonFloorOf(policy *configv1.ClientVersionPolicy) daemonFloor {
	return daemonFloor{
		minVersion:        policy.GetDaemonMinVersion(),
		minCommitTimeUnix: policy.GetDaemonMinCommitTimeUnix(),
		upgradeURL:        policy.GetDaemonUpgradeUrl(),
		upgradeCommand:    policy.GetDaemonUpgradeCommand(),
	}
}

// judgeDaemonBuild holds build against floor (see the top of this file).
func judgeDaemonBuild(build db.DaemonBuild, floor daemonFloor) floorVerdict {
	if minTime := floor.minCommitTimeUnix; minTime > 0 && build.CommitTimeUnix > 0 {
		if build.CommitTimeUnix < minTime {
			return floorBelow
		}
		return floorMet
	}
	minVersion, version := daemonSemver(floor.minVersion), daemonSemver(build.Version)
	if minVersion == "" || version == "" {
		return floorUnknown
	}
	if semver.Compare(version, minVersion) < 0 {
		return floorBelow
	}
	return floorMet
}

// decideFloorAction is what the gateway does about a daemon registering with
// reg, under policy. managed is whether the daemon may restart itself when
// asked (a workspace pod); outbound is a stream the gateway dialed.
func decideFloorAction(policy *configv1.ClientVersionPolicy, reg *reliantv1.DaemonRegister, managed, outbound bool) (floorAction, floorVerdict) {
	verdict := judgeDaemonBuild(daemonBuildFromRegister(reg), daemonFloorOf(policy))
	if verdict != floorBelow {
		return floorActionNone, verdict
	}
	enforce := policy.GetEnforce()
	switch {
	case reg.GetDrainsOnRestartRequest() && (!managed || enforce):
		return floorActionRestart, verdict
	case !reg.GetDrainsOnRestartRequest() && enforce && !outbound:
		return floorActionRefuse, verdict
	default:
		return floorActionObserve, verdict
	}
}

// registeredManaged reports whether a registration says the daemon runs on a
// managed machine, in the vocabulary the daemon row stores. A daemon that may
// drain itself (daemonruntime Host.Managed) always registers as "managed";
// one that says "cloud" without being declared managed is judged managed
// too, which only ever holds a restart back.
func registeredManaged(reg *reliantv1.DaemonRegister) bool {
	t := normalizeRegisteredDaemonType(reg.GetDaemonType())
	return t != nil && *t == "managed"
}

// applyVersionFloor decides what to do about a registering daemon's build,
// logs and counts it. It returns the RestartRequested to send once the
// registration is acknowledged (nil: none), or the refusal to end the
// registration with (nil: none).
func (s *Service) applyVersionFloor(ctx context.Context, userID, daemonID string, reg *reliantv1.DaemonRegister, managed, outbound bool) (*reliantv1.ServerMessage, error) {
	policy := s.deps.ClientVersions
	action, verdict := decideFloorAction(policy, reg, managed, outbound)
	if action == floorActionNone {
		return nil, nil
	}
	floor := daemonFloorOf(policy)
	observability.DaemonVersionFloorTotal.WithLabelValues(action.outcome()).Inc()
	attrs := []any{
		"userID", userID, "daemonID", daemonID, "verdict", verdict.String(),
		"version", reg.GetVersion(), "commit", reg.GetCommit(), "commitTimeUnix", reg.GetCommitTimeUnix(),
		"minVersion", floor.minVersion, "minCommitTimeUnix", floor.minCommitTimeUnix,
		"drainsOnRestartRequest", reg.GetDrainsOnRestartRequest(), "managed", managed,
		"outbound", outbound, "enforce", policy.GetEnforce(),
	}
	switch action {
	case floorActionRestart:
		logging.InfoContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Daemon is below the minimum supported version; asking it to restart", attrs...)
		return floorRestartRequest(floor), nil
	case floorActionRefuse:
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Refused a daemon below the minimum supported version that cannot be asked to restart", attrs...)
		return nil, floorRefusal(reg, floor)
	default:
		logging.WarnContext(ctx, LOG_PREFIX_TOOLS_DAEMON+" Daemon is below the minimum supported version (observed, not acted on)", attrs...)
		return nil, nil
	}
}

// floorRestartRequest is the RestartRequested a daemon below floor is sent.
// NORMAL urgency with no deadline of the gateway's own: a managed daemon
// restarts at a quiet point within its drain limit, and a person's machine is
// only told (D6).
func floorRestartRequest(floor daemonFloor) *reliantv1.ServerMessage {
	return &reliantv1.ServerMessage{Message: &reliantv1.ServerMessage_RestartRequested{RestartRequested: &reliantv1.RestartRequested{
		TargetVersion:  floor.minVersion,
		CommitTimeUnix: floor.minCommitTimeUnix,
		Urgency:        reliantv1.RestartUrgency_RESTART_URGENCY_NORMAL,
		Reason:         floorRestartReason,
		UpgradeCommand: floor.upgradeCommand,
		UpgradeUrl:     floor.upgradeURL,
	}}}
}

// floorRefusal is the refusal of a daemon below floor that cannot be asked to
// restart, in the frozen upgrade_required shape every client reads.
func floorRefusal(reg *reliantv1.DaemonRegister, floor daemonFloor) error {
	version := reg.GetVersion()
	if version == "" {
		version = "unknown version"
	}
	minimum := floor.minVersion
	if t := floor.minCommitTimeUnix; t > 0 && reg.GetCommitTimeUnix() > 0 {
		minimum = "a build of " + time.Unix(t, 0).UTC().Format(time.RFC3339) + " or later"
	}
	how := floor.upgradeCommand
	if how == "" {
		how = floor.upgradeURL
	}
	if how == "" {
		how = "install the latest version"
	}
	msg := fmt.Sprintf("this daemon (%s) is older than the minimum this server supports (%s); upgrade: %s", version, minimum, how)
	return svcerr.WithDetail(
		svcerr.WithReason(svcerr.FailedPrecondition(msg), upgradeRequiredReason),
		&errdetails.ErrorInfo{
			Reason: upgradeRequiredInfoReason,
			Domain: upgradeRequiredInfoDomain,
			Metadata: map[string]string{
				"client_kind":     "daemon",
				"client_version":  reg.GetVersion(),
				"min_version":     floor.minVersion,
				"upgrade_url":     floor.upgradeURL,
				"upgrade_command": floor.upgradeCommand,
			},
		})
}

// sendFloorRestart queues the one RestartRequested applyVersionFloor decided
// on for conn (nil: none). Best effort: a request that cannot be queued is
// sent again at the daemon's next registration.
func (s *Service) sendFloorRestart(conn *daemonConnection, msg *reliantv1.ServerMessage) {
	if msg == nil {
		return
	}
	if err := s.sendToConn(conn, msg); err != nil {
		logging.Warn(LOG_PREFIX_TOOLS_DAEMON+" Could not queue the version floor's restart request; it is sent again at the next registration",
			"error", err, "userID", conn.userID, "daemonID", conn.daemonID)
	}
}
