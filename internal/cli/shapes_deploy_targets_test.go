package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/kcl"
)

// TestScanDeployTargetsReflectsSchema asserts the shapes scanner reports
// exactly the set kcl.DeployTargets reflects — no filtering, no extra, no
// hand-written entry. The expectation is derived from the same reflection
// (whose own fidelity to schema.k is pinned in kcl/targets_test.go) rather
// than written down here, because a literal list in this file would be the
// second source of truth the whole change exists to avoid.
func TestScanDeployTargetsReflectsSchema(t *testing.T) {
	want, err := kcl.DeployTargets()
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("no deploy targets reflected from the embedded schema")
	}

	// The scanner reads the EMBEDDED module, so it answers correctly with a
	// directory that is not a forge project at all — which is what lets
	// `forge project shapes --kind deploy-target` work anywhere.
	got := scanDeployTargets(t.TempDir())
	if len(got) != len(want) {
		t.Fatalf("got %d shapes, want %d", len(got), len(want))
	}

	byName := map[string]shape{}
	for _, s := range got {
		if s.Kind != "deploy-target" {
			t.Errorf("%s: kind %q, want deploy-target", s.Name, s.Kind)
		}
		byName[s.Name] = s
	}
	for _, w := range want {
		s, ok := byName[w.Name]
		if !ok {
			t.Errorf("%s missing from scanner output", w.Name)
			continue
		}
		if s.Line != w.Line {
			t.Errorf("%s: line %d, want %d", w.Name, s.Line, w.Line)
		}
		// The schema is in forge's KCL module, which the binary supplies —
		// not a project file — so it is labelled as a path inside that
		// module, never as a project-relative path someone would grep for.
		if !strings.HasPrefix(s.File, kclModuleRefPrefix) {
			t.Errorf("%s: file %q should name a path in forge's KCL module (%s…)", w.Name, s.File, kclModuleRefPrefix)
		}
		// Which workload kind accepts the target is the distinction users
		// are missing, so it must be in the output, not just the model.
		for _, workload := range w.Workloads {
			if !strings.Contains(s.Detail, workload) {
				t.Errorf("%s: detail %q omits owning workload %q", w.Name, s.Detail, workload)
			}
		}
		if w.Doc != "" && !strings.Contains(s.Detail, w.Doc) {
			t.Errorf("%s: detail omits the docstring", w.Name)
		}
	}
}

// TestDeployTargetShapesFilterByKind: `--kind deploy-target` selects them and
// every other kind excludes them.
func TestDeployTargetShapesFilterByKind(t *testing.T) {
	if kindRank("deploy-target") == kindRank("unknown-kind") {
		t.Error("deploy-target has no distinct sort rank; it will interleave with unknown kinds")
	}
	for _, s := range scanDeployTargets(t.TempDir()) {
		if s.Kind != "deploy-target" {
			t.Fatalf("scanner emitted kind %q", s.Kind)
		}
	}
}

// TestRequiredFieldsExcludeDiscriminator: `type` is the union discriminator,
// always defaulted by the schema and never written by a user, so it must not
// appear as something the user has to supply.
func TestRequiredFieldsExcludeDiscriminator(t *testing.T) {
	fields := []kcl.SchemaField{
		{Name: "type", Default: `"firebase"`},
		{Name: "project"},
		{Name: "target", Optional: true},
		{Name: "bundle", Default: "[]"},
	}
	if got := strings.Join(requiredFieldNames(fields), ","); got != "project" {
		t.Errorf("required: got %q, want %q", got, "project")
	}
	if got := strings.Join(optionalFieldNames(fields), ","); got != "target,bundle" {
		t.Errorf("optional: got %q, want %q", got, "target,bundle")
	}
}

// ── the deploy warning ───────────────────────────────────────────────────

func frontendCfg(names ...string) *config.ProjectConfig {
	cfg := &config.ProjectConfig{}
	for _, n := range names {
		cfg.Frontends = append(cfg.Frontends, config.FrontendConfig{Name: n, Type: "vite-spa"})
	}
	return cfg
}

// TestWarnsWhenFrontendAbsentFromEnv is the reported bug: the scaffold emits
// forge.Frontend in dev only, so staging/prod render no frontend workload at
// all and the deploy dispatch has nothing to iterate over. The frontend is
// ABSENT from entities.
func TestWarnsWhenFrontendAbsentFromEnv(t *testing.T) {
	var sb strings.Builder
	warnUndeployedFrontends(&sb, frontendCfg("web"), &KCLEntities{}, "prod", nil)

	out := sb.String()
	if !strings.Contains(out, `frontend "web" is not declared in env "prod"`) {
		t.Errorf("missing the warning line:\n%s", out)
	}
	if !strings.Contains(out, "will NOT be deployed") {
		t.Errorf("warning does not say the frontend will not ship:\n%s", out)
	}
	if !strings.Contains(out, "deploy/kcl/prod/main.k") {
		t.Errorf("warning does not name the file to edit:\n%s", out)
	}
	if !strings.Contains(out, "forge project shapes --kind deploy-target") {
		t.Errorf("warning does not point at the discovery command:\n%s", out)
	}

	// The hint's target names must be REFLECTED, matching the Frontend
	// union exactly — not a literal, and not the service-side union.
	avail, err := kcl.DeployTargetsFor("Frontend")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range avail {
		if !strings.Contains(out, name) {
			t.Errorf("hint omits frontend deploy target %q:\n%s", name, out)
		}
	}
	all, err := kcl.DeployTargets()
	if err != nil {
		t.Fatal(err)
	}
	for _, tgt := range all {
		frontendOK := false
		for _, w := range tgt.Workloads {
			if w == "Frontend" {
				frontendOK = true
			}
		}
		if !frontendOK && strings.Contains(out, tgt.Name) {
			t.Errorf("hint offers %q, which the Frontend schema does not accept:\n%s", tgt.Name, out)
		}
	}
}

