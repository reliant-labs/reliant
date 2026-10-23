package cli

// `forge env diff` — the §8.2 categories and the §8.3 guards.
//
// The render itself is NOT exercised here. internal/bundle tests the
// projection from a literal fixture, and evaluating real KCL per case would
// make this file a test of KCL rather than of the diff. What this file owns
// is everything around the render: which Live side is chosen, how each guard
// reports, and that a failure never reads as "no changes".
//
// So the tests drive the renderEnvShapeForDiff seam, supplying a shape and a
// write list directly. That is the seam's whole purpose, and it is the same
// pattern env_shape.go uses for projectEnvShapeFn.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// stubRender points renderEnvShapeForDiff at a fixed answer per env.
func stubRender(t *testing.T, answers map[string]stubbedRender) {
	t.Helper()
	prev := renderEnvShapeForDiff
	renderEnvShapeForDiff = func(_ context.Context, _, env string, _ io.Writer) (envShapeDoc, []string, error) {
		answer, ok := answers[env]
		if !ok {
			return envShapeDoc{}, nil, fmt.Errorf("no stub for env %q", env)
		}
		return envShapeDoc{Env: env, Kind: string(answer.shape.Kind), Shape: answer.shape}, answer.wrote, answer.err
	}
	t.Cleanup(func() { renderEnvShapeForDiff = prev })
}

// stubbedRender is one env's rendered answer.
type stubbedRender struct {
	shape release.Shape
	wrote []string
	err   error
}

// diffShape is a small candidate shape.
func diffShape(objects ...release.ShapeObject) release.Shape {
	return release.Shape{Kind: release.EnvSelfManaged, Objects: objects}
}

// obj is one rendered object with a hash.
func obj(kind, name, hash string) release.ShapeObject {
	return release.ShapeObject{
		Cluster: "c1", APIVersion: "apps/v1", Kind: kind, Namespace: "ns", Name: name,
		Workload: name, Hash: "sha256:" + strings.Repeat(hash, 64), ConfigHash: "sha256:" + strings.Repeat(hash, 64),
	}
}

// runDiffEntry renders and diffs one env against a supplied Live side.
func runDiffEntry(t *testing.T, env string, live liveSide, answers map[string]stubbedRender) envDiffEntry {
	t.Helper()
	stubRender(t, answers)
	// The tree-hash bracket is skipped for a non-git temp dir (an empty
	// hash means "I could not measure", which must not read as "it
	// moved") — which is what lets these tests exercise the other guards
	// in isolation.
	return diffOneEnv(context.Background(), t.TempDir(), env, live, envDiffOptions{}, io.Discard)
}

// ─── §8.2 categories ────────────────────────────────────────────────────────

// Objects added, removed and changed, from a set diff over per-object
// hashes.
func TestEnvDiff_ReportsAddedRemovedAndChangedObjects(t *testing.T) {
	liveShape := diffShape(obj("Deployment", "api", "a"), obj("Service", "gone", "b"))
	candidate := diffShape(obj("Deployment", "api", "c"), obj("Deployment", "new", "d"))

	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle", Shape: &liveShape},
		map[string]stubbedRender{"prod": {shape: candidate}})

	if entry.Status != diffOK {
		t.Fatalf("status = %q (%s), want ok", entry.Status, entry.Detail)
	}
	d := entry.Diff
	if len(d.Added) != 1 || d.Added[0].Name != "new" {
		t.Errorf("added = %+v, want just new", d.Added)
	}
	if len(d.Removed) != 1 || d.Removed[0].Name != "gone" {
		t.Errorf("removed = %+v, want just gone", d.Removed)
	}
	if len(d.Changed) != 1 || d.Changed[0].Candidate.Name != "api" {
		t.Errorf("changed = %+v, want just api", d.Changed)
	}
}

