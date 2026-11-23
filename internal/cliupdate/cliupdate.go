// Copyright (c) 2026 Reliant Labs

// Package cliupdate is how the reliant binary replaces itself with a newer
// signed release: forge's pkg/selfupdate (the mechanism: stage, verify, swap,
// confirm, revert) pointed at reliant's own release channel, the
// daemon-channels.json manifest and the keyless Sigstore signature that
// release.yml publishes (dev-docs/cli-release-artifacts.md).
//
// Two callers share it: `reliant update`, which a person runs, and a daemon
// started with --auto-update. Both refuse an install something else owns
// (selfupdate.Detect): the desktop app's bundle, Homebrew, a system package,
// a workspace pod's image, or a directory this user cannot write. Owner
// decisions D6 (never block a person's machine) and D7 (self-run auto-update
// is opt-in), forge docs/proposals/self-upgrade.md §4.
//
//forge:exclude-contract: configuration for forge's selfupdate.Updater (the one implementation) pinned to reliant's release channel; no second implementation and no seam to fake
package cliupdate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/pkg/selfupdate"
	"github.com/reliant-labs/forge/pkg/selfupdate/sigstore"

	"github.com/reliant-labs/reliant/internal/version"
)

// The release channel and its signing contract. These are the values
// dev-docs/cli-release-artifacts.md publishes for verifiers ("Signing"), and
// release.yml signs with (tools/test-affected/release_cli_workflow_test.go);
// TestSigningContractMatchesTheReleaseDoc holds them equal to the doc.
const (
	// ManifestURL is the live channel manifest, uploaded last by each release.
	ManifestURL = "https://downloads.reliantlabs.io/daemon-channels.json"
	// Repository is the repository the signed predicate must name.
	Repository = "reliant-labs/reliant"
	// SignerIdentity is the certificate identity: release.yml at a release tag.
	SignerIdentity = `^https://github\.com/reliant-labs/reliant/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`
	// SignerIssuer is the certificate's OIDC issuer.
	SignerIssuer = sigstore.GitHubActionsIssuer
	// PredicateType is the in-toto predicate type release.yml signs.
	PredicateType = "https://github.com/reliant-labs/reliant/attestations/release-assets/v1"
	// SignatureAsset is the bundle's file name in a release.
	SignatureAsset = "reliant-release.sigstore.json"
)

// Binary is the program's name: the archives are reliant_<os>_<arch>.tar.gz
// holding reliant (reliant.exe), forge's default naming for Binary, and the
// state directory is ~/.reliant/upgrade.
const Binary = "reliant"

// The channels the manifest carries. They follow the installers': a
// prerelease tag (-rc.N, -beta, -alpha) writes alpha, any other tag latest.
const (
	ChannelLatest = "latest"
	ChannelAlpha  = "alpha"
)

// DefaultConfirmWithin is how long a swapped daemon has to reach its gateway
// before the previous binary is put back. Longer than forge's 60s default: a
// daemon re-dials with backoff, and a revert marks the release failed on this
// machine until a newer one appears, so a short network blip must not cost a
// good release.
const DefaultConfirmWithin = 5 * time.Minute

// Options is what a caller may set; the zero value is the production channel
// for this binary.
type Options struct {
	// Channel is "latest" or "alpha"; empty is DefaultChannel of the running
	// version.
	Channel string
	// StateDir is the machine-level state directory; empty is
	// ~/.reliant/upgrade (selfupdate.DefaultStateDir).
	StateDir string
	// Current is the running build; zero is this binary's stamp.
	Current selfupdate.Build
	// ManifestURL replaces the production manifest (tests).
	ManifestURL string
	// HTTPClient fetches the manifest and the release files; nil is forge's
	// default client.
	HTTPClient *http.Client
	// Verifier replaces the Sigstore verifier pinned to release.yml (tests).
	Verifier selfupdate.Verifier
	// Managed is the caller's "something else runs me" predicate (a daemon
	// passes its host.Managed()).
	Managed       func() (owner string, managed bool)
	ConfirmWithin time.Duration
	Logger        *slog.Logger
}