// TestNoWarningWhenFrontendIsDevServedByDeclaration: a frontend the env binds
// to forge.OnHost ships nothing, and that is a DECLARATION, not the silence
// this warning exists for (a frontend with no runtime is a render error, so
// the old "present with no deploy block" shape no longer exists).
func TestNoWarningWhenFrontendIsDevServedByDeclaration(t *testing.T) {
	entities := &KCLEntities{Frontends: []FrontendEntity{{Name: "web", Path: "frontends/web", Runtime: FrontendRuntime{Type: FrontendRuntimeHost}}}}
	var sb strings.Builder
	warnUndeployedFrontends(&sb, frontendCfg("web"), entities, "staging", nil)
	if sb.String() != "" {
		t.Errorf("warned about a frontend its env binds to forge.OnHost:\n%s", sb.String())
	}
}

// TestNoWarningWhenFrontendHasDeployTarget: the correctly-configured case
// must stay silent, or the warning becomes noise users learn to ignore.
func TestNoWarningWhenFrontendHasDeployTarget(t *testing.T) {
	entities := &KCLEntities{Frontends: []FrontendEntity{{
		Name:    "web",
		Path:    "frontends/web",
		Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase},
	}}}
	var sb strings.Builder
	warnUndeployedFrontends(&sb, frontendCfg("web"), entities, "prod", nil)
	if sb.String() != "" {
		t.Errorf("warned about a frontend that HAS a deploy target:\n%s", sb.String())
	}
}

// TestNoWarningWithoutFrontends: a backend-only project must never see this.
func TestNoWarningWithoutFrontends(t *testing.T) {
	var sb strings.Builder
	warnUndeployedFrontends(&sb, &config.ProjectConfig{}, &KCLEntities{}, "prod", nil)
	if sb.String() != "" {
		t.Errorf("warned on a project with no frontends:\n%s", sb.String())
	}
	sb.Reset()
	warnUndeployedFrontends(&sb, nil, nil, "prod", nil)
	if sb.String() != "" {
		t.Errorf("warned with a nil config:\n%s", sb.String())
	}
}

// TestNoWarningWhenTargetScoped: --target names the apps to deploy; a
// frontend not named was excluded deliberately and its absence is not a
// surprise worth reporting.
func TestNoWarningWhenTargetScoped(t *testing.T) {
	var sb strings.Builder
	warnUndeployedFrontends(&sb, frontendCfg("web"), &KCLEntities{}, "prod", []string{"api"})
	if sb.String() != "" {
		t.Errorf("warned under an explicit --target scope:\n%s", sb.String())
	}
}

// TestNoWarningInDev: dev dev-serves frontends (`npm run dev`) and never
// consumes a deployed frontend artifact — `forge env up` passes skipFrontend
// to the deploy phase for exactly that reason. A dev frontend with no deploy
// target is correct, so warning there would be noise in the one env every
// user runs constantly.
func TestNoWarningInDev(t *testing.T) {
	var sb strings.Builder
	warnUndeployedFrontends(&sb, frontendCfg("web"), &KCLEntities{}, "dev", nil)
	if sb.String() != "" {
		t.Errorf("warned in dev, where frontends are dev-served not deployed:\n%s", sb.String())
	}
	// ...but a non-dev env with the same inputs still warns, or the guard
	// above has silenced everything.
	sb.Reset()
	warnUndeployedFrontends(&sb, frontendCfg("web"), &KCLEntities{}, "staging", nil)
	if sb.String() == "" {
		t.Error("the dev guard suppressed the warning in staging too")
	}
}

// TestWarningIsPerFrontend: each undeployed frontend gets its own line, and a
// deployed sibling does not suppress the warning for the others.
func TestWarningIsPerFrontend(t *testing.T) {
	entities := &KCLEntities{Frontends: []FrontendEntity{{
		Name:    "admin",
		Runtime: FrontendRuntime{Type: FrontendRuntimeFirebase},
	}}}
	var sb strings.Builder
	warnUndeployedFrontends(&sb, frontendCfg("web", "admin", "marketing"), entities, "prod", nil)

	out := sb.String()
	if strings.Contains(out, `"admin"`) {
		t.Errorf("warned about the deployed frontend:\n%s", out)
	}
	for _, want := range []string{`"web"`, `"marketing"`} {
		if !strings.Contains(out, want) {
			t.Errorf("no warning for %s:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "will NOT be deployed"); n != 2 {
		t.Errorf("got %d warnings, want 2:\n%s", n, out)
	}
}
