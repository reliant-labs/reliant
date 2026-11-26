package topology

import (
	"fmt"
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/release"
)

// Input is everything [Assemble] needs, as plain records. A gatherer fills it
// from wherever it reads — a checkout, a control plane's store — and never
// decides anything Assemble decides.
type Input struct {
	// Project is the forge project name.
	Project string
	// Ledger is the report-level ledger label (Report.Ledger). A control
	// plane passes its endpoint, never a machine path.
	Ledger string
	// GeneratedAt is when the gatherer took the snapshot. The package has
	// no clock, so the caller passes it.
	GeneratedAt time.Time
	// Releases is the project's release set for the report header
	// (latest_release, releases[]). Assemble orders a copy with
	// SortReleasesNewestFirst, so any order is accepted.
	Releases []release.Release
	// Envs is one entry per environment. Assemble sorts by name and keeps
	// the first entry of a duplicated name.
	Envs []EnvInput
	// Verified reports that the gatherer ran a verification pass. It
	// decides the verdict: an unverified report is always OK.
	Verified bool
	// Source and Catalog are copied to the report.
	Source  string
	Catalog *Catalog
}

// EnvInput is one environment's gathered facts.
//
// THE ERROR FIELDS ARE FACTS TOO. A gatherer that could not read an env's
// ledger reports that here rather than failing the report: one env's failed
// read becomes a note on its row, and the other environments' answers are
// still worth having.
type EnvInput struct {
	// Name is the environment name.
	Name string
	// Declared is whether the gatherer's source declares the env.
	Declared bool
	// Lifecycle is the declared lifecycle ("", "local", "ephemeral").
	Lifecycle string
	// Note is the gatherer's own note about the declaration — an env not
	// declared here, a destination that could not be resolved. Later
	// facts may replace it (a failed ledger read) or defer to it (the
	// "never promoted" note is only used when Note is empty). A control
	// plane uses fixed templates here, never a machine path.
	Note string

	// Where the env runs; copied as given (see Env).
	Destination      string
	ControlPlaneKind string
	Endpoint         string
	EnvironmentID    string
	Verdict          string
	Workloads        []Workload
	Placements       []Placement
	// KubeContext and Namespace are reported only for a bound env.
	KubeContext string
	Namespace   string

	// LedgerErr is set when the env's ledger could not be resolved at
	// all. The row then stops at its declaration.
	LedgerErr error
	// Ledger is the env's ledger label; Hosted is whether it is a control
	// plane (rollout phase applies only then).
	Ledger string
	Hosted bool
	// BindingErr is set when the ledger was resolved but its current
	// binding could not be read. The row then stops there.
	BindingErr error
	// Binding is the env's current promotion; nil when it was never
	// promoted.
	Binding *release.Promotion
	// NotApplied and ApplyError record that the binding's apply failed.
	NotApplied bool
	ApplyError string
	// RolloutPhase is the current promotion's rollout phase, or
	// RolloutErr why it could not be read. Used only when Hosted.
	RolloutPhase string
	RolloutErr   error

	// Releases is the release set this env's provenance and lag are
	// measured against — the project's for a file ledger, the control
	// plane's list for an env recorded there. Any order is accepted.
	Releases []release.Release
	// ReleasesLocation names where Releases came from, for the note on a
	// binding whose release is not in them.
	ReleasesLocation string
	// BoundRelease is the bound release's record when the gatherer holds
	// it but Releases is a bounded window that does not reach it (a
	// control plane reading the newest N). With ReleasesBehind — the exact
	// count of releases cut after it — it yields provenance and an exact
	// lag without shipping every release. Ignored when Releases holds the
	// bound version.
	BoundRelease   *release.Release
	ReleasesBehind int

	// Observed is a verdict per image name, from a verification pass or
	// a platform observation. Images with no entry stay NotVerified.
	Observed map[string]Observation

	// Tracked, BundleDigest and BundleGit are copied to the row.
	Tracked      *bool
	BundleDigest string
	BundleGit    *BundleGit
}

