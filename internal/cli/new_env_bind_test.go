package cli

import (
	"strings"
	"testing"
)

// TestEnvNew_ClusterBindFromHostedTemplate pins TL-74: deriving a cluster env
// from a hosted template writes a ClusterTarget of placeholders for `--check`
// to find, renames the env where the file names it, and names the hosted
// parts left behind.
func TestEnvNew_ClusterBindFromHostedTemplate(t *testing.T) {
	template := "\"\"\"\nThe `prod` environment of shop, hosted on Reliant.\n`forge env deploy prod`, `forge secret set --env prod KEY`.\n\"\"\"\n" +
		"_db = forge.ManagedDatabase {\n    name = \"shop\"\n    runtime = forge.OnHosted {}\n}\n" +
		"_cluster = None\n" +
		"_on_cluster = lambda w: fw.Workload -> fw.Workload {\n    envbase.on_cluster(w, appcfg.app_config, _cluster, \"prod\", _r, _n)\n}\n" +
		"_workloads = [\n    _hosted(_api)\n]\n" +
		"_bundle = forge.Bundle {\n    project = \"shop\"\n    control_plane = forge.ControlPlane {}\n    secret_provider = forge.HostedSecrets {}\n}\n"
	got, err := bindEnvContent(t.TempDir(), "k8s", transformEnvFile(template, "prod", "k8s"), []string{"api=cluster"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"The `k8s` environment of shop",
		"`forge env deploy k8s`",
		"--env k8s KEY",
		`envbase.on_cluster(w, appcfg.app_config, _cluster, "k8s", _r, _n)`,
		"_cluster = forge.ClusterTarget {",
		`cluster = "REPLACE_ME_CLUSTER_CONTEXT"`,
		`namespace = "REPLACE_ME_NAMESPACE"`,
		`platform = "REPLACE_ME_PLATFORM"`,
		"_on_cluster(_api)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("derived main.k misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "_cluster = None") || strings.Contains(got, `"prod"`) {
		t.Errorf("derived main.k keeps the template's unset cluster or its env name:\n%s", got)
	}
	if left := hostedLeftovers(got); len(left) != 2 {
		t.Errorf("hostedLeftovers = %v, want the managed database and the hosted secret store", left)
	}
	// A hosted rebind declares no cluster target.
	hosted, err := bindEnvContent(t.TempDir(), "k8s", transformEnvFile(template, "prod", "k8s"), []string{"api=hosted"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hosted, "_cluster = None") || hostedLeftovers(hosted) != nil {
		t.Errorf("a hosted rebind changed the cluster target or reported leftovers:\n%s", hosted)
	}
}
