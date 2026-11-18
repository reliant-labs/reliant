package codegen

import (
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/kcltest"
	"github.com/reliant-labs/forge/internal/templates"
)

// mustMarkRootConfig is markRootConfig, failing the test on a refusal.
func mustMarkRootConfig(t *testing.T, views []graphRootView, pc *processConfigs, reads map[string]*configRead) {
	t.Helper()
	if err := markRootConfig(views, pc, reads); err != nil {
		t.Fatal(err)
	}
}

// binConfigMessages is the `rbc` binary binding config per command:
//
//	AppConfig     {binary: "rbc"}                      port, log_level, database_url (required, sensitive)
//	ServerConfig  {subtrees: [{path: ["server"]}]}     region, audit_sink (required)
//	ApiConfig     {command: ["server", "api"]}         signing_key (required, sensitive), greeting
//	WorkerConfig  {command: ["server", "worker"]}      tick_every, queue_token (required, sensitive)
//	CliConfig     {subtrees: [{path: ["login"]}]}      server_url
func binConfigMessages() []ConfigMessage {
	str := func(name, goName, env string, mod func(*ConfigField)) ConfigField {
		f := ConfigField{Name: name, GoName: goName, GoType: "string", ProtoType: "string", EnvVar: env}
		if mod != nil {
			mod(&f)
		}
		return f
	}
	secret := func(f *ConfigField) { f.Required, f.Sensitive = true, true }
	required := func(f *ConfigField) { f.Required = true }
	return []ConfigMessage{
		{Name: "AppConfig", Binary: "rbc", Fields: []ConfigField{
			{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: "PORT", DefaultValue: "8080"},
			str("log_level", "LogLevel", "LOG_LEVEL", nil),
			str("database_url", "DatabaseUrl", "DATABASE_URL", secret),
		}},
		{Name: "ServerConfig", Binary: "rbc", Subtrees: [][]string{{"server"}}, Fields: []ConfigField{
			str("region", "Region", "REGION", nil),
			str("audit_sink", "AuditSink", "AUDIT_SINK", required),
		}},
		{Name: "ApiConfig", Binary: "rbc", Command: []string{"server", "api"}, Fields: []ConfigField{
			str("signing_key", "SigningKey", "API_SIGNING_KEY", secret),
			str("greeting", "Greeting", "API_GREETING", nil),
		}},
		{Name: "WorkerConfig", Binary: "rbc", Command: []string{"server", "worker"}, Fields: []ConfigField{
			str("tick_every", "TickEvery", "TICK_EVERY", nil),
			str("queue_token", "QueueToken", "QUEUE_TOKEN", secret),
		}},
		{Name: "CliConfig", Binary: "rbc", Subtrees: [][]string{{"login"}}, Fields: []ConfigField{
			str("server_url", "ServerUrl", "SERVER_URL", nil),
		}},
	}
}

// binConfigCompose is compose.go for the two roots: the API's item handler
// takes the whole api config and the audit sink, the worker's tick reads
// its cadence and the region.
const binConfigCompose = `package app

func (g *Graph) newItem() (*item.Service, error) {
	return item.New(item.Deps{Log: g.Log, API: g.ApiConfig, AuditSink: g.ServerConfig.GetAuditSink()})
}

func (g *Graph) newTick() (*tick.Worker, error) {
	return tick.New(tick.Deps{Every: g.WorkerConfig.GetTickEvery(), Region: g.ServerConfig.Region}), nil
}
`

func binConfigViews() []graphRootView {
	return []graphRootView{
		{Name: "api", Command: []string{"server", "api"}, Reach: []string{"DB", "Item"}, Migrate: true},
		{Name: "worker", Command: []string{"server", "worker"}, Reach: []string{"Tick"}},
	}
}

func markBinConfig(t *testing.T, compose string, views []graphRootView) error {
	t.Helper()
	pc := processConfigsFrom(binConfigMessages(), "rbc")
	if pc == nil {
		t.Fatal("the binary's root config is the root config")
	}
	nodes := []*graphNode{{Name: "Item"}, {Name: "Tick"}, {Name: "DB"}}
	return markProcessConfig(views, pc, graphComposeInputConfigReads(compose, nodes, pc.inputNames()))
}

func loadPaths(v graphRootView) map[string]string {
	out := map[string]string{}
	for _, l := range v.Loads {
		out[l.Message] = strings.Join(paths(l.Config), ",")
	}
	return out
}

