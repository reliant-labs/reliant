package collector

import "github.com/reliant-labs/forge/pkg/telemetry"

// The fixtures are shaped after real systems so the goldens read like the
// configs they will become. The vendor names and metric names are
// illustrative only: forge knows none of them.

func ptr[T any](v T) *T { return &v }

func ratio(v float64) *float64 { return &v }

// clusterManifests is a raw-manifest system on a cluster: a selector-scoped
// scrape with every metrics knob, a pprof pull, a JSON log parse with a
// demotion rule, and a check.
func clusterManifests() telemetry.Target {
	return telemetry.Target{
		ServiceName: "temporal", Scope: telemetry.ScopeProject,
		Source: "manifests:temporal", Attachment: telemetry.AttachManifests,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-hub",
		Namespaces: []string{"temporal"},
		Selector:   map[string]string{"app.kubernetes.io/name": "temporal", "app.kubernetes.io/component": "frontend"},
		Container:  "temporal",
		Metrics: []telemetry.MetricsScrape{{
			Port: 9090, Allow: `service_requests|service_latency_(bucket|sum|count)|num_goroutines`,
			Deny: `service_latency_bucket`, Counters: `num_goroutines`,
			DropLabels: []string{"workflowType", "namespace_id"}, SampleLimit: 20000,
		}},
		Profiles: []telemetry.PprofScrape{{Port: 7936, Types: []string{"cpu", "heap"}}},
		Logs: &telemetry.LogParse{
			Format:    "json",
			Timestamp: &telemetry.LogTimestamp{Field: "ts", Layout: "epoch_s"},
			Severity:  &telemetry.LogSeverity{Field: "level"},
			Body:      "msg",
			Drop:      []string{"logging-call-at"},
			Rules: []telemetry.LogSeverityRule{
				{BodyMatch: `(?i)workflow (execution )?not found`, FromSeverity: "error", Severity: "warn"},
				{Field: "error", ValueMatch: `^context canceled$`, Severity: "info"},
			},
		},
		Checks: []telemetry.HealthCheck{{Name: "frontend", Kind: "tcp", Port: 7233}},
	}
}

// clusterSecured is an https scrape with a bearer token and a CA, platform
// scoped, plus an authenticated pprof pull.
func clusterSecured() telemetry.Target {
	return telemetry.Target{
		ServiceName: "openbao", Scope: telemetry.ScopePlatform,
		Source: "observability", Attachment: telemetry.AttachObservability,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-hub",
		Namespaces: []string{"openbao"},
		Selector:   map[string]string{"app.kubernetes.io/name": "openbao"},
		Metrics: []telemetry.MetricsScrape{{
			Name: "sys", Port: 8200, Path: "/v1/sys/metrics", Scheme: "https",
			Params:      map[string][]string{"format": {"prometheus"}},
			Allow:       `vault_core_(unsealed|active)|vault_token_count`,
			BearerToken: &telemetry.SecretMount{Secret: "openbao-metrics", Key: "token"},
			TLS:         &telemetry.TLS{CA: &telemetry.SecretMount{Secret: "openbao-ca", Key: "ca.crt"}, ServerName: "openbao.openbao.svc"},
		}},
		Profiles: []telemetry.PprofScrape{{
			Port: 8200, PathPrefix: "/v1/sys/pprof", Types: []string{"heap"}, Scheme: "https",
			BearerToken: &telemetry.SecretMount{Secret: "openbao-metrics", Key: "token"},
			TLS:         &telemetry.TLS{InsecureSkipVerify: true},
		}},
		Logs: &telemetry.LogParse{
			Format:    "json",
			Timestamp: &telemetry.LogTimestamp{Field: "@timestamp", Layout: "rfc3339"},
			Severity:  &telemetry.LogSeverity{Field: "@level"},
			Body:      "@message",
		},
		NetworkPolicy: ptr(true),
	}
}

// clusterAddress is a cluster target reached through a Service address: an
// http check, isolated.
func clusterAddress() telemetry.Target {
	return telemetry.Target{
		ServiceName: "alertmanager", Scope: telemetry.ScopePlatform,
		Source: "observability", Attachment: telemetry.AttachObservability,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-hub",
		Address:  "alertmanager.observability.svc.cluster.local",
		Isolated: true,
		Metrics:  []telemetry.MetricsScrape{{Port: 9093, Allow: `alertmanager_(alerts|notifications_total|notifications_failed_total)`}},
		Checks:   []telemetry.HealthCheck{{Kind: "http", Port: 9093, Path: "/-/healthy"}},
	}
}

