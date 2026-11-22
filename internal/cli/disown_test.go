// Tests for `forge project disown <path>... --reason <text>` — see disown.go.
//
// The tests build a synthetic project root (forge.yaml + on-disk files
// carrying embedded forge:hash markers + saved .forge ownership state),
// chdir into it, and drive runDisown directly. We avoid spawning the
// cobra binary so the assertions can read / re-decode the ownership
// state post-write without any process boundary.
//
// Ownership is read from the files themselves now: a disownable path is
// one carrying forge's certification (an embedded marker or a scoped
// .forge/hashes.json entry) — the manifest-era Tier/Disowned entry
// shapes are gone.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// withDisownProjectRoot builds a synthetic project root (forge.yaml +
// saved .forge ownership state), chdirs into it for the duration of the
// test, and returns the root. Successor of the unfork-era helper that
// seeded a .forge/checksums.json manifest.
func withDisownProjectRoot(t *testing.T, cs *checksums.FileChecksums) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cs != nil {
		if err := checksums.Save(root, cs); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	return root
}

// seedDisownFile writes content at root/rel (creating parents).
func seedDisownFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedStampedDisownFile writes content at root/rel with the embedded
// forge:hash marker stamped in — a forge-certified (Tier-1) file, the
// only kind `forge project disown` accepts.
func seedStampedDisownFile(t *testing.T, root, rel, content string) {
	t.Helper()
	stamped, ok := checksums.Stamp(rel, []byte(content))
	if !ok {
		t.Fatalf("stamp %s: format is unstampable", rel)
	}
	seedDisownFile(t, root, rel, string(stamped))
}

// TestDisown_RequiresReason: --reason is the design-feedback payload;
// the command refuses to run without it.
func TestDisown_RequiresReason(t *testing.T) {
	root := withDisownProjectRoot(t, &checksums.FileChecksums{})
	seedStampedDisownFile(t, root, "pkg/app/wire_gen.go", "package app\n")

	for _, reason := range []string{"", "   "} {
		err := runDisown([]string{"pkg/app/wire_gen.go"}, reason, false)
		if err == nil {
			t.Fatalf("reason=%q: expected refusal without --reason; got nil", reason)
		}
		if !strings.Contains(err.Error(), "--reason is required") {
			t.Errorf("error should name the --reason requirement; got %q", err.Error())
		}
	}
}

// TestDisown_FlipsEntryAndRecordsFriction is the happy path: the path
// lands in .forge/disowned.json with reason + timestamp, the embedded
// forge:hash marker is stripped from the file (a user-owned file must
// not advertise forge certification), and one friction entry
// (area=disown) lands per path with the reason text.
func TestDisown_FlipsEntryAndRecordsFriction(t *testing.T) {
	const rel = "pkg/app/wire_gen.go"
	userContent := "package app // user edit\n"
	root := withDisownProjectRoot(t, &checksums.FileChecksums{})
	seedStampedDisownFile(t, root, rel, userContent)

	if err := runDisown([]string{rel}, "custom pool wiring forge can't express", false); err != nil {
		t.Fatalf("runDisown: %v", err)
	}

	got, err := checksums.Load(root)
	if err != nil {
		t.Fatalf("re-load ownership state: %v", err)
	}
	entry, ok := got.Disowned[rel]
	if !ok {
		t.Fatalf("path missing from .forge/disowned.json; have: %v", got.Disowned)
	}
	if entry.DisownedAt == "" {
		t.Errorf("entry = %+v, want disowned_at set", entry)
	}
	if entry.Reason != "custom pool wiring forge can't express" {
		t.Errorf("entry reason = %q, want the --reason text", entry.Reason)
	}

	// The user's content survives sans marker — the bytes are theirs now.
	onDisk, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if _, hasMarker := checksums.ExtractMarker(onDisk); hasMarker {
		t.Errorf("forge:hash marker survived the disown; got:\n%s", onDisk)
	}
	if string(onDisk) != userContent {
		t.Errorf("disowned content = %q, want the user's content with only the marker removed", onDisk)
	}

	// Reason lands in the manifest: .forge/disowned.json carries the WHY.
	csAfter, err := checksums.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := csAfter.Disowned[rel].Reason; got != "custom pool wiring forge can't express" {
		t.Errorf("manifest reason = %q, want the --reason text", got)
	}
}

