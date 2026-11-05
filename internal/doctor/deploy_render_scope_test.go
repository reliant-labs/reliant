package doctor

// deploy_render_scope_test.go — the degradation contract.
//
// The defect these guard: the helper these checks used to open with
// (renderPreamble, since deleted) bailed out only on ZERO environments
// or on ALL renders failing, so a single environment that failed to
// evaluate was invisible. It contributed no objects, and the
// check reported StatusPass over the subset it had — a confident green
// about environments it never read. CheckDeployServiceAccount could go
// one worse and reach StatusSkip, "not applicable", because the only
// environment declaring a ServiceAccount was the one that failed.
//
// Three properties are asserted, in the order they matter:
//
//  1. a partial render can never come out Pass or Skip, and the result
//     names the environment that went unread;
//  2. no readable environment at all is a FAIL only for
//     CheckDeployManifests — unrenderable is its subject — and
//     UNDETERMINED for every check that reasons about manifest content;
//  3. a complete render is byte-identical to what it always was.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// healthyRenderJSON is an environment with nothing wrong with it: probes
// and limits on the serving container, a bound ServiceAccount, a migrate
// initContainer, and every credential sourced from a Secret. Each check
// below passes on it, so any non-Pass in these tests comes from the
// scope contract and not from the manifests.
//
// The namespace is per-environment for CheckObjectCollision's sake: two
// environments writing one namespace IS a collision, and a fixture that
// contained one would mask the degradation under test behind a genuine
// finding. Each env therefore owns its own address space, which is also
// what a correctly-configured project looks like.
func healthyRenderJSON(namespace string) string {
	return `{"output":{
  "frontends":[],
  "manifests":[
    {"apiVersion":"v1","kind":"ServiceAccount","metadata":{"name":"api","namespace":"` + namespace + `"}},
    {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"` + namespace + `"},
     "spec":{"template":{"spec":{
       "serviceAccountName":"api",
       "initContainers":[{"name":"migrate","command":["/app/demo","db","migrate","up"],
         "env":[{"name":"DATABASE_URL","valueFrom":{"secretKeyRef":{"name":"db","key":"url"}}}]}],
       "containers":[{"name":"api",
         "ports":[{"containerPort":8080,"name":"http","protocol":"TCP"}],
         "readinessProbe":{"httpGet":{"path":"/readyz","port":8080}},
         "livenessProbe":{"httpGet":{"path":"/healthz","port":8080}},
         "resources":{"requests":{"cpu":"500m","memory":"512Mi"},"limits":{"cpu":"2","memory":"1Gi"}},
         "env":[{"name":"DATABASE_URL","valueFrom":{"secretKeyRef":{"name":"db","key":"url"}}}]}]
     }}}}
  ]
}}`
}

// leakyRenderJSON carries the same shape with the credential inlined —
// a real, determined finding, used to prove the fold does not bury one.
const leakyRenderJSON = `{"output":{
  "manifests":[
    {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"ns"},
     "spec":{"template":{"spec":{"containers":[{"name":"api",
       "env":[{"name":"DATABASE_URL","value":"postgres://u:p@h/db"}]}]}}}}
  ]
}}`

var errStagingRender = errors.New("kcl: undefined variable 'ingress_host'")

// unreadable is an environment whose KCL did not evaluate — the state
// renderDeployEnvs records when kclrender.Run returns an error.
func unreadable(name string) envRender {
	return envRender{env: name, err: errStagingRender}
}

// scopeProject writes the on-disk facts the two checks with a
// filesystem precondition need: a forge.yaml (frontend drift) and one
// SQL migration (migrations). Without them those checks Skip before
// they ever reach a render, and would prove nothing here.
func scopeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "forge.yaml"),
		[]byte("name: proj\nmodule_path: example.com/proj\n"), 0o600); err != nil {
		t.Fatalf("write forge.yaml: %v", err)
	}
	migrations := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(migrations, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(migrations, "0001_init.sql"),
		[]byte("CREATE TABLE t (id int);\n"), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}
	return dir
}

func scopeEnv(t *testing.T, dir string, renders ...envRender) *Environment {
	t.Helper()
	e := envWithRender(renders)
	e.ProjectDir = dir
	return e
}

