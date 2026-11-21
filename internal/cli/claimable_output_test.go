package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/release"
)

// useClaimableForOutput makes cloud.ClaimableActive true for the test, as it is
// for the length of an anonymous deploy.
func useClaimableForOutput(t *testing.T) {
	t.Helper()
	restore := cloud.UseClaimableCredential(cloud.Endpoint{URL: "https://cp.example.com"}, "rlat_test", nil)
	t.Cleanup(restore)
}

func TestDescribeAdoption(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		before  map[string]string
		changed []string
		want    []string
		notWant []string
	}{
		{
			name:    "new files, a new directory once, and an appended .gitignore",
			before:  map[string]string{".gitignore": "1/1", "package.json": "2/2"},
			changed: []string{".gitignore", "AGENTS.md", "deploy/kcl/dev/main.k", "deploy/kcl/prod/main.k", "forge.yaml"},
			want:    []string{"added AGENTS.md, deploy/, forge.yaml", "appended forge's entries to .gitignore", "untouched"},
			notWant: []string{"deploy/kcl", "added .gitignore"},
		},
		{
			name:    "no .gitignore before: it is created, not appended to",
			before:  map[string]string{"package.json": "2/2"},
			changed: []string{".gitignore", "AGENTS.md", "forge.yaml"},
			want:    []string{"added .gitignore, AGENTS.md, forge.yaml"},
			notWant: []string{"appended"},
		},
		{
			name:    "an existing file the adoption rewrote is named as changed",
			before:  map[string]string{"AGENTS.md": "1/1"},
			changed: []string{"AGENTS.md", "forge.yaml"},
			want:    []string{"added forge.yaml", "changed AGENTS.md"},
		},
		{
			name:    "nothing written",
			before:  map[string]string{},
			changed: nil,
			want:    []string{"nothing needed writing"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := describeAdoption(tc.before, tc.changed)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("describeAdoption = %q, want it to contain %q", got, w)
				}
			}
			for _, nw := range tc.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("describeAdoption = %q, must not contain %q", got, nw)
				}
			}
		})
	}
}

// A first deploy has no earlier state, so "no recorded config to compare
// against" and "live drift is not observable" are true of every environment
// and say nothing. They are dropped there, and only there.
func TestRenderDeployPlan_FirstDeployHidesNothingToCompareWarnings(t *testing.T) {
	t.Parallel()
	unknown := func(section string) release.PlanFinding {
		return release.PlanFinding{Code: release.FindingUnknown, Class: release.ClassWarn, Section: section, Subject: section, Detail: "nothing to compare"}
	}
	added := release.PlanFinding{Code: release.FindingObjectAdded, Class: release.ClassInfo, Section: release.SectionObjects, Subject: "hosted/StaticSite//web"}
	stateful := release.PlanFinding{Code: release.FindingStatefulDeletion, Class: release.ClassStop, Section: release.SectionStateful, Subject: "c/PersistentVolumeClaim/ns/data"}

	render := func(basis release.PlanBasis, fs ...release.PlanFinding) string {
		var b strings.Builder
		renderDeployPlan(&b, "prod", release.Plan{LiveBasis: basis, Findings: fs, Digest: "sha256:d"})
		return b.String()
	}

	first := render(release.PlanBasis{}, unknown(release.SectionObjects), added, unknown(release.SectionDrift))
	for _, bad := range []string{"[warn]", "no recorded config", "drift (changed outside forge)", "computed against: no applied bundle"} {
		if strings.Contains(first, bad) {
			t.Errorf("a first deploy's plan says %q:\n%s", bad, first)
		}
	}
	if !strings.Contains(first, "hosted/StaticSite//web") {
		t.Errorf("a first deploy's plan lost its object finding:\n%s", first)
	}

	// Only the two nothing-to-compare findings go: a stop finding on a first
	// deploy is still shown.
	if got := render(release.PlanBasis{}, unknown(release.SectionObjects), stateful); !strings.Contains(got, "DESTRUCTIVE") || !strings.Contains(got, "PersistentVolumeClaim") {
		t.Errorf("a first deploy's stop finding was hidden:\n%s", got)
	}

	// An env with a recorded bundle is not a first deploy: the same warnings
	// are real there and stay.
	later := render(release.PlanBasis{CurrentPromotionID: "pr_1", AppliedBundleID: "bnd_1"}, unknown(release.SectionDrift))
	if !strings.Contains(later, "[warn] drift") || !strings.Contains(later, "drift (changed outside forge)") {
		t.Errorf("a later deploy lost its drift warning:\n%s", later)
	}
	observed := render(release.PlanBasis{DriftObserved: true}, release.PlanFinding{Code: release.FindingDrift, Class: release.ClassWarn, Section: release.SectionDrift, Subject: "c/Deployment/ns/web"})
	if !strings.Contains(observed, "c/Deployment/ns/web") {
		t.Errorf("observed drift was hidden:\n%s", observed)
	}
}

