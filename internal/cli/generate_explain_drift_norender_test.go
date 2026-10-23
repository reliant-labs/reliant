package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// `--explain-drift` is the remedy the drift error itself names, so the one
// thing it must never do is promise a diff and then produce nothing. When no
// fresh render exists the note has to say WHICH of the two causes applies,
// because they call for opposite actions: re-run without --steps, versus
// accept that forge no longer emits the file at all.
func TestExplainDriftNoRenderNote(t *testing.T) {
	prev := checksums.Tier1Targets()
	t.Cleanup(func() { checksums.SetTier1TargetSet(prev) })

	const gatedOff = "frontends/web/src/lib/apiurl_gen.ts"
	const noLongerEmitted = "frontends/web/src/app/dashboard_gen.tsx"
	checksums.SetTier1TargetSet(map[string]bool{gatedOff: true})

	t.Run("emitter gated off this run", func(t *testing.T) {
		got := explainDriftNoRenderNote(gatedOff)
		for _, want := range []string{"gated off", "--steps"} {
			if !strings.Contains(got, want) {
				t.Errorf("note for a gated-off emitter missing %q; got:\n%s", want, got)
			}
		}
		// This case IS recoverable, so it must not claim otherwise.
		if strings.Contains(got, "no longer emits") {
			t.Errorf("gated-off note wrongly claims forge stopped emitting the file:\n%s", got)
		}
	})

	t.Run("forge no longer emits the file", func(t *testing.T) {
		got := explainDriftNoRenderNote(noLongerEmitted)
		for _, want := range []string{"no longer emits", "nothing you can pass", "forge project disown"} {
			if !strings.Contains(got, want) {
				t.Errorf("note for a retired Tier-1 path missing %q; got:\n%s", want, got)
			}
		}
		// The old message sent users hunting for a --steps flag they had
		// never passed. It must not do that when no render is possible.
		if strings.Contains(got, "--steps") {
			t.Errorf("retired-path note still points at --steps, which cannot help:\n%s", got)
		}
	})
}
