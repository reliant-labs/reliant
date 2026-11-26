// Copyright (c) 2025 Reliant Labs
package templates

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadedSkillsPointAtLibraryLookup — the command that answers "what is this
// forge/pkg symbol's signature" must be named in the skills a unit ACTUALLY
// LOADS, not only in the one about libraries.
//
// Measured, run 3 of the roofloop dogfood: units ran `forge project libraries`
// ZERO times and grepped forge's own source tree 92 times, burning 35.5 minutes.
// The command existed and worked. It was named only in `forge/SKILL.md` and
// `forge-libraries/SKILL.md` — and backend units load `service-layer`, `api`,
// `auth` and `testing`. `api/SKILL.md` names `svcerr` sixteen times and never
// said how to look its API up, so the grep started there.
//
// A capability is discoverable at the point of NEED or it is not discoverable.
// This pins the pointer to the skills where the need arises.
func TestLoadedSkillsPointAtLibraryLookup(t *testing.T) {
	t.Parallel()

	for _, skill := range []string{"api", "service-layer"} {
		path := filepath.Join("skills", "forge", skill, "SKILL.md")
		body, err := ProjectTemplates().Get(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(body)

		if !strings.Contains(text, "forge project libraries") {
			t.Errorf("%s never names `forge project libraries` — it is a skill units load "+
				"while reaching for forge/pkg symbols, and without the pointer they grep "+
				"forge's source (measured: 92 greps, 35.5 min)", path)
		}

		// Naming the command is not enough if bare `go doc <pkg>` still looks
		// like an equivalent route: it renders a struct as `struct{ ... }` with
		// NO methods, which is exactly why `pkg/crud/repo.go` was grepped 14
		// times for one signature.
		if !strings.Contains(text, "no methods") {
			t.Errorf("%s does not warn that bare `go doc <pkg>` omits methods — agents "+
				"followed that instruction, got a page without the answer, and grepped", path)
		}
	}
}
