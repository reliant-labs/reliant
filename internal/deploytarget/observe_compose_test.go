package deploytarget

import (
	"context"
	"strings"
	"testing"
)

// composeGroup is one compose service, shaped as the deploy dispatch
// builds it.
func composeGroup() ServiceGroup {
	return ServiceGroup{
		Env: "dev",
		Services: []ResolvedService{
			{Name: "api", Compose: &ComposeSpec{ComposeFile: "docker-compose.yml", Service: "api"}},
		},
	}
}

// observeCompose runs the provider against canned `compose ps` output.
func observeCompose(t *testing.T, psOutput string) (ObservedItem, *fakeRunner) {
	t.Helper()
	runner := &fakeRunner{outputs: map[string]string{"docker": psOutput}}
	obs, err := ComposeProvider{Runner: runner}.Observe(context.Background(), composeGroup())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Items) != 1 {
		t.Fatalf("got %d items, want 1", len(obs.Items))
	}
	return obs.Items[0], runner
}

func TestComposeObserve_RunningContainerIsHealthy(t *testing.T) {
	item, runner := observeCompose(t,
		`[{"Name":"proj-api-1","State":"running","Health":"","Image":"ghcr.io/x/api:v1","ExitCode":0}]`)

	if item.Health != HealthHealthy {
		t.Errorf("health = %v (detail %q), want healthy", item.Health, item.Detail)
	}
	// A mutable tag yields no digest, exactly as it does for the cluster
	// tier. Reporting "ghcr.io/x/api:v1" in a field called Digest would
	// invite a caller to compare it against a release ledger's digests.
	if item.Digest != "" {
		t.Errorf("digest = %q, want empty for a tag-pinned image", item.Digest)
	}
	// Replicas stays nil: compose has no rollout, so the four-field
	// ReplicaCounts vocabulary does not apply.
	if item.Replicas != nil {
		t.Errorf("replicas = %+v, want nil — compose has no replica/rollout concept", item.Replicas)
	}
	// --all is load-bearing: without it an exited container is invisible
	// and reads as "never created".
	if len(runner.calls) != 1 || !strings.Contains(runner.calls[0], "--all") {
		t.Errorf("compose ps call %v does not pass --all; a crashed container would read as absent",
			runner.calls)
	}
	if !strings.Contains(runner.calls[0], "--format json") {
		t.Errorf("compose ps call %v does not request json", runner.calls)
	}
}

// TestComposeObserve_DigestPinnedImageIsReported is the control on the
// assertion above: "digest is empty" must be a property of TAGS, not a
// provider that never populates the field.
func TestComposeObserve_DigestPinnedImageIsReported(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	item, _ := observeCompose(t,
		`[{"Name":"proj-api-1","State":"running","Image":"ghcr.io/x/api@`+digest+`"}]`)
	if item.Digest != digest {
		t.Errorf("digest = %q, want %q — a digest-pinned image DOES yield an identity, and a "+
			"provider that never set the field would pass the tag test for the wrong reason",
			item.Digest, digest)
	}
}

// TestComposeObserve_ExitedContainerIsAbsentNotHealthy is the state a
// state-file read gets wrong. forge's DeployState says this service
// deployed successfully; the container has since died.
func TestComposeObserve_ExitedContainerIsAbsentNotHealthy(t *testing.T) {
	item, _ := observeCompose(t,
		`[{"Name":"proj-api-1","State":"exited","Image":"ghcr.io/x/api:v1","ExitCode":137}]`)
	if item.Health != HealthAbsent {
		t.Fatalf("health = %v, want absent", item.Health)
	}
	if !strings.Contains(item.Detail, "137") {
		t.Errorf("detail = %q, want the exit code — it is what distinguishes 'stopped' from 'crashed'",
			item.Detail)
	}
}

// TestComposeObserve_UnhealthyContainerIsNotHealthy is the assertion that
// makes reading the Health field load-bearing. The container's State is
// "running", so a provider that looked only at State reports this green —
// while the service's OWN healthcheck says it is not serving.
func TestComposeObserve_UnhealthyContainerIsNotHealthy(t *testing.T) {
	item, _ := observeCompose(t,
		`[{"Name":"proj-api-1","State":"running","Health":"unhealthy","Image":"ghcr.io/x/api:v1"}]`)
	if item.Health != HealthDegraded {
		t.Fatalf("health = %v, want degraded — State is \"running\", so a provider that read only "+
			"State would report this healthy while the service's own probe says otherwise", item.Health)
	}
	if !strings.Contains(item.Detail, "unhealthy") {
		t.Errorf("detail = %q, want it to name the healthcheck verdict", item.Detail)
	}
}