// clusterWorkload is an image-only Workload: forge renders its pods, so its
// collector rule joins forge's own policy.
func clusterWorkload() telemetry.Target {
	return telemetry.Target{
		ServiceName: "zitadel", Scope: telemetry.ScopeProject,
		Source: "workload:zitadel", Attachment: telemetry.AttachWorkload,
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-hub",
		Namespaces: []string{"env-dev"},
		Selector:   map[string]string{"app.kubernetes.io/name": "zitadel"},
		Ports:      map[string]int{"http": 8080},
		Metrics:    []telemetry.MetricsScrape{{PortName: "http", Path: "/debug/metrics", Allow: `projection_events_processed_total`}},
		OTLP:       &telemetry.OTLPPush{Signals: []string{"traces"}, SamplerRatio: ratio(0.05)},
	}
}

// hostEngine is a host-run engine under env up: a static scrape, a pprof
// pull, a regex log parse, and a check.
func hostEngine() telemetry.Target {
	return telemetry.Target{
		ServiceName: "nats", Scope: telemetry.ScopeProject,
		Source: "hostinfra:nats", Attachment: telemetry.AttachHostInfra,
		Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Ports:    map[string]int{"server": 4222, "monitor": 8222},
		Metrics:  []telemetry.MetricsScrape{{PortName: "monitor", Path: "/varz", Allow: `.*`, Counters: `in_msgs|out_msgs`}},
		Profiles: []telemetry.PprofScrape{{Port: 6060, Types: []string{"cpu", "goroutine"}, Interval: "10m", CPUSeconds: 10}},
		Logs: &telemetry.LogParse{
			Format:    "regex",
			Regex:     `^\[(?P<pid>\d+)\] (?P<ts>\S+ \S+) \[(?P<lvl>[A-Z]{3})\] (?P<msg>.*)$`,
			Timestamp: &telemetry.LogTimestamp{Field: "ts", Layout: "%Y/%m/%d %H:%M:%S.%f"},
			Severity: &telemetry.LogSeverity{Field: "lvl", Mapping: map[string][]string{
				"debug": {"DBG", "TRC"}, "info": {"INF"}, "warn": {"WRN"}, "error": {"ERR"}, "fatal": {"FTL"},
			}},
			Body: "msg",
			Drop: []string{"pid"},
		},
		Checks: []telemetry.HealthCheck{{Kind: "tcp", PortName: "server"}},
	}
}

// hostRenamed is a host workload whose service_name is not its log file's
// name, with a logfmt parse and nothing pulled.
func hostRenamed() telemetry.Target {
	return telemetry.Target{
		ServiceName: "billing-worker", Scope: telemetry.ScopePlatform,
		Source: "workload:worker", Attachment: telemetry.AttachWorkload,
		Runtime: telemetry.RuntimeHost, Address: "127.0.0.1",
		Logs: &telemetry.LogParse{
			Format:   "logfmt",
			Severity: &telemetry.LogSeverity{Field: "level"},
			Body:     "msg",
			TraceID:  "trace_id",
			SpanID:   "span_id",
		},
	}
}

func clusterTargets() []telemetry.Target {
	return []telemetry.Target{clusterManifests(), clusterSecured(), clusterAddress(), clusterWorkload(), hostEngine()}
}

func hostTargets() []telemetry.Target {
	return []telemetry.Target{hostEngine(), hostRenamed(), clusterManifests()}
}

func clusterOptions() Options {
	return Options{
		Runtime: telemetry.RuntimeCluster, Cluster: "k3d-hub", Discovery: DiscoverNodePods,
		MountRoot:         "/etc/forge/telemetry/secrets",
		ResourceAttrs:     map[string]string{"deployment.environment.name": "dev", "service.namespace": "acme"},
		EnableProfiles:    true,
		Processors:        []string{"memory_limiter"},
		PostProcessors:    []string{"batch"},
		MetricsExporters:  []string{"otlphttp/destination"},
		ProfilesExporters: []string{"otlphttp/destination"},
		Isolated: &IsolatedExporter{
			Name:   "otlphttp/telemetry_isolated",
			Config: map[string]any{"endpoint": "http://gateway.observability.svc:4318", "timeout": "10s"},
		},
	}
}

func hostOptions() Options {
	return Options{
		Runtime:           telemetry.RuntimeHost,
		ResourceAttrs:     map[string]string{"deployment.environment.name": "dev"},
		EnableProfiles:    true,
		Processors:        []string{"memory_limiter"},
		PostProcessors:    []string{"batch"},
		MetricsExporters:  []string{"otlphttp/clickstack"},
		ProfilesExporters: []string{"otlphttp/profiles"},
	}
}
