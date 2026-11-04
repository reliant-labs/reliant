package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codemod"
	"github.com/reliant-labs/forge/internal/frontenddeps"
)

// ── fixture ──────────────────────────────────────────────────────────────────

const convergeLegacyLifecycle = "package app\n\nimport \"github.com/reliant-labs/forge/pkg/lifecyclekit\"\n\nvar _ lifecyclekit.Options\n"

const convergeGoodLock = `{"lockfileVersion":3,"packages":{"":{"name":"web","version":"1.0.0","dependencies":{"locallib":"file:../../libs/locallib"}}}}`

// newConvergeFixture is a project pinned at v0.1.45 that the v0.1.46 operatorkit
// codemod has work in (one codemod) and whose frontend lockfile records none of
// the dependencies its package.json declares (one lock refresh).
func newConvergeFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake npm")
	}
	dir := newTestProjectWithVersion(t, "v0.1.45")
	upgradeWrite(t, filepath.Join(dir, "internal", "app", "lifecycle.go"), convergeLegacyLifecycle)
	upgradeWrite(t, filepath.Join(dir, "frontends", "web", "vite.config.ts"), "export default {};\n")
	upgradeWrite(t, filepath.Join(dir, "frontends", "web", "package.json"),
		`{"name":"web","version":"1.0.0","private":true,"dependencies":{"locallib":"file:../../libs/locallib"}}`)
	upgradeWrite(t, filepath.Join(dir, "frontends", "web", "package-lock.json"),
		`{"lockfileVersion":3,"packages":{"":{"name":"web","version":"1.0.0"}}}`)
	return dir
}

