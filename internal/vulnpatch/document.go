package vulnpatch

import "github.com/reliant-labs/forge/internal/vulnscan"

// DocumentSchema names the vuln-patch document and its major version. Field
// names under it are stable: a change that would break a v1 reader is a v2,
// never an edit of v1.
const DocumentSchema = "forge.dev/vuln-patch/v1"

// Ecosystem is the package manager a finding's package belongs to.
type Ecosystem string

// The ecosystems with a patcher.
const (
	EcosystemGo   Ecosystem = "go"
	EcosystemNPM  Ecosystem = "npm"
	EcosystemPNPM Ecosystem = "pnpm"
	EcosystemYarn Ecosystem = "yarn"
	// EcosystemDocker (ospkg.go) is a Dockerfile's base image.
)

// Ecosystems are the values --ecosystem accepts.
func Ecosystems() []Ecosystem {
	return []Ecosystem{EcosystemGo, EcosystemNPM, EcosystemPNPM, EcosystemYarn, EcosystemDocker}
}

// Status is what happened to one finding.
type Status string

const (
	// StatusFixed is bumped, and the rescan no longer reports it (or the tree
	// already required a fixed version).
	StatusFixed Status = "fixed"
	// StatusStillPresent is bumped, and the rescan still reports it.
	StatusStillPresent Status = "still_present"
	// StatusBumped is bumped, and not verified (no --verify).
	StatusBumped Status = "bumped"
	// StatusFailed is when the ecosystem's tool failed applying the bump; the tree
	// may be partly changed.
	StatusFailed Status = "failed"
	// StatusSkipped is nothing deterministic to do; Reason says why. The
	// agent's job.
	StatusSkipped Status = "skipped"
)

// Document is `forge vuln patch --json`: what the patcher did to each
// finding, the verification, and the exit code it chose. Its JSON Schema is
// `forge project schema vuln-patch`.
type Document struct {
	// Schema is DocumentSchema.
	Schema string `json:"schema"`
	// Env is the env whose findings were patched; "" for a directory scan.
	Env string `json:"env"`
	// Release is the release the findings were read from; "" for a
	// directory scan.
	Release string `json:"release"`
	// Dir is the tree that was patched.
	Dir string `json:"dir"`
	// AllowMajor reports --allow-major.
	AllowMajor bool `json:"allow_major"`
	// Changed reports that the patcher changed the tree.
	Changed bool `json:"changed"`
	// Findings are the requested findings and what happened to each.
	Findings []Result `json:"findings"`
	// Bumps are the version changes applied, one per package per manifest.
	Bumps []Bump `json:"bumps"`
	// Verify is the verification run; absent without --verify.
	Verify *VerifyResult `json:"verify,omitempty"`
	// PR is the pull request --pr opened; absent without --pr.
	PR *PRResult `json:"pr,omitempty"`
	// Summary counts Findings by status.
	Summary Summary `json:"summary"`
	// ExitCode is the exit code the command returned: 0 every requested
	// finding bumped (and verified with --verify); 1 a bump failed or
	// verification failed — the agent takeover signal; 2 nothing
	// deterministic left to do for at least one requested finding (skipped);
	// 4 refused.
	ExitCode int `json:"exit_code"`
	// Refusals are why the patch was refused (exit 4): nothing was changed.
	Refusals []string `json:"refusals,omitempty"`
	// Notes are what the reader should know that changed no status.
	Notes []string `json:"notes,omitempty"`
}