// ValidChannel reports whether channel is one the manifest carries.
func ValidChannel(channel string) bool {
	return channel == ChannelLatest || channel == ChannelAlpha
}

// DefaultChannel is the channel a build of version v follows: alpha for a
// prerelease tag's build, latest for everything else.
func DefaultChannel(v string) string {
	cv := v
	if !strings.HasPrefix(cv, "v") {
		cv = "v" + cv
	}
	pre := semver.Prerelease(cv)
	for _, p := range []string{"-rc", "-alpha", "-beta"} {
		if strings.HasPrefix(pre, p) {
			return ChannelAlpha
		}
	}
	return ChannelLatest
}

// CurrentBuild is this binary's version and build number, the ordering the
// manifest's releases carry.
func CurrentBuild() selfupdate.Build {
	b := selfupdate.Build{Version: version.Version}
	if b.Version == "unknown" {
		b.Version = ""
	}
	if n := version.CommitTimeUnix(); n > 0 {
		b.CommitTime = time.Unix(n, 0).UTC()
	}
	return b
}

// Config is forge's selfupdate.Config for reliant with o applied. The caller
// adds what only it knows (Policy, Drainer, Shutdown) and calls
// selfupdate.New.
func Config(o Options) (selfupdate.Config, error) {
	current := o.Current
	if current == (selfupdate.Build{}) {
		current = CurrentBuild()
	}
	if err := orderable(current); err != nil {
		return selfupdate.Config{}, err
	}
	channel := o.Channel
	if channel == "" {
		channel = DefaultChannel(current.Version)
	}
	if !ValidChannel(channel) {
		return selfupdate.Config{}, fmt.Errorf("unknown release channel %q: use %q or %q", channel, ChannelLatest, ChannelAlpha)
	}
	stateDir := o.StateDir
	if stateDir == "" {
		dir, err := selfupdate.DefaultStateDir(Binary)
		if err != nil {
			return selfupdate.Config{}, err
		}
		stateDir = dir
	}
	verifier := o.Verifier
	if verifier == nil {
		v, err := NewVerifier(filepath.Join(stateDir, "tuf"))
		if err != nil {
			return selfupdate.Config{}, err
		}
		verifier = v
	}
	manifest := o.ManifestURL
	if manifest == "" {
		manifest = ManifestURL
	}
	confirm := o.ConfirmWithin
	if confirm <= 0 {
		confirm = DefaultConfirmWithin
	}
	return selfupdate.Config{
		Binary:        Binary,
		Current:       current,
		Source:        NewSource(manifest, o.HTTPClient),
		Channel:       channel,
		Verifier:      verifier,
		StateDir:      stateDir,
		ConfirmWithin: confirm,
		HTTPClient:    o.HTTPClient,
		Managed:       o.Managed,
		Logger:        o.Logger,
	}, nil
}

// ErrDevBuild is Config's refusal of a build with neither a version nor a
// build number: a local `go build`, which no release can be ordered against.
var ErrDevBuild = errors.New("this reliant is a development build (no release version or build number), so no release can be ordered against it; install a release build to use reliant update")

// orderable refuses a build no release can be compared with.
func orderable(b selfupdate.Build) error {
	if b.Version == "" && b.CommitTime.IsZero() {
		return ErrDevBuild
	}
	return nil
}

// NewVerifier is the keyless Sigstore verifier pinned to release.yml at a
// release tag, the predicate type it signs, and this repository. It fetches
// Sigstore's public-good trusted root through TUF at each verification,
// caching the metadata under tufCache (empty: memory only).
func NewVerifier(tufCache string) (*sigstore.Verifier, error) {
	return sigstore.New(verifierConfig(tufCache))
}

// verifierConfig is the signing contract NewVerifier pins; a test pins the
// trusted root as well.
func verifierConfig(tufCache string) sigstore.Config {
	return sigstore.Config{
		Repo:          Repository,
		Identity:      SignerIdentity,
		Issuer:        SignerIssuer,
		PredicateType: PredicateType,
		TUFCachePath:  tufCache,
	}
}

