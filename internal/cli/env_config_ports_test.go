package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Ranger friction #6: `env config dev` printed `PORT 8080` and a reader
// took it for the api's port, while the api was on 8087. The 8080 belonged to
// workloads that bind nothing (the migrate job, a worker), which are handed the
// project's PORT default. Each long-running host workload's heading now says
// which port it listens on, and --json carries it as listen_ports (additive);
// a job, a worker without listen ports and an off-host workload say nothing
// about ports.
func TestEnvConfigNamesEachHostProcessListenPort(t *testing.T) {
	e := &KCLEntities{Workloads: []WorkloadEntity{
		{Name: "api", Kind: "service", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{ListenPorts: &[]int{8087}}}},
		{Name: "migrate", Kind: "job", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}},
		{Name: "alerts", Kind: "worker", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{ListenPorts: &[]int{}}}},
		{Name: "pods", Kind: "service", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}}},
	}}
	rows := map[string]envConfigWorkload{}
	for _, w := range collectEnvConfigWorkloads(e) {
		rows[w.Name] = w
	}
	want := map[string]string{
		"api":     "api (service) — on host, listens on localhost:8087",
		"migrate": "migrate (job) — on host",
		"alerts":  "alerts (worker) — on host",
		"pods":    "pods (service) — on cluster",
	}
	for name, heading := range want {
		if got := envConfigHeading(rows[name]); got != heading {
			t.Errorf("heading = %q, want %q", got, heading)
		}
	}
	if got := envConfigHeading(envConfigWorkload{Name: "web", Kind: "frontend"}); got != "web (frontend)" {
		t.Errorf("frontend heading = %q", got)
	}

	doc, err := json.Marshal(rows["api"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `"listen_ports":[8087]`) || !strings.Contains(string(doc), `"runtime":"host"`) {
		t.Errorf("--json lacks the api's runtime and listen ports: %s", doc)
	}
	doc, _ = json.Marshal(rows["migrate"])
	if strings.Contains(string(doc), "listen_ports") {
		t.Errorf("a job binds no port, yet --json lists some: %s", doc)
	}

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	report := envConfigReport{Env: "dev", Workloads: []envConfigWorkload{rows["api"]}}
	report.Workloads[0].Env = map[string]string{"PORT": "8087"}
	if err := renderEnvConfigTable(cmd, report, false); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "api (service) — on host, listens on localhost:8087\n") {
		t.Errorf("table heading:\n%s", out.String())
	}
}