// Result is one finding and what happened to it.
type Result struct {
	// ID is the advisory (CVE-…, GHSA-…, GO-…), and Aliases its other ids.
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
	// Severity is the scanner's severity.
	Severity vulnscan.Severity `json:"severity"`
	// Package is the vulnerable package as the scan named it.
	Package vulnscan.Package `json:"package"`
	// Ecosystem is the patcher that handled it; "" when none applies.
	Ecosystem Ecosystem `json:"ecosystem,omitempty"`
	// Installed is the version the finding names, and Target the version
	// it is bumped to ("" when skipped before a target was chosen). For an
	// OS package, Target is the base image reference its Dockerfile is
	// refreshed to.
	Installed string `json:"installed"`
	Target    string `json:"target,omitempty"`
	// Manifests are the tree's files the package resolved to, relative to
	// Dir (go.mod, package-lock.json, Dockerfile, ...).
	Manifests []string `json:"manifests,omitempty"`
	// Status is fixed, still_present, bumped, failed or skipped.
	Status Status `json:"status"`
	// Reason says why it was skipped, failed or is still present.
	Reason string `json:"reason,omitempty"`
}

// Bump is one package raised in one manifest.
type Bump struct {
	// Ecosystem is the tool that applied it.
	Ecosystem Ecosystem `json:"ecosystem"`
	// Manifest is the go.mod, lockfile or Dockerfile, relative to the
	// patched tree.
	Manifest string `json:"manifest"`
	// Package is the module or package name; "toolchain" for a go.mod's
	// toolchain directive, and the image (alpine, golang) for a
	// Dockerfile's FROM.
	Package string `json:"package"`
	// From and To are the versions before and after; for a Dockerfile, the
	// image references.
	From string `json:"from"`
	To   string `json:"to"`
	// Direct reports a direct dependency; a transitive npm, pnpm or yarn
	// one is raised by an override (overrides, pnpm.overrides, resolutions).
	Direct bool `json:"direct"`
	// Commands are the commands run for its manifest, each as argv (every
	// bump of one manifest shares them).
	Commands [][]string `json:"commands"`
	// Edits are the changes the patcher made to a file itself rather than
	// through a tool: an override in package.json, a Dockerfile's FROM.
	Edits []string `json:"edits,omitempty"`
	// Error is the failure, with the tool's output tail; "" on success.
	Error string `json:"error,omitempty"`
}

// VerifyResult is the verification of a patch.
type VerifyResult struct {
	// Command is the verify command (sh -c), "" when the project has none
	// and only the rescan ran.
	Command string `json:"command"`
	// Source says where Command came from: flag, forge.yaml, taskfile, go,
	// npm, pnpm, yarn, or none.
	Source string `json:"source"`
	// Passed reports that Command exited 0 (true when there is none).
	Passed bool `json:"passed"`
	// Output is the tail of Command's combined output.
	Output string `json:"output,omitempty"`
	// Rescanned are the directories rescanned, relative to the tree.
	Rescanned []string `json:"rescanned"`
	// Rebuilt are the Dockerfiles changed, rebuilt with docker and whose
	// images were rescanned, relative to the tree.
	Rebuilt []string `json:"rebuilt,omitempty"`
	// RescanError is why the rescan could not run, when it could not.
	RescanError string `json:"rescan_error,omitempty"`
}

// PRResult is what --pr did.
type PRResult struct {
	// Branch is the branch the patch is committed on.
	Branch string `json:"branch"`
	// Committed reports the commit; false when there was nothing to commit
	// or verification failed (the changes stay on Branch, uncommitted).
	Committed bool `json:"committed"`
	// URL is the opened pull request.
	URL string `json:"url,omitempty"`
}

// Summary counts results by status.
type Summary struct {
	Fixed        int `json:"fixed"`
	StillPresent int `json:"still_present"`
	Bumped       int `json:"bumped"`
	Failed       int `json:"failed"`
	Skipped      int `json:"skipped"`
}

func summarize(rs []Result) Summary {
	var s Summary
	for _, r := range rs {
		switch r.Status {
		case StatusFixed:
			s.Fixed++
		case StatusStillPresent:
			s.StillPresent++
		case StatusBumped:
			s.Bumped++
		case StatusFailed:
			s.Failed++
		case StatusSkipped:
			s.Skipped++
		}
	}
	return s
}
