package templates

// Guards for the generated ClickStack compose service, derived from the parsed
// compose graph. Each one is a failure the first ClickStack attempt shipped.

import (
	"path"
	"regexp"
	"slices"
	"strings"
	"testing"

	yaml "gopkg.in/yaml.v3"
)

type clickstackCompose struct {
	Services map[string]struct {
		Image       string            `yaml:"image"`
		Init        bool              `yaml:"init"`
		Ports       []string          `yaml:"ports"`
		Environment map[string]string `yaml:"environment"`
		Volumes     []string          `yaml:"volumes"`
		Entrypoint  []string          `yaml:"entrypoint"`
		Healthcheck struct {
			Test []string `yaml:"test"`
		} `yaml:"healthcheck"`
		Networks  yaml.Node `yaml:"networks"`
		Command   []string  `yaml:"command"`
		DependsOn yaml.Node `yaml:"depends_on"`
	} `yaml:"services"`
	Volumes map[string]any `yaml:"volumes"`
}

func renderClickstackCompose(t *testing.T) (clickstackCompose, string) {
	t.Helper()
	out, err := ProjectTemplates().Render("docker-compose.yml.tmpl", struct {
		ProjectName string
		HasFrontend bool
	}{ProjectName: "shopdemo"})
	if err != nil {
		t.Fatalf("render docker-compose.yml.tmpl: %v", err)
	}
	var cf clickstackCompose
	if err := yaml.Unmarshal(out, &cf); err != nil {
		t.Fatalf("rendered compose is not valid YAML: %v\n%s", err, out)
	}
	if _, ok := cf.Services["clickstack"]; !ok {
		t.Fatalf("no clickstack service in the rendered compose (the assertions below would be vacuous): %v", cf.Services)
	}
	return cf, string(out)
}

func TestClickstackComposeIsOneLoopbackOnlyService(t *testing.T) {
	cf, raw := renderClickstackCompose(t)
	svc := cf.Services["clickstack"]

	// Every published port is bound to loopback; ClickHouse is not published.
	if len(svc.Ports) != 3 {
		t.Fatalf("clickstack publishes %v, want exactly the UI and the two OTLP ports", svc.Ports)
	}
	for _, p := range svc.Ports {
		if !strings.HasPrefix(p, "127.0.0.1:") {
			t.Errorf("port %q is not bound to 127.0.0.1", p)
		}
		for _, db := range []string{":8123", ":9000", ":27017"} {
			if strings.HasSuffix(p, db) {
				t.Errorf("port %q publishes a database", p)
			}
		}
	}
	for name, s := range cf.Services {
		for _, p := range s.Ports {
			if strings.HasPrefix(p, "0:") || strings.HasPrefix(p, "0.0.0.0") {
				if name == "clickstack" {
					t.Errorf("clickstack publishes on all interfaces: %q", p)
				}
			}
		}
	}

	// Non-default credentials; the passwordless default user is not the one in use.
	if svc.Environment["CLICKHOUSE_USER"] == "" || svc.Environment["CLICKHOUSE_USER"] == "default" {
		t.Errorf("CLICKHOUSE_USER = %q, want a non-default user", svc.Environment["CLICKHOUSE_USER"])
	}
	if svc.Environment["CLICKHOUSE_PASSWORD"] == "" {
		t.Error("ClickHouse has no password")
	}

	// A healthcheck that proves ingest, not just the UI.
	hc := strings.Join(svc.Healthcheck.Test, " ")
	for _, want := range []string{"/api/health", "4318/v1/traces"} {
		if !strings.Contains(hc, want) {
			t.Errorf("healthcheck %q does not probe %s", hc, want)
		}
	}

	// Persistent state, a dashboard provisioner dir, and the file log mount.
	mounts := strings.Join(svc.Volumes, "\n")
	for _, want := range []string{
		"clickstack_clickhouse:/var/lib/clickhouse",
		"clickstack_mongo:/data/db",
		"./deploy/observability/dashboards:/etc/forge/dashboards:ro",
		"./deploy/observability/otel-collector.yaml:",
		"./.forge/logs:/var/log/forge:ro",
	} {
		if !strings.Contains(mounts, want) {
			t.Errorf("clickstack does not mount %s; mounts:\n%s", want, mounts)
		}
	}
	for _, v := range []string{"clickstack_clickhouse", "clickstack_mongo"} {
		if _, ok := cf.Volumes[v]; !ok {
			t.Errorf("top-level volume %s is not declared", v)
		}
	}
	if svc.Environment["DASHBOARD_PROVISIONER_DIR"] != "/etc/forge/dashboards" {
		t.Errorf("DASHBOARD_PROVISIONER_DIR = %q", svc.Environment["DASHBOARD_PROVISIONER_DIR"])
	}

	// Compose services reach the collector by its network name.
	var nets map[string]struct {
		Aliases []string `yaml:"aliases"`
	}
	if err := svc.Networks.Decode(&nets); err != nil {
		t.Fatalf("clickstack networks must use the map form so it can carry an alias: %v", err)
	}
	aliased := false
	for _, n := range nets {
		for _, a := range n.Aliases {
			aliased = aliased || a == "otel-collector"
		}
	}
	if !aliased {
		t.Error("clickstack has no `otel-collector` network alias; compose services cannot reach it")
	}
	if got := cf.Services["app"].Environment["OTEL_EXPORTER_OTLP_ENDPOINT"]; got != "http://otel-collector:4318" {
		t.Errorf("the compose app exports to %q, want http://otel-collector:4318", got)
	}

	// Pinned by digest, and the LOCAL-mode image: the separate collector image
	// only opens OTLP after a UI registration and then demands a key.
	if !regexp.MustCompile(`^mirror\.gcr\.io/clickhouse/clickstack-local:\d+\.\d+\.\d+@sha256:[0-9a-f]{64}$`).MatchString(svc.Image) {
		t.Errorf("image %q is not the clickstack-local image pinned by version and digest", svc.Image)
	}
	for _, banned := range []string{"clickstack-otel-collector", "OPAMP_SERVER_URL", "grafana/", "otel-lgtm", "image: mongo", "pyroscope", "alloy", "INGESTION_API_KEY"} {
		if strings.Contains(raw, banned) {
			t.Errorf("the compose file mentions %q: the single-container local mode has none of that", banned)
		}
	}
}