// contentChecks are the checks whose subject is the CONTENT of the
// rendered manifests. Every one of them must degrade when an
// environment goes unread; none of them may report the render itself.
//
// CheckObjectCollision is the member that needs this most. The others
// ask a question of each environment separately, so an unread one costs
// them that environment's answer. This one asks what environments have
// in COMMON — so an unread environment does not merely go unexamined, it
// takes every collision it was half of out of the search space with it,
// and the check reports "no two environments write the same object"
// BECAUSE one of them was missing. A false all-clear on the exact defect
// it exists to catch.
func contentChecks() map[string]CheckFunc {
	return map[string]CheckFunc{
		"Deploy Probes":     CheckDeployProbes,
		"Deploy Resources":  CheckDeployResources,
		"Deploy Secrets":    CheckDeploySecrets,
		"Deploy SA Binding": CheckDeployServiceAccount,
		"Deploy Migrations": CheckDeployMigrations,
		"Frontend Code":     CheckFrontendCode,
		"Object Collision":  CheckObjectCollision,
	}
}

// THE BUG. Two environments render clean, one does not. Every check
// used to answer Pass — a claim about three environments made from two.
func TestRenderScope_PartialRenderIsNeverAPass(t *testing.T) {
	dir := scopeProject(t)
	renders := []envRender{
		renderFromJSON(t, "preprod", healthyRenderJSON("preprod")),
		renderFromJSON(t, "prod", healthyRenderJSON("prod")),
		unreadable("staging"),
	}

	for name, fn := range contentChecks() {
		t.Run(name, func(t *testing.T) {
			got := fn(context.Background(), scopeEnv(t, dir, renders...))

			if got.Status == StatusPass || got.Status == StatusSkip {
				t.Fatalf("Status = %q — a check that read 2 of 3 environments must not "+
					"answer %q about all three.\nmessage: %s", got.Status, got.Status, got.Message)
			}
			if got.Status != StatusUnknown {
				t.Fatalf("Status = %q, want %q — nothing is known to be broken, the facts "+
					"are simply missing.\nmessage: %s", got.Status, StatusUnknown, got.Message)
			}
			// The scope of an answer has to be visible in the one line the
			// report prints, not only under -v.
			if !strings.Contains(got.Message, "2 of 3 env(s) read") {
				t.Errorf("message does not say how much was examined: %s", got.Message)
			}
			if !strings.Contains(got.Message, "staging") {
				t.Errorf("message does not name the unread environment: %s", got.Message)
			}
			// …and -v has to say WHY it went unread.
			if !strings.Contains(got.Evidence, "staging") ||
				!strings.Contains(got.Evidence, "ingress_host") {
				t.Errorf("evidence does not carry staging's render error: %s", got.Evidence)
			}
		})
	}
}

// A determined finding survives the fold. Downgrading a plaintext
// credential to "undetermined" because a DIFFERENT environment failed to
// render would hide a defect forge is certain about — the fold names the
// scope, it does not soften the verdict.
func TestRenderScope_PartialRenderKeepsADeterminedFailure(t *testing.T) {
	dir := scopeProject(t)
	env := scopeEnv(t, dir,
		renderFromJSON(t, "prod", leakyRenderJSON),
		unreadable("staging"))

	got := CheckDeploySecrets(context.Background(), env)
	if got.Status != StatusFail {
		t.Fatalf("Status = %q, want %q — prod's credential is plaintext whether or not "+
			"staging rendered.\nmessage: %s", got.Status, StatusFail, got.Message)
	}
	if !strings.Contains(got.Message, "1 of 2 env(s) read") {
		t.Errorf("a failure must still disclose its scope: %s", got.Message)
	}
	if !strings.Contains(got.Evidence, "DATABASE_URL") {
		t.Errorf("the finding itself was lost: %s", got.Evidence)
	}
	if !strings.Contains(got.Evidence, "staging") {
		t.Errorf("evidence does not name the unread environment: %s", got.Evidence)
	}
}

// The worst arm of the old behaviour: StatusSkip, "no ServiceAccount
// rendered", reached because the only environment that declares one is
// the environment that failed. That is a security-shaped check asserting
// "not applicable" from facts it could not obtain.
func TestRenderScope_ServiceAccountSkipBecomesUnknown(t *testing.T) {
	const noServiceAccount = `{"output":{"manifests":[
	  {"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"api","namespace":"ns"},
	   "spec":{"template":{"spec":{"containers":[{"name":"api"}]}}}}]}}`

	env := scopeEnv(t, scopeProject(t),
		renderFromJSON(t, "dev", noServiceAccount),
		unreadable("prod")) // the only env declaring an SA — and unread

	got := CheckDeployServiceAccount(context.Background(), env)
	if got.Status == StatusSkip {
		t.Fatalf("Status = %q — \"no ServiceAccount rendered\" is a claim about prod, "+
			"which was never read.\nmessage: %s", got.Status, got.Message)
	}
	if got.Status != StatusUnknown {
		t.Fatalf("Status = %q, want %q.\nmessage: %s", got.Status, StatusUnknown, got.Message)
	}
	if !strings.Contains(got.Message, "prod") {
		t.Errorf("message does not name the unread environment: %s", got.Message)
	}
}

