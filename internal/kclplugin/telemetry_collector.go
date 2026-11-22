package kclplugin

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/reliant-labs/forge/pkg/telemetry"
	"github.com/reliant-labs/forge/pkg/telemetry/collector"
)

// collectorOptions is the options dict kcl_plugin.forge.telemetry_collector
// takes: collector.Options in snake_case, plus the collector a NetworkPolicy
// admits. kcl/telemetry.k's TelemetryCollectorOptions builds it; the keys are
// the contract between the two and are decoded strictly, so a key one side
// renames fails the render instead of being dropped.
type collectorOptions struct {
	Runtime string `json:"runtime"`
	Cluster string `json:"cluster"`
	// Discovery is collector.Discovery; "all" is accepted for DiscoverAll
	// (""), which reads better in KCL than an empty string.
	Discovery         string                     `json:"discovery"`
	NodeNameEnv       string                     `json:"node_name_env"`
	MountRoot         string                     `json:"mount_root"`
	ResourceAttrs     map[string]string          `json:"resource_attrs"`
	EnableProfiles    bool                       `json:"enable_profiles"`
	Processors        []string                   `json:"processors"`
	PostProcessors    []string                   `json:"post_processors"`
	MetricsExporters  []string                   `json:"metrics_exporters"`
	ProfilesExporters []string                   `json:"profiles_exporters"`
	Isolated          *collectorIsolatedExporter `json:"isolated"`
	// Collector, when set, asks for the standalone NetworkPolicies
	// (collector.NetworkPolicies) admitting this collector. Unset renders
	// none: two collectors that split discovery would otherwise each render
	// the same policies.
	Collector *collectorRef `json:"collector"`
}

type collectorIsolatedExporter struct {
	Name            string         `json:"name"`
	Config          map[string]any `json:"config"`
	QueueSize       int            `json:"queue_size"`
	RetryMaxElapsed string         `json:"retry_max_elapsed"`
}

type collectorRef struct {
	Namespace string            `json:"namespace"`
	PodLabels map[string]string `json:"pod_labels"`
}

// collectorResult is what telemetry_collector returns to KCL: the whole
// collector.Fragment as plain data, plus the NetworkPolicies. Lists are never
// null, so KCL can concatenate them without a guard.
type collectorResult struct {
	Extensions            map[string]any   `json:"extensions"`
	Receivers             map[string]any   `json:"receivers"`
	Processors            map[string]any   `json:"processors"`
	Exporters             map[string]any   `json:"exporters"`
	Pipelines             map[string]any   `json:"pipelines"`
	ServiceExtensions     []string         `json:"service_extensions"`
	Logs                  collectorLogs    `json:"logs"`
	NodeMetricsProcessors []string         `json:"node_metrics_processors"`
	K8sAttributesLabels   []any            `json:"k8sattributes_labels"`
	NetworkPolicies       []map[string]any `json:"network_policies"`
	Warnings              []string         `json:"warnings"`
}

type collectorLogs struct {
	Preserve   map[string]any `json:"preserve"`
	Processors []string       `json:"processors"`
}