// NewSource reads the channel manifest at url. The alpha channel is the newer
// of alpha and latest: an RC train's stable release writes only latest, so an
// alpha client would otherwise stay on the last RC behind it
// (dev-docs/cli-release-artifacts.md, "Channels").
func NewSource(url string, client *http.Client) selfupdate.Source {
	m := selfupdate.ManifestSource{URL: url, Repository: Repository, Client: client}
	return selfupdate.SourceFunc(func(ctx context.Context, channel string) (selfupdate.Release, error) {
		if channel != ChannelAlpha {
			return m.Latest(ctx, channel)
		}
		alpha, alphaErr := m.Latest(ctx, ChannelAlpha)
		latest, latestErr := m.Latest(ctx, ChannelLatest)
		switch {
		case alphaErr != nil && latestErr != nil:
			return selfupdate.Release{}, errors.Join(alphaErr, latestErr)
		case alphaErr != nil:
			return latest, nil
		case latestErr != nil:
			return alpha, nil
		case releaseNewer(latest, alpha):
			return latest, nil
		default:
			return alpha, nil
		}
	})
}

// releaseNewer reports whether a is newer than b: by build number (commit
// time) when both carry one, else by semver.
func releaseNewer(a, b selfupdate.Release) bool {
	if !a.CommitTime.IsZero() && !b.CommitTime.IsZero() {
		return a.CommitTime.After(b.CommitTime)
	}
	av, bv := a.Version, b.Version
	if !strings.HasPrefix(av, "v") {
		av = "v" + av
	}
	if !strings.HasPrefix(bv, "v") {
		bv = "v" + bv
	}
	return semver.Compare(av, bv) > 0
}

// Owner names who owns the stamped installs reliant builds itself: the
// workspace image (Dockerfile, -X …selfupdate.managedBy=…) and the desktop
// app's copies of its CLI outside the bundle (a reliant.managed-by file).
const (
	OwnerWorkspaceImage = "reliant-workspace-image"
	OwnerDesktopApp     = "reliant-desktop"
)

// desktopAdvice is how the desktop app's reliant is updated.
const desktopAdvice = "update the Reliant desktop app (Reliant → Check for Updates, or brew upgrade --cask reliant if Homebrew installed it); it updates the reliant it ships"

// Advice is how the owner of in updates it: `reliant update` for a
// self-owned install, otherwise whatever owns it.
func Advice(in selfupdate.Install) string {
	switch in.Kind {
	case selfupdate.SelfOwned:
		return "reliant update"
	case selfupdate.Bundle:
		return desktopAdvice
	case selfupdate.Stamped:
		switch in.Owner {
		case OwnerDesktopApp:
			return desktopAdvice
		case OwnerWorkspaceImage:
			return "nothing to do: the Reliant platform updates this machine's image"
		}
		return "update it the way " + in.Owner + " installed it"
	case selfupdate.Managed:
		return "nothing to do: the Reliant platform updates this machine"
	case selfupdate.PackageManager:
		if in.Hint != "" {
			return in.Hint
		}
		return "update it with " + in.Owner
	case selfupdate.ReadOnly:
		return "run `sudo reliant update`, or reinstall reliant into a directory you own (such as ~/.local/bin) so it can update itself"
	}
	return "reinstall reliant from https://github.com/reliant-labs/reliant/releases/latest"
}

// OwnerPlatform is the owner Detect names for a managed host (a Reliant cloud
// machine), from the caller's predicate.
const OwnerPlatform = "reliant-platform"

// ManagedHost turns a "this is a managed host" fact into selfupdate's
// predicate.
func ManagedHost(managed bool) func() (string, bool) {
	return func() (string, bool) {
		if managed {
			return OwnerPlatform, true
		}
		return "", false
	}
}

// Detect says who may replace the running binary; managed is the caller's
// managed-host predicate (nil: none).
func Detect(managed func() (string, bool)) (selfupdate.Install, error) {
	return selfupdate.Detect(selfupdate.DetectOptions{Managed: managed})
}
