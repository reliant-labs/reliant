package deploytarget

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
)

// nthBuildFails is a fakeRunner whose Nth `npm run build` fails.
type nthBuildFails struct {
	*fakeRunner
	failOn, builds int
}

func (r *nthBuildFails) RunInDir(ctx context.Context, dir string, env map[string]string, name string, args ...string) error {
	if err := r.fakeRunner.RunInDir(ctx, dir, env, name, args...); err != nil {
		return err
	}
	if name+" "+strings.Join(args, " ") == "npm run build" {
		r.builds++
		if r.builds == r.failOn {
			return errors.New("npm run build: exit status 1")
		}
	}
	return nil
}

// 2026-10-19, control-plane prod: reliant-web's `npm run build` failed before
// anything was uploaded, and forge reported "PARTLY [firebase] site=reliant-prod:
// reliant-web — failed part-way; some of it may be live". Nothing was live. A
// frontend that fails before its host is written must say nothing shipped
// (cluster.ErrNothingApplied, which the deploy's ship log reads); a failure
// once the host is being written must not.
func TestFirebaseDeploy_FailureBeforeUploadShippedNothing(t *testing.T) {
	newProvider := func(t *testing.T, runner commandRunner) (FirebaseProvider, FirebaseFrontend) {
		projectDir := t.TempDir()
		staging := filepath.Join(t.TempDir(), "public")
		writeFile(t, filepath.Join(projectDir, "admin-web", "out", "index.html"), "<admin/>")
		writeFile(t, filepath.Join(filepath.Dir(projectDir), "reliant-web", "dist", "index.html"), "<spa/>")
		return FirebaseProvider{ProjectDir: projectDir, Runner: runner, StagingRoot: staging}, fakeFirebaseFrontend(projectDir, staging)
	}

	t.Run("build fails", func(t *testing.T) {
		prov, fe := newProvider(t, &nthBuildFails{fakeRunner: &fakeRunner{}, failOn: 1})
		err := prov.Deploy(context.Background(), ServiceGroup{ProviderID: prov.Name(), Frontends: []FirebaseFrontend{fe}})
		if err == nil || !errors.Is(err, cluster.ErrNothingApplied) {
			t.Fatalf("a build failure before any upload must report nothing shipped, got %v", err)
		}
		if !strings.Contains(err.Error(), "npm run build: exit status 1") {
			t.Errorf("the build's own error must survive: %v", err)
		}
	})

	t.Run("firebase deploy fails", func(t *testing.T) {
		prov, fe := newProvider(t, &fakeRunner{runErrs: map[string]error{"firebase deploy": errors.New("exit status 2")}})
		err := prov.Deploy(context.Background(), ServiceGroup{ProviderID: prov.Name(), Frontends: []FirebaseFrontend{fe}})
		if err == nil || errors.Is(err, cluster.ErrNothingApplied) {
			t.Fatalf("a failed `firebase deploy` may have published part of the site; got %v", err)
		}
	})

	t.Run("second frontend's build fails after the first shipped", func(t *testing.T) {
		prov, fe := newProvider(t, &nthBuildFails{fakeRunner: &fakeRunner{}, failOn: 2})
		err := prov.Deploy(context.Background(), ServiceGroup{ProviderID: prov.Name(), Frontends: []FirebaseFrontend{fe, fe}})
		if err == nil || errors.Is(err, cluster.ErrNothingApplied) {
			t.Fatalf("the group shipped its first frontend, so it did not ship nothing; got %v", err)
		}
	})
}

func TestStaticSiteDeploy_FailureBeforeUploadShippedNothing(t *testing.T) {
	prov := StaticSiteProvider{ProjectDir: t.TempDir(), Runner: &nthBuildFails{fakeRunner: &fakeRunner{}, failOn: 1}}
	err := prov.Deploy(context.Background(), ServiceGroup{ProviderID: prov.Name(), StaticSites: []StaticSiteFrontend{fakeStaticSiteFrontend()}})
	if err == nil || !errors.Is(err, cluster.ErrNothingApplied) {
		t.Fatalf("a build failure before any upload must report nothing shipped, got %v", err)
	}
}