// fakeUpgradeNPM puts an npm on PATH whose lockfile-only install writes the
// lockfile a real one would have, and records the directories it ran in.
func fakeUpgradeNPM(t *testing.T) (ranIn func() string) {
	t.Helper()
	bin := t.TempDir()
	good := filepath.Join(bin, "good-lock.json")
	upgradeWrite(t, good, convergeGoodLock)
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$PWD $*\" >> '" + log + "'\ncp '" + good + "' package-lock.json\n"
	if err := os.WriteFile(filepath.Join(bin, "npm"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string { b, _ := os.ReadFile(log); return string(b) }
}

// recordedSteps are follow-up phases that only record what they were asked.
type recordedSteps struct {
	calls  []string
	lint   string // the `forge lint --json` document the fake answers with
	genErr error
	ran    [][]string // vendored hook argv, with env
	env    [][]string
}

func (r *recordedSteps) steps() *upgradeSteps {
	return &upgradeSteps{
		generate: func(string) error { r.calls = append(r.calls, "generate"); return r.genErr },
		lint: func(string) ([]byte, error) {
			r.calls = append(r.calls, "lint")
			if r.lint == "" {
				return []byte(`{"findings":[],"summary":{"errors":0,"warnings":0,"infos":0,"total":0},"ok":true}`), nil
			}
			return []byte(r.lint), nil
		},
		command: func(_ string, env []string, argv []string) error {
			r.calls = append(r.calls, "command:"+strings.Join(argv, " "))
			r.ran = append(r.ran, argv)
			r.env = append(r.env, env)
			return nil
		},
	}
}

func runConverge(t *testing.T, dir string, opts upgradeOptions) (*upgradeReport, error) {
	t.Helper()
	var rep *upgradeReport
	var err error
	if opts.metas == nil {
		// The shipped playbooks detect on shapes unrelated to what each test
		// is about; a test that wants playbooks passes its own.
		opts.metas = []migrationMeta{}
	}
	withCwd(t, dir, func() {
		captureStdout(t, func() { rep, err = runUpgradeConverge(opts) })
	})
	return rep, err
}

func phaseNames(rep *upgradeReport) []string {
	var out []string
	for _, p := range rep.Phases {
		if p.Status == "done" {
			out = append(out, p.Name)
		}
	}
	return out
}

func noGo(string, ...string) ([]byte, error) { return nil, nil }

// ── the range: one codemod, one lock refresh, then regenerate and lint ───────

func TestUpgradeConverge_AppliesTheRangeWithNoHandEdits(t *testing.T) {
	dir := newConvergeFixture(t)
	ranIn := fakeUpgradeNPM(t)
	rec := &recordedSteps{}

	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err != nil {
		t.Fatalf("a fully mechanical range must converge, got %v", err)
	}
	if !rep.OK || rep.ExitCode != 0 || !rep.Converged || len(rep.NeedsHuman) != 0 {
		t.Fatalf("report = ok:%v exit:%d converged:%v needs_human:%+v", rep.OK, rep.ExitCode, rep.Converged, rep.NeedsHuman)
	}

	// The codemod rewrote the file; nobody edited it by hand.
	if got, _ := os.ReadFile(filepath.Join(dir, "internal/app/lifecycle.go")); !strings.Contains(string(got), "operatorkit.Options") {
		t.Errorf("operatorkit codemod did not run:\n%s", got)
	}
	// The lockfile now agrees with package.json, refreshed by the package manager.
	fe := filepath.Join(dir, "frontends", "web")
	lock, ok := frontenddeps.FindLock(fe, dir)
	if !ok {
		t.Fatal("fixture lost its lockfile")
	}
	if d, err := frontenddeps.LockDrift(lock, fe); err != nil || d != nil {
		t.Errorf("lockfile still drifts after upgrade: %+v %v", d, err)
	}
	if !strings.Contains(ranIn(), "--package-lock-only") {
		t.Errorf("lock refresh did not run the lockfile-only install: %q", ranIn())
	}
	if len(rep.Locks) != 1 || rep.Locks[0].Path != "frontends/web/package-lock.json" {
		t.Errorf("locks = %+v, want frontends/web/package-lock.json", rep.Locks)
	}
	// The pin moved, and generate then lint ran after everything that edits the tree.
	if y, _ := os.ReadFile(filepath.Join(dir, "forge.yaml")); !strings.Contains(string(y), "forge_version: v1.0.0") {
		t.Errorf("forge.yaml pin not bumped:\n%s", y)
	}
	if got := strings.Join(rec.calls, ","); got != "generate,lint" {
		t.Errorf("follow-up order = %q, want generate,lint", got)
	}
	want := []string{"modules", "codemods", "templates", "pin", "playbooks", "vendored-contracts", "generate", "locks", "lint"}
	if got := phaseNames(rep); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("phases done = %v, want %v", got, want)
	}
	if len(rep.Codemods) == 0 || rep.Codemods[0].Name != "operatorkit" || rep.Codemods[0].Source != "upgrade" {
		t.Errorf("codemods = %+v, want operatorkit from the upgrade registry", rep.Codemods)
	}

	// A second run finds nothing to do: the verb is idempotent.
	rec2 := &recordedSteps{}
	rep2, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec2.steps()})
	if err != nil || len(rep2.Codemods) != 0 || len(rep2.Locks) != 0 {
		t.Errorf("second run: err=%v codemods=%+v locks=%+v", err, rep2.Codemods, rep2.Locks)
	}
}

// A pin bot upgrades to a pseudo-version of main's head. That is a pre-release of
// the next release, and the codemods main already holds are keyed to it.
func TestUpgradeCodemods_PrereleaseTargetReachesItsOwnRelease(t *testing.T) {
	names := func(from, to string) string {
		var out []string
		for _, c := range upgradeCodemods(from, to) {
			out = append(out, c.Name)
		}
		return strings.Join(out, ",")
	}
	pinLastRelease := func(v string) {
		prev := upgradeLastRelease
		upgradeLastRelease = func() string { return v }
		t.Cleanup(func() { upgradeLastRelease = prev })
	}

	// A binary built after v0.1.46: main's head is v0.1.47-0.… to Go, but the
	// release it leads to is v1.0.0 (NEXT_VERSION), and so are its codemods.
	pinLastRelease("v0.1.46")
	if got := names("v0.1.45", "v0.1.47-0.20261025215718-a53d26d70f80"); !strings.Contains(got, "config-proto-package") || !strings.Contains(got, "operatorkit") {
		t.Errorf("a pseudo-version after v0.1.46 must run the v1.0.0 and v0.1.46 codemods, got %q", got)
	}
	if got := names("v0.1.45", "v0.1.46-0.20261019044744-357579470b0d"); strings.Contains(got, "config-proto-package") {
		t.Errorf("a v0.1.46 pre-release target must not run a v1.0.0 codemod, got %q", got)
	}

	// Past the release the core version decides: v1.0.1-0.… reaches v1.0.0.
	pinLastRelease("v1.0.0")
	if got := names("v0.1.45", "v1.0.1-0.20261101000000-bbbbbbbbbbbb"); !strings.Contains(got, "config-proto-package") {
		t.Errorf("a pseudo-version after v1.0.0 must run the v1.0.0 codemods, got %q", got)
	}
}

