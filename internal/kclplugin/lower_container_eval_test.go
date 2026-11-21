package kclplugin_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// kcl_plugin.forge.lower_container hands a raw pod the SAME container forge's
// own sidecar lowering produces, so a hand-written Deployment never carries a
// second copy of the probe defaults. The fixture is a native, startup-probed
// sidecar (the Cloud SQL proxy shape): its lowering must carry
// restartPolicy Always and forge's startup/readiness/liveness timings.
const lowerFixture = `import forge.workloads as fw
import kcl_plugin.forge as fp

_proxy = fw.Container {
    name = "cloud-sql-proxy"
    image = "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1"
    args = ["--port=5432", "inst"]
    native = True
    probes = fw.Probes {
        port = 9801
        startupPath = "/startup"
        readinessPath = "/readiness"
        livenessPath = "/liveness"
    }
}
plain = fp.lower_container(fw.Container {name = "plain", image = "x/y:1"})
native = fp.lower_container(_proxy)
`

func TestLowerContainerMatchesForgeLowering(t *testing.T) {
	dir := t.TempDir()
	forgeMod := os.Getenv("FORGE_KCL_MOD")
	_ = forgeMod
	mod := "[package]\nname = \"lower\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"
	for name, body := range map[string]string{"kcl.mod": mod, "main.k": lowerFixture} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := kclrender.Run(dir, dir, nil)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var got struct {
		Plain, Native map[string]any
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if got.Native["restartPolicy"] != "Always" {
		t.Errorf("native sidecar lost restartPolicy: %v", got.Native)
	}
	if _, ok := got.Plain["restartPolicy"]; ok {
		t.Errorf("plain container must not carry restartPolicy: %v", got.Plain)
	}
	startup, _ := got.Native["startupProbe"].(map[string]any)
	if startup["periodSeconds"] != float64(1) || startup["failureThreshold"] != float64(360) || startup["timeoutSeconds"] != float64(3) {
		t.Errorf("startupProbe timings are not forge's defaults: %v", startup)
	}
	if hg, _ := startup["httpGet"].(map[string]any); hg["path"] != "/startup" || hg["port"] != float64(9801) {
		t.Errorf("startupProbe handler wrong: %v", startup)
	}
	live, _ := got.Native["livenessProbe"].(map[string]any)
	if live["initialDelaySeconds"] != float64(30) || live["periodSeconds"] != float64(10) {
		t.Errorf("livenessProbe timings must be forge's derived ones (30s/10s): %v", live)
	}
	ready, _ := got.Native["readinessProbe"].(map[string]any)
	if ready["periodSeconds"] != float64(5) || ready["failureThreshold"] != float64(3) {
		t.Errorf("readinessProbe timings wrong: %v", ready)
	}
}
