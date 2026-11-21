// Package releasecompose adapts the shared public release composition engine.
// The pure authority lives in pkg/release so server and CLI use one algorithm.
package releasecompose

import (
	"encoding/json"

	"github.com/reliant-labs/forge/pkg/release"
)

// Input is the shared engine's complete composition request.
type Input = release.CompositionInput

// ClosureError identifies additional consumers that must be explicitly selected.
type ClosureError = release.ClosureError

// Compose delegates to the single public composition authority.
func Compose(in Input) (release.Snapshot, error) { return release.ComposeSnapshot(in) }

func clone(s release.Snapshot) release.Snapshot {
	b, _ := json.Marshal(s)
	var out release.Snapshot
	_ = json.Unmarshal(b, &out)
	return out
}