// Secrets newly declared, joined with presence. A name ABSENT from presence
// is "not verifiable" rather than "missing" — the forge#398 rule, and the
// difference decides whether the user has something to do.
func TestEnvDiff_SecretPresenceDistinguishesMissingFromUnverifiable(t *testing.T) {
	liveShape := release.Shape{Kind: release.EnvSelfManaged}
	candidate := release.Shape{
		Kind: release.EnvSelfManaged,
		Secrets: []release.ShapeSecret{
			{Name: "SET_ONE", Provider: "hosted", DeclaredBy: []string{"api"}},
			{Name: "MISSING_ONE", Provider: "hosted", DeclaredBy: []string{"api"}},
			{Name: "EXTERNAL_ONE", Provider: "external_secrets", DeclaredBy: []string{"api"}},
		},
	}

	entry := runDiffEntry(t, "prod", liveSide{
		Known:  true,
		Source: "bundle",
		Shape:  &liveShape,
		// EXTERNAL_ONE is deliberately ABSENT: an external provider
		// cannot be queried from here.
		Secrets: map[string]bool{"SET_ONE": true, "MISSING_ONE": false},
	}, map[string]stubbedRender{"prod": {shape: candidate}})

	if entry.Status != diffOK {
		t.Fatalf("status = %q (%s), want ok", entry.Status, entry.Detail)
	}
	if len(entry.Diff.SecretsAdded) != 3 {
		t.Fatalf("secrets_added = %+v, want all three", entry.Diff.SecretsAdded)
	}
	if set, known := entry.SecretPresence["SET_ONE"]; !known || !set {
		t.Errorf("SET_ONE presence = %v/%v, want known and set", set, known)
	}
	if set, known := entry.SecretPresence["MISSING_ONE"]; !known || set {
		t.Errorf("MISSING_ONE presence = %v/%v, want known and NOT set", set, known)
	}
	if _, known := entry.SecretPresence["EXTERNAL_ONE"]; known {
		t.Error("an external-provider secret is UNVERIFIABLE; it must be absent, not reported false")
	}

	// And the rendered line says which, because "MISSING" and "not
	// verifiable" ask different things of the reader.
	var out bytes.Buffer
	writeOneEnvDiff(&out, entry)
	got := out.String()
	if !strings.Contains(got, "MISSING_ONE [hosted] — MISSING") {
		t.Errorf("want MISSING_ONE marked missing, got:\n%s", got)
	}
	if !strings.Contains(got, "EXTERNAL_ONE [external_secrets] — declared; presence not verifiable") {
		t.Errorf("want EXTERNAL_ONE marked unverifiable, got:\n%s", got)
	}
	if !strings.Contains(got, "SET_ONE [hosted] — already set") {
		t.Errorf("want SET_ONE marked set, got:\n%s", got)
	}
}

// Domains added and removed, with the removal labelled: a domain leaving the
// declaration does not delete anything.
func TestEnvDiff_ReportsDomainChanges(t *testing.T) {
	liveShape := release.Shape{Kind: release.EnvSelfManaged, Domains: []string{"old.example.com"}}
	candidate := release.Shape{Kind: release.EnvSelfManaged, Domains: []string{"new.example.com"}}

	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle", Shape: &liveShape},
		map[string]stubbedRender{"prod": {shape: candidate}})

	if got := entry.Diff.DomainsAdded; len(got) != 1 || got[0] != "new.example.com" {
		t.Errorf("domains_added = %v, want new.example.com", got)
	}
	if got := entry.Diff.DomainsRemoved; len(got) != 1 || got[0] != "old.example.com" {
		t.Errorf("domains_removed = %v, want old.example.com", got)
	}
}