// Each root's process loads its binary's root config, the subtree configs
// over its command and its own command config — and of each, reaches only
// the fields its closure reads.
func TestProcessConfig_EachRootReachesWhatItsClosureReadsOfWhatItsProcessLoads(t *testing.T) {
	views := binConfigViews()
	if err := markBinConfig(t, binConfigCompose, views); err != nil {
		t.Fatal(err)
	}
	api, worker := views[0], views[1]
	if got := strings.Join(paths(api.Config), ","); got != "port,log_level,database_url" {
		t.Errorf("api's root config reach = %s", got)
	}
	if got := strings.Join(paths(worker.Config), ","); got != "port,log_level" {
		t.Errorf("worker's root config reach = %s: the process fields, and no database", got)
	}
	// CliConfig's subtree does not cover either command, and each command
	// config is loaded by its own command alone.
	if got, want := loadPaths(api), map[string]string{"ServerConfig": "audit_sink", "ApiConfig": "signing_key,greeting"}; !mapsEqual(got, want) {
		t.Errorf("api loads %v, want %v", got, want)
	}
	if got, want := loadPaths(worker), map[string]string{"ServerConfig": "region", "WorkerConfig": "tick_every"}; !mapsEqual(got, want) {
		t.Errorf("worker loads %v, want %v", got, want)
	}
	// Declaration order, so roots_gen.go loads subtree configs before the
	// command config, as config_gen.k projects them.
	if api.Loads[0].Message != "ServerConfig" || api.Loads[1].Message != "ApiConfig" {
		t.Errorf("loads out of declaration order: %+v", api.Loads)
	}

	// Beyond the process fields, the two roots reach disjoint env — the
	// shared subtree config included: each reaches its own leaf of it.
	envOf := func(v graphRootView) []string {
		var out []string
		for _, l := range graphRoots([]graphRootView{v})[0].Leaves() {
			out = append(out, l.EnvVar)
		}
		return out
	}
	apiEnv, workerEnv := envOf(api), envOf(worker)
	for _, k := range workerEnv {
		if slices.Contains(apiEnv, k) && k != "PORT" && k != "LOG_LEVEL" {
			t.Errorf("both roots reach %s", k)
		}
	}
	for _, k := range []string{"API_SIGNING_KEY", "AUDIT_SINK", "DATABASE_URL", "QUEUE_TOKEN"} {
		if slices.Contains(workerEnv, k) {
			t.Errorf("the worker reaches %s, which only the api's closure (or nothing) reads", k)
		}
	}
}

// A loaded config the closure never reads is still loaded — its flags are
// the command's surface — and demands nothing.
func TestProcessConfig_ALoadedConfigNothingReadsDemandsNothing(t *testing.T) {
	views := binConfigViews()
	const compose = `package app

func (g *Graph) newTick() (*tick.Worker, error) { return tick.New(tick.Deps{}), nil }
`
	if err := markBinConfig(t, compose, views); err != nil {
		t.Fatal(err)
	}
	worker := views[1]
	if len(worker.Loads) != 2 {
		t.Fatalf("worker loads %+v", worker.Loads)
	}
	for _, l := range worker.Loads {
		if l.Read || l.Config == nil || len(l.Config) != 0 {
			t.Errorf("%s: read=%v config=%v; want unread, an empty (non-nil) reach", l.Message, l.Read, l.Config)
		}
	}
}

// A closure that reads a config its root's process never loads would read
// nil at run time: generate refuses it, naming the node and the config.
func TestProcessConfig_RefusesAClosureReadingAConfigItsProcessDoesNotLoad(t *testing.T) {
	const compose = `package app

func (g *Graph) newTick() (*tick.Worker, error) {
	return tick.New(tick.Deps{Every: g.ApiConfig.GetGreeting()}), nil
}
`
	err := markBinConfig(t, compose, binConfigViews())
	if err == nil {
		t.Fatal("the worker's closure reads g.ApiConfig, which `rbc server worker` never loads")
	}
	for _, want := range []string{`root "worker" (` + "`rbc server worker`" + `)`, "reaches Tick", "g.ApiConfig", "the `rbc server api` command config", "nil there"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q:\n%v", want, err)
		}
	}
}