// TestDisown_RejectsUncertifiedAndMissing pins the validation set:
// paths carrying no forge certification (untracked user files and
// scaffold-once Tier-2 files alike — both are unmarked, the manifest-era
// distinction collapsed) and paths missing from disk are refused with
// distinct messages, and a typo on one path means nothing half-applies.
func TestDisown_RejectsUncertifiedAndMissing(t *testing.T) {
	root := withDisownProjectRoot(t, &checksums.FileChecksums{})
	// Ordinary starter / user file: exists, but carries no marker.
	seedDisownFile(t, root, "internal/svc/service.go", "package svc\n")
	// A certified sibling, to prove the refusal is about the named path.
	seedStampedDisownFile(t, root, "handlers/api/handlers.go", "package api\n")

	cases := []struct {
		path    string
		wantErr string
	}{
		{"not/tracked.go", "missing on disk"},
		{"internal/svc/service.go", "no forge certification"},
		{"pkg/app/wire_gen.go", "missing on disk"},
	}
	for _, tc := range cases {
		err := runDisown([]string{tc.path}, "why", false)
		if err == nil {
			t.Errorf("%s: expected refusal; got nil", tc.path)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error %q missing %q", tc.path, err.Error(), tc.wantErr)
		}
	}
}

// TestDisown_DryRunPreservesState: targets are listed but nothing is
// written — no disowned.json, the marker stays in the file, and the
// friction log is untouched.
func TestDisown_DryRunPreservesState(t *testing.T) {
	const rel = "pkg/app/wire_gen.go"
	root := withDisownProjectRoot(t, &checksums.FileChecksums{})
	seedStampedDisownFile(t, root, rel, "package app\n")

	if err := runDisown([]string{rel}, "why", true); err != nil {
		t.Fatalf("runDisown --dry-run: %v", err)
	}
	got, _ := checksums.Load(root)
	if got.IsDisowned(rel) {
		t.Errorf("--dry-run recorded a disown on disk")
	}
	onDisk, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	if _, hasMarker := checksums.ExtractMarker(onDisk); !hasMarker {
		t.Errorf("--dry-run stripped the forge:hash marker")
	}
	if csAfter, err := checksums.Load(root); err != nil || len(csAfter.Disowned) != 0 {
		t.Errorf("--dry-run wrote disowned entries: %v (err=%v)", csAfter.Disowned, err)
	}
}

// TestDisown_IdempotentOnAlreadyDisowned: re-disowning is a friendly
// no-op, not an error — safe for scripts.
func TestDisown_IdempotentOnAlreadyDisowned(t *testing.T) {
	const rel = "pkg/app/wire_gen.go"
	cs := &checksums.FileChecksums{
		Disowned: map[string]checksums.DisownedEntry{
			rel: {Reason: "original reason", DisownedAt: "2026-06-01T00:00:00Z"},
		},
	}
	root := withDisownProjectRoot(t, cs)
	seedDisownFile(t, root, rel, "package app\n")

	if err := runDisown([]string{rel}, "why again", false); err != nil {
		t.Fatalf("runDisown on already-disowned entry should not error; got %v", err)
	}
	got, _ := checksums.Load(root)
	if got.Disowned[rel].DisownedAt != "2026-06-01T00:00:00Z" {
		t.Errorf("no-op disown must not re-stamp DisownedAt; got %+v", got.Disowned[rel])
	}
}

