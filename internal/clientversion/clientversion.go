// Copyright (c) 2026 Reliant Labs

// Package clientversion is how the server knows which Reliant client is
// calling and whether it is older than the server serves.
//
// A client names itself in ONE request header, `Reliant-Client:
// <kind>/<version>`: kind is one of web, desktop, daemon, cli, sdk-go, sdk-ts;
// version is semver without a leading v, prerelease and build metadata
// allowed (`1.9.2`, `1.9.2-dev+abc123`). User-Agent is left alone (a browser
// cannot set it).
//
// The floors are ServerProcessConfig.client_versions (ClientVersionPolicy).
// Judge is tri-state, like control-plane's daemon floor: a client is Below
// only when its version is valid semver that orders before a configured
// min_version (semver precedence, so 1.9.0-dev < 1.9.0; build metadata is
// ignored). No floor, an unknown kind or an unparseable version is Unknown —
// never forced. Pure functions over a header value and the typed config.
package clientversion

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/mod/semver"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
)

// Header is the request header a client names itself in.
const Header = "Reliant-Client"

// Kind is a client kind, as spelled in the header.
type Kind string

// The client kinds. The spelling is the wire contract (frontends/web,
// electron, the daemon, cmd/reliant, sdk/go and sdk/ts send it).
const (
	KindWeb     Kind = "web"
	KindDesktop Kind = "desktop"
	KindDaemon  Kind = "daemon"
	KindCLI     Kind = "cli"
	KindSDKGo   Kind = "sdk-go"
	KindSDKTS   Kind = "sdk-ts"
)

// Kinds is every client kind, in the order ClientVersionPolicy declares them.
var Kinds = []Kind{KindWeb, KindDesktop, KindDaemon, KindCLI, KindSDKGo, KindSDKTS}

// Client is a parsed Reliant-Client header.
type Client struct {
	Kind    Kind
	Version string
}

// ErrMissing is Parse's error for an absent or blank header.
var ErrMissing = errors.New("no Reliant-Client header")

// semverRe is semver 2.0.0 with no leading v: x/mod/semver alone would also
// accept the shorthands "1" and "1.2", which no client sends and no floor may
// name. It is the same pattern buf.validate holds min_version to
// (proto/reliant/config/v1/client_version_config.proto).
var semverRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(\.(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// ValidVersion reports whether v is semver without a leading v.
func ValidVersion(v string) bool { return semverRe.MatchString(v) }

// Parse reads a Reliant-Client header value. It returns ErrMissing for an
// absent or blank value, and another error for one it cannot read: an
// unknown kind, or a version that is not semver.
func Parse(value string) (Client, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return Client{}, ErrMissing
	}
	kind, version, ok := strings.Cut(value, "/")
	if !ok {
		return Client{}, fmt.Errorf("Reliant-Client %q is not <kind>/<version>", value)
	}
	k := Kind(strings.TrimSpace(kind))
	if !knownKind(k) {
		return Client{}, fmt.Errorf("Reliant-Client %q names an unknown client kind", value)
	}
	version = strings.TrimSpace(version)
	if !ValidVersion(version) {
		return Client{}, fmt.Errorf("Reliant-Client %q carries a version that is not semver", value)
	}
	return Client{Kind: k, Version: version}, nil
}

func knownKind(k Kind) bool {
	for _, known := range Kinds {
		if k == known {
			return true
		}
	}
	return false
}

// Floor is one client kind's floor: the oldest version served (empty: no
// floor) and what a client below it tells its user.
type Floor struct {
	MinVersion     string
	UpgradeURL     string
	UpgradeCommand string
}

// FloorFor is the policy's floor for kind; a kind the policy does not name
// (or a nil policy) has the zero Floor, which is no floor.
func FloorFor(p *configv1.ClientVersionPolicy, kind Kind) Floor {
	switch kind {
	case KindWeb:
		return Floor{p.GetWebMinVersion(), p.GetWebUpgradeUrl(), p.GetWebUpgradeCommand()}
	case KindDesktop:
		return Floor{p.GetDesktopMinVersion(), p.GetDesktopUpgradeUrl(), p.GetDesktopUpgradeCommand()}
	case KindDaemon:
		return Floor{p.GetDaemonMinVersion(), p.GetDaemonUpgradeUrl(), p.GetDaemonUpgradeCommand()}
	case KindCLI:
		return Floor{p.GetCliMinVersion(), p.GetCliUpgradeUrl(), p.GetCliUpgradeCommand()}
	case KindSDKGo:
		return Floor{p.GetSdkGoMinVersion(), p.GetSdkGoUpgradeUrl(), p.GetSdkGoUpgradeCommand()}
	case KindSDKTS:
		return Floor{p.GetSdkTsMinVersion(), p.GetSdkTsUpgradeUrl(), p.GetSdkTsUpgradeCommand()}
	}
	return Floor{}
}

// Result is Judge's verdict.
type Result int

const (
	// Unknown: no floor, or a version that cannot be compared. Never forced.
	Unknown Result = iota
	// Met: at or above the floor.
	Met
	// Below: older than the floor.
	Below
)

// Judge compares version against minVersion. Either one empty or not
// semver is Unknown.
func Judge(version, minVersion string) Result {
	if !ValidVersion(version) || !ValidVersion(minVersion) {
		return Unknown
	}
	if semver.Compare("v"+version, "v"+minVersion) < 0 {
		return Below
	}
	return Met
}
