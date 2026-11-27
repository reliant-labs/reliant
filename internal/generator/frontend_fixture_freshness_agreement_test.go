package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFreshnessGuardHashAgreesWithGo is the load-bearing test of this whole
// surface.
//
// The staleness guard works by computing the SAME digest in two languages:
// Go (pkg/seedplan.FingerprintInputs, at `forge generate` time) and TypeScript
// (the emitted vitest, at `npm test` time). Two implementations of one
// algorithm is a divergence risk, and the failure mode is asymmetric and
// nasty:
//
//   - If they disagree, the guard fails on a CLEAN tree. Loud, immediate, and
//     someone deletes the guard to make CI green — leaving the staleness hole
//     wide open again.
//
// A unit test of the Go side alone cannot catch this, because the bug lives in
// the AGREEMENT, not in either side. So this runs the emitted TypeScript in a
// real Node process against a real migration directory and requires the two
// digests to match exactly.
//
// It is skipped under -short (it shells out to node) and when node is absent,
// but never silently: an absent node is reported so a green run cannot be
// mistaken for a verified one.
func TestFreshnessGuardHashAgreesWithGo(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a node process; skipped under -short")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — cross-language fingerprint agreement NOT verified in this run")
	}

	// A schema with the shapes most likely to expose an encoding
	// disagreement: multi-byte UTF-8 (byte-length vs. UTF-16 code-unit
	// length framing), embedded quotes, and a vocab overlay.
	root := t.TempDir()
	migDir := filepath.Join(root, "db", "migrations")
	seedsDir := filepath.Join(root, "db", "seeds")
	for _, d := range []string{migDir, seedsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(migDir, "00001_create_orders.up.sql"),
		"CREATE TABLE orders (\n  id UUID PRIMARY KEY,\n  customer_name TEXT NOT NULL,\n"+
			"  note TEXT DEFAULT 'café — naïve \"quoted\"'\n);\n")
	write(filepath.Join(migDir, "00002_add_status.up.sql"),
		"ALTER TABLE orders ADD COLUMN status TEXT CHECK (status IN ('open','closed'));\n")
	// A down migration that must NOT contribute — if the TS side hashed it,
	// the digests would differ and this test would catch it.
	write(filepath.Join(migDir, "00002_add_status.down.sql"),
		"ALTER TABLE orders DROP COLUMN status;\n")
	write(filepath.Join(seedsDir, "vocab.yaml"),
		"columns:\n  orders.customer_name: [Ada Lovelace, Grace Hopper, José]\n")

	// The Go answer, read from the EMITTED MARKER rather than by calling
	// seedplan directly.
	//
	// Reading it from the marker is the whole point. An earlier version of
	// this test compared node's output to a fresh FingerprintInputs call and
	// passed — while the emitter was recording FingerprintWithConfig, a
	// digest with the seed salt folded in that no file-reading process can
	// reproduce. Both sides were individually correct and the SHIPPED
	// artifact was still broken: `npm test` on a freshly generated project
	// reported STALE MOCK FIXTURES against fixtures written seconds earlier.
	// Comparing against the emitted value is what makes this test see the
	// thing users actually get.
	_, guard := emitFreshness(t, root, filepath.Join("frontends", "web"))
	marker := readFixtureMarker(t, root, filepath.Join("frontends", "web"))
	want := marker.SeedFingerprint
	if want == "" {
		t.Fatal("the emitted marker records no fingerprint — nothing would be compared")
	}

	// The TypeScript answer, from the hashing logic the guard template
	// actually ships. Extracted from the emitted file rather than
	// re-typed here: a copy would agree with Go while the SHIPPED code
	// disagreed, which is precisely the bug this test exists to find.
	script := nodeScriptFromGuard(t, guard, migDir)

	// `.mts`, so Node strips the TypeScript annotations itself (Node >= 22).
	// The alternative — stripping them here — would mean testing a rewritten
	// copy of the guard instead of the guard.
	scriptPath := filepath.Join(t.TempDir(), "fp.mts")
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, scriptPath).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	got := strings.TrimSpace(string(out))

	if got != want {
		t.Fatalf("the emitted TypeScript fingerprint disagrees with the digest recorded in "+
			"the emitted marker.\n  marker   = %s\n  node     = %s\n\n"+
			"The two implementations of one digest have diverged. On a real project this "+
			"fails the freshness guard on a CLEAN tree, and the likely response is to "+
			"delete the guard — which silently restores the staleness hole it exists to close.",
			want, got)
	}

	// The config-folded digest must ALSO be recorded, and must differ from
	// the file-only one — if they were equal, the manifest would be
	// recording the same value twice and the salt/row-count dimension would
	// be invisible.
	cfgFP := marker.SeedConfigFingerprint
	if cfgFP == "" {
		t.Error("seed_config_fingerprint is empty for a project with migrations")
	}
	if cfgFP == want {
		t.Error("seed_config_fingerprint equals seed_fingerprint — the seed config is not " +
			"actually folded in, so a salt change would leave stale fixtures undetected")
	}
}

