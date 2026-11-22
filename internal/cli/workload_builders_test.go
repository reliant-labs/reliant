package cli

import (
	"encoding/json"
	"fmt"

	deployv1alpha1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// Test builders for the workload entity model. Most entity-struct fixtures
// need only a name, a runtime and a little spec; these keep them one line.

// hostWL is a long-running host workload (service) with a go-run GoBuild.
func hostWL(name string, opts ...func(*WorkloadEntity)) WorkloadEntity {
	w := WorkloadEntity{
		Name: name, Kind: "service", Image: name,
		Build:   BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/" + name, OutputName: name}},
		Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{Runner: "go-run"}},
		Spec:    deployv1alpha1.WorkloadSpec{Kind: deployv1alpha1.KindService},
	}
	for _, o := range opts {
		o(&w)
	}
	return w
}

// clusterWL is a cluster-bound service on (cluster, namespace).
func clusterWL(name, clusterCtx, namespace string, opts ...func(*WorkloadEntity)) WorkloadEntity {
	w := WorkloadEntity{
		Name: name, Kind: "service", Image: name,
		Build:   BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/" + name, OutputName: name}},
		Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: clusterCtx, Namespace: namespace}},
		Spec:    deployv1alpha1.WorkloadSpec{Kind: deployv1alpha1.KindService, Image: "reg/" + name + ":dev"},
	}
	for _, o := range opts {
		o(&w)
	}
	return w
}

// hostedWL is a workload bound to the control plane.
func hostedWL(name string, opts ...func(*WorkloadEntity)) WorkloadEntity {
	w := WorkloadEntity{
		Name: name, Kind: "service",
		Runtime: RuntimeEntity{Type: RuntimeHosted},
		Spec: deployv1alpha1.WorkloadSpec{
			Kind: deployv1alpha1.KindService, Image: "ghcr.io/acme/" + name + ":v1",
			Ports:  []deployv1alpha1.Port{{Name: "http", Port: 8080, Expose: true}},
			Probes: &deployv1alpha1.Probes{},
		},
	}
	for _, o := range opts {
		o(&w)
	}
	return w
}

// composeWL is a compose-bound workload (no build).
func composeWL(name, file string) WorkloadEntity {
	return WorkloadEntity{
		Name: name, Kind: "service",
		Runtime: RuntimeEntity{Type: RuntimeCompose, Compose: &ComposeRuntime{Service: name, File: file}},
	}
}

// withEnv sets literal env values on the spec.
func withEnv(kv ...string) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		for i := 0; i+1 < len(kv); i += 2 {
			w.Spec.Env = append(w.Spec.Env, deployv1alpha1.EnvVar{Name: kv[i], Value: kv[i+1]})
		}
	}
}

// withSecretRef adds a secretRef env var.
func withSecretRef(name, secret, key string) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		w.Spec.Env = append(w.Spec.Env, deployv1alpha1.EnvVar{Name: name, SecretRef: &deployv1alpha1.SecretKeyRef{Name: secret, Key: key}})
	}
}

// withListenPorts declares the host runtime's listen ports (nil args ⇒ the
// explicit "binds nothing").
func withListenPorts(ports ...int) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		p := append([]int{}, ports...)
		w.Runtime.Host.ListenPorts = &p
	}
}

// withPorts declares spec.ports (named http first).
func withPorts(ports ...int32) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		for i, p := range ports {
			name := "http"
			if i > 0 {
				name = fmt.Sprintf("p%d", p)
			}
			w.Spec.Ports = append(w.Spec.Ports, deployv1alpha1.Port{Name: name, Port: p})
		}
	}
}

// asJob makes the workload a job gating `before`.
func asJob(args []string, before ...string) func(*WorkloadEntity) {
	return func(w *WorkloadEntity) {
		w.Kind = "job"
		w.Spec.Kind = deployv1alpha1.KindJob
		w.Spec.Args = args
		w.Spec.Before = before
	}
}

// contractJSON serialises an entity set to the §9.1 render contract
// (`{"output": {...}}`), so a test that stubs RenderKCL through
// FORGE_KCL_RENDER_FIXTURE feeds the production decoder the same shape a
// real `forge.render(bundle)` emits.
func contractJSON(t interface{ Fatalf(string, ...any) }, e *KCLEntities) []byte {
	type raw struct {
		Name    string                      `json:"name"`
		Kind    string                      `json:"kind"`
		Image   string                      `json:"image"`
		Build   any                         `json:"build"`
		Runtime any                         `json:"runtime"`
		Spec    deployv1alpha1.WorkloadSpec `json:"spec"`
	}
	base, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal entities: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(base, &out); err != nil {
		t.Fatalf("remarshal entities: %v", err)
	}
	var ws []raw
	for _, w := range e.Workloads {
		r := raw{Name: w.Name, Kind: w.Kind, Image: w.Image, Spec: w.Spec}
		switch w.Build.Type {
		case "go":
			r.Build = mergeType("go", w.Build.Go)
		case "docker":
			r.Build = mergeType("docker", w.Build.Docker)
		case "shell":
			r.Build = mergeType("shell", w.Build.Shell)
		case "remote":
			r.Build = mergeType("remote", w.Build.Remote)
		}
		switch w.Runtime.Type {
		case RuntimeHost:
			r.Runtime = mergeType(RuntimeHost, w.Runtime.Host)
		case RuntimeCompose:
			r.Runtime = mergeType(RuntimeCompose, w.Runtime.Compose)
		case RuntimeCluster:
			r.Runtime = mergeType(RuntimeCluster, w.Runtime.Cluster)
		case RuntimeBuildOnly:
			r.Runtime = mergeType(RuntimeBuildOnly, w.Runtime.BuildOnly)
		default:
			r.Runtime = map[string]any{"type": w.Runtime.Type}
		}
		if r.Spec.Kind == "" {
			r.Spec.Kind = deployv1alpha1.WorkloadKind(w.Kind)
		}
		ws = append(ws, r)
	}
	out["workloads"] = ws
	b, err := json.Marshal(map[string]any{"output": out})
	if err != nil {
		t.Fatalf("marshal contract: %v", err)
	}
	return b
}

func mergeType(typ string, v any) map[string]any {
	m := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	m["type"] = typ
	return m
}