// One env var two configs one root loads both declare has no single source:
// the reach refuses it rather than pick one.
func TestProcessConfig_RefusesOneEnvVarFromTwoLoadedConfigs(t *testing.T) {
	v := graphRootView{
		Name:   "api",
		Config: []ConfigLeaf{{Path: "region", EnvVar: "REGION"}},
		Loads:  []RootConfigLoad{{Message: "ApiConfig", Config: []ConfigLeaf{{Path: "region", EnvVar: "REGION"}}}},
	}
	got := duplicateRootEnv(v)
	if len(got) != 1 || !strings.Contains(got[0], "REGION") || !strings.Contains(got[0], "ApiConfig") {
		t.Errorf("duplicate env = %v", got)
	}
	v.Loads[0].Config[0].EnvVar = "API_REGION"
	if got := duplicateRootEnv(v); len(got) != 0 {
		t.Errorf("distinct env vars are no conflict: %v", got)
	}
}

// The root config is the project's AppConfig when it has one, else the
// primary binary's own root config; the inputs are the primary binary's
// other configs, and no other binary's.
func TestProcessConfigsFrom_RootAndInputs(t *testing.T) {
	pc := processConfigsFrom(binConfigMessages(), "rbc")
	if got := pc.inputNames(); strings.Join(got, ",") != "Cfg,ServerConfig,ApiConfig,WorkerConfig,CliConfig" {
		t.Errorf("inputs = %v", got)
	}
	if processConfigsFrom(binConfigMessages(), "other") != nil {
		t.Error("no AppConfig and no root config bound to the primary binary: no root config to read reach against")
	}
	// A single-AppConfig project: no inputs, exactly PR 6's model.
	single := processConfigsFrom([]ConfigMessage{{Name: "AppConfig", Fields: []ConfigField{{Name: "port", GoName: "Port", EnvVar: "PORT"}}}}, "rbc")
	if single == nil || len(single.inputs) != 0 || single.configInputs() != nil {
		t.Errorf("single AppConfig: %+v", single)
	}
	// Another binary's configs are never this graph's inputs.
	msgs := append([]ConfigMessage{{Name: "AppConfig", Fields: []ConfigField{{Name: "port", GoName: "Port", EnvVar: "PORT"}}}},
		ConfigMessage{Name: "AdminConfig", Binary: "admin"})
	if pc := processConfigsFrom(msgs, "rbc"); len(pc.inputs) != 0 {
		t.Errorf("admin's config is not rbc's: %+v", pc.inputs)
	}
}

func TestGraphComposeInputConfigReads_ReadsEachConfigInput(t *testing.T) {
	reads := graphComposeInputConfigReads(binConfigCompose, []*graphNode{{Name: "Item"}, {Name: "Tick"}}, []string{"Cfg", "ApiConfig", "ServerConfig", "WorkerConfig"})
	if r := reads["Item"]["ApiConfig"]; r == nil || !r.whole {
		t.Errorf("Item hands g.ApiConfig on whole: %+v", r)
	}
	if r := reads["Item"]["ServerConfig"]; r == nil || !r.fields["AuditSink"] {
		t.Errorf("Item reads ServerConfig.AuditSink: %+v", r)
	}
	if r := reads["Tick"]["WorkerConfig"]; r == nil || !r.fields["TickEvery"] {
		t.Errorf("Tick reads WorkerConfig.TickEvery: %+v", r)
	}
	if r := reads["Tick"]["ApiConfig"]; r != nil {
		t.Errorf("Tick reads nothing of ApiConfig: %+v", r)
	}
	// g.Cfg reads are recorded for every node, read or not, as before.
	if r := reads["Tick"]["Cfg"]; r == nil || r.whole || len(r.fields) != 0 {
		t.Errorf("Tick's g.Cfg read = %+v", r)
	}
}

// The single-AppConfig ROOTS block is exactly what it was: RootsKCL is
// RootsKCLFor through appConfigEnvMap.
func TestRootsKCLFor_AppConfigIsRootsKCL(t *testing.T) {
	_, roots := twoRootFixture()
	got := RootsKCLFor(roots, appConfigRootsEnv)
	if got != RootsKCL(roots) {
		t.Fatal("RootsKCL must be RootsKCLFor(…, appConfigRootsEnv)")
	}
	for _, want := range []string{
		"rootEnvMap = lambda c: AppConfig, w: any",
		"_all = appConfigEnvMap(c, _r.required_secrets + [",
		"rootEnvMap(c, w) if w.root else appConfigEnvMap(c, w.config_secrets or [])",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("AppConfig ROOTS lacks %q", want)
		}
	}
}

