package cli

import "testing"

// The predicate itself, over the shapes it must tell apart.
func TestEnvRunsOnlyOnThisMachine(t *testing.T) {
	host := WorkloadEntity{Name: "api", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}}
	compose := WorkloadEntity{Name: "clickstack", Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{}}}
	cluster := WorkloadEntity{Name: "op", Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{}}}
	hosted := WorkloadEntity{Name: "web", Runtime: RuntimeEntity{Type: RuntimeHosted, Hosted: &HostedRuntime{}}}
	for _, tc := range []struct {
		name string
		e    *KCLEntities
		want bool
	}{
		{"local host+compose", &KCLEntities{Lifecycle: lifecycleLocal, Workloads: []WorkloadEntity{host, compose}}, true},
		{"ephemeral host", &KCLEntities{Lifecycle: lifecycleEphemeral, Workloads: []WorkloadEntity{host}}, true},
		{"local with a host frontend", &KCLEntities{Lifecycle: lifecycleLocal, Workloads: []WorkloadEntity{host}, Frontends: []FrontendEntity{{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeHost}}}}, true},
		{"no lifecycle", &KCLEntities{Workloads: []WorkloadEntity{host}}, false},
		{"local with a cluster workload", &KCLEntities{Lifecycle: lifecycleLocal, Workloads: []WorkloadEntity{host, cluster}}, false},
		{"local with a hosted workload", &KCLEntities{Lifecycle: lifecycleLocal, Workloads: []WorkloadEntity{host, hosted}}, false},
		{"local with a hosted frontend", &KCLEntities{Lifecycle: lifecycleLocal, Workloads: []WorkloadEntity{host}, Frontends: []FrontendEntity{{Name: "web", Runtime: FrontendRuntime{Type: FrontendRuntimeHosted}}}}, false},
		{"local with a managed database", &KCLEntities{Lifecycle: lifecycleLocal, Workloads: []WorkloadEntity{host}, Databases: []DatabaseEntity{{Name: "db", Runtime: RuntimeHosted}}}, false},
		{"nil", nil, false},
	} {
		if got := envRunsOnlyOnThisMachine(tc.e); got != tc.want {
			t.Errorf("%s: envRunsOnlyOnThisMachine = %v, want %v", tc.name, got, tc.want)
		}
	}
}
