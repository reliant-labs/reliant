package kclrender_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/kclrender"
)

// The `kubeconfig` binding is consumed FROM KCL — `option("kubeconfig")`,
// behind forge.default_kubeconfig() and Cluster.kubeconfig — so the contract
// that matters is what a MODULE sees, not what the Go helper returns.
//
// This is not a redundant second layer over kubeconfig_darg_test.go. Those
// tests pin what withKubeconfigDArg computes; deleting the CALL to it from Run
// left every one of them green while no module could see the binding at all.
// The same shape of gap is what made a declared api_port silently inert for
// config-file clusters. So this renders real KCL through the seam every forge
// command takes.

// TestKubeconfigOptionReachesTheModule evaluates a module that reads
// option("kubeconfig") and asserts it receives the machine's resolved default
// kubeconfig path.
func TestKubeconfigOptionReachesTheModule(t *testing.T) {
	want := filepath.Join(t.TempDir(), "kube", "config")
	t.Setenv("KUBECONFIG", want)

	dir := t.TempDir()
	writeKubeconfigProbe(t, dir)

	out, err := kclrender.Run(dir, dir, []string{"env=dev"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var parsed struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse render output: %v\n%s", err, out)
	}
	if parsed.Kubeconfig != want {
		t.Fatalf("module saw kubeconfig = %q; want %q.\n"+
			"Nothing bound the option, so `<cluster>.kubeconfig` would render empty and a "+
			"host process would be told its kubeconfig is at \"\".", parsed.Kubeconfig, want)
	}
}

// writeKubeconfigProbe lays down a minimal module that surfaces the binding
// the way forge's own base.k accessor does.
func writeKubeconfigProbe(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"kcl.mod": "[package]\nname = \"kubeconfig_probe\"\n",
		"main.k":  "kubeconfig = option(\"kubeconfig\") or \"\"\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}