// nodeScriptFromGuard lifts the hashing half of the emitted vitest into a
// standalone ESM script that prints the digest for migDir.
//
// It reuses the SHIPPED source (everything from the first import through the
// `exists` helper) so this test verifies the code that actually runs, then
// appends its own entry point in place of the vitest `describe` block.
func nodeScriptFromGuard(t *testing.T, guard, migDir string) string {
	t.Helper()

	start := strings.Index(guard, `import { createHash }`)
	if start < 0 {
		t.Fatal("the emitted guard has no createHash import — it did not render the " +
			"schema-bearing branch, so there is no hashing code to verify")
	}
	end := strings.Index(guard, `describe(`)
	if end < 0 || end <= start {
		t.Fatal("cannot locate the end of the emitted guard's hashing section")
	}
	body := guard[start:end]

	// Drop the imports the standalone script supplies itself: vitest (not
	// running under vitest here) and the generated manifest (this script
	// computes the digest, it does not compare to a recorded one).
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") &&
			(strings.Contains(trimmed, `"vitest"`) || strings.Contains(trimmed, "fixture-manifest_gen")) {
			continue
		}
		kept = append(kept, line)
	}

	return strings.Join(kept, "\n") + "\n" +
		"const fp = await computeFingerprint(" + tsStringLiteral(migDir) + ");\n" +
		"process.stdout.write(fp ?? \"\");\n"
}