// ── what is left for a human ─────────────────────────────────────────────────

func TestUpgradeConverge_LintFindingsAreWhatIsLeftForAHuman(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{lint: `{"findings":[
	  {"severity":"error","rule":"forgeconv-example","file":"internal/x.go","line":7,"message":"bad","fix_hint":"do the thing"},
	  {"severity":"warning","rule":"forge-frontend-lock-drift","message":"drift","grace":{"introduced_in":"v1.0.0","gates_from":"v1.0.1","fix":"forge fix lock"}},
	  {"severity":"warning","rule":"plain-warning","message":"ignore me"}
	],"summary":{"errors":1,"warnings":2,"infos":0,"total":3},"ok":false}`}

	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err == nil {
		t.Fatal("an unresolved lint error must not read as converged")
	}
	if got := exitCodeForError(err); got != exitNeedsHuman {
		t.Errorf("exit code = %d, want exitNeedsHuman (%d)", got, exitNeedsHuman)
	}
	if rep.OK || rep.Converged || rep.ExitCode != exitNeedsHuman {
		t.Errorf("report = ok:%v converged:%v exit:%d", rep.OK, rep.Converged, rep.ExitCode)
	}
	if len(rep.NeedsHuman) != 1 || rep.NeedsHuman[0].Kind != "lint-error" || rep.NeedsHuman[0].Rule != "forgeconv-example" ||
		rep.NeedsHuman[0].File != "internal/x.go" || rep.NeedsHuman[0].Fix != "do the thing" {
		t.Errorf("needs_human = %+v", rep.NeedsHuman)
	}
	// The new rule still in its grace is reported, not gating: it will gate at v1.0.1.
	if len(rep.Upcoming) != 1 || rep.Upcoming[0].Rule != "forge-frontend-lock-drift" || rep.Upcoming[0].GatesFrom != "v1.0.1" {
		t.Errorf("upcoming = %+v", rep.Upcoming)
	}
}

func TestUpgradeConverge_AFailedGenerateIsAFailureNotAHandoff(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{genErr: errors.New("buf exploded")}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err == nil || exitCodeForError(err) != exitWrong {
		t.Fatalf("generate failure must exit %d, got %v (%d)", exitWrong, err, exitCodeForError(err))
	}
	if !strings.Contains(err.Error(), "buf exploded") {
		t.Errorf("error must carry the cause: %v", err)
	}
	// The mechanical work already done is still reported, and lint did not run on a half-built tree.
	if len(rep.Codemods) == 0 || strings.Contains(strings.Join(rec.calls, ","), "lint") {
		t.Errorf("codemods=%+v calls=%v", rep.Codemods, rec.calls)
	}
	var generate upgradePhase
	for _, p := range rep.Phases {
		if p.Name == "generate" {
			generate = p
		}
	}
	if generate.Status != "failed" {
		t.Errorf("generate phase = %+v, want failed", generate)
	}
}

func TestUpgradeConverge_LintThatCannotBeReadIsUndetermined(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{lint: "not json"}
	_, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err == nil || exitCodeForError(err) != exitUndetermined {
		t.Fatalf("an unreadable lint verdict is 'could not look' (%d), got %v (%d)", exitUndetermined, err, exitCodeForError(err))
	}
}

