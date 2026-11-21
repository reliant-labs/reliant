// File: resource_metrics.go — the process and host resource metrics
// (pkg/resmetrics) Setup starts in every binary, and the switch that turns them
// off.
//
// The precedence is the profiler's (profiles_config.go): the environment
// (FORGE_OBSERVE_RESOURCE_METRICS, FORGE_OBSERVE_RESOURCE_INTERVAL, read in
// otelenv.go), then Config, then the defaults. An invalid value is ignored with
// a warning, so a typo in a deploy override never stops the service.

package observe

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"

	"github.com/reliant-labs/forge/pkg/resmetrics"
)

// ResourceMetricsMode selects the process and host resource metrics.
type ResourceMetricsMode string

const (
	// ResourceMetricsAuto is the default: the process group, and the host
	// group, which inside a Kubernetes pod reports only the container's cgroup
	// (the node is the node agent's to report).
	ResourceMetricsAuto ResourceMetricsMode = "auto"
	// ResourceMetricsAll is both groups with the whole machine as the host,
	// even inside a pod.
	ResourceMetricsAll ResourceMetricsMode = "all"
	// ResourceMetricsProcess is the process group only.
	ResourceMetricsProcess ResourceMetricsMode = "process"
	// ResourceMetricsHost is the host group only, narrowed in a pod as under
	// auto.
	ResourceMetricsHost ResourceMetricsMode = "host"
	// ResourceMetricsOff starts no sampler at all.
	ResourceMetricsOff ResourceMetricsMode = "off"
)

var resourceMetricsModes = []ResourceMetricsMode{ResourceMetricsAuto, ResourceMetricsAll, ResourceMetricsProcess, ResourceMetricsHost, ResourceMetricsOff}

// resourceMeterName is the instrumentation scope the resource instruments are
// registered under.
const resourceMeterName = "github.com/reliant-labs/forge/pkg/resmetrics"

// resourceSettings is Config resolved against the environment: whether a
// sampler starts, and with what options.
type resourceSettings struct {
	mode ResourceMetricsMode
	opts resmetrics.Options
}

// resolveResourceMetrics applies, most specific first: the environment, then
// cfg, then the defaults.
func resolveResourceMetrics(cfg Config) (resourceSettings, []error) {
	var warns []error
	envMode, envInterval, inPod := resourceMetricsEnv()

	mode := ResourceMetricsAuto
	if cfg.ResourceMetrics != "" {
		if m, ok := parseResourceMetricsMode(string(cfg.ResourceMetrics)); ok {
			mode = m
		} else {
			warns = append(warns, fmt.Errorf("observe.Config.ResourceMetrics %q is not one of %s; using %s", cfg.ResourceMetrics, joinResourceModes(), mode))
		}
	}
	if envMode != "" {
		if m, ok := parseResourceMetricsMode(envMode); ok {
			mode = m
		} else {
			warns = append(warns, fmt.Errorf("%s=%q: want one of %s; ignoring it", EnvResourceMetrics, envMode, joinResourceModes()))
		}
	}

	interval := resmetrics.DefaultInterval
	switch {
	case cfg.ResourceMetricsInterval < 0:
		warns = append(warns, fmt.Errorf("observe.Config.ResourceMetricsInterval %s is negative; using %s", cfg.ResourceMetricsInterval, interval))
	case cfg.ResourceMetricsInterval > 0 && cfg.ResourceMetricsInterval < resmetrics.MinInterval:
		warns = append(warns, fmt.Errorf("observe.Config.ResourceMetricsInterval %s is below %s; using %s", cfg.ResourceMetricsInterval, resmetrics.MinInterval, resmetrics.MinInterval))
		interval = resmetrics.MinInterval
	case cfg.ResourceMetricsInterval > 0:
		interval = cfg.ResourceMetricsInterval
	}
	if envInterval != "" {
		if d, err := time.ParseDuration(envInterval); err != nil || d < resmetrics.MinInterval {
			warns = append(warns, fmt.Errorf("%s=%q: want a Go duration of at least %s; ignoring it", EnvResourceInterval, envInterval, resmetrics.MinInterval))
		} else {
			interval = d
		}
	}

	scope := resmetrics.ScopeHost
	if inPod {
		scope = resmetrics.ScopeCgroup
	}
	s := resourceSettings{mode: mode, opts: resmetrics.Options{Interval: interval, Scope: scope}}
	switch mode {
	case ResourceMetricsAll:
		s.opts.Groups, s.opts.Scope = resmetrics.GroupAll, resmetrics.ScopeHost
	case ResourceMetricsProcess:
		s.opts.Groups = resmetrics.GroupProcess
	case ResourceMetricsHost:
		s.opts.Groups = resmetrics.GroupHost
	default:
		s.opts.Groups = resmetrics.GroupAll
	}
	return s, warns
}

func parseResourceMetricsMode(v string) (ResourceMetricsMode, bool) {
	m := ResourceMetricsMode(strings.ToLower(strings.TrimSpace(v)))
	return m, slices.Contains(resourceMetricsModes, m)
}

func joinResourceModes() string {
	names := make([]string, len(resourceMetricsModes))
	for i, m := range resourceMetricsModes {
		names[i] = string(m)
	}
	return strings.Join(names, ", ")
}

// startResourceMetrics starts the sampler and registers its instruments on mp.
// Best effort by design, like the profiler: a sampler that cannot start is a
// warning, never a reason for Setup to fail.
func startResourceMetrics(ctx context.Context, cfg Config, mp metric.MeterProvider) {
	settings, warns := resolveResourceMetrics(cfg)
	for _, w := range warns {
		slog.WarnContext(ctx, "resource metrics: invalid setting", "error", w)
	}
	if settings.mode == ResourceMetricsOff {
		return
	}
	s, err := resmetrics.Start(ctx, settings.opts)
	if err != nil {
		slog.WarnContext(ctx, "resource metrics are off", "error", err)
		return
	}
	if _, err := resmetrics.Register(mp.Meter(resourceMeterName), s); err != nil {
		slog.WarnContext(ctx, "resource metrics are off", "error", err)
	}
}
