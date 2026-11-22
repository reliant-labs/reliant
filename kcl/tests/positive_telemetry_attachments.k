"""
Positive test — what each attachment infers. A HostInfra names its ports
server/monitor/ui/metrics and projects temporal_metrics_port; a Manifests
entry takes the group's cluster and namespace (else the primary target's) and
the group's name; an Observability entry with no cluster is a host address;
a Telemetry's alerts join the env's rules labelled with its service; an unset
network_policy is "auto" and renders no key, an explicit one renders as set.
"""

import forge
import forge.workloads as fw

_k3d = forge.ClusterTarget {cluster = "k3d-demo", namespace = "demo-dev"}

_out = forge.render(forge.Bundle {
    project = "demo"
    env = "dev"
    observability = forge.Observability {
        local_port = 4318
        alerts = forge.Alerts {rules = [forge.AlertRule {alert = "Own", expr = "SELECT 1 AS value"}]}
        telemetry = [forge.Telemetry {service_name = "devbox", address = "127.0.0.1", checks = [forge.HealthCheck {port = 22}]}]
    }
    workloads = [fw.Workload {name = "api", image = "docker.io/example/api:1", runtime = forge.OnCluster {target = _k3d}}]
    infra = [forge.HostInfra {
        name = "temporal", engine = "temporal", port = 7233, temporal_ui_port = 8233, temporal_metrics_port = 9233
        telemetry = forge.Telemetry {
            metrics = [forge.MetricsScrape {port = "metrics"}]
            alerts = [forge.AlertRule {alert = "TemporalDown", expr = "SELECT 1 AS value", labels = {team = "core"}}]
        }
    }]
    manifests = [
        forge.Manifests {name = "redis", telemetry = [forge.Telemetry {selector = {app = "redis"}, checks = [forge.HealthCheck {port = 6379}]}]}
        forge.Manifests {name = "obs", namespace = "observability", cluster = "k3d-hub", telemetry = [
            forge.Telemetry {service_name = "ch", selector = {app = "ch"}, network_policy = True, metrics = [forge.MetricsScrape {port = 9363, allow = "ClickHouse.*"}]}
            forge.Telemetry {service_name = "am", namespaces = ["alerting"], selector = {app = "am"}, network_policy = False, metrics = [forge.MetricsScrape {port = 9093}]}
        ]}
    ]
})

_t = {t.service_name: t for t in _out.telemetry}

assert_order = [t.service_name for t in _out.telemetry] == ["temporal", "redis", "ch", "am", "devbox"]
assert_hostinfra_ports = _t.temporal.ports == {"server": 7233, "ui": 8233, "metrics": 9233} and _t.temporal.address == "127.0.0.1" and _t.temporal.runtime == "host"
assert_hostinfra_port_name = _t.temporal.metrics[0].port_name == "metrics" and _t.temporal.metrics[0].port == 0
assert_metrics_port_projected = [i.temporal_metrics_port for i in _out.infra] == [9233]
assert_manifests_primary = _t.redis.cluster == "k3d-demo" and _t.redis.namespaces == ["demo-dev"] and _t.redis.source == "manifests:redis"
assert_manifests_group = _t.ch.cluster == "k3d-hub" and _t.ch.namespaces == ["observability"] and _t.am.namespaces == ["alerting"]
assert_observability_host = _t.devbox.runtime == "host" and _t.devbox.cluster == "" and _t.devbox.attachment == "observability"
assert_alerts_joined = [r.alert for r in _out.observability.alerts.rules] == ["Own", "TemporalDown"]
assert_alert_service_label = _out.observability.alerts.rules[1].labels == {"severity": "warning", "service": "temporal", "team": "core"}
assert_own_alert_unlabelled = "service" not in _out.observability.alerts.rules[0].labels
assert_network_policy_auto = all n in ["temporal", "redis", "devbox"] { "network_policy" not in _t[n] }
assert_network_policy_explicit = _t.ch.network_policy == True and _t.am.network_policy == False