// The collector pipeline file is what makes logs flow: it must read the files
// forge writes and parse the fields the runtime emits.
func TestClickstackCollectorConfigTailsForgeLogs(t *testing.T) {
	out, err := ProjectTemplates().Render("otel-collector.yaml.tmpl", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Receivers map[string]struct {
			Include   []string `yaml:"include"`
			Exclude   []string `yaml:"exclude"`
			Operators []struct {
				Type string `yaml:"type"`
			} `yaml:"operators"`
		} `yaml:"receivers"`
		Service struct {
			Pipelines map[string]struct {
				Receivers  []string `yaml:"receivers"`
				Exporters  []string `yaml:"exporters"`
				Processors []string `yaml:"processors"`
			} `yaml:"pipelines"`
		} `yaml:"service"`
	}
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("collector config is not YAML: %v\n%s", err, out)
	}
	fl, ok := cfg.Receivers["filelog/forge"]
	if !ok || len(fl.Include) != 1 || fl.Include[0] != "/var/log/forge/*/*.log" {
		t.Fatalf("filelog/forge = %+v, want it to include /var/log/forge/*/*.log", fl)
	}
	// The browser console mirror (frontend_<name>.log) is shipped by the HyperDX
	// SDK; tailing it too would store every console line twice, under two
	// service names.
	if !slices.Contains(fl.Exclude, "/var/log/forge/*/frontend_*.log") {
		t.Errorf("filelog/forge exclude = %v, want it to exclude the frontend console mirror /var/log/forge/*/frontend_*.log", fl.Exclude)
	}
	for _, f := range []string{"/var/log/forge/dev/frontend_web.log", "/var/log/forge/dev/frontend_admin.log"} {
		if !excluded(fl.Exclude, f) {
			t.Errorf("%s is tailed: the browser console would ship twice", f)
		}
	}
	for _, f := range []string{"/var/log/forge/dev/api.log", "/var/log/forge/dev/frontendish.log"} {
		if excluded(fl.Exclude, f) {
			t.Errorf("%s is excluded: a backend log must still ship", f)
		}
	}
	var types []string
	for _, op := range fl.Operators {
		types = append(types, op.Type)
	}
	for _, want := range []string{"regex_parser", "json_parser", "trace_parser"} {
		if !strings.Contains(strings.Join(types, " "), want) {
			t.Errorf("filelog operators %v lack %s", types, want)
		}
	}
	p, ok := cfg.Service.Pipelines["logs/forge"]
	if !ok || len(p.Receivers) != 1 || p.Receivers[0] != "filelog/forge" || len(p.Exporters) != 1 || p.Exporters[0] != "clickhouse" {
		t.Errorf("logs/forge pipeline = %+v, want filelog/forge -> clickhouse", p)
	}
	// Exactly one local log path: the runtime must not also export logs over OTLP.
	for name, pl := range cfg.Service.Pipelines {
		for _, r := range pl.Receivers {
			if strings.HasPrefix(r, "otlp") {
				t.Errorf("pipeline %s also receives logs over OTLP; one shipping path only", name)
			}
		}
	}
}