// The "rollback floors are not supported ... upgrade the control plane" line
// is advice an anonymous user cannot act on; it is hidden for a claimable
// deploy and still shown to everyone else.
func TestRenderFloorVerdict_UnsupportedIsHiddenForClaimable(t *testing.T) {
	v := &promoteFloorVerdict{Verdict: "unsupported", Note: (&floorsUnsupportedError{location: "https://cp"}).Error()}
	var normal strings.Builder
	renderFloorVerdict(&normal, v)
	if !strings.Contains(normal.String(), "upgrade the control plane") {
		t.Fatalf("a signed-in user lost the floor line:\n%s", normal.String())
	}

	useClaimableForOutput(t)
	var claimable strings.Builder
	renderFloorVerdict(&claimable, v)
	if claimable.Len() != 0 {
		t.Errorf("a claimable deploy is told %q", claimable.String())
	}
	// A real floor verdict is not an unsupported one: it still renders.
	var admitted strings.Builder
	renderFloorVerdict(&admitted, &promoteFloorVerdict{Verdict: "admitted"})
	if !strings.Contains(admitted.String(), "admitted") {
		t.Errorf("an admitted floor verdict is hidden:\n%s", admitted.String())
	}
}

func TestControlPlanePredatesBundlesNote_NoUpgradeAdviceWhenAnonymous(t *testing.T) {
	if got := controlPlanePredatesBundlesNote(); !strings.Contains(got, "upgrade") {
		t.Fatalf("a signed-in user lost the remedy: %q", got)
	}
	useClaimableForOutput(t)
	if got := controlPlanePredatesBundlesNote(); strings.Contains(got, "upgrade") {
		t.Errorf("an anonymous user is told to upgrade: %q", got)
	}
	var out strings.Builder
	planUnavailable(errControlPlanePredatesBundles, "prod", &out)
	if strings.Contains(strings.ToLower(out.String()), "upgrad") {
		t.Errorf("the plan fallback tells an anonymous user to upgrade: %q", out.String())
	}
}