// --check changes nothing and runs neither generate nor lint, but still says what it would do.
func TestUpgradeConverge_CheckWritesNothing(t *testing.T) {
	dir := newConvergeFixture(t)
	ranIn := fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{check: true, to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "internal/app/lifecycle.go")); string(got) != convergeLegacyLifecycle {
		t.Errorf("--check rewrote a file:\n%s", got)
	}
	if ranIn() != "" || len(rec.calls) != 0 {
		t.Errorf("--check ran a tool: npm=%q calls=%v", ranIn(), rec.calls)
	}
	if rep.Mode != "check" || len(rep.Codemods) == 0 || len(rep.Locks) != 1 || !rep.Locks[0].Pending {
		t.Errorf("--check must report the pending work: %+v / %+v", rep.Codemods, rep.Locks)
	}
}

func TestUpgradeConverge_SkipNamesPhasesNotRun(t *testing.T) {
	dir := newConvergeFixture(t)
	ranIn := fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps(),
		skip: map[string]bool{"generate": true, "lint": true, "locks": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.calls) != 0 || ranIn() != "" {
		t.Errorf("skipped phases ran: %v %q", rec.calls, ranIn())
	}
	for _, p := range rep.Phases {
		if (p.Name == "generate" || p.Name == "lint" || p.Name == "locks") && p.Status != "skipped" {
			t.Errorf("phase %s = %s, want skipped", p.Name, p.Status)
		}
	}
}

// --skip codemods leaves the registry codemods the range crosses undone, says
// which ones, and still runs everything else. The pin moves in the same run, so
// the report is the only record that they were not applied.
func TestUpgradeConverge_SkipCodemodsNamesWhatItLeftForYou(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps(),
		skip: map[string]bool{"codemods": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "internal/app/lifecycle.go")); string(got) != convergeLegacyLifecycle {
		t.Errorf("--skip codemods still rewrote lifecycle.go:\n%s", got)
	}
	if len(rep.Codemods) != 0 {
		t.Errorf("codemods ran: %+v", rep.Codemods)
	}
	var detail string
	for _, p := range rep.Phases {
		if p.Name == "codemods" {
			if p.Status != "skipped" {
				t.Errorf("codemods phase = %s, want skipped", p.Status)
			}
			detail = p.Detail
		}
	}
	if !strings.Contains(detail, "forge fix operatorkit") && !strings.Contains(detail, " fix operatorkit") {
		t.Errorf("the skipped phase does not name the codemod to run yourself: %q", detail)
	}
	if y, _ := os.ReadFile(filepath.Join(dir, "forge.yaml")); !strings.Contains(string(y), "forge_version: v1.0.0") {
		t.Errorf("the pin must still move:\n%s", y)
	}
	if got := strings.Join(rec.calls, ","); got != "generate,lint" {
		t.Errorf("the other phases did not run: %q", got)
	}
}

// connect-errors and composition-graph run as a playbook's automation, so
// --skip playbooks is how a consumer splits them into a PR of their own. The
// skipped phase names them.
func TestUpgradeConverge_SkipPlaybooksNamesTheCodemodsItLeftForYou(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	upgradeWrite(t, filepath.Join(dir, "needs-fix.txt"), "old\n")
	metas := []migrationMeta{{ID: "v1.0.0", Version: "v1.0.0", Automation: []string{"fix:connect-errors", "fix:composition-graph"},
		Detection: "grep -qs old needs-fix.txt"}}
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps(), metas: metas,
		skip: map[string]bool{"playbooks": true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Playbooks) != 0 {
		t.Errorf("playbooks ran: %+v", rep.Playbooks)
	}
	for _, p := range rep.Phases {
		if p.Name != "playbooks" {
			continue
		}
		for _, want := range []string{"fix connect-errors", "fix composition-graph"} {
			if !strings.Contains(p.Detail, want) {
				t.Errorf("skipped playbooks phase does not name %q: %q", want, p.Detail)
			}
		}
	}
}

// ── playbooks: automate what they declare, hand over what remains ────────────

