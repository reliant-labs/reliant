package selfupdate

import (
	"context"
	"errors"
	"time"
)

// ErrUnsupported is returned where this platform cannot swap a running
// binary (neither unix nor Windows).
var ErrUnsupported = errors.New("selfupdate: replacing a running binary is not supported on this platform yet")

// Build is what is running, in the same ordering a [Release] carries.
type Build struct {
	// Version is the running binary's version, e.g. "1.4.2" or "v1.4.2".
	Version string
	// CommitTime orders builds whose versions are not semver (or are equal).
	CommitTime time.Time
}

// Asset is one downloadable file of a release.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Release is one candidate target, as a [Source] describes it.
type Release struct {
	Version    string
	Tag        string
	CommitTime time.Time
	Channel    string
	// Assets are the release's files: the per-os/arch archives and
	// checksums.txt.
	Assets []Asset
	// Bundle is the signature (a Sigstore attestation bundle) over Assets.
	// Zero when the release carries none.
	Bundle Asset
}

// label is the release's version, or its tag when it has none: the name of
// its staged directory and of its state records.
func (r Release) label() string {
	if r.Version != "" {
		return r.Version
	}
	return r.Tag
}

// Asset returns the asset named name.
func (r Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Source answers "what is the target release for this channel".
type Source interface {
	Latest(ctx context.Context, channel string) (Release, error)
}

// SourceFunc adapts a function to [Source].
type SourceFunc func(ctx context.Context, channel string) (Release, error)

// Latest calls f.
func (f SourceFunc) Latest(ctx context.Context, channel string) (Release, error) {
	return f(ctx, channel)
}

// Verifier checks downloaded files against the release's signature. files
// maps each downloaded asset's name (the archive, checksums.txt and the
// bundle, when the release has one) to its local path. A nil error means the
// files are exactly the ones the release's signer published FOR rel.Tag: a
// verifier must refuse a valid signature made for another release.
type Verifier interface {
	Verify(ctx context.Context, rel Release, files map[string]string) error
}

// Action is what to do about a release.
type Action int

const (
	// Skip does nothing.
	Skip Action = iota
	// Stage downloads, verifies and probes the release, without disruption.
	Stage
	// Apply stages it, drains, and swaps.
	Apply
)

func (a Action) String() string {
	switch a {
	case Skip:
		return "skip"
	case Stage:
		return "stage"
	case Apply:
		return "apply"
	}
	return "unknown"
}

// Urgency is how soon a release must be applied. The package only carries
// it; the app's [Policy] and [Drainer] give it meaning.
type Urgency int

// The urgencies, least urgent first. The order is all this package defines:
// what each one demands is the app's [Policy] and [Drainer] to decide.
const (
	// Routine is the least urgent, and the zero value.
	Routine Urgency = iota
	// Normal is more urgent than Routine.
	Normal
	// Security is more urgent than Normal: by its name, a security fix.
	Security
	// Emergency is the most urgent.
	Emergency
)

func (u Urgency) String() string {
	switch u {
	case Routine:
		return "routine"
	case Normal:
		return "normal"
	case Security:
		return "security"
	case Emergency:
		return "emergency"
	}
	return "unknown"
}

// Decision is what a [Policy] decided about one release.
type Decision struct {
	Action   Action
	Urgency  Urgency
	Deadline time.Time
	// Reason says why, for logs and the app's UI.
	Reason string
}

// Policy is the only policy in the package. [Updater.Check] asks it only
// about a release that is newer than the running build and has not failed
// here before.
type Policy interface {
	Decide(ctx context.Context, cur Build, rel Release) Decision
}

// StageOnly is the default [Policy]: stage every newer release, never apply
// one. An app that wants to apply supplies its own policy.
type StageOnly struct{}

// Decide returns [Stage].
func (StageOnly) Decide(context.Context, Build, Release) Decision {
	return Decision{Action: Stage, Reason: "default policy stages and never applies"}
}

// DrainReport is what a [Drainer] reports once in-flight work is finished or
// its deadline passed.
type DrainReport struct {
	// Interrupted counts in-flight work the drain cut off at its deadline.
	Interrupted int
	// Detail is free text for the log.
	Detail string
}

// Drainer is the app's: stop admitting new work and finish what is in flight
// by deadline. An error aborts the upgrade (nothing is swapped), which is how
// an app declines to interrupt work at a routine urgency.
type Drainer interface {
	Drain(ctx context.Context, deadline time.Time) (DrainReport, error)
}