// A first promote has no earlier release: its plan drops the lines that only
// explain that, and an anonymous deploy's post-write plan is the shipped list.
func TestPromotePlanText_FirstPromoteAndClaimable(t *testing.T) {
	plan := promotePlan{
		Env: "prod", Target: promotePlanTarget{Release: "v1"}, Direction: promoteDirectionInitial,
		Expected: "unbound", Commits: promoteCommitRange{State: promoteRangeFirstPromote, Detail: "no commit range — first promote"},
		pending: planApproved,
	}
	plan.Current.Note = "never promoted — no release is bound to prod, so this is its first promote"
	var b strings.Builder
	renderPromotePlanText(&b, plan)
	got := b.String()
	for _, bad := range []string{"compare-and-set", "never promoted — no release is bound", "Commits", "no commit range"} {
		if strings.Contains(got, bad) {
			t.Errorf("a first promote's plan says %q:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "FIRST PROMOTE") && !strings.Contains(got, "INITIAL") {
		t.Errorf("a first promote's plan lost its direction:\n%s", got)
	}

	// A promote that is not the first keeps the lines.
	plan.Direction, plan.Current.Bound, plan.Commits.State = promoteDirectionAhead, true, promoteRangeComputed
	b.Reset()
	renderPromotePlanText(&b, plan)
	if !strings.Contains(b.String(), "compare-and-set") || !strings.Contains(b.String(), "Commits") {
		t.Errorf("a later promote lost its compare-and-set or Commits lines:\n%s", b.String())
	}

	// After the write, an anonymous deploy prints only what shipped.
	useClaimableForOutput(t)
	done := promotePlan{Env: "prod", Target: promotePlanTarget{Release: "v1"}, Applied: true, followed: true, Shipped: []string{"[hosted] published: web"}}
	b.Reset()
	renderPromotePlanText(&b, done)
	if !strings.Contains(b.String(), "Promoted env") || !strings.Contains(b.String(), "shipped   [hosted] published: web") {
		t.Errorf("the post-write plan lost the headline or what shipped:\n%s", b.String())
	}
	for _, bad := range []string{"Capacity", "Images", "Binding:", "Verify:", "forge env status"} {
		if strings.Contains(b.String(), bad) {
			t.Errorf("an anonymous deploy's post-write plan repeats %q:\n%s", bad, b.String())
		}
	}
}

// A successful `docker login` prints "Login Succeeded" and a plaintext-
// credentials warning; neither is the user's to see. A failed one shows what
// docker said.
func TestDockerLogin_OutputOnlyOnFailure(t *testing.T) {
	requirePOSIXFake(t, "docker")
	bin := t.TempDir()
	script := "#!/bin/sh\ncat >/dev/null\necho 'Login Succeeded'\necho \"WARNING! Your credentials are stored unencrypted in '$DOCKER_CONFIG/config.json'.\"\n" +
		"if [ \"$FAKE_DOCKER_FAIL\" = 1 ]; then echo 'Error response from daemon: unauthorized' >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	stdout, stderr := captureStdoutAndStderr(t, func() {
		if err := dockerLogin(context.Background(), dockerLoginArgs("registry.example.com", "forge"), strings.NewReader("tok")); err != nil {
			t.Errorf("a successful login failed: %v", err)
		}
	})
	if stdout != "" || stderr != "" {
		t.Errorf("a successful login printed output:\nstdout: %q\nstderr: %q", stdout, stderr)
	}

	t.Setenv("FAKE_DOCKER_FAIL", "1")
	var err error
	stdout, stderr = captureStdoutAndStderr(t, func() {
		err = dockerLogin(context.Background(), dockerLoginArgs("registry.example.com", "forge"), strings.NewReader("tok"))
	})
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Errorf("a failed login must carry docker's own words, got %v", err)
	}
	if strings.Contains(stdout+stderr, "unauthorized") {
		t.Errorf("the failure is reported twice (once raw, once in the error):\nstdout: %q\nstderr: %q", stdout, stderr)
	}
}