// telemetryCollector backs kcl_plugin.forge.telemetry_collector(targets,
// options): a project that runs its own OpenTelemetry Collector in KCL
// renders the same configuration forge's agents do, from the same targets,
// through the one translator (collector.Build). targets is the env's
// normalized list (forge.telemetry_targets(bundle), the `output.telemetry` a
// render produces); Build keeps those this collector reaches.
//
// Every failure names what failed — the target index and service for one
// that does not decode, Build's own "telemetry <source> (service <name>)" for
// one it refuses — and reaches KCL as an evaluation error.
func telemetryCollector(rawTargets, rawOpts any) (any, error) {
	targets, err := decodeTargets(rawTargets)
	if err != nil {
		return nil, fmt.Errorf("telemetry_collector: %w", err)
	}
	var o collectorOptions
	if err := strictDecode(rawOpts, &o); err != nil {
		return nil, fmt.Errorf("telemetry_collector: options: %w", err)
	}
	opts, err := o.options()
	if err != nil {
		return nil, fmt.Errorf("telemetry_collector: options: %w", err)
	}
	f, err := collector.Build(targets, opts)
	if err != nil {
		return nil, fmt.Errorf("telemetry_collector: %w", err)
	}
	res := collectorResult{
		Extensions:            f.Extensions,
		Receivers:             f.Receivers,
		Processors:            f.Processors,
		Exporters:             f.Exporters,
		Pipelines:             f.Pipelines,
		ServiceExtensions:     nonNil(f.ServiceExtensions),
		Logs:                  collectorLogs{Preserve: f.Logs.Preserve, Processors: nonNil(f.Logs.Processors)},
		NodeMetricsProcessors: nonNil(f.NodeMetricsProcessors),
		K8sAttributesLabels:   nonNil(f.K8sAttributesLabels),
		NetworkPolicies:       []map[string]any{},
		Warnings:              nonNil(f.Warnings),
	}
	if o.Collector != nil && opts.Runtime == telemetry.RuntimeCluster {
		policies, skipped := collector.NetworkPolicies(clusterTargets(targets, opts.Cluster), collector.CollectorRef{
			Namespace: o.Collector.Namespace,
			PodLabels: o.Collector.PodLabels,
		})
		res.NetworkPolicies = nonNil(policies)
		res.Warnings = append(res.Warnings, skipped...)
	}
	return res, nil
}

func (o collectorOptions) options() (collector.Options, error) {
	discovery := collector.Discovery(o.Discovery)
	if o.Discovery == "all" {
		discovery = collector.DiscoverAll
	}
	opts := collector.Options{
		Runtime:           telemetry.Runtime(o.Runtime),
		Cluster:           o.Cluster,
		Discovery:         discovery,
		NodeNameEnv:       o.NodeNameEnv,
		MountRoot:         o.MountRoot,
		ResourceAttrs:     o.ResourceAttrs,
		EnableProfiles:    o.EnableProfiles,
		Processors:        o.Processors,
		PostProcessors:    o.PostProcessors,
		MetricsExporters:  o.MetricsExporters,
		ProfilesExporters: o.ProfilesExporters,
	}
	if o.Isolated != nil {
		opts.Isolated = &collector.IsolatedExporter{
			Name:            o.Isolated.Name,
			Config:          o.Isolated.Config,
			QueueSize:       o.Isolated.QueueSize,
			RetryMaxElapsed: o.Isolated.RetryMaxElapsed,
		}
	}
	if o.Collector != nil && o.Collector.Namespace == "" {
		return collector.Options{}, fmt.Errorf("collector.namespace is empty: the NetworkPolicies admit the collector by its namespace")
	}
	return opts, nil
}

// decodeTargets reads the targets list one entry at a time, so an entry that
// does not decode is named by its index and, when it has one, its service.
func decodeTargets(raw any) ([]telemetry.Target, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("targets must be a list (forge.telemetry_targets(bundle)), got %T", raw)
	}
	out := make([]telemetry.Target, 0, len(list))
	for i, entry := range list {
		data, err := json.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("targets[%d]: %w", i, err)
		}
		ts, err := telemetry.Decode([]byte("[" + string(data) + "]"))
		if err != nil {
			who := fmt.Sprintf("targets[%d]", i)
			if m, ok := entry.(map[string]any); ok {
				if svc, ok := m["service_name"].(string); ok && svc != "" {
					who += " (service " + svc + ")"
				}
			}
			return nil, fmt.Errorf("%s: %w", who, err)
		}
		out = append(out, ts...)
	}
	return out, nil
}

// strictDecode decodes a KCL value into v, refusing unknown keys. Numbers
// stay json.Number inside free-form maps (an exporter's config), so an
// integer is never rounded through a float64.
func strictDecode(raw, v any) error {
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	return dec.Decode(v)
}

// clusterTargets are the targets a collector on cluster reaches, the same
// selection collector.Build makes, for collector.NetworkPolicies (which
// renders a policy for every cluster target it is given).
func clusterTargets(targets []telemetry.Target, cluster string) []telemetry.Target {
	var out []telemetry.Target
	for _, t := range targets {
		if t.Runtime == telemetry.RuntimeCluster && (cluster == "" || t.Cluster == cluster) {
			out = append(out, t)
		}
	}
	return out
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
