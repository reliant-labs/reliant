package codegen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// topLevelReads is the top-level Go names r reads (a leaf read counts its
// field), "*" for a whole-config read.
func topLevelReads(r *configRead) []string {
	if r == nil {
		return nil
	}
	set := map[string]bool{}
	if r.whole {
		set["*"] = true
	}
	for f := range r.fields {
		set[f] = true
	}
	for l := range r.leaves {
		set[l[0]] = true
	}
	return boolKeys(set)
}

func parseGoFiles(t *testing.T, srcs ...string) []*ast.File {
	t.Helper()
	var out []*ast.File
	for i, src := range srcs {
		f, err := parser.ParseFile(token.NewFileSet(), "f.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("source %d: %v\n%s", i, err, src)
		}
		out = append(out, f)
	}
	return out
}

// The scaffolded serve.go reads exactly the serve fields — the fallback a
// command tree without runRoot reaches — off the root config and off each
// config its command loaded, so the analysis and the fallback cannot drift.
func TestProcessReads_TheScaffoldedServeReadsTheServeFields(t *testing.T) {
	all := map[string]bool{}
	for _, f := range serveConfigFields(true) {
		all[f] = true
	}
	out, err := templates.ProjectTemplates().Render("cmd-tree-serve-graph.go.tmpl", CmdServerTemplateData{
		Module: "example.com/x", Name: "x", ConfigFields: all,
		ConfigInputs: []ServeConfigInput{{Name: "RoleConfig", Doc: "the role config", Fields: all}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reads := runRootConfigReadsOf(parseGoFiles(t, string(out)), map[string]bool{"RoleConfig": true, "OtherConfig": true})
	want := boolKeys(all)
	for _, in := range []string{rootConfigInput, "RoleConfig"} {
		if got := topLevelReads(reads[in]); !slices.Equal(got, want) {
			t.Errorf("the scaffolded runRoot reads %v of %s, want the serve fields %v", got, in, want)
		}
	}
	if r := reads["OtherConfig"]; r != nil {
		t.Errorf("the scaffolded runRoot reads %v of a config it never names", topLevelReads(r))
	}
}

// ownedServe is a reliant-shaped owned serve.go: the root config's log and
// runtime blocks, the `server` subtree config's data dir and observability
// block (through a helper, as a block), the gateway command config's health
// port, and the database settings — guarded by a nil check, handed to the
// graph whole, compared with nil.
const ownedServe = `package cmd

func runRoot(ctx context.Context, deps Deps, root rootSpec) error {
	cfg, err := config.LoadReach(deps.Cmd, root.Config)
	if err != nil {
		return err
	}
	runenv.Configure(cfg.GetRuntime().GetEnvironment())
	skCfg, err := serverkitConfig(cfg, root.Configs)
	if err != nil {
		return err
	}
	process := root.Configs.ServerProcessConfig
	logging.Setup(logSettings(cfg), filepath.Join(process.GetDataDir(), "logs", root.Name))
	obs, err := observability.Init(observability.NewConfig("svc", observabilitySettings(process.GetObservability(), cfg)))
	if err != nil {
		return err
	}
	g := &app.Graph{Ctx: ctx, Cfg: cfg, ServerCfg: skCfg}
	return root.Run(g, ctx, lifecyclekit.Policy{Flush: func(context.Context) error { return obs.Shutdown() }})
}

func serverkitConfig(cfg *config.Config, configs rootConfigs) (serverkit.Config, error) {
	skCfg := serverkit.Config{LogFormat: cfg.GetLog().GetFormat()}
	if c := configs.ServerProcessConfig; c != nil {
		skCfg.DatabaseURL = c.GetDatabaseUrl()
	}
	if c := configs.GatewayConfig; c != nil {
		skCfg.Addr = fmt.Sprintf(":%d", c.GetHealthPort())
	}
	return skCfg, nil
}

func logSettings(cfg *config.Config) logging.Settings {
	return logging.Settings{Level: cfg.GetLog().GetLevel()}
}

func observabilitySettings(o *configv1.ObservabilityConfig, base *config.Config) observability.Settings {
	env := o.GetEnvironment()
	if env == "" {
		env = base.GetRuntime().GetEnvironment()
	}
	return observability.Settings{Environment: env, OTLPEndpoint: o.GetOtlpEndpoint(), PrometheusEnabled: o.GetPrometheusEnabled()}
}
`

func TestProcessReads_FollowsTheConfigsThroughTheOwnedPolicy(t *testing.T) {
	inputs := map[string]bool{"ServerProcessConfig": true, "GatewayConfig": true, "ApiConfig": true}
	reads := runRootConfigReadsOf(parseGoFiles(t, ownedServe), inputs)
	got := map[string]string{}
	for in, r := range reads {
		var ps []string
		for f := range r.fields {
			ps = append(ps, f)
		}
		for l := range r.leaves {
			ps = append(ps, l[0]+"."+l[1])
		}
		if r.whole {
			ps = append(ps, "*")
		}
		sort.Strings(ps)
		got[in] = strings.Join(ps, ",")
	}
	want := map[string]string{
		rootConfigInput:       "Log.Format,Log.Level,Runtime.Environment",
		"ServerProcessConfig": "DataDir,DatabaseUrl,Observability.Environment,Observability.OtlpEndpoint,Observability.PrometheusEnabled",
		"GatewayConfig":       "HealthPort",
	}
	if !mapsEqual(got, want) {
		t.Errorf("runRoot reads %v, want %v", got, want)
	}
}

// A config value runRoot hands to code forge does not read is the whole of
// it; the rootConfigs bundle handed on whole is every config in it.
func TestProcessReads_AConfigHandedOnWholeIsAllOfIt(t *testing.T) {
	src := `package cmd

func runRoot(ctx context.Context, deps Deps, root rootSpec) error {
	cfg, _ := config.LoadReach(deps.Cmd, root.Config)
	telemetry.Install(cfg.GetObservability())
	audit.Configure(root.Configs)
	return nil
}
`
	reads := runRootConfigReadsOf(parseGoFiles(t, src), map[string]bool{"ApiConfig": true})
	if got := topLevelReads(reads[rootConfigInput]); !slices.Equal(got, []string{"Observability"}) {
		t.Errorf("root config reads = %v, want the observability block whole", got)
	}
	if got := topLevelReads(reads["ApiConfig"]); !slices.Equal(got, []string{"*"}) {
		t.Errorf("ApiConfig reads = %v, want all of it: the bundle went to audit.Configure", got)
	}
}

// A command tree with no runRoot has no reads to take: nil, and the
// scaffolded serve fields stand in.
func TestProcessReads_NoRunRootIsNil(t *testing.T) {
	if reads := runRootConfigReadsOf(parseGoFiles(t, "package cmd\n\nfunc serve() {}\n"), nil); reads != nil {
		t.Errorf("reads = %v, want nil", reads)
	}
}

// observedMessages is reliantShapedMessages with an `observability` block
// on the `server` subtree config, a data dir, and the gateway's health port
// — what reliant's own serve.go reads beyond forge's serve fields.
func observedMessages() []ConfigMessage {
	msgs := reliantShapedMessages()
	str := func(name, goName, env string) ConfigField {
		return ConfigField{Name: name, GoName: goName, GoType: "string", ProtoType: "string", EnvVar: env}
	}
	for i := range msgs {
		switch msgs[i].Name {
		case "LogConfig":
			msgs[i].Fields = append(msgs[i].Fields, str("format", "Format", "LOG_FORMAT"))
		case "ServerProcessConfig":
			msgs[i].Fields = append(msgs[i].Fields,
				str("data_dir", "DataDir", "DATA_DIR"),
				ConfigField{Name: "observability", GoName: "Observability", ProtoType: "message", MessageType: "ObservabilityConfig"})
		case "GatewayConfig":
			msgs[i].Fields = append(msgs[i].Fields, ConfigField{Name: "health_port", GoName: "HealthPort", GoType: "int32", ProtoType: "int32", EnvVar: "HEALTH_PORT"})
		case "RbcConfig":
			msgs[i].Fields = append(msgs[i].Fields, ConfigField{Name: "runtime", GoName: "Runtime", ProtoType: "message", MessageType: "RuntimeConfig"})
		}
	}
	return append(msgs,
		ConfigMessage{Name: "RuntimeConfig", Fields: []ConfigField{str("environment", "Environment", "RELIANT_ENV")}},
		ConfigMessage{Name: "ObservabilityConfig", Fields: []ConfigField{
			str("environment", "Environment", "ENVIRONMENT"),
			str("otlp_endpoint", "OtlpEndpoint", "OTEL_EXPORTER_OTLP_ENDPOINT"),
			{Name: "prometheus_enabled", GoName: "PrometheusEnabled", GoType: "bool", ProtoType: "bool", EnvVar: "PROMETHEUS_ENABLED", Required: true},
			{Name: "otel_enabled", GoName: "OtelEnabled", GoType: "bool", ProtoType: "bool", EnvVar: "OTEL_ENABLED"},
		}},
	)
}

// The fields the owned serve.go reads are every root's reach — in ROOTS,
// in Roots() and in what LoadReach demands — and a database field only of a
// root that opens the database. Before, only forge's own serve-field names
// counted, so a role's OTEL_*/PROMETHEUS_ENABLED were outside every root.
func TestProcessConfig_TheOwnedServeReadsAreEveryRootsReach(t *testing.T) {
	dir := t.TempDir()
	writeRootGo(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "cmd", "rbc", "cmd", "serve.go"), []byte(ownedServe), 0o644); err != nil {
		t.Fatal(err)
	}
	pc := processConfigsFrom(observedMessages(), "rbc")
	pc.process = runRootConfigReads(dir, pc.configInputs())
	views := []graphRootView{
		{Name: "api", Command: []string{"server", "api"}, Reach: []string{"DB"}, ReachesDB: true, Migrate: true},
		{Name: "gateway", Command: []string{"server", "gateway"}},
	}
	if err := markProcessConfig(views, pc, nil); err != nil {
		t.Fatal(err)
	}
	api, gateway := views[0], views[1]
	for _, v := range views {
		if got := strings.Join(paths(v.Config), ","); got != "log.level,log.format,runtime.environment" {
			t.Errorf("%s's root config reach = %s, want what runRoot reads of it", v.Name, got)
		}
	}
	if got, want := loadPaths(gateway), map[string]string{
		"ServerProcessConfig": "data_dir,observability.environment,observability.otlp_endpoint,observability.prometheus_enabled",
		"GatewayConfig":       "health_port",
	}; !mapsEqual(got, want) {
		t.Errorf("gateway loads %v, want %v: the observability leaves runRoot reads, and no database", got, want)
	}
	if got := loadPaths(api)["ServerProcessConfig"]; got != "database_url,data_dir,observability.environment,observability.otlp_endpoint,observability.prometheus_enabled" {
		t.Errorf("api's ServerProcessConfig reach = %s: it opens the database, so the URL runRoot reads is its", got)
	}
	// What reaches ROOTS and Roots(): the variables, required included.
	var vars []string
	for _, l := range graphRoots(views)[1].Leaves() {
		vars = append(vars, l.EnvVar)
	}
	for _, want := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "PROMETHEUS_ENABLED", "HEALTH_PORT", "LOG_FORMAT"} {
		if !slices.Contains(vars, want) {
			t.Errorf("the gateway root reaches %v, which lacks %s", vars, want)
		}
	}
	if slices.Contains(vars, "OTEL_ENABLED") || slices.Contains(vars, "DATABASE_URL") {
		t.Errorf("the gateway root reaches %v: runRoot never reads otel_enabled, and the gateway opens no database", vars)
	}
}