// tsStringLiteral renders a Go string as a JSON-safe TS/JS string literal.
func tsStringLiteral(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// TestFreshnessGuardVerdicts runs the emitted guard's test body in node, with
// a minimal stand-in for vitest, against the four states a checkout can be
// in. The fingerprint is not committed (FixtureMarkerDir), so the guard must
// tell "these fixtures went stale here" from "nothing here can vouch for
// them" — failing the first, skipping the second, never passing either.
func TestFreshnessGuardVerdicts(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns node processes; skipped under -short")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed — the guard's verdicts are NOT verified in this run")
	}
	feRel := filepath.Join("frontends", "web")
	setup := func(t *testing.T) (root, mocks string) {
		t.Helper()
		root = freshnessProject(t, map[string]string{"00001_create_orders.up.sql": "CREATE TABLE orders (id UUID PRIMARY KEY);\n"})
		emitFreshness(t, root, feRel)
		return root, filepath.Join(root, feRel, "src", "mocks")
	}
	run := func(t *testing.T, root, mocks string) string {
		t.Helper()
		guard, err := os.ReadFile(filepath.Join(mocks, fixtureFreshnessFiles[1]))
		if err != nil {
			t.Fatal(err)
		}
		src := string(guard)
		shim := "const __dirname = " + tsStringLiteral(mocks) + ";\n" +
			"const PROJECT_ROOT_FROM_FRONTEND = " + tsStringLiteral(projectRootFromMocksDir(filepath.Join(feRel, "src", "mocks"))) + ";\n" +
			"const FIXTURE_MARKER = " + tsStringLiteral(FixtureMarkerPath(feRel)) + ";\n" +
			"const tests: Array<() => Promise<void>> = [];\n" +
			"function describe(_: string, f: () => void) { f(); }\n" +
			"class Skip extends Error {}\n" +
			"function it(_: string, f: (ctx: { skip: (m?: string) => never }) => Promise<void> | void) {\n" +
			"  tests.push(async () => { await f({ skip: (m?: string) => { throw new Skip(m); } }); });\n" +
			"}\n" +
			"function expect(v: unknown, msg?: string) {\n" +
			"  return { toBe(w: unknown) { if (v !== w) throw new Error(msg ?? `${v} !== ${w}`); } };\n" +
			"}\n"
		var kept []string
		for _, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "import ") && (strings.Contains(trimmed, `"vitest"`) || strings.Contains(trimmed, "fixture-manifest_gen")) {
				continue
			}
			kept = append(kept, line)
		}
		script := strings.Join(kept, "\n") + "\n" + shim +
			"for (const t of tests) {\n" +
			"  try { await t(); process.stdout.write(\"PASS\"); }\n" +
			"  catch (e) { process.stdout.write(e instanceof Skip ? \"SKIP \" + e.message : \"FAIL \" + (e as Error).message); }\n" +
			"}\n"
		// The shim's declarations must precede their use at module scope:
		// hoist it above the guard's describe block.
		script = strings.Replace(script, "describe(\"mock fixture freshness\"", shim+"describe(\"mock fixture freshness\"", 1)
		script = strings.Replace(script, "\n"+shim+"for (const t", "\nfor (const t", 1)
		p := filepath.Join(t.TempDir(), "guard.mts")
		if err := os.WriteFile(p, []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(node, p).CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, out)
		}
		return string(out)
	}

	t.Run("fresh: the marker describes these fixtures and these inputs", func(t *testing.T) {
		root, mocks := setup(t)
		if got := run(t, root, mocks); got != "PASS" {
			t.Errorf("verdict %q, want PASS", got)
		}
	})
	t.Run("stale: a migration landed without a generate", func(t *testing.T) {
		root, mocks := setup(t)
		if err := os.WriteFile(filepath.Join(root, "db", "migrations", "00002_add_status.up.sql"), []byte("ALTER TABLE orders ADD COLUMN status TEXT;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := run(t, root, mocks); !strings.HasPrefix(got, "FAIL STALE MOCK FIXTURES") {
			t.Errorf("verdict %q, want the STALE failure", got)
		}
	})
	t.Run("fresh clone: no marker", func(t *testing.T) {
		root, mocks := setup(t)
		if err := os.RemoveAll(filepath.Join(root, FixtureMarkerDir)); err != nil {
			t.Fatal(err)
		}
		if got := run(t, root, mocks); !strings.HasPrefix(got, "SKIP") || !strings.Contains(got, "forge generate") {
			t.Errorf("verdict %q, want a SKIP naming forge generate", got)
		}
	})
	t.Run("pulled: the fixtures are not the ones the marker describes", func(t *testing.T) {
		root, mocks := setup(t)
		f := filepath.Join(mocks, fixtureFreshnessFiles[0])
		body, _ := os.ReadFile(f)
		if err := os.WriteFile(f, append(body, []byte("// pulled\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "db", "migrations", "00002_add_status.up.sql"), []byte("ALTER TABLE orders ADD COLUMN status TEXT;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := run(t, root, mocks); !strings.HasPrefix(got, "SKIP") {
			t.Errorf("verdict %q, want SKIP (nothing here can vouch for pulled fixtures)", got)
		}
	})
}