// excluded reports whether any exclude glob matches path (collector globs use
// doublestar; for this fixed-depth pattern path.Match agrees).
func excluded(globs []string, p string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, p); ok {
			return true
		}
	}
	return false
}

// Profiles take a second, stock-contrib collector because ClickStack's own cannot
// write them. These pin the pieces that make it work, each of which fails silently
// otherwise: no feature gate means a 404 the app only logs at DEBUG; an unpinned
// image means the profiles wire format moves under the producer; a collector that
// starts before the schema job writes to tables the exporter would not create.
func TestComposeRunsAProfilesCollectorAndItsSchemaJob(t *testing.T) {
	cf, raw := renderClickstackCompose(t)
	prof, ok := cf.Services["otel-profiles"]
	if !ok {
		t.Fatal("no otel-profiles service: the named seam must be filled")
	}
	if !regexp.MustCompile(`^ghcr\.io/open-telemetry/opentelemetry-collector-releases/opentelemetry-collector-contrib:0\.(1[6-9]\d|[2-9]\d\d)\.\d+@sha256:[0-9a-f]{64}$`).MatchString(prof.Image) {
		t.Errorf("image %q must be contrib >= 0.162, pinned by version and digest", prof.Image)
	}
	if !slices.Contains(prof.Command, "--feature-gates=+service.profilesSupport") {
		t.Errorf("command %v lacks the profiles feature gate", prof.Command)
	}
	if len(prof.Ports) != 1 || !strings.HasPrefix(prof.Ports[0], "127.0.0.1:") || !strings.HasSuffix(prof.Ports[0], ":4318") {
		t.Errorf("ports %v: want exactly OTLP/HTTP on loopback", prof.Ports)
	}
	if !strings.Contains(strings.Join(prof.Volumes, "\n"), "./deploy/observability/otel-profiles.yaml:") {
		t.Errorf("the collector config is not mounted: %v", prof.Volumes)
	}
	if prof.Environment["CLICKHOUSE_PASSWORD"] != cf.Services["clickstack"].Environment["CLICKHOUSE_PASSWORD"] ||
		prof.Environment["CLICKHOUSE_USER"] != cf.Services["clickstack"].Environment["CLICKHOUSE_USER"] {
		t.Error("the profiles collector must use the same ClickHouse credentials as clickstack, not invent its own")
	}
	var deps map[string]struct {
		Condition string `yaml:"condition"`
	}
	if err := prof.DependsOn.Decode(&deps); err != nil || deps["profiles-schema"].Condition != "service_completed_successfully" {
		t.Errorf("otel-profiles must wait for profiles-schema to COMPLETE: %v (%v)", deps, err)
	}
	schema, ok := cf.Services["profiles-schema"]
	if !ok {
		t.Fatal("no profiles-schema job: tables and TTLs are created here, idempotently, on every up")
	}
	if !strings.Contains(strings.Join(schema.Volumes, "\n"), "./deploy/observability/profiles-schema.sql:") {
		t.Errorf("profiles-schema mounts %v", schema.Volumes)
	}
	var sdeps map[string]struct {
		Condition string `yaml:"condition"`
	}
	if err := schema.DependsOn.Decode(&sdeps); err != nil || sdeps["clickstack"].Condition != "service_healthy" {
		t.Errorf("profiles-schema must wait for clickstack to be healthy: %v (%v)", sdeps, err)
	}
	var nets map[string]struct {
		Aliases []string `yaml:"aliases"`
	}
	if err := prof.Networks.Decode(&nets); err != nil {
		t.Fatal(err)
	}
	aliased := false
	for _, n := range nets {
		aliased = aliased || slices.Contains(n.Aliases, "otel-profiles")
	}
	if !aliased {
		t.Error("otel-profiles has no network alias for compose services")
	}
	for _, name := range []string{"app", "app-debug"} {
		if got := cf.Services[name].Environment["OTEL_EXPORTER_OTLP_PROFILES_ENDPOINT"]; got != "http://otel-profiles:4318/v1development/profiles" {
			t.Errorf("%s pushes profiles to %q", name, got)
		}
	}
	// Profiles are PUSHED. The collector never scrapes application pprof endpoints.
	for _, banned := range []string{"pprofreceiver", "pyroscope"} {
		if strings.Contains(raw, banned) {
			t.Errorf("the compose file mentions %q: profiles are pushed in-process over OTLP", banned)
		}
	}
}