// Observation is one image's observed state.
type Observation struct {
	State   ImageState
	Running string
	Detail  string
	// ObservedBy is ObservedByPlatform for a control plane's own
	// observation; empty for the caller's --verify.
	ObservedBy string
}

// Assemble builds the report. Pure and deterministic: the same Input always
// yields the same Report.
func Assemble(in Input) Report {
	report := Report{
		Project:      in.Project,
		Ledger:       in.Ledger,
		GeneratedAt:  formatTime(in.GeneratedAt),
		Releases:     []string{},
		Images:       []string{},
		Environments: []Env{},
		Verified:     in.Verified,
		OK:           true,
		Source:       in.Source,
		Catalog:      in.Catalog,
	}

	releases := sortedCopy(in.Releases)
	for _, rel := range releases {
		report.Releases = append(report.Releases, rel.Version)
	}
	if len(releases) > 0 {
		report.LatestRelease = releases[0].Version
		report.LatestReleaseCreatedAt = formatTime(releases[0].CreatedAt)
	}

	envs := append([]EnvInput(nil), in.Envs...)
	sort.SliceStable(envs, func(i, j int) bool { return envs[i].Name < envs[j].Name })
	imageSet := map[string]bool{}
	for i, e := range envs {
		if i > 0 && e.Name == envs[i-1].Name {
			continue
		}
		row := assembleEnv(e)
		report.Environments = append(report.Environments, row)
		for _, img := range row.Images {
			imageSet[img.Image] = true
			switch img.State {
			case NotVerified:
				report.Tally.NotVerified++
			case Match:
				report.Tally.Match++
			case Drift:
				report.Tally.Drift++
			case Missing:
				report.Tally.Missing++
			case Untagged:
				report.Tally.Untagged++
			case Unreachable:
				report.Tally.Unreachable++
			}
		}
	}
	for image := range imageSet {
		report.Images = append(report.Images, image)
	}
	sort.Strings(report.Images)

	// THE VERDICT. An unverified report is always OK: it read a ledger and
	// reported what was in it, which cannot prove an environment wrong. A
	// report that went red because nothing had been verified would be red
	// on every read, and a permanently-red signal is a deleted one.
	if in.Verified {
		switch {
		case report.Tally.Drift > 0 || report.Tally.Missing > 0:
			report.Detail = fmt.Sprintf(
				"%d image(s) drifted and %d missing across %d environment(s) — at least one environment is not running the release it is bound to",
				report.Tally.Drift, report.Tally.Missing, len(report.Environments))
		case report.Tally.Unreachable > 0:
			report.Detail = fmt.Sprintf(
				"%d image(s) could not be checked (cluster, context or credentials), %d matched, 0 drifted",
				report.Tally.Unreachable, report.Tally.Match)
		}
	}
	report.OK = report.ExitCode() == 0
	return report
}

// ExitCode is the process exit status the report's verdict maps to: 0 when
// unverified or clean, 1 when an environment is proven not to run what it is
// bound to (drift or missing), 2 when some image could not be checked and
// none was proven wrong. "Proven wrong" and "could not check" differ because
// a VPN drop is not evidence that a release is wrong.
func (r Report) ExitCode() int {
	if !r.Verified {
		return 0
	}
	switch {
	case r.Tally.Drift > 0 || r.Tally.Missing > 0:
		return 1
	case r.Tally.Unreachable > 0:
		return 2
	}
	return 0
}