// TestComposeObserve_NoHealthcheckIsNotPenalized is the other half: an
// EMPTY Health field is the common case (most compose services declare no
// healthcheck) and absent evidence must not read as ill health.
func TestComposeObserve_NoHealthcheckIsNotPenalized(t *testing.T) {
	item, _ := observeCompose(t,
		`[{"Name":"proj-api-1","State":"running","Health":"","Image":"x:v1"}]`)
	if item.Health != HealthHealthy {
		t.Errorf("health = %v, want healthy — a service that declares no healthcheck reports an "+
			"empty Health, and treating that as a failure would downgrade most compose projects",
			item.Health)
	}
}

func TestComposeObserve_NoContainerIsAbsent(t *testing.T) {
	item, _ := observeCompose(t, "")
	if item.Health != HealthAbsent {
		t.Errorf("health = %v, want absent", item.Health)
	}
	if strings.TrimSpace(item.Detail) == "" {
		t.Error("absent with no detail")
	}
}

// TestComposeObserve_NDJSONIsParsed covers the shape docker compose
// v2.21+ emits: newline-delimited objects rather than an array. Both are
// accepted because pinning a minimum compose version would be a gap the
// user cannot close from their side.
func TestComposeObserve_NDJSONIsParsed(t *testing.T) {
	item, _ := observeCompose(t,
		"{\"Name\":\"proj-api-1\",\"State\":\"running\",\"Image\":\"x:v1\"}\n"+
			"{\"Name\":\"proj-api-2\",\"State\":\"exited\",\"Image\":\"x:v1\",\"ExitCode\":1}\n")
	if item.Health != HealthDegraded {
		t.Fatalf("health = %v, want degraded (1 of 2 running) — if the NDJSON parse silently "+
			"produced zero containers this would read absent", item.Health)
	}
	if !strings.Contains(item.Detail, "1/2") {
		t.Errorf("detail = %q, want it to report 1/2 running", item.Detail)
	}
}

// TestComposeObserve_UnreadableDaemonIsUnknownNotAbsent keeps the
// distinction the cluster tier makes: "I looked and nothing is there" and
// "I could not look" are opposite instructions to a caller (create vs.
// investigate), and absent is the more dangerous of the two to guess.
func TestComposeObserve_UnreadableDaemonIsUnknownNotAbsent(t *testing.T) {
	runner := &fakeRunner{
		runErrs: map[string]error{"docker": errKubectlExit1},
		outputs: map[string]string{"docker": "Cannot connect to the Docker daemon"},
	}
	obs, err := ComposeProvider{Runner: runner}.Observe(context.Background(), composeGroup())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Items[0].Health != HealthUnknown {
		t.Errorf("health = %v, want unknown — a dead docker daemon is not evidence that the "+
			"service is absent", obs.Items[0].Health)
	}
}

// TestComposeObserve_UsesTheDeclaredEnvOverlay pins the one thing that is
// easy to get wrong and invisible when wrong: `compose ps` re-reads and
// re-interpolates the compose file exactly like `compose up`, so without
// the same overlay it resolves a DIFFERENT project than the one deployed
// and reports on containers that are not the ones in question.
func TestComposeObserve_UsesTheDeclaredEnvOverlay(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{"docker": `[]`}}
	group := ServiceGroup{
		Env: "dev",
		Services: []ResolvedService{{Name: "api", Compose: &ComposeSpec{
			ComposeFile: "docker-compose.yml",
			Env:         map[string]string{"IDP_PORT": "8099"},
		}}},
	}
	if _, err := (ComposeProvider{Runner: runner}).Observe(context.Background(), group); err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(runner.envCalls) != 1 {
		t.Fatalf("want 1 recorded call, got %d", len(runner.envCalls))
	}
	if got := runner.envCalls[0]["IDP_PORT"]; got != "8099" {
		t.Errorf("compose ps ran with IDP_PORT=%q, want \"8099\" — without the declared env the "+
			"ps resolves a different config than the up did", got)
	}
}

// TestComposeObserve_MisroutedGroupIsUnknown covers the nil spec: a
// misrouted group must produce an unknown ITEM, not a panic and not a
// silently shorter report.
func TestComposeObserve_MisroutedGroupIsUnknown(t *testing.T) {
	obs, err := ComposeProvider{Runner: &fakeRunner{}}.Observe(context.Background(), ServiceGroup{
		Env:      "dev",
		Services: []ResolvedService{{Name: "api"}},
	})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if len(obs.Items) != 1 || obs.Items[0].Health != HealthUnknown {
		t.Errorf("got %+v, want one unknown item", obs.Items)
	}
}