// A workload moving runtime or cluster is its own category, not an object
// change — "web: hosted → bucket" is the sentence a user needs.
func TestEnvDiff_ReportsRuntimeMoves(t *testing.T) {
	liveShape := release.Shape{
		Kind:      release.EnvSelfManaged,
		Workloads: []release.ShapeWorkload{{Name: "web", Runtime: "hosted", Cluster: "c1"}},
	}
	candidate := release.Shape{
		Kind:      release.EnvSelfManaged,
		Workloads: []release.ShapeWorkload{{Name: "web", Runtime: "bucket"}},
	}

	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle", Shape: &liveShape},
		map[string]stubbedRender{"prod": {shape: candidate}})

	if len(entry.Diff.RuntimeChanges) != 1 {
		t.Fatalf("runtime_changes = %+v, want one", entry.Diff.RuntimeChanges)
	}
	rc := entry.Diff.RuntimeChanges[0]
	if rc.From != "hosted" || rc.To != "bucket" {
		t.Errorf("runtime change = %+v, want hosted → bucket", rc)
	}

	var out bytes.Buffer
	writeOneEnvDiff(&out, entry)
	if !strings.Contains(out.String(), "web: hosted on c1 → bucket") {
		t.Errorf("want the move spelled out, got:\n%s", out.String())
	}
}

// A kind change is an ERROR, not a change: a kind is immutable, so it cannot
// be deployed at all, and the output has to say so rather than listing it
// among the diffs.
func TestEnvDiff_KindChangeIsReportedAsAnError(t *testing.T) {
	liveShape := release.Shape{Kind: release.EnvPersistent}
	candidate := release.Shape{Kind: release.EnvSelfManaged}

	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle", Shape: &liveShape},
		map[string]stubbedRender{"prod": {shape: candidate}})

	if entry.Diff.KindChanged == nil {
		t.Fatal("a kind disagreement must be reported")
	}
	var out bytes.Buffer
	writeOneEnvDiff(&out, entry)
	if !strings.Contains(out.String(), "ERROR kind is immutable") {
		t.Errorf("a kind change must read as an error, got:\n%s", out.String())
	}
}

// An env the control plane has no row for "would be created" (§8.2).
func TestEnvDiff_UnknownEnvWouldBeCreated(t *testing.T) {
	entry := runDiffEntry(t, "brand-new",
		liveSide{Known: false, Source: "none"},
		map[string]stubbedRender{"brand-new": {shape: diffShape(obj("Deployment", "api", "a"))}})

	if !entry.WouldBeCreated {
		t.Error("an env with no control-plane row would be created")
	}
	var out bytes.Buffer
	writeOneEnvDiff(&out, entry)
	if !strings.Contains(out.String(), "would be created") {
		t.Errorf("want the would-be-created line, got:\n%s", out.String())
	}
}

// §8.2's rule, and the one most likely to be got wrong: an env with NOTHING
// recorded is reported as "no recorded config", never as "everything is
// added". Presenting a first-ever render as a change set invites someone to
// read a 149-object list as 149 changes.
func TestEnvDiff_NoRecordedConfigIsNotEverythingAdded(t *testing.T) {
	entry := runDiffEntry(t, "prod",
		// Known to the control plane, but nothing recorded for it.
		liveSide{Known: true, Source: "none"},
		map[string]stubbedRender{"prod": {shape: diffShape(obj("Deployment", "api", "a"), obj("Service", "api-svc", "b"))}})

	if entry.LiveSource != "none" {
		t.Errorf("live_source = %q, want none", entry.LiveSource)
	}
	// DiffShapes still reports LiveUnknown with everything in Added —
	// that is its contract — and the RENDERER is what must not present it
	// as a comparison.
	if !entry.Diff.LiveUnknown {
		t.Error("a nil live shape must be flagged unknown, so a reader cannot mistake it for empty")
	}
	var out bytes.Buffer
	writeOneEnvDiff(&out, entry)
	got := out.String()
	if !strings.Contains(got, "no recorded config") {
		t.Errorf("want the no-recorded-config line, got:\n%s", got)
	}
	if strings.Contains(got, "2 added") {
		t.Errorf("a first-ever render must not be presented as a change set, got:\n%s", got)
	}
}