func binConfigRoots(t *testing.T) []GraphRoot {
	t.Helper()
	views := binConfigViews()
	if err := markBinConfig(t, binConfigCompose, views); err != nil {
		t.Fatal(err)
	}
	return graphRoots(views)
}

// A binary that binds config per command projects a root's env through its
// process selector; each root's env and secrets span every config it loads.
func TestRootsKCLFor_ProcessSelector(t *testing.T) {
	env := RootsEnv{ConfigType: "RbcProcess", EnvMap: "rbcProcessEnvMap", Argv: true}
	src := RootsKCLFor(binConfigRoots(t), env)
	for _, want := range []string{
		`"api" = Root {`,
		`args = ["server", "api"]`,
		`env = ["PORT", "LOG_LEVEL", "AUDIT_SINK", "API_GREETING"]`,
		`secrets = ["DATABASE_URL", "API_SIGNING_KEY"]`,
		`required_secrets = ["DATABASE_URL", "API_SIGNING_KEY"]`,
		`env = ["PORT", "LOG_LEVEL", "REGION", "TICK_EVERY"]`,
		"rootEnvMap = lambda c: RbcProcess, w: any",
		"_all = rbcProcessEnvMap(c, _r.args, _r.required_secrets + [",
		"rbcProcessEnvMap(c, (w.command or []) + (w.args or []), w.config_secrets or [])",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("ROOTS lacks %q:\n%s", want, src)
		}
	}
}

