package cli

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

const probeHost = "registry.example.test"

func probeEntities(frontends ...FrontendEntity) *KCLEntities {
	return &KCLEntities{
		ControlPlane: &ControlPlaneEntity{Endpoint: "https://cp.example.test", RegistryHost: probeHost},
		Frontends:    frontends,
	}
}

func hostedFE(name, image, sourcemaps string) FrontendEntity {
	return FrontendEntity{Name: name, Image: image, Sourcemaps: sourcemaps, Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}}
}

func probeOpts() buildOptions {
	return buildOptions{buildTarget: "all", pushPlan: pushPlan{push: true, pushBase: probeHost + "/org/proj"}}
}

// The repositories asked about are exactly the ones the in-process pushes
// write on the platform registry: each hosted site's release repository and,
// unless maps are off, its source-map repository.
func TestRegistryPushProbeTargets(t *testing.T) {
	t.Parallel()
	e := probeEntities(
		hostedFE("web", "web", ""),
		hostedFE("docs", "docs", deploytarget.SourcemapsOff),
		hostedFE("ext", "ghcr.io/acme/ext", ""),
	)
	host, got := registryPushProbeTargets(e, probeOpts())
	// ext's site goes to its own registry, which this realm cannot answer
	// for; its maps are still filed in the platform's, so they are asked about.
	want := []string{
		probeHost + "/org/proj/web/static.v1",
		probeHost + "/org/proj/web-sourcemaps",
		probeHost + "/org/proj/docs/static.v1",
		probeHost + "/org/proj/ext-sourcemaps",
	}
	if host != probeHost || !slices.Equal(got, want) {
		t.Errorf("targets = %q %v\nwant %q %v", host, got, probeHost, want)
	}
	noPush := probeOpts()
	noPush.pushPlan.push = false
	if _, got := registryPushProbeTargets(e, noPush); len(got) != 0 {
		t.Errorf("a build that pushes nothing probes %v", got)
	}
	// A local registry takes no credentials and is logged in to by nothing,
	// so there is no credential to ask its realm about.
	for _, local := range []string{"localhost:5051", "127.0.0.1:44513"} {
		le := probeEntities(hostedFE("web", "web", ""))
		le.ControlPlane.RegistryHost = local
		lo := probeOpts()
		lo.pushPlan.pushBase = local + "/org/proj"
		if _, got := registryPushProbeTargets(le, lo); len(got) != 0 {
			t.Errorf("a build pushing to the local registry %s probes %v", local, got)
		}
	}
}

func stubPushProbe(t *testing.T, res deploytarget.PushAccess) *[]string {
	t.Helper()
	var asked []string
	prev := registryPushProbe
	registryPushProbe = func(_ context.Context, host string, repos []string) deploytarget.PushAccess {
		asked = append(asked, repos...)
		return res
	}
	t.Cleanup(func() { registryPushProbe = prev })
	return &asked
}

// Denied refuses before the build, with the push refusal's own remedy; the
// other verdicts proceed, and say which they were.
func TestPreflightRegistryPushAccess(t *testing.T) {
	e := probeEntities(hostedFE("web", "web", ""))
	maps := probeHost + "/org/proj/web-sourcemaps"

	stubPushProbe(t, deploytarget.PushAccess{Verdict: deploytarget.PushAccessDenied, Missing: []string{maps},
		Reason: "the token " + probeHost + " issues for this credential grants no push on " + maps})
	err := preflightRegistryPushAccess(context.Background(), &bytes.Buffer{}, e, probeOpts())
	var coded exitCodeError
	if !errors.As(err, &coded) || coded.code != exitRefused {
		t.Fatalf("denied: err = %v, want exit 4", err)
	}
	for _, want := range []string{maps, "Nothing was built or pushed", "forge registry login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%s", want, err)
		}
	}

	asked := stubPushProbe(t, deploytarget.PushAccess{Verdict: deploytarget.PushAccessGranted})
	var out bytes.Buffer
	if err := preflightRegistryPushAccess(context.Background(), &out, e, probeOpts()); err != nil || !strings.Contains(out.String(), "push access verified") {
		t.Errorf("granted: err %v, out %q", err, out.String())
	}
	if len(*asked) != 2 {
		t.Errorf("asked about %v, want the site and its maps", *asked)
	}

	stubPushProbe(t, deploytarget.PushAccess{Reason: "could not reach " + probeHost})
	out.Reset()
	if err := preflightRegistryPushAccess(context.Background(), &out, e, probeOpts()); err != nil || !strings.Contains(out.String(), "not verified") {
		t.Errorf("unknown: err %v, out %q; want a note and no refusal", err, out.String())
	}
}
