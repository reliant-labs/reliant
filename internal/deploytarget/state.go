package deploytarget

import (
	"time"

	"github.com/reliant-labs/forge/internal/statefile"
)

// DeployState records the last image+tag a non-cluster provider
// successfully shipped for one service-env pair (compose, and the
// static-site provider, which reads it back as what it last published). It
// exists for targets with nothing else to ask what is deployed. forge.OnExternal does NOT
// use it: its record is the release ledger, and its read-back is the
// binding's `status` hook (external.go).
//
// The shape mirrors internal/cli.BuildState (image / tag /
// deployed_at) but it's a deliberate copy rather than a shared type:
// internal/cli depends on internal/deploytarget, so reusing the cli
// type would require either an import cycle or hoisting the type into
// a third package for one struct. The cost of a 4-field duplicate is
// lower than the cost of either alternative.
//
// File layout: one file per (provider, env, service) under
// .forge/state/. The directory is already in .gitignore via the
// existing `.forge/` rule; the file is 0o644 (same as build-state) so
// users can peek at it without sudo.
type DeployState struct {
	Image      string `json:"image"`
	Tag        string `json:"tag"`
	DeployedAt string `json:"deployed_at"` // RFC3339, wall-clock
}

// deployStatePath returns the absolute path for a per-(provider, env,
// service) state file. The env and service segments are sanitized to
// flat filenames — they come from KCL-validated identifiers in
// practice, but we strip path separators defensively.
func deployStatePath(projectDir, provider, env, service string) string {
	if env == "" {
		env = "default"
	}
	name := provider + "-" + statefile.SafeSegment(env) + "-" + statefile.SafeSegment(service) + ".json"
	return statefile.Path(projectDir, name)
}

// WriteDeployState persists a successful provider deploy. Returns the
// path written so callers can include it in log lines / errors. The
// state directory is created lazily so projects that never use
// non-cluster targets never grow the tree.
func WriteDeployState(projectDir, provider, env, service string, st DeployState) (string, error) {
	path := deployStatePath(projectDir, provider, env, service)
	if st.DeployedAt == "" {
		st.DeployedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := statefile.Write(path, "deploy state", st); err != nil {
		return path, err
	}
	return path, nil
}

// ReadDeployState loads the per-(provider, env, service) state file.
// Returns (nil, nil) when the file is missing — that's "no previous
// deploy", which the caller handles separately from "file exists but
// is malformed" (returns (nil, err)).
func ReadDeployState(projectDir, provider, env, service string) (*DeployState, error) {
	return statefile.Read[DeployState](deployStatePath(projectDir, provider, env, service), "deploy state")
}