// assembleEnv builds one environment's row. The order of the steps is the
// note precedence: a later failure replaces the declaration's note, a later
// explanation defers to it.
func assembleEnv(in EnvInput) Env {
	row := Env{
		Env:              in.Name,
		Declared:         in.Declared,
		Lifecycle:        in.Lifecycle,
		Note:             in.Note,
		Images:           []Image{},
		Destination:      in.Destination,
		ControlPlaneKind: in.ControlPlaneKind,
		Endpoint:         in.Endpoint,
		EnvironmentID:    in.EnvironmentID,
		Verdict:          in.Verdict,
		Workloads:        in.Workloads,
		Placements:       in.Placements,
		Tracked:          in.Tracked,
		BundleDigest:     in.BundleDigest,
		BundleGit:        in.BundleGit,
	}

	if in.LedgerErr != nil {
		row.Note = fmt.Sprintf("could not resolve this environment's release ledger: %v", in.LedgerErr)
		return row
	}
	row.Hosted = in.Hosted
	row.Ledger = in.Ledger
	if in.BindingErr != nil {
		row.Note = fmt.Sprintf("could not read the promotion ledger: %v", in.BindingErr)
		return row
	}
	if in.Binding == nil {
		if row.Note == "" {
			row.Note = "never promoted — no release is bound to this environment"
		}
		return row
	}

	b := in.Binding
	row.Bound = true
	row.Release = b.Release
	row.Kind = b.Kind
	row.PromotedAt = formatTime(b.PromotedAt)
	row.PromotionID = b.ID
	row.FromEnv = b.FromEnv
	row.GatesSummary = SummarizeGates(b.Gates, b.RecordedGates)
	if in.NotApplied {
		row.NotApplied, row.ApplyError = true, in.ApplyError
	}
	if in.Hosted {
		if in.RolloutErr == nil {
			row.RolloutPhase = in.RolloutPhase
		} else if row.Note == "" {
			row.Note = fmt.Sprintf("rollout phase unavailable: %v", in.RolloutErr)
		}
	}

	releases := sortedCopy(in.Releases)
	if rel, ok := findRelease(releases, b.Release); ok {
		row.ReleaseKnown = true
		git := rel.Git
		row.Git = &git
		row.ReleaseCreatedAt = formatTime(rel.CreatedAt)
		row.Lag = PromotionLag(releases, rel)
	} else if in.BoundRelease != nil && in.BoundRelease.Version == b.Release {
		rel := *in.BoundRelease
		row.ReleaseKnown = true
		git := rel.Git
		row.Git = &git
		row.ReleaseCreatedAt = formatTime(rel.CreatedAt)
		if len(releases) > 0 {
			row.Lag = lagAt(releases[0], rel, in.ReleasesBehind)
		}
	} else if row.Note == "" {
		// A FIRST-CLASS STATE, NOT AN ERROR. The binding is real and says
		// exactly what this env runs; only the record describing the
		// release is absent. Provenance and lag are omitted rather than
		// guessed — a lag against a release set that does not contain
		// the env's own release would be a number with no meaning.
		row.Note = fmt.Sprintf("release %s is not in %s — it was cut elsewhere, so its provenance and promotion lag are unknown",
			b.Release, in.ReleasesLocation)
	}

	images := make([]string, 0, len(b.Resolved))
	for name := range b.Resolved {
		images = append(images, name)
	}
	sort.Strings(images)
	for _, name := range images {
		img := Image{Image: name, Digest: b.Resolved[name], State: NotVerified}
		// An image with no observation stays not_verified: inventing a
		// state for an image nothing reported on is the one move that
		// could make an unchecked cell look checked.
		if o, ok := in.Observed[name]; ok {
			img.State, img.Running, img.Detail, img.ObservedBy = o.State, o.Running, o.Detail, o.ObservedBy
		}
		row.Images = append(row.Images, img)
	}

	row.KubeContext = in.KubeContext
	row.Namespace = in.Namespace
	return row
}

func sortedCopy(releases []release.Release) []release.Release {
	out := append([]release.Release(nil), releases...)
	SortReleasesNewestFirst(out)
	return out
}

func findRelease(releases []release.Release, version string) (release.Release, bool) {
	for _, rel := range releases {
		if rel.Version == version {
			return rel, true
		}
	}
	return release.Release{}, false
}