func TestUpgradeConverge_PlaybookAutomationRunsCodemodsAndReportsWhatRemains(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	upgradeWrite(t, filepath.Join(dir, "needs-fix.txt"), "old\n")
	upgradeWrite(t, filepath.Join(dir, "needs-human.txt"), "old\n")

	var ran []string
	fake := codemod.Codemod{Name: "fake-fix", Version: "v1.0.0", Run: func(d string, o codemod.Options) (codemod.Report, error) {
		ran = append(ran, "fake-fix")
		if o.Write {
			_ = os.WriteFile(filepath.Join(d, "needs-fix.txt"), []byte("new\n"), 0o644)
		}
		return codemod.Report{Rewrites: []string{"needs-fix.txt: old → new"}}, nil
	}}
	metas := []migrationMeta{
		{ID: "v1.0.0", SkillPath: "migrations/v1.0.0", Version: "v1.0.0", Description: "automatable",
			Detection: "grep -q old needs-fix.txt", Automation: []string{"fix:fake-fix"}},
		{ID: "v0.1.46", SkillPath: "migrations/v0.1.46", Version: "v0.1.46", Description: "prose only",
			Detection: "grep -q old needs-human.txt"},
	}
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps(), metas: metas,
		lookup: func(n string) (codemod.Codemod, bool) { return fake, n == "fake-fix" }})

	if strings.Join(ran, ",") != "fake-fix" {
		t.Fatalf("automation ran %v", ran)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "needs-fix.txt")); string(got) != "new\n" {
		t.Errorf("automation did not rewrite: %q", got)
	}
	// The automated playbook no longer matches its detection; the prose one still does.
	byID := map[string]upgradePlaybookRun{}
	for _, p := range rep.Playbooks {
		byID[p.ID] = p
	}
	if !byID["v1.0.0"].Resolved || byID["v0.1.46"].Resolved {
		t.Errorf("playbooks = %+v", rep.Playbooks)
	}
	if err == nil || exitCodeForError(err) != exitNeedsHuman {
		t.Fatalf("a playbook still matching its detection is for a human: %v", err)
	}
	if len(rep.NeedsHuman) != 1 || rep.NeedsHuman[0].Kind != "playbook" || rep.NeedsHuman[0].ID != "v0.1.46" ||
		!strings.Contains(rep.NeedsHuman[0].Fix, "forge skill load migrations/v0.1.46") ||
		!strings.Contains(rep.NeedsHuman[0].Fix, "project upgrade apply v0.1.46") {
		t.Errorf("needs_human = %+v", rep.NeedsHuman)
	}
}

func TestUpgradeConverge_ABlockedAutomationStepIsReportedWithItsReasons(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	upgradeWrite(t, filepath.Join(dir, "marker.txt"), "x\n")
	fake := codemod.Codemod{Name: "blocker", Version: "v1.0.0", Run: func(string, codemod.Options) (codemod.Report, error) {
		return codemod.Report{Blocked: []string{"internal/a.go:3: a ServeSpec knob"}, Next: []string{"clear it"}}, nil
	}}
	metas := []migrationMeta{{ID: "v1.0.0", SkillPath: "migrations/v1.0.0", Version: "v1.0.0", Detection: "grep -q x marker.txt", Automation: []string{"fix:blocker"}}}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: (&recordedSteps{}).steps(), metas: metas,
		lookup: func(n string) (codemod.Codemod, bool) { return fake, n == "blocker" }})
	if err == nil || exitCodeForError(err) != exitNeedsHuman {
		t.Fatalf("err = %v", err)
	}
	var blocked *upgradeHumanItem
	for i := range rep.NeedsHuman {
		if rep.NeedsHuman[i].Kind == "codemod-blocked" {
			blocked = &rep.NeedsHuman[i]
		}
	}
	if blocked == nil || blocked.ID != "blocker" || !strings.Contains(blocked.Message, "a ServeSpec knob") || !strings.Contains(blocked.Fix, "forge fix blocker") {
		t.Errorf("needs_human = %+v", rep.NeedsHuman)
	}
}

