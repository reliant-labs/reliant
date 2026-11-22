package cli

import (
	"fmt"
	"strings"
	"testing"
)

// `forge env up` allocates an ephemeral API port, PRINTS it, inlines it into the
// frontend bundle as NEXT_PUBLIC_API_URL, and hands it to the readiness gate
// and the pre-flight conflict guard. Only the process itself reads the port
// out of the environment — and host env layering is shell-wins, so an
// inherited PORT used to beat the allocated one. A measured run printed
// "ephemeral dev port 64157", wired the frontend to :64157, and bound :8099.
//
// The frontend launch path already forces its allocated PORT. These tests
// hold the same property for host services.

func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			return strings.TrimPrefix(kv, prefix), true
		}
	}
	return "", false
}

func TestForceHostBindPortsBeatsInheritedPort(t *testing.T) {
	// Not parallel: mutates the process environment, which forceHostBindPorts
	// reads to report what it is overriding.
	t.Setenv("PORT", "8099")

	// What LayerHostEnv produces today: the shell's PORT survives because
	// base wins, so the allocated 64157 never reaches the process.
	layered := []string{"PORT=8099", "DATABASE_URL=postgres://shell"}
	declared := map[string]string{"PORT": "64157"}

	got := forceHostBindPorts(layered, "peptides", declared)

	if v, _ := envValue(got, "PORT"); v != "64157" {
		t.Fatalf("PORT = %q, want the published 64157 — forge prints this port, "+
			"wires the frontend to it and probes it for readiness; the app must bind it", v)
	}
	if n := strings.Count(strings.Join(got, "\n"), "PORT="); n != 1 {
		t.Fatalf("PORT appears %d times in the child env; exactly one entry must survive", n)
	}
}

func TestForceHostBindPortsLeavesNonPortVarsToTheShell(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://shell")

	layered := []string{"DATABASE_URL=postgres://shell", "PORT=64157"}
	declared := map[string]string{"PORT": "64157", "DATABASE_URL": "postgres://kcl"}

	got := forceHostBindPorts(layered, "peptides", declared)

	// Overriding a dev's shell DATABASE_URL is NOT the fix. Shell-wins is the
	// deliberate policy for every host env var that is not a bind port.
	if v, _ := envValue(got, "DATABASE_URL"); v != "postgres://shell" {
		t.Fatalf("DATABASE_URL = %q, want the shell's value — only bind ports are forced", v)
	}
}

func TestForceHostBindPortsCoversServiceSpecificPortVars(t *testing.T) {
	t.Setenv("METRICS_PORT", "9000")

	layered := []string{"METRICS_PORT=9000"}
	declared := map[string]string{"METRICS_PORT": "51234"}

	got := forceHostBindPorts(layered, "peptides", declared)

	// hostEnvPorts treats every `<...>_PORT` var as a port this service binds,
	// and the pre-flight conflict guard reads that same set. A shell value
	// winning on any of them desynchronizes the guard from reality.
	if v, _ := envValue(got, "METRICS_PORT"); v != "51234" {
		t.Fatalf("METRICS_PORT = %q, want the declared 51234", v)
	}
}

func TestForceHostBindPortsIsANoOpWithoutDeclaredPorts(t *testing.T) {
	t.Parallel()

	layered := []string{"DATABASE_URL=postgres://shell"}
	got := forceHostBindPorts(layered, "peptides", map[string]string{"DATABASE_URL": "postgres://kcl"})

	if len(got) != 1 || got[0] != "DATABASE_URL=postgres://shell" {
		t.Fatalf("env changed when no bind port was declared: %v", got)
	}
}

// The three tests above prove the helper. This one proves it is WIRED —
// a decorated value constructed and then not injected is the failure this
// exercise has already found once, so the assertion belongs at the call
// site, not at the construction.
func TestBuildHostServiceCmdBindsThePublishedPort(t *testing.T) {
	t.Setenv("PORT", "8099")

	svc := hostWL("peptides", withListenPorts(64157), withEnv("PORT", "64157"))

	cmd, _, err := buildHostServiceCmd(t.Context(), nil, svc, nil, "dev")
	if err != nil {
		t.Fatalf("buildHostServiceCmd: %v", err)
	}

	got, ok := envValue(cmd.Env, "PORT")
	if !ok {
		t.Fatal("the launched process gets no PORT at all")
	}
	if got != "64157" {
		t.Fatalf("the launched process binds PORT=%s but forge published 64157 — "+
			"the printed URL, the frontend's inlined API URL and the readiness probe all use the published value", got)
	}
}

// A host service that DECLARES zero listen ports binds nothing, and forge must
// neither invent a port for it nor fail it for not binding one.
//
// The distinction is only expressible because ListenPorts is a POINTER: with a
// plain slice, "not declared" and "declared empty" are the same value, so forge
// allocated an ephemeral port and then failed the readiness gate. Observed with
// the packaged desktop app — it launched correctly and was still reported as
// "nothing is listening — the service failed to bind its port".
func TestHostServiceDeclaringNoPortsGetsNone(t *testing.T) {
	desktop := hostWL("reliant-desktop", withListenPorts(), withPorts(8080))

	if got := desktop.HostPorts(); len(got) != 0 {
		t.Fatalf("HostPorts with an explicit empty declaration = %v, want none (spec.ports must not stand in)", got)
	}
	if got := desktop.HostPort(); got != 0 {
		t.Fatalf("HostPort with an explicit empty declaration = %d, want 0", got)
	}

	// And an ephemeral port must not be allocated for it.
	ents := &KCLEntities{Workloads: []WorkloadEntity{desktop}}
	resolveEphemeralHostPorts(ents)
	if got := ents.Workloads[0].Runtime.Host.ListenPorts; got == nil || len(*got) != 0 {
		t.Fatalf("resolveEphemeralHostPorts assigned %v to a service that binds nothing", got)
	}
	if _, set := ents.Workloads[0].HostEnv()["PORT"]; set {
		t.Fatal("resolveEphemeralHostPorts published a PORT for a service that binds nothing")
	}
}

// A host service that declares NO ports at all (neither listen_ports nor
// spec.ports) gets an ephemeral one: forge allocates it, records it as the
// listen port the readiness gate checks, and hands it to the process as
// PORT. There is no env-var inference — a *_PORT value is a dependency
// address as often as a bind port.
func TestHostServiceDeclaringNothingGetsAnEphemeralPort(t *testing.T) {
	ents := &KCLEntities{Workloads: []WorkloadEntity{hostWL("api", withEnv("TEMPORAL_PORT", "7233"))}}
	resolveEphemeralHostPorts(ents)
	w := ents.Workloads[0]
	ports := w.HostPorts()
	if len(ports) != 1 || ports[0] == 7233 {
		t.Fatalf("HostPorts after allocation = %v, want one ephemeral port (not the TEMPORAL_PORT dependency address)", ports)
	}
	if got := w.HostEnv()["PORT"]; got != fmt.Sprint(ports[0]) {
		t.Fatalf("PORT handed to the process = %q, want the allocated %d", got, ports[0])
	}
}
