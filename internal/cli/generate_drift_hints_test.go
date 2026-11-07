package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestTier1ExtensionPointHint(t *testing.T) {
	tests := []struct {
		path     string
		wantSubs []string
	}{
		{"pkg/app/bootstrap.go", []string{"internal/app/providers.go", "internal/app/compose.go", "OpenInfra"}},
		{"pkg/app/app_gen.go", []string{"internal/app/providers.go", "internal/app/compose.go"}},
		{"pkg/app/wire_gen.go", []string{"internal/app/providers.go", "internal/app/compose.go"}},
		{"internal/handlers/orders/handlers_gen.go", []string{"contract.go", "proto"}},
		{"internal/handlers/orders/mock_gen.go", []string{"contract.go", "proto"}},

		// pkg/app/testing.go is RETIRED: as a non-test .go file importing
		// "testing" in a package cmd/<proj> imports, it linked package
		// `testing` into every production binary. The hint must say so and
		// name the replacement, so a project still carrying the file knows
		// where its harness went rather than re-forking it.
		{"pkg/app/testing.go", []string{"RETIRED", "helpers_gen_test.go"}},
		{"pkg/app/factory_gen.go", []string{"RETIRED", "factories_gen_test.go"}},

		// The HTTP edge: forge owns serve.go end-to-end and exposes no
		// seam. The hint must SAY that and name each missing seam, so
		// the user files an issue instead of forking.
		{"cmd/control-plane/cmd/serve.go", []string{
			"NO EXTENSION POINT EXISTS", "interceptor chain", "CORS", "ExtraRoutes", "forge gap",
		}},
		{"cmd/api/cmd/server.go", []string{"NO EXTENSION POINT EXISTS", "Extras"}},
		{"cmd/api/cmd/version.go", []string{"commands.go", "userCommands"}},

		// mounts_services.go has a per-service seam AND a project-level
		// gap; the hint carries both halves.
		{"internal/app/mounts_services_gen.go", []string{"RegisterHTTP", "NO EXTENSION POINT EXISTS", "ExtraRoutes"}},

		{"pkg/config/config_gen.go", []string{"forge.yaml"}},
		{"internal/db/order_orm_gen.go", []string{"migration", "proto"}},
		{"frontends/admin-web/src/app/dashboard_gen.tsx", []string{"regenerate from proto"}},

		// Leading ./ normalized.
		{"./pkg/app/bootstrap.go", []string{"internal/app/providers.go"}},

		// Unknown Tier-1 path: still never silent.
		{"some/unmapped/thing_gen.go", []string{"NO EXTENSION POINT EXISTS"}},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := tier1ExtensionPointHint(tt.path)
			for _, want := range tt.wantSubs {
				if !strings.Contains(got, want) {
					t.Errorf("hint for %s missing %q; got %q", tt.path, want, got)
				}
			}
		})
	}
}

// TestTier1ExtensionPointHintIsTotal pins the property that replaces
// `forge project disown`: the hint is never empty. A blank hint reads as
// "forge has no opinion", which leaves the one-way door as the only
// remedy on screen — the exact mechanism that produced a fleet in which
// zero disowns were deliberate.
func TestTier1ExtensionPointHintIsTotal(t *testing.T) {
	paths := []string{
		"pkg/app/wire_gen.go", "pkg/app/testing.go", "pkg/config/config_gen.go",
		"cmd/x/cmd/serve.go", "cmd/x/cmd/server.go", "cmd/x/cmd/svc_register.go",
		"internal/app/mounts_services_gen.go", "internal/app/auth.go",
		"internal/handlers/orders/handlers_gen.go", "internal/db/thing_orm_gen.go",
		"frontends/web/src/lib/apiurl_gen.ts", "deploy/kcl/main.k",
		"", ".", "weird", "a/b/c/d/e_gen.go",
	}
	for _, p := range paths {
		if got := tier1ExtensionPointHint(p); strings.TrimSpace(got) == "" {
			t.Errorf("tier1ExtensionPointHint(%q) is empty — the hint must be total", p)
		}
	}
}

// TestFormatTier1DriftReport pins the message-design contract: the
// extension point leads, `forge project disown` trails as the LAST resort and
// is described as a one-way permanent transfer, friction recording and
// --explain-drift are advertised.
func TestFormatTier1DriftReport(t *testing.T) {
	drift := []checksums.Tier1DriftEntry{
		{Path: "pkg/app/wire_gen.go", RecordedHash: "aaaa1111", OnDiskHash: "bbbb2222"},
		{Path: "pkg/config/config_gen.go", RecordedHash: "cccc3333", OnDiskHash: "dddd4444"},
	}
	got := formatTier1DriftReport(drift)

	for _, want := range []string{
		"2 Tier-1 file(s) modified",
		"pkg/app/wire_gen.go",
		// The hash lines speak the self-certification vocabulary: the
		// EMBEDDED marker hash vs the recomputed CURRENT body hash.
		"embedded: aaaa1111",
		"current:  bbbb2222",
		"↪ custom wiring belongs in internal/app/providers.go (OpenInfra) + internal/app/compose.go (NewComponents) — the retired pkg/app DI unit no longer runs",
		"↪ this change belongs in forge.yaml",
		"--explain-drift",
		"forge project disown <path> --reason",
		// Disown is described as a recorded gap, never as an end state.
		"not a solution",
		"not an end state",
		"ZERO that were deliberate",
		"Expect to delete the entry",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q; got:\n%s", want, got)
		}
	}

	// EVERY drifted file carries a hint. The hint is total, so a silent
	// row — the state that left the one-way door as the only remedy on
	// screen — can never come back.
	if n := strings.Count(got, "↪"); n != len(drift) {
		t.Errorf("expected one hint line per drifted file (%d), got %d:\n%s", len(drift), n, got)
	}

	// Option ordering: the extension-point option must come before the
	// disown one-way door.
	if strings.Index(got, "extension point") > strings.Index(got, "forge project disown") {
		t.Errorf("extension-point guidance must precede the disown option:\n%s", got)
	}
	// And reporting the gap must precede it too — a missing seam is a
	// forge defect, and the issue tracker outranks the escape hatch.
	if strings.Index(got, "report the named gap") > strings.Index(got, "forge project disown") {
		t.Errorf("gap-reporting guidance must precede the disown option:\n%s", got)
	}
}

// TestFormatTier1DriftReport_UnverifiedSentinel pins the wording for
// the legacy-migration sentinel: a file whose provenance could not be
// established when the project migrated off .forge/checksums.json is
// reported with the unverified-legacy marker value and an explanation,
// not the ordinary "hash stamped at the last forge write" line.
func TestFormatTier1DriftReport_UnverifiedSentinel(t *testing.T) {
	drift := []checksums.Tier1DriftEntry{
		{Path: "pkg/app/wire_gen.go", OnDiskHash: "bbbb2222", Unverified: true},
	}
	got := formatTier1DriftReport(drift)
	for _, want := range []string{
		"embedded: " + checksums.UnverifiedMarkerValue,
		"provenance unknown since the legacy checksums.json migration",
		"current:  bbbb2222",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unverified report missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "hash stamped at the last forge write") {
		t.Errorf("unverified entry must not claim a recorded write hash:\n%s", got)
	}
}