// Every codemod forge registers is reachable from `project upgrade`: either it
// runs on every crossing (Upgrade) or a shipped playbook names it as an
// automation step. A breaking change registers its codemod (the API-diff gate
// requires one); this is what makes that registration take effect for a consumer.
func TestEveryRegisteredCodemodIsReachableFromUpgrade(t *testing.T) {
	metas, err := loadMigrationMetas()
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]string{}
	for _, m := range metas {
		for _, step := range m.Automation {
			kind, name, ok := strings.Cut(step, ":")
			if !ok || kind != "fix" {
				t.Errorf("playbook %s: automation step %q must be fix:<codemod>", m.ID, step)
				continue
			}
			if _, found := codemod.Lookup(name); !found {
				t.Errorf("playbook %s: automation names codemod %q, which is not registered", m.ID, name)
			}
			named[name] = m.ID
		}
	}
	for _, c := range codemod.All() {
		if !c.Upgrade && named[c.Name] == "" {
			t.Errorf("codemod %q (%s) is reachable from neither Upgrade:true nor a playbook's `automation:` — `forge project upgrade` would never run it", c.Name, c.Version)
		}
	}
}

// ── vendored contracts: a declared hook, not a reliant-specific path ────────

const vendoredLagReport = `{"schema":"reliant-vendored-proto/v1","status":"lag","blocking":false,
 "files":[{"path":"a.proto","state":"differs"}],
 "revendor":{"repo":"reliant","env":{"CONTROL_PLANE_DIR":"/work/cp"},"commands":["ignored: the project declares its own"]}}`

func vendoredFixture(t *testing.T, report string) string {
	t.Helper()
	dir := newConvergeFixture(t)
	yaml, _ := os.ReadFile(filepath.Join(dir, "forge.yaml"))
	upgradeWrite(t, filepath.Join(dir, "forge.yaml"), string(yaml)+`upgrade:
  vendored_contracts:
    - name: controlplane
      report: tmp/ci/reliant-vendored-proto.json
      schema: reliant-vendored-proto/v1
      env_path: revendor.env
      commands:
        - [node, .github/scripts/sync-controlplane-proto.mjs]
        - [npm, run, "proto:generate:controlplane"]
`)
	if report != "" {
		upgradeWrite(t, filepath.Join(dir, "tmp/ci/reliant-vendored-proto.json"), report)
	}
	return dir
}

func TestUpgradeConverge_RevendorsADeclaredContractThatLags(t *testing.T) {
	dir := vendoredFixture(t, vendoredLagReport)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err != nil {
		t.Fatal(err)
	}
	// Re-vendor commands run before generate, in order, with the report's env.
	want := "command:node .github/scripts/sync-controlplane-proto.mjs,command:npm run proto:generate:controlplane,generate,lint"
	if got := strings.Join(rec.calls, ","); got != want {
		t.Errorf("calls = %q\nwant    %q", got, want)
	}
	if len(rec.env) == 0 || !containsEnv(rec.env[0], "CONTROL_PLANE_DIR=/work/cp") {
		t.Errorf("hook env = %v, want the report's CONTROL_PLANE_DIR", rec.env)
	}
	if len(rep.Vendored) != 1 || rep.Vendored[0].Name != "controlplane" || rep.Vendored[0].Status != "lag" || !rep.Vendored[0].Revendored {
		t.Errorf("vendored = %+v", rep.Vendored)
	}
}

func containsEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestUpgradeConverge_VendoredContractThatIsInSyncOrAbsentRunsNothing(t *testing.T) {
	for name, report := range map[string]string{
		"in-sync": `{"schema":"reliant-vendored-proto/v1","status":"in-sync"}`,
		"absent":  "",
	} {
		t.Run(name, func(t *testing.T) {
			dir := vendoredFixture(t, report)
			fakeUpgradeNPM(t)
			rec := &recordedSteps{}
			rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
			if err != nil {
				t.Fatal(err)
			}
			if len(rec.ran) != 0 || len(rep.Vendored) != 1 || rep.Vendored[0].Revendored {
				t.Errorf("ran=%v vendored=%+v", rec.ran, rep.Vendored)
			}
			if name == "absent" && rep.Vendored[0].Status != "no-report" {
				t.Errorf("a missing report is 'no-report', got %q", rep.Vendored[0].Status)
			}
		})
	}
}

