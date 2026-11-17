package cli

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 4dfe2b53: the obs target of a container-free env (reliant's dev, minimized:
// host processes, no ClickStack, no collector port) says it has no local
// store, so `forge obs` answers without calling docker. An env that declares
// the scaffold's ClickStack, or a collector port, keeps the local backend.
func TestObsTargetMarksAContainerFreeEnvAsHavingNoLocalStore(t *testing.T) {
	host := WorkloadEntity{Name: "reliant-api-server", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}}
	containerFree := &KCLEntities{
		Lifecycle:     lifecycleLocal,
		ControlPlane:  &ControlPlaneEntity{Type: "control_plane", Endpoint: "https://api.reliantapi.com"},
		Observability: &ObservabilityEntity{Enabled: true, DevTelemetry: devTelemetryAuto},
		Workloads:     []WorkloadEntity{host},
	}
	if f := obsTargetFromEntities("dev", containerFree).Facts; !f.NoLocalStore || !f.HasLocal {
		t.Errorf("container-free env: facts %+v, want HasLocal and NoLocalStore", f)
	}

	withClickstack := *containerFree
	withClickstack.Workloads = []WorkloadEntity{host, {Name: "clickstack", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: "clickstack"}}}}
	if obsTargetFromEntities("dev", &withClickstack).Facts.NoLocalStore {
		t.Error("an env declaring the ClickStack compose service has a local store")
	}
	withPort := *containerFree
	withPort.Observability = &ObservabilityEntity{Enabled: true, LocalPort: 4318}
	if obsTargetFromEntities("dev", &withPort).Facts.NoLocalStore {
		t.Error("an env declaring a collector port has a local store")
	}
}

// The docker engine probe in front of every local ClickStack query is bounded:
// a docker CLI whose engine never answers made `forge obs status` hang.
func TestDockerEngineProbeIsBounded(t *testing.T) {
	prevTimeout, prevProbe := dockerProbeTimeout, dockerDaemonProbeFn
	t.Cleanup(func() { dockerProbeTimeout, dockerDaemonProbeFn = prevTimeout, prevProbe })
	dockerProbeTimeout = 50 * time.Millisecond
	bounded := false
	dockerDaemonProbeFn = func(ctx context.Context) (string, error) {
		_, bounded = ctx.Deadline()
		<-ctx.Done() // an engine that never answers: only the deadline ends this
		return "", ctx.Err()
	}

	err := probeDockerEngine(context.Background())
	if !bounded {
		t.Error("the probe's context carries no deadline")
	}
	if err == nil || !strings.Contains(err.Error(), "did not answer within") {
		t.Errorf("probe error = %v, want one naming the engine's silence", err)
	}
}