func TestProfilesCollectorConfigAndSchema(t *testing.T) {
	out, err := ProjectTemplates().Render("otel-profiles.yaml.tmpl", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Receivers map[string]any `yaml:"receivers"`
		Exporters map[string]struct {
			CreateSchema *bool  `yaml:"create_schema"`
			Endpoint     string `yaml:"endpoint"`
			Username     string `yaml:"username"`
			Password     string `yaml:"password"`
		} `yaml:"exporters"`
		Service struct {
			Pipelines map[string]struct {
				Receivers  []string `yaml:"receivers"`
				Processors []string `yaml:"processors"`
				Exporters  []string `yaml:"exporters"`
			} `yaml:"pipelines"`
		} `yaml:"service"`
	}
	if err := yaml.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("invalid YAML: %v\n%s", err, out)
	}
	ch := cfg.Exporters["clickhouse"]
	if ch.CreateSchema == nil || *ch.CreateSchema {
		t.Error("create_schema must be false: the TTLs and materialized views are ours, in profiles-schema.sql")
	}
	if ch.Endpoint != "tcp://clickstack:9000" || ch.Username != "${env:CLICKHOUSE_USER}" || ch.Password != "${env:CLICKHOUSE_PASSWORD}" {
		t.Errorf("clickhouse exporter = %+v: credentials come from the environment, never the file", ch)
	}
	pl, ok := cfg.Service.Pipelines["profiles"]
	if !ok || len(cfg.Service.Pipelines) != 1 {
		t.Fatalf("pipelines = %v, want exactly one: profiles", cfg.Service.Pipelines)
	}
	if !slices.Equal(pl.Receivers, []string{"otlp"}) || !slices.Equal(pl.Exporters, []string{"clickhouse"}) || len(pl.Processors) != 0 {
		t.Errorf("profiles pipeline = %+v: OTLP in, clickhouse out, no batch processor (it has no profiles support)", pl)
	}

	sql, err := ProjectTemplates().Render("profiles-schema.sql.tmpl", struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(sql)
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS default.otel_profiles", "TTL toDateTime(Timestamp) + INTERVAL 72 HOUR",
		"CREATE TABLE IF NOT EXISTS default.profile_stacks", "CREATE MATERIALIZED VIEW IF NOT EXISTS default.profile_stacks_mv",
		"CREATE TABLE IF NOT EXISTS default.profile_samples_1m", "CREATE MATERIALIZED VIEW IF NOT EXISTS default.profile_samples_1m_mv",
		"INTERVAL 30 DAY", "SampleType != 'samples'",
	} {
		if !strings.Contains(ddl, want) {
			t.Errorf("profiles-schema.sql lacks %q", want)
		}
	}
	// Every statement must be idempotent: the job runs on every `forge env up`.
	for _, stmt := range strings.Split(ddl, ";") {
		st := strings.TrimSpace(stmt)
		if strings.HasPrefix(st, "CREATE") && !strings.Contains(strings.SplitN(st, "\n", 2)[0], "IF NOT EXISTS") {
			t.Errorf("not idempotent: %.80s", st)
		}
	}
}