// The Live side is the APPLIED BUNDLE, and declared_shape is only the
// fallback — labelled as weaker evidence, because `forge env build`
// refreshes declared_shape from the very render being deployed.
func TestEnvDiff_DeclaredShapeFallbackIsLabelled(t *testing.T) {
	declared := diffShape(obj("Deployment", "api", "a"))
	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "declared_shape", Shape: &declared},
		map[string]stubbedRender{"prod": {shape: diffShape(obj("Deployment", "api", "a"))}})

	if entry.LiveSource != "declared_shape" {
		t.Errorf("live_source = %q, want declared_shape", entry.LiveSource)
	}
	var out bytes.Buffer
	writeOneEnvDiff(&out, entry)
	if !strings.Contains(out.String(), "weaker evidence") {
		t.Errorf("a declared-shape comparison must be labelled as weaker, got:\n%s", out.String())
	}
}

// §8.2's short-circuit, computed through release.ConfigDigest (F2's shared
// implementation) so the digest a bundle recorded and the digest this render
// produces cannot disagree.
func TestEnvDiff_ConfigIdenticalShortCircuit(t *testing.T) {
	shape := diffShape(obj("Deployment", "api", "a"))
	digest, err := release.ConfigDigest(shape)
	if err != nil {
		t.Fatalf("config digest: %v", err)
	}

	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle", Shape: &shape, ConfigDigest: digest},
		map[string]stubbedRender{"prod": {shape: shape}})

	if !entry.ConfigIdentical {
		t.Error("a render that normalizes to the deployed config digest must report config identical")
	}

	// A DIFFERENT digest must not report identical — otherwise the field
	// would be true whenever a digest was recorded at all.
	other := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle", Shape: &shape, ConfigDigest: "sha256:" + strings.Repeat("f", 64)},
		map[string]stubbedRender{"prod": {shape: shape}})
	if other.ConfigIdentical {
		t.Error("a mismatched config digest must not report identical")
	}
}

// Image changes are split out from config changes, which is §8.2's
// "admin-server: config changed" versus "would run a new build".
func TestEnvDiff_SplitsImageChangesFromConfigChanges(t *testing.T) {
	live := obj("Deployment", "api", "a")
	live.Images = map[string]string{"api": "sha256:" + strings.Repeat("1", 64)}
	candidate := obj("Deployment", "api", "b")
	// Same CONFIG hash, different image: an image-only change.
	candidate.ConfigHash = live.ConfigHash
	candidate.Images = map[string]string{"api": "sha256:" + strings.Repeat("2", 64)}

	liveShape := diffShape(live)
	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle", Shape: &liveShape},
		map[string]stubbedRender{"prod": {shape: diffShape(candidate)}})

	if len(entry.Diff.Changed) != 1 {
		t.Fatalf("changed = %+v, want one", entry.Diff.Changed)
	}
	change := entry.Diff.Changed[0]
	if change.ConfigChanged {
		t.Error("the config hash is unchanged, so this is an image-only change")
	}
	if len(change.Images) != 1 {
		t.Fatalf("images = %+v, want one", change.Images)
	}
	var out bytes.Buffer
	writeOneEnvDiff(&out, entry)
	if !strings.Contains(out.String(), "api: would run a new build") {
		t.Errorf("an image-only change must read as a new build, got:\n%s", out.String())
	}
}

// ─── §8.3 guards ────────────────────────────────────────────────────────────

