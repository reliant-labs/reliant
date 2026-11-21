package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/reliant-labs/forge/pkg/telemetry"
)

// validateEnvTelemetry checks the env's declared telemetry (forge.Telemetry,
// decoded into entities.Telemetry) for what its KCL schema cannot: RE2
// syntax, a service_name used twice, a port name the workload does not
// declare, a cluster the env does not declare, and the hosted rules. Nil when
// the env declares none.
// checkEnvTelemetry validates the env's declared telemetry and, when it is
// valid, prints its notes (envTelemetryWarnings) on errOut.
func checkEnvTelemetry(errOut io.Writer, envName string, entities *KCLEntities) error {
	if err := validateEnvTelemetry(envName, entities); err != nil {
		return err
	}
	for _, w := range envTelemetryWarnings(entities) {
		fmt.Fprintln(errOut, w.Error())
	}
	return nil
}

// envTelemetryWarnings are telemetry.Warnings for the env's declared telemetry.
func envTelemetryWarnings(entities *KCLEntities) []telemetry.Problem {
	if entities == nil {
		return nil
	}
	return telemetry.Warnings(entities.Telemetry)
}

func validateEnvTelemetry(envName string, entities *KCLEntities) error {
	if entities == nil || len(entities.Telemetry) == 0 {
		return nil
	}
	return telemetry.Validate(entities.Telemetry, telemetry.Options{Env: envName, Clusters: envKubeContexts(entities)})
}

// envKubeContexts is every kubectl context the env's declaration names: its
// clusters, its primary target, its cluster-bound workloads, its manifest
// groups and the clusters it connects to. Never nil, so an env that declares
// no cluster refuses a target that names one.
func envKubeContexts(e *KCLEntities) []string {
	set := map[string]bool{}
	add := func(c string) {
		if c != "" {
			set[c] = true
		}
	}
	for _, c := range e.Clusters {
		add(c.Context)
		add(c.Name)
	}
	if e.ClusterTarget != nil {
		add(e.ClusterTarget.Cluster)
	}
	for _, w := range e.Workloads {
		if w.Runtime.Cluster != nil {
			add(w.Runtime.Cluster.Cluster)
		}
	}
	for _, g := range e.ManifestOwners {
		add(g.Cluster)
	}
	for _, m := range e.ManifestClusters {
		add(m.Cluster)
	}
	for name, ctx := range e.ConnectedClusters {
		add(name)
		add(ctx)
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// envRenderJSONSchema names the document `forge env render --json` prints.
const envRenderJSONSchema = "forge.env-render/v1"

// envRenderDocument is `forge env render --json`: the objects the --list
// view prints, and the env's declared telemetry, normalized (the
// pkg/telemetry.Target contract every collector consumes). Fields are added,
// never renamed.
type envRenderDocument struct {
	Schema    string               `json:"schema"`
	Project   string               `json:"project"`
	Env       string               `json:"env"`
	Namespace string               `json:"namespace,omitempty"`
	Clusters  []string             `json:"clusters"`
	Objects   []envRenderDocObject `json:"objects"`
	// Telemetry is [] when the env declares none.
	Telemetry []telemetry.Target `json:"telemetry"`
}

// envRenderDocObject is one rendered object, as the --list row shows it.
type envRenderDocObject struct {
	Clusters  []string `json:"clusters"`
	Kind      string   `json:"kind"`
	Namespace string   `json:"namespace,omitempty"`
	Name      string   `json:"name"`
	App       string   `json:"app,omitempty"`
	// Chart names the forge.HelmChart the object was templated from.
	Chart string `json:"chart,omitempty"`
}

func writeRenderedJSON(w io.Writer, project, envName, namespace string, clusters []string, objects []renderedObject, entities *KCLEntities) error {
	doc := envRenderDocument{
		Schema:    envRenderJSONSchema,
		Project:   project,
		Env:       envName,
		Namespace: namespace,
		Clusters:  append([]string{}, clusters...),
		Objects:   make([]envRenderDocObject, 0, len(objects)),
		Telemetry: []telemetry.Target{},
	}
	for _, o := range objects {
		doc.Objects = append(doc.Objects, envRenderDocObject{
			Clusters: append([]string{}, o.Clusters...), Kind: o.Kind, Namespace: o.Namespace, Name: o.Name, App: o.App, Chart: o.Chart,
		})
	}
	if entities != nil && entities.Telemetry != nil {
		doc.Telemetry = entities.Telemetry
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