// End to end through KCL: the per-binary config module plus ROOTS gives each
// root's workload exactly its env — the api's credential never reaches the
// worker — and a workload bound to no root keeps the process selector's.
func TestRootsKCLFor_ProcessSelectorRendersDisjointEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL in a kcl subprocess (~3s); runs in task test")
	}
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl not on PATH; skipping the evaluation half")
	}
	messages := binConfigMessages()
	perBinary := BinaryConfigFieldsFrom(messages)
	module, err := GenerateConfigKCLPerBinary(nil, perBinary, "rbc")
	if err != nil {
		t.Fatal(err)
	}
	module += RootsKCLFor(binConfigRoots(t), RootsEnv{ConfigType: "RbcProcess", EnvMap: "rbcProcessEnvMap", Argv: true})
	dir := writeRootsKCLProjectModule(t, module, `import config_gen

_p = config_gen.RbcProcess {
    app_config = config_gen.AppConfig {}
    server_config = config_gen.ServerConfig {audit_sink = "s3://audit"}
    api_config = config_gen.ApiConfig {}
    worker_config = config_gen.WorkerConfig {}
}
_api = {name = "api"} | config_gen.root_of("api")
_worker = {name = "worker"} | config_gen.root_of("worker")
_unbound = {name = "unbound", args = ["server", "worker"], config_secrets = ["QUEUE_TOKEN"]}
api_env = sorted([_k for _k in config_gen.workloadEnvMap(_p, _api)])
worker_env = sorted([_k for _k in config_gen.workloadEnvMap(_p, _worker)])
unbound_env = sorted([_k for _k in config_gen.workloadEnvMap(_p, _unbound)])
`)
	out, err := kcltest.Run(t.Context(), dir, "run", ".", "--format", "json")
	if err != nil {
		t.Fatalf("kcl run failed: %v\n%s", err, out)
	}
	var got struct {
		APIEnv     []string `json:"api_env"`
		WorkerEnv  []string `json:"worker_env"`
		UnboundEnv []string `json:"unbound_env"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if want := "API_GREETING,API_SIGNING_KEY,AUDIT_SINK,DATABASE_URL,LOG_LEVEL,PORT"; strings.Join(got.APIEnv, ",") != want {
		t.Errorf("api env = %v, want %s", got.APIEnv, want)
	}
	if want := "LOG_LEVEL,PORT,REGION,TICK_EVERY"; strings.Join(got.WorkerEnv, ",") != want {
		t.Errorf("worker env = %v, want %s", got.WorkerEnv, want)
	}
	// Bound to no root: the whole `rbc server worker` process, as before.
	if !slices.Contains(got.UnboundEnv, "QUEUE_TOKEN") || !slices.Contains(got.UnboundEnv, "AUDIT_SINK") {
		t.Errorf("an unbound workload keeps the process selector's env: %v", got.UnboundEnv)
	}
}

// Load<Msg>Reach is generated for a graph project's primary binary only,
// checking the config's command or subtree like Load<Msg> does.
func TestConfigLoader_LoadMsgReachIsGraphOnly(t *testing.T) {
	render := func(graph bool, primary string) string {
		t.Helper()
		messages := append(binConfigMessages(), ConfigMessage{Name: "AdminConfig", Binary: "admin"})
		data := BuildConfigTemplateData(messages)
		data.PrimaryConfigMessage = "AppConfig"
		data.Module, data.Graph = "example.com/rbc", graph
		markReachLoaders(&data, primary)
		out, err := templates.ProjectTemplates().Render("config.go.tmpl", data)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	graph := render(true, "rbc")
	for _, want := range []string{
		"func LoadApiConfigReach(cmd *cobra.Command, reach []string) (*ApiConfig, error) {",
		`forgeconfig.CheckCommandPath(cmd, "ApiConfig", "server", "api")`,
		"func LoadServerConfigReach(cmd *cobra.Command, reach []string) (*ServerConfig, error) {",
		`forgeconfig.CheckCommandSubtree(cmd, "ServerConfig", []string{"server"})`,
		"forgeconfig.LoadTyped[*configv1.ApiConfig](cmd, forgeconfig.RequiredWithin(reach...))",
	} {
		if !strings.Contains(graph, want) {
			t.Errorf("graph loader lacks %q", want)
		}
	}
	if strings.Contains(graph, "func LoadAdminConfigReach(") {
		t.Error("only the primary binary's roots load with reach: another binary's loader gains no Load<Msg>Reach")
	}
	if legacy := render(false, "rbc"); strings.Contains(legacy, "ConfigReach(cmd *cobra.Command") {
		t.Error("a legacy-composition project's loader gains no Load<Msg>Reach")
	}
	if noTree := render(true, ""); strings.Contains(noTree, "ConfigReach(cmd *cobra.Command") {
		t.Error("a project with no command tree for its roots gains no Load<Msg>Reach")
	}
}

// roots_gen.go: a root whose command loads configs of its own loads each
// with its reach and sets it on the graph; the configs' flags go where #957
// homes them. A root that loads none renders exactly as before.
func TestRootsGen_LoadsAndRegistersEachConfig(t *testing.T) {
	views := binConfigViews()
	if err := markBinConfig(t, binConfigCompose, views); err != nil {
		t.Fatal(err)
	}
	for i := range views {
		views[i].TypeName = strings.ToUpper(views[i].Name[:1]) + views[i].Name[1:]
	}
	cmds, top := rootCommandViews(views, nil)
	inputs := processConfigsFrom(binConfigMessages(), "rbc").inputViews()
	src, err := renderGoTemplate("roots_gen.go.tmpl", rootsGenView{Module: "example.com/rbc", Commands: cmds, Top: top, Fmt: true, ConfigInputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	gen := strings.Join(strings.Fields(string(src)), " ")
	for _, want := range []string{
		`"fmt"`,
		`config.RegisterServerConfigFlags(serverCmd)`,
		`config.RegisterApiConfigFlags(serverAPICmd)`,
		`config.RegisterWorkerConfigFlags(serverWorkerCmd)`,
		`apiConfig, err := config.LoadApiConfigReach(cmd, []string{ "signing_key", "greeting", })`,
		`serverConfig, err := config.LoadServerConfigReach(cmd, []string{ "region", })`,
		`g.ApiConfig = apiConfig`,
		`return g.RunWorker(ctx, policy)`,
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("roots_gen.go lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(gen, "RegisterServerConfigFlags(serverAPICmd)") {
		t.Error("a subtree config's flags are registered once, on its path's command")
	}

	// No loads: the PR 6 shape, byte for byte.
	plain := []graphRootView{{Name: "server", TypeName: "Server", Command: []string{"server"}, HasConfig: true, Config: []ConfigLeaf{{Path: "port"}}}}
	cmds, top = rootCommandViews(plain, nil)
	src, err = renderGoTemplate("roots_gen.go.tmpl", rootsGenView{Module: "example.com/rbc", Commands: cmds, Top: top})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(src); strings.Contains(s, `"fmt"`) || strings.Contains(s, "Reach(cmd") || strings.Contains(s, "rootConfigs") || !strings.Contains(s, "Run: (*app.Graph).RunServer,") {
		t.Errorf("a root with no loads renders as before:\n%s", s)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