func TestUpgradeConverge_VendoredReportOfAnotherSchemaIsForAHuman(t *testing.T) {
	dir := vendoredFixture(t, `{"schema":"something-else/v9","status":"lag"}`)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err == nil || exitCodeForError(err) != exitNeedsHuman {
		t.Fatalf("err = %v", err)
	}
	if len(rec.ran) != 0 {
		t.Errorf("a report of an unknown schema must not trigger commands: %v", rec.ran)
	}
	if len(rep.NeedsHuman) == 0 || rep.NeedsHuman[0].Kind != "vendored-contract" {
		t.Errorf("needs_human = %+v", rep.NeedsHuman)
	}
}

// ── the command: --json is one document, exit codes are the contract ─────────

func TestUpgradeCmd_JSONIsOneDocumentOnStdout(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	rec := &recordedSteps{}
	cmd := newUpgradeCmd()
	cmd.SetArgs([]string{"--to", "v1.0.0", "--json"})
	upgradeStepsOverride = rec.steps()
	upgradeGoRunnerOverride = noGo
	upgradeMetasOverride = []migrationMeta{}
	t.Cleanup(func() { upgradeStepsOverride, upgradeGoRunnerOverride, upgradeMetasOverride = nil, nil, nil })

	var stdout string
	var execErr error
	withCwd(t, dir, func() {
		stdout = captureStdout(t, func() { execErr = cmd.Execute() })
	})
	if execErr != nil {
		t.Fatal(execErr)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(stdout)))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Errorf("stdout carries more than one document:\n%s", stdout)
	}
	for _, key := range []string{"ok", "exit_code", "schema", "mode", "from", "to", "phases", "codemods", "locks", "playbooks", "vendored_contracts", "needs_human", "upcoming", "converged"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("document has no %q key:\n%s", key, stdout)
		}
	}
	if doc["schema"] != "forge-project-upgrade/v1" || doc["ok"] != true || doc["exit_code"] != float64(0) || doc["converged"] != true {
		t.Errorf("doc = %v", doc)
	}
	// An array is [], never null, so a consumer indexes it without a nil check.
	for _, key := range []string{"needs_human", "upcoming", "vendored_contracts"} {
		if arr, ok := doc[key].([]any); !ok || arr == nil {
			t.Errorf("%s = %v, want an array", key, doc[key])
		}
	}
}

func TestUpgradeExitCodesAreDistinct(t *testing.T) {
	seen := map[int]string{}
	for name, code := range map[string]int{"ok": exitOK, "wrong": exitWrong, "undetermined": exitUndetermined, "needs-human": exitNeedsHuman} {
		if other, dup := seen[code]; dup {
			t.Errorf("%s and %s share exit code %d", name, other, code)
		}
		seen[code] = name
	}
	for _, reserved := range []int{exitConflict, exitRefused, exitPlanUnconfirmed, exitSuperseded, exitQueued, exitTimedOut} {
		if exitNeedsHuman == reserved {
			t.Errorf("exitNeedsHuman collides with a deploy code %d", reserved)
		}
	}
}

// ── scaffold-once files the project deleted ─────────────────────────────────

// reliant's cmd/reliant/cmd/metrics.go and commands.go were recorded deleted in
// .forge/scaffolded.json, and the pin bot's first upgrade wrote them back
// (they did not compile: `undefined: version`). A scaffold-once file the
// ledger records as deleted must stay deleted through the whole verb.
func TestUpgradeConverge_LeavesLedgerDeletedScaffoldOnceFilesDeleted(t *testing.T) {
	dir := newConvergeFixture(t)
	fakeUpgradeNPM(t)
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)

	deleted := []string{
		"cmd/test-project/cmd/metrics.go",
		"cmd/test-project/cmd/commands.go",
		"internal/metrics/metrics.go",
	}
	for _, rel := range deleted {
		checksums.RecordScaffold(dir, rel)
	}

	rec := &recordedSteps{}
	rep, err := runConverge(t, dir, upgradeOptions{to: "v1.0.0", goRun: noGo, steps: rec.steps()})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	for _, rel := range deleted {
		if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(statErr) {
			t.Errorf("upgrade re-created %s, which the ledger records as deleted (stat err %v)", rel, statErr)
		}
	}
	if !rep.Converged {
		t.Errorf("a deleted scaffold is not work for a person: %+v", rep.NeedsHuman)
	}

}