// GUARD 1. An impure render's result is DISCARDED with the paths named —
// What-if must never modify a worktree, and a diff from a render nobody can
// reproduce is not evidence.
func TestEnvDiff_ImpureRenderIsDiscardedWithThePaths(t *testing.T) {
	entry := runDiffEntry(t, "prod",
		liveSide{Known: true, Source: "bundle"},
		map[string]stubbedRender{"prod": {
			shape: diffShape(obj("Deployment", "api", "a")),
			wrote: []string{"deploy/generated/out.yaml", "src/thing.go"},
		}})

	if entry.Status != diffImpure {
		t.Fatalf("status = %q, want impure", entry.Status)
	}
	if entry.Diff != nil {
		t.Error("an impure render's diff must be DISCARDED, not returned beside the status")
	}
	if len(entry.Wrote) != 2 {
		t.Errorf("wrote = %v, want both paths — they are what makes the report actionable", entry.Wrote)
	}
	var out bytes.Buffer
	writeEnvDiff(&out, envDiffDoc{Environments: []envDiffEntry{entry}})
	got := out.String()
	for _, path := range entry.Wrote {
		if !strings.Contains(got, path) {
			t.Errorf("want %q named in the output, got:\n%s", path, got)
		}
	}
}

// A render failure is a PER-ENV status carrying forge's own message, and
// every other env still diffs (F-8). A partial diff that hid a failure would
// read as "no changes".
func TestEnvDiff_ARenderFailureDoesNotHideTheOtherEnvs(t *testing.T) {
	stubRender(t, map[string]stubbedRender{
		"broken": {err: fmt.Errorf("render deploy/kcl/broken: EvaluationError: undefined name")},
		"fine":   {shape: diffShape(obj("Deployment", "api", "a"))},
	})

	entries := diffEnvironments(context.Background(), t.TempDir(),
		[]string{"broken", "fine"}, envDiffOptions{}, io.Discard)

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want both envs", len(entries))
	}
	if entries[0].Status != diffError {
		t.Errorf("broken status = %q, want error", entries[0].Status)
	}
	if !strings.Contains(entries[0].Detail, "EvaluationError") {
		t.Errorf("want forge's own message, got %q", entries[0].Detail)
	}
	if entries[0].Diff != nil {
		t.Error("a failed render returns no diff to mistake for 'no changes'")
	}
	// The other env is unaffected, which is the point of a per-env status.
	if entries[1].Status != diffOK {
		t.Errorf("fine status = %q (%s), want ok", entries[1].Status, entries[1].Detail)
	}
}

// The order of entries follows the ENVS given, not scheduling order: the
// renders run concurrently, and a report whose rows moved between runs would
// be unreadable.
func TestEnvDiff_EntryOrderFollowsTheEnvListNotScheduling(t *testing.T) {
	answers := map[string]stubbedRender{}
	envs := []string{"alpha", "beta", "gamma", "delta"}
	for _, env := range envs {
		answers[env] = stubbedRender{shape: diffShape(obj("Deployment", env, "a"))}
	}
	stubRender(t, answers)

	entries := diffEnvironments(context.Background(), t.TempDir(), envs, envDiffOptions{}, io.Discard)
	if len(entries) != len(envs) {
		t.Fatalf("got %d entries, want %d", len(entries), len(envs))
	}
	for i, env := range envs {
		if entries[i].Env != env {
			t.Errorf("entry %d is %q, want %q — the order must be the caller's", i, entries[i].Env, env)
		}
	}
}

// ─── The --json contract ────────────────────────────────────────────────────

// Diff is nil for every non-ok status, so a consumer that read it without
// checking Status has nothing to mistake for a comparison.
func TestEnvDiff_JSONCarriesNoDiffForANonOKStatus(t *testing.T) {
	for _, entry := range []envDiffEntry{
		{Env: "a", Status: diffImpure, Wrote: []string{"x"}},
		{Env: "b", Status: diffStale},
		{Env: "c", Status: diffError, Detail: "boom"},
	} {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatalf("encode %s: %v", entry.Env, err)
		}
		if strings.Contains(string(encoded), `"diff"`) {
			t.Errorf("%s (%s) carries a diff: %s", entry.Env, entry.Status, encoded)
		}
		if !strings.Contains(string(encoded), `"status":"`+string(entry.Status)+`"`) {
			t.Errorf("%s must carry its status: %s", entry.Env, encoded)
		}
	}
}

