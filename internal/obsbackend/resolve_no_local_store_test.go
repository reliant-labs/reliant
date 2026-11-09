package obsbackend

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/devtelemetry"
)

// 4dfe2b53: a container-free dev stack (reliant's: host processes and
// forge.HostInfra only) whose telemetry `forge env up` left local has no
// store at all. `forge obs status dev` resolved it to the local ClickStack
// anyway, called docker, and told the developer to start a ClickStack that
// `env up` never starts. It must answer at once, without docker: nothing was
// collected, here is how to ship it to the hosted store, here is how to opt
// into a local store.
func TestResolveContainerFreeLocalEnvHasNoStore(t *testing.T) {
	cp := "https://api.reliantapi.com"
	f := Facts{
		Env: "dev", Declared: true, Enabled: true, HasLocal: true, LocalLifecycle: true, NoLocalStore: true, ControlPlane: cp,
		DevTelemetry: &devtelemetry.Record{Env: "dev", Backend: "local", Reason: "dev_telemetry=auto (default): not signed in to Reliant"},
	}
	dec, err := Resolve(f, "")
	if err == nil {
		t.Fatalf("a container-free env with local telemetry resolved to %+v; there is no store to query", dec)
	}
	msg := err.Error()
	for _, want := range []string{
		"telemetry for dev is local (dev_telemetry=auto (default): not signed in to Reliant)",
		"this env has no local store",
		"forge login",
		"forge env up dev",
		"clickstack",
		"observability.local_port",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("answer does not say %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "is not running") {
		t.Errorf("answer still tells the developer to start a ClickStack env up never starts:\n%s", msg)
	}
	if code := err.(*Error).ExitCode(); code != ExitUndetermined {
		t.Errorf("exit code %d, want %d (could not look)", code, ExitUndetermined)
	}

	// No control plane declared: the hosted path needs one first.
	f.ControlPlane = ""
	if _, err := Resolve(f, ""); err == nil || !strings.Contains(err.Error(), "declare a `control_plane = forge.ControlPlane {}`") {
		t.Errorf("without a control_plane the answer must say to declare one: %v", err)
	}
}

// The same env, once `forge env up` shipped its telemetry to the hosted store,
// resolves there; and an env that does declare a store stays local.
func TestResolveNoLocalStoreKeepsTheOtherPaths(t *testing.T) {
	hosted := Facts{
		Env: "dev", Declared: true, Enabled: true, HasLocal: true, LocalLifecycle: true, NoLocalStore: true, ControlPlane: "https://cp.example.com",
		DevTelemetry: &devtelemetry.Record{Env: "dev", Backend: "cloud", OrgID: "o", EnvironmentID: "e", InstanceID: "i"},
	}
	if dec, err := Resolve(hosted, ""); err != nil || dec.Backend != BackendCloud {
		t.Errorf("recorded hosted: got %+v, %v; want the cloud backend", dec, err)
	}
	withStore := Facts{Env: "dev", Declared: true, Enabled: true, HasLocal: true, LocalLifecycle: true}
	if dec, err := Resolve(withStore, ""); err != nil || dec.Backend != BackendLocal {
		t.Errorf("an env with a ClickStack: got %+v, %v; want local", dec, err)
	}
}
