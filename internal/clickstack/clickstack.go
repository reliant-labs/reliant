// Package clickstack names the facts about the local ClickStack compose
// service that `forge env up`, the doctor checks and the integration gate all
// have to agree on, so none of them learns a name from another's source.
package clickstack

const (
	// Service is the compose service (and the workload name in dev/main.k).
	Service = "clickstack"

	// EnvOTLPHTTPPort is the collector's published OTLP/HTTP port. The port
	// variables are declared once in deploy/kcl/<env>/main.k, handed to
	// compose through OnCompose.env, and read back by `forge env up` to build
	// the endpoint host processes export to.
	EnvOTLPHTTPPort = "CLICKSTACK_OTLP_HTTP_PORT"
	// EnvOTLPGRPCPort is the collector's published OTLP/gRPC port.
	EnvOTLPGRPCPort = "CLICKSTACK_OTLP_GRPC_PORT"
	// ProfilesService is the compose service (and workload name) of the profiles
	// collector: contrib with the profiles feature gate, writing to the ClickHouse
	// inside Service. ClickStack's own collector cannot write profiles.
	ProfilesService = "otel-profiles"
	// EnvProfilesOTLPPort is the profiles collector's published OTLP/HTTP port.
	EnvProfilesOTLPPort = "CLICKSTACK_PROFILES_OTLP_PORT"
	// EnvOTLPProfilesEndpoint is the standard per-signal profiles variable. Unlike
	// the base endpoint it is a full URL.
	EnvOTLPProfilesEndpoint = EnvOTLPPrefix + "PROFILES_ENDPOINT"

	// EnvUIPort is the published HyperDX UI port.
	EnvUIPort = "CLICKSTACK_UI_PORT"

	// EnvOTLPPrefix prefixes the standard OTel exporter variables a process
	// is handed.
	EnvOTLPPrefix = "OTEL_EXPORTER_OTLP_"
	// EnvOTLPEndpoint is the standard OTLP base endpoint variable.
	EnvOTLPEndpoint = EnvOTLPPrefix + "ENDPOINT"
	// EnvOTLPProtocol is the standard OTLP protocol variable.
	EnvOTLPProtocol = EnvOTLPPrefix + "PROTOCOL"

	// ProfilesCollectorConfig and ProfilesSchema are the project-relative files
	// the profiles collector and its schema job mount.
	ProfilesCollectorConfig = "deploy/observability/otel-profiles.yaml"
	// ProfilesSchema creates the profile tables, TTLs and materialized views.
	ProfilesSchema = "deploy/observability/profiles-schema.sql"

	// DashboardsDir is the project-relative directory mounted as HyperDX's
	// DASHBOARD_PROVISIONER_DIR. A HyperDX v2 dashboard JSON dropped here is
	// loaded without a restart.
	DashboardsDir = "deploy/observability/dashboards"

	// ForwarderService is the compose service that stands in for ClickStack
	// when the env declares a destination: a plain collector that forwards
	// OTLP and the host logs there instead of storing them.
	ForwarderService = "otel-forwarder"
	// ForwarderConfigDir is the project-relative directory `forge env up`
	// renders the forwarder's pipeline file into (gitignored under .forge).
	ForwarderConfigDir = ".forge/observability"
	// ForwarderConfigFile is that file's name.
	ForwarderConfigFile = "forwarder.yaml"

	// CollectorConfig is the project-relative collector pipeline file.
	CollectorConfig = "deploy/observability/otel-collector.yaml"

	// AlertmanagerService is the compose service (and workload name) of the
	// local Alertmanager, which delivers what the local evaluator fires.
	AlertmanagerService = "alertmanager"
	// EnvAlertmanagerPort is its published loopback port.
	EnvAlertmanagerPort = "ALERTMANAGER_PORT"
	// AlertmanagerDir is the project-relative directory `forge env up`
	// renders the Alertmanager configuration into (gitignored under .forge)
	// and compose mounts at /etc/alertmanager: AlertmanagerConfigFile, plus
	// every receiver secret as a file under secrets/ (amconfig.SecretDir).
	AlertmanagerDir = ".forge/observability/alertmanager"
	// AlertmanagerConfigFile is the configuration's file name in that dir.
	AlertmanagerConfigFile = "alertmanager.yml"
	// AlertRulesFile is the project-relative rules document (pkg/alerting
	// RulesDocument) `forge env up` writes and the local evaluator reads.
	AlertRulesFile = ".forge/observability/alert-rules.json"
	// AlertsInAppTokenFile is the bearer the in-app receiver presents to the
	// evaluator's alert-log sink, a file under AlertmanagerDir/secrets.
	AlertsInAppTokenFile = "inapp-token"
	// AlertsSinkPath is where the evaluator receives the in-app receiver's
	// deliveries.
	AlertsSinkPath = "/alerts"
	// AlertsWorkload is the scaffolded evaluator host workload; its output is
	// .forge/logs/<env>/alerts.log, the local in-app notifications.
	AlertsWorkload = "alerts"
	// AlertOrg is a local stack's one routing org.
	AlertOrg = "local"
)