// ─── Secret presence, on the real path ──────────────────────────────────────

// Only the MANAGED store can be asked for presence. An env whose secrets
// live with an external provider is left UNVERIFIABLE rather than queried
// against a store that does not hold its values — which would report every
// secret missing and tell a user to set secrets that are already fine.
func TestEnvDiff_OnlyTheManagedStoreIsAskedForPresence(t *testing.T) {
	managed := LiveEnvironment{
		EnvironmentID: "env-1",
		Env: release.EnvRecord{Name: "prod", Kind: release.EnvPersistent, DeclaredShape: &release.Shape{
			Kind:    release.EnvPersistent,
			Secrets: []release.ShapeSecret{{Name: "A", Provider: "hosted"}},
		}},
	}
	if !managedSecretProvider(managed) {
		t.Error("an env declaring a hosted secret must be queryable")
	}

	external := LiveEnvironment{
		EnvironmentID: "env-2",
		Env: release.EnvRecord{Name: "prod", Kind: release.EnvSelfManaged, DeclaredShape: &release.Shape{
			Kind:    release.EnvSelfManaged,
			Secrets: []release.ShapeSecret{{Name: "A", Provider: "external_secrets"}},
		}},
	}
	if managedSecretProvider(external) {
		t.Error("an external-provider env must NOT be queried: its secrets are unverifiable from here")
	}

	// A mixed env IS queried: its managed names get a real verdict and
	// the rest stay absent, which is the per-name distinction the rule
	// asks for.
	mixed := LiveEnvironment{
		EnvironmentID: "env-3",
		Env: release.EnvRecord{Name: "prod", Kind: release.EnvSelfManaged, DeclaredShape: &release.Shape{
			Kind: release.EnvSelfManaged,
			Secrets: []release.ShapeSecret{
				{Name: "A", Provider: "external_secrets"},
				{Name: "B", Provider: "hosted"},
			},
		}},
	}
	if !managedSecretProvider(mixed) {
		t.Error("a mixed env must be queried, so its managed names get a real verdict")
	}
}

// An env with no control-plane row is never queried: a never-registered
// env's secrets are unverifiable by definition, and asking would need an id
// that does not exist.
func TestEnvDiff_AnUnregisteredEnvHasUnverifiableSecrets(t *testing.T) {
	got := fetchSecretPresence(context.Background(), nil,
		LiveEnvironment{Env: release.EnvRecord{Name: "brand-new", Kind: release.EnvSelfManaged}}, io.Discard)
	if got != nil {
		t.Errorf("presence = %v, want nil (unverifiable) for an env with no row", got)
	}
}

// `--against <bundle-ref>` is REFUSED, not accepted and quietly compared
// against live. Accepting it would print "diff of <checkout> against
// <some-bundle>" above a comparison made against something else — a wrong
// answer labelled as the right one, which is worse than a missing feature.
func TestEnvDiff_RefusesAnUnimplementedAgainstTarget(t *testing.T) {
	if err := checkDiffAgainst("live"); err != nil {
		t.Errorf("live is the implemented target: %v", err)
	}
	if err := checkDiffAgainst(""); err != nil {
		t.Errorf("an empty target is the default, which is live: %v", err)
	}
	err := checkDiffAgainst("sha256:abc")
	if err == nil {
		t.Fatal("a bundle reference must be refused while it is not implemented")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("error = %v, want it to say so plainly", err)
	}
	if !strings.Contains(err.Error(), "silently comparing against live") {
		t.Errorf("error = %v, want it to say why it refuses instead of falling back", err)
	}
}

// Naming an env AND --all, or neither, is refused rather than resolved to a
// guess about which the user meant.
func TestEnvDiff_RefusesBothOrNeitherTarget(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"prod", "--all"},
	} {
		cmd := newEnvDiffCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetContext(context.Background())
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v must be refused", args)
		}
	}
}