// TestDisown_PreMigrationFileGetsMigrationHint: a file with no embedded
// marker on a project that still carries the legacy manifest is refused
// with the "run forge generate to migrate" hint — the legacy fork/
// disown entry conversion itself now lives in
// checksums.MigrateLegacyManifest (covered at the pipeline level by the
// stepMigrateLegacyManifest tests).
func TestDisown_PreMigrationFileGetsMigrationHint(t *testing.T) {
	const rel = "pkg/app/bootstrap.go"
	root := withDisownProjectRoot(t, &checksums.FileChecksums{})
	seedDisownFile(t, root, rel, "package app // legacy fork\n")
	// Legacy manifest present: pre-migration project shape.
	if err := os.MkdirAll(filepath.Join(root, ".forge"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"forge_version":"old","files":{"` + rel + `":{"hash":"abc","tier":1,"forked":true}}}`
	if err := os.WriteFile(filepath.Join(root, checksums.LegacyChecksumFile), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runDisown([]string{rel}, "settling a legacy fork", false)
	if err == nil {
		t.Fatal("expected refusal for an uncertified pre-migration file; got nil")
	}
	if !strings.Contains(err.Error(), "no forge certification") {
		t.Errorf("error should explain the missing certification; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "migrate") {
		t.Errorf("error should point pre-migration projects at `forge generate`; got %q", err.Error())
	}
}

// TestDisown_CleansSideRenders: stale parked renders (legacy fork-era
// or --explain-drift leftovers) are removed at disown time.
func TestDisown_CleansSideRenders(t *testing.T) {
	const rel = "pkg/app/wire_gen.go"
	root := withDisownProjectRoot(t, &checksums.FileChecksums{})
	seedStampedDisownFile(t, root, rel, "package app\n")
	if err := checksums.WriteSideRenderNoBase(root, rel, []byte("stale render\n")); err != nil {
		t.Fatal(err)
	}

	if err := runDisown([]string{rel}, "why", false); err != nil {
		t.Fatalf("runDisown: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, checksums.RenderDir, rel)); !os.IsNotExist(err) {
		t.Errorf("side render not cleaned on disown")
	}
}

// TestDisownCmd_FlagsAndHelp pins the cobra surface: the command
// exists at top level, requires <path> args, exposes --reason and
// --dry-run, and the help text teaches the one-way contract and the
// delete + generate re-adoption path.
func TestDisownCmd_FlagsAndHelp(t *testing.T) {
	cmd := newDisownCmd()
	if cmd == nil {
		t.Fatal("newDisownCmd returned nil")
	}
	for _, flag := range []string{"reason", "dry-run"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("forge project disown is missing --%s flag", flag)
		}
	}
	long := strings.ToLower(cmd.Long)
	for _, kw := range []string{"one-way", "forge generate", "disowned.json", "extension point"} {
		if !strings.Contains(long, kw) {
			t.Errorf("forge project disown --help long text should mention %q", kw)
		}
	}
}

// The VENDORED protos are a third ownership shape the marker rule did not
// anticipate: forge COPIES them in (proto/forge/v1/forge.proto,
// proto/buf/validate/validate.proto), so they are forge-owned — but they
// carry no forge:hash marker, because a `//` header in the annotation
// definitions would itself be vendored into every project.
//
// That made them undisownable, which broke a runbook: the
// vendored-proto-drift lint tells a project that deliberately customized
// its copy to run `forge project disown <path>`, and the command refused
// with "carries no forge certification". A lint whose escape hatch does
// not exist is a lint people learn to ignore.
func TestRunDisown_AcceptsVendoredProtoWithoutMarker(t *testing.T) {
	root := withDisownProjectRoot(t, nil)
	const rel = "proto/forge/v1/forge.proto"
	seedDisownFile(t, root, rel, "syntax = \"proto3\";\n// locally customized\n")

	if err := runDisown([]string{rel}, "deliberately customized vendored annotations", false); err != nil {
		t.Fatalf("runDisown: %v", err)
	}

	cs, err := checksums.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.IsDisowned(rel) {
		t.Fatalf("expected %s to be disowned; disowned set = %v", rel, cs.Disowned)
	}
}

// A hand-written hooks barrel is the other unmarked path forge names: generate
// leaves it alone and says `forge project disown <path>` keeps it and silences
// the notice. The command has to take that advice, and once it has, the
// barrel writer must skip the file without a notice.
func TestRunDisown_AcceptsHandWrittenHooksBarrel(t *testing.T) {
	root := withDisownProjectRoot(t, nil)
	const rel = "frontends/web/src/hooks/index.ts"
	const mine = "export { useFullscreen } from './useFullscreen';\n"
	seedDisownFile(t, root, rel, mine)

	if err := runDisown([]string{rel}, "the app's own hooks barrel", false); err != nil {
		t.Fatalf("runDisown: %v", err)
	}
	cs, err := checksums.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.IsDisowned(rel) {
		t.Fatalf("expected %s to be disowned; disowned set = %v", rel, cs.Disowned)
	}

	checksums.ResetPerRunState()
	files := []hookFileEntry{{fileName: "user-service-hooks_gen.ts", nsAlias: "userService", symbols: []string{"useGetUser"}}}
	if err := writeHooksBarrel(root, rel, files, cs); err != nil {
		t.Fatalf("writeHooksBarrel: %v", err)
	}
	if got := readAdoptionFile(t, root, rel); got != mine {
		t.Fatalf("disowned barrel was rewritten:\n%s", got)
	}
	if why, ok := leftAloneFor(rel); ok {
		t.Fatalf("a disowned barrel is still named in the summary: %q", why)
	}

	// The disown is load-bearing: without it the next run prints the
	// left-alone notice that told the user to disown. Retirement must keep it.
	if retired := checksums.RetireObsoleteDisowns(cs, nil); len(retired) != 0 {
		t.Fatalf("a disowned hooks barrel was retired as obsolete: %v", retired)
	}
	if !cs.IsDisowned(rel) {
		t.Fatalf("expected %s to stay disowned; disowned set = %v", rel, cs.Disowned)
	}
}

// The barrel allowance is the barrel paths only: an index.ts elsewhere is an
// ordinary unmarked file.
func TestIsHooksBarrelRelPath(t *testing.T) {
	for p, want := range map[string]bool{
		"frontends/web/src/hooks/index.ts":      true,
		"src/hooks/index.ts":                    true,
		"packages/hooks/src/generated/index.ts": true,
		"frontends/web/src/lib/index.ts":        false,
		"frontends/web/src/hooks/useThing.ts":   false,
		"frontends/web/mysrc/hooks/index.ts":    false,
	} {
		if got := isHooksBarrelRelPath(p); got != want {
			t.Errorf("isHooksBarrelRelPath(%q) = %v, want %v", p, got, want)
		}
	}
}

// The marker rule still holds for everything else: an ordinary unmarked
// file is a scaffold-once "yours" file and there is nothing to disown.
// The vendored allowance must be a narrow, named exception, not a hole.
func TestRunDisown_StillRefusesOrdinaryUnmarkedFile(t *testing.T) {
	root := withDisownProjectRoot(t, nil)
	const rel = "internal/mine/thing.go"
	seedDisownFile(t, root, rel, "package mine\n")

	err := runDisown([]string{rel}, "because", false)
	if err == nil {
		t.Fatal("expected a refusal for an unmarked, non-vendored file")
	}
	if !strings.Contains(err.Error(), "no forge certification") {
		t.Errorf("unexpected error: %v", err)
	}
}

// gen/forge_service_catalog.json is certified by nothing (it has no
// hashes.json entry), and is still forge's Tier-1 output: it can be
// disowned, and a disowned catalog is never overwritten by generate's writer.
func TestDisown_UncertifiedServiceCatalog(t *testing.T) {
	const rel = "gen/forge_service_catalog.json"
	userContent := "{\"schema\": \"mine\"}\n"
	root := withDisownProjectRoot(t, &checksums.FileChecksums{})
	seedDisownFile(t, root, rel, userContent)

	if err := runDisown([]string{rel}, "the catalog forge renders does not fit our explorer", false); err != nil {
		t.Fatalf("disown the uncertified catalog: %v", err)
	}
	cs, err := checksums.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cs.IsDisowned(rel) {
		t.Fatal("the catalog was not recorded as disowned")
	}
	if wrote, err := checksums.WriteGeneratedFile(root, rel, []byte("{\"schema\": \"forge\"}\n"), cs, true); err != nil || wrote {
		t.Fatalf("generate's writer touched a disowned catalog: wrote=%v err=%v", wrote, err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, rel)); string(got) != userContent {
		t.Fatalf("disowned catalog overwritten:\n%s", got)
	}
	// A user's own JSON under gen/ is still not disownable: no certificate
	// and not one of forge's uncertified outputs.
	seedDisownFile(t, root, "gen/notes.json", "{}\n")
	if err := runDisown([]string{"gen/notes.json"}, "x", false); err == nil {
		t.Fatal("an arbitrary gen/ file was disownable")
	}
}