// An anonymous deploy's registry login never touches the user's ~/.docker.
func TestIsolateDockerConfig(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, ".docker")
	for _, d := range []string{"contexts", "cli-plugins", "buildx"} {
		if err := os.MkdirAll(filepath.Join(real, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// State forge does not know: a credential helper's cache and Docker
	// Desktop's own files. None of it may be reachable from the throwaway.
	unknown := []string{".token_seed", "desktop", ".ecr"}
	for _, f := range unknown {
		if err := os.WriteFile(filepath.Join(real, f), []byte("secret-state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	orig := `{"auths":{"ghcr.io":{"auth":"c2VjcmV0"}},"credsStore":"desktop","currentContext":"colima","aliases":{"x":"y"}}`
	if err := os.WriteFile(filepath.Join(real, "config.json"), []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("DOCKER_CONFIG", "")
	os.Unsetenv("DOCKER_CONFIG")

	restore, err := isolateDockerConfig(false, "registry.example.com/org")
	if err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" || dir == real {
		t.Fatalf("DOCKER_CONFIG = %q, want a throwaway directory", dir)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"auths", "credsStore", "c2VjcmV0", "ghcr.io"} {
		if bytes.Contains(raw, []byte(leaked)) {
			t.Errorf("the throwaway config carries %q: %s", leaked, raw)
		}
	}
	if !bytes.Contains(raw, []byte("colima")) {
		t.Errorf("the throwaway config lost the user's docker context: %s", raw)
	}
	// The links need symlink support: Windows without Developer Mode or the
	// privilege refuses them, and isolation then goes on without them.
	if probe := filepath.Join(t.TempDir(), "link"); os.Symlink(t.TempDir(), probe) != nil {
		t.Log("this host cannot create symlinks: the linked-entry assertions are skipped")
	} else {
		for _, d := range []string{"contexts", "cli-plugins", "buildx"} {
			if _, err := os.Stat(filepath.Join(dir, d)); err != nil {
				t.Errorf("the user's docker %s are not reachable: %v", d, err)
			}
		}
	}
	for _, f := range unknown {
		if _, err := os.Lstat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("the throwaway config links %s, state forge does not know (err=%v)", f, err)
		}
	}

	// A login in the throwaway directory writes there and nowhere else.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{"registry.example.com":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	restore()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the throwaway config survived the deploy: %v", err)
	}
	if v, ok := os.LookupEnv("DOCKER_CONFIG"); ok && v != "" {
		t.Errorf("DOCKER_CONFIG = %q after restore, want it unset", v)
	}
	after, _ := os.ReadFile(filepath.Join(real, "config.json"))
	if string(after) != orig {
		t.Errorf("the user's docker config changed:\n%s", after)
	}

	// A deploy that builds an image keeps the user's registry credentials, so
	// a private base image still pulls.
	restore, err = isolateDockerConfig(true, "registry.example.com/org")
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	kept, err := os.ReadFile(filepath.Join(os.Getenv("DOCKER_CONFIG"), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(kept, []byte("ghcr.io")) || !bytes.Contains(kept, []byte("desktop")) {
		t.Errorf("a build deploy's throwaway config lost the user's credentials: %s", kept)
	}
	// The platform login still never reaches the user's credsStore: an empty
	// credHelpers entry pins that one host to the throwaway config file.
	var keptCfg struct {
		CredHelpers map[string]string `json:"credHelpers"`
	}
	if err := json.Unmarshal(kept, &keptCfg); err != nil {
		t.Fatal(err)
	}
	if h, ok := keptCfg.CredHelpers["registry.example.com"]; !ok || h != "" {
		t.Errorf("credHelpers[registry.example.com] = %q (present %v), want \"\" so the login stays in the file: %s", h, ok, kept)
	}
	for _, f := range unknown {
		if _, err := os.Lstat(filepath.Join(os.Getenv("DOCKER_CONFIG"), f)); !os.IsNotExist(err) {
			t.Errorf("a build deploy's throwaway config links %s (err=%v)", f, err)
		}
	}
}

// No throwaway config, no deploy: when isolation cannot be set up the error
// reaches the caller, and DOCKER_CONFIG is left exactly as it was rather than
// silently pointing the login at the user's real config.
func TestIsolateDockerConfigFailsClosed(t *testing.T) {
	real := t.TempDir()
	// A config.json that cannot be parsed: the throwaway cannot be built from it.
	if err := os.WriteFile(filepath.Join(real, "config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", real)

	restore, err := isolateDockerConfig(false, "registry.example.com")
	if err == nil {
		restore()
		t.Fatal("isolateDockerConfig set up a config from an unreadable one; want an error")
	}
	if got := os.Getenv("DOCKER_CONFIG"); got != real {
		t.Errorf("DOCKER_CONFIG = %q after a failed isolation, want it unchanged (%q)", got, real)
	}

	// With no user config at all (no DOCKER_CONFIG, no home), the throwaway
	// starts empty: isolation still holds.
	t.Setenv("DOCKER_CONFIG", "")
	os.Unsetenv("DOCKER_CONFIG")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "") // os.UserHomeDir on Windows
	restore, err = isolateDockerConfig(true, "registry.example.com")
	if err != nil {
		t.Fatalf("isolation with no user config: %v", err)
	}
	defer restore()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("DOCKER_CONFIG"), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"registry.example.com":""`)) {
		t.Errorf("the empty throwaway does not pin the platform login to its file: %s", raw)
	}
}