// When NOTHING renders, the two kinds of check part company:
// CheckDeployManifests is asking whether the environments render, so it
// has its answer and FAILS. The rest observed no probe, no limit and no
// leaked secret, so they have no answer at all.
func TestRenderScope_NoReadableEnvironment(t *testing.T) {
	dir := scopeProject(t)
	renders := []envRender{unreadable("preprod"), unreadable("prod")}

	t.Run("Deploy Manifests fails", func(t *testing.T) {
		got := CheckDeployManifests(context.Background(), scopeEnv(t, dir, renders...))
		if got.Status != StatusFail {
			t.Fatalf("Status = %q, want %q — an environment that does not render is "+
				"exactly this check's finding.\nmessage: %s", got.Status, StatusFail, got.Message)
		}
		if !strings.Contains(got.Evidence, "ingress_host") {
			t.Errorf("evidence does not carry the render errors: %s", got.Evidence)
		}
	})

	for name, fn := range contentChecks() {
		t.Run(name+" is undetermined", func(t *testing.T) {
			got := fn(context.Background(), scopeEnv(t, dir, renders...))
			if got.Status != StatusUnknown {
				t.Fatalf("Status = %q, want %q — this check observed no manifest content "+
					"whatsoever; reporting a failure would blame the project for a defect "+
					"it never demonstrated.\nmessage: %s", got.Status, StatusUnknown, got.Message)
			}
			if !strings.Contains(got.Message, "could not render any of 2 environment(s)") {
				t.Errorf("message does not say how many environments went unread: %s", got.Message)
			}
			if !strings.Contains(got.Evidence, "preprod") || !strings.Contains(got.Evidence, "prod") {
				t.Errorf("evidence does not name both unread environments: %s", got.Evidence)
			}
		})
	}
}

// The regression guard. With every environment readable there is nothing
// to degrade, so the fold is a no-op and the report is exactly what it
// was before the contract existed — asserted on the literal strings,
// because "unchanged" that only checks the status would not have caught
// a scope prefix leaking into the happy path.
func TestRenderScope_CompleteRenderIsUnchanged(t *testing.T) {
	dir := scopeProject(t)
	renders := []envRender{
		renderFromJSON(t, "preprod", healthyRenderJSON("preprod")),
		renderFromJSON(t, "prod", healthyRenderJSON("prod")),
	}

	want := map[string]string{
		"Deploy Manifests":  "2 env(s), 4 manifest(s) — all applyable",
		"Deploy Probes":     "2 container(s) probe readiness + liveness",
		"Deploy Resources":  "2 container(s) set cpu+memory requests and limits",
		"Deploy Secrets":    "4 credential env var(s), all sourced from a Secret",
		"Deploy SA Binding": "2 ServiceAccount(s), all bound to a workload",
		"Deploy Migrations": "1 SQL migration(s), applied by all 2 environment(s)",
		"Frontend Code":     "2 env(s): every KCL-declared frontend has code forge can build",
		"Object Collision":  "4 object address(es) across 2 environment(s) — no two environments write the same one",
	}

	checks := contentChecks()
	checks["Deploy Manifests"] = CheckDeployManifests

	for name, fn := range checks {
		t.Run(name, func(t *testing.T) {
			got := fn(context.Background(), scopeEnv(t, dir, renders...))
			if got.Status != StatusPass {
				t.Fatalf("Status = %q, want %q.\nmessage: %s\nevidence: %s",
					got.Status, StatusPass, got.Message, got.Evidence)
			}
			if got.Message != want[name] {
				t.Errorf("message drifted on the happy path:\n got: %s\nwant: %s", got.Message, want[name])
			}
			if got.Evidence != "" {
				t.Errorf("a complete pass carries no evidence, got: %s", got.Evidence)
			}
		})
	}
}

// A project with no environments at all (--kind cli / library) is
// untouched: nothing was unreadable, so nothing degrades, and SKIP —
// "the project's own shape answers the question" — stays the right word.
func TestRenderScope_NoEnvironmentsStillSkips(t *testing.T) {
	dir := scopeProject(t)
	checks := contentChecks()
	checks["Deploy Manifests"] = CheckDeployManifests

	for name, fn := range checks {
		t.Run(name, func(t *testing.T) {
			got := fn(context.Background(), scopeEnv(t, dir))
			if got.Status != StatusSkip {
				t.Fatalf("Status = %q, want %q.\nmessage: %s", got.Status, StatusSkip, got.Message)
			}
		})
	}
}
