package codegen

import (
	"go/format"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// The runRoot seams a binary_config project needs (roots plan F1, F2, F6):
// serve.go reads only the root config's own fields off *config.Config, the
// process settings a command config declares reach runRoot before the policy
// runs, and a root declares what its process does about the schema.

func TestRootOptions_MigratePolicy(t *testing.T) {
	for _, tc := range []struct {
		opts    []string
		want    string
		path    string
		wantErr string
	}{
		{opts: nil, want: "", path: "gw"},
		{opts: []string{"migrate=wait"}, want: migrateWait, path: "gw"},
		{opts: []string{"command=server,gateway", "migrate=none"}, want: migrateNone, path: "server gateway"},
		{opts: []string{"migrate=apply", "command=server,api"}, want: migrateApply, path: "server api"},
		{opts: []string{"migrate=later"}, wantErr: "not a migrate policy"},
		{opts: []string{"migrate"}, wantErr: "unknown //forge:root option"},
	} {
		path, migrate, err := rootOptions("gw", tc.opts)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%v: err = %v, want %q", tc.opts, err, tc.wantErr)
			}
			continue
		}
		if err != nil || migrate != tc.want || strings.Join(path, " ") != tc.path {
			t.Errorf("%v = %v, %q, %v; want %q, %q", tc.opts, path, migrate, err, tc.path, tc.want)
		}
	}
}

func TestDiscoverAppDecls_RootMigrateOption(t *testing.T) {
	dir := t.TempDir()
	writeAppFile(t, dir, "roots.go", `package app

// Gateway waits for the api's migrations.
//
//forge:root gateway command=server,gateway migrate=wait
type Gateway struct{}

//forge:root migrate=none
type Worker struct{}
`)
	_, roots, err := discoverAppDecls(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range roots {
		got[r.Name] = strings.Join(r.Command, " ") + "|" + r.Migrate
	}
	if got["gateway"] != "server gateway|wait" || got["worker"] != "worker|none" {
		t.Errorf("roots = %v", got)
	}
}

// The default is today's: apply when the closure reaches the database. An
// explicit policy overrides reach either way, and a process that migrates or
// waits reads the database fields even when no node reaches the database.
func TestMarkRootReach_MigratePolicy(t *testing.T) {
	db := &graphNode{Name: "DB", Kind: nodeProvider, Type: "*DB"}
	tick := &graphNode{Name: "Tick"}
	byName := map[string]*graphNode{"DB": db, "Tick": tick}
	u := &graphUniverse{}
	views := []graphRootView{
		{Name: "api", Reach: []string{"DB"}},
		{Name: "worker", Reach: []string{"Tick"}},
		{Name: "gateway", Reach: []string{"DB"}, MigratePolicy: migrateWait},
		{Name: "ro", Reach: []string{"DB"}, MigratePolicy: migrateNone},
		{Name: "migrator", MigratePolicy: migrateApply},
	}
	markRootReach(views, nil, byName, u)
	type facts struct{ reachesDB, migrate, await, opensDB bool }
	want := map[string]facts{
		"api":      {reachesDB: true, migrate: true, opensDB: true},
		"worker":   {},
		"gateway":  {reachesDB: true, await: true, opensDB: true},
		"ro":       {reachesDB: true, opensDB: true},
		"migrator": {migrate: true, opensDB: true},
	}
	for _, v := range views {
		if got := (facts{v.ReachesDB, v.Migrate, v.AwaitMigrations, v.opensDB()}); got != want[v.Name] {
			t.Errorf("%s: %+v, want %+v", v.Name, got, want[v.Name])
		}
	}
}

// writeRootGo gives dir a primary binary rbc: the cmd tree's root.go.
func writeRootGo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "cmd", "rbc", "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cmd", "rbc", "cmd", "root.go"), []byte("package cmd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// reliantShapedMessages is a binary whose root config holds only blocks (no
// process field of its own), whose `server` subtree config holds the
// database, and whose command configs each hold their role's port and drain
// budget — under env names of their own.
func reliantShapedMessages() []ConfigMessage {
	str := func(name, goName, env string) ConfigField {
		return ConfigField{Name: name, GoName: goName, GoType: "string", ProtoType: "string", EnvVar: env}
	}
	port := func(env string) ConfigField {
		return ConfigField{Name: "port", GoName: "Port", GoType: "int32", ProtoType: "int32", EnvVar: env}
	}
	dur := func(name, goName, env string) ConfigField {
		return ConfigField{Name: name, GoName: goName, GoType: "*durationpb.Duration", ProtoType: "message", MessageType: "google.protobuf.Duration", EnvVar: env}
	}
	return []ConfigMessage{
		{Name: "RbcConfig", Binary: "rbc", Fields: []ConfigField{
			{Name: "log", GoName: "Log", ProtoType: "message", MessageType: "LogConfig"},
		}},
		{Name: "LogConfig", Fields: []ConfigField{str("level", "Level", "LOG_LEVEL")}},
		{Name: "ServerProcessConfig", Binary: "rbc", Subtrees: [][]string{{"server"}}, Fields: []ConfigField{
			str("database_url", "DatabaseUrl", "DATABASE_URL"),
			{Name: "auto_migrate", GoName: "AutoMigrate", GoType: "bool", ProtoType: "bool", EnvVar: "AUTO_MIGRATE"},
		}},
		{Name: "ApiConfig", Binary: "rbc", Command: []string{"server", "api"}, Fields: []ConfigField{
			port("API_PORT"), dur("pre_stop_delay", "PreStopDelay", "API_PRE_STOP_DELAY"), dur("shutdown_timeout", "ShutdownTimeout", "API_SHUTDOWN_TIMEOUT"),
			str("signing_key", "SigningKey", "API_SIGNING_KEY"),
		}},
		{Name: "GatewayConfig", Binary: "rbc", Command: []string{"server", "gateway"}, Fields: []ConfigField{
			port("GATEWAY_PORT"), dur("shutdown_timeout", "ShutdownTimeout", "GATEWAY_SHUTDOWN_TIMEOUT"), str("pprof_addr", "PprofAddr", "GATEWAY_PPROF_ADDR"),
		}},
	}
}

// F1: the root config's own fields, and nothing else — not another config's,
// not a composed block's.
func TestRootConfigFieldNames_OnlyTheRootConfigsOwnFields(t *testing.T) {
	dir := t.TempDir()
	writeRootGo(t, dir)
	if got := boolKeys(RootConfigFieldNames(reliantShapedMessages(), dir)); !slices.Equal(got, []string{"Log"}) {
		t.Errorf("root config fields = %v, want [Log]: Port, PprofAddr and the rest are other configs' fields", got)
	}
	// One AppConfig: exactly the fields every message contributed before.
	app := DefaultConfigMessages()
	if got, want := boolKeys(RootConfigFieldNames(app, dir)), boolKeys(ConfigFieldNamesFromMessages(app)); !slices.Equal(got, want) {
		t.Errorf("a single AppConfig's fields changed: %v, want %v", got, want)
	}
}

// ServeConfigInputs orders the configs least specific first and keeps only
// their own serve fields.
func TestServeConfigInputs_LeastSpecificFirst(t *testing.T) {
	dir := t.TempDir()
	writeRootGo(t, dir)
	inputs := ServeConfigInputs(reliantShapedMessages(), dir)
	var got []string
	for _, in := range inputs {
		got = append(got, in.Name+":"+strings.Join(boolKeys(in.Fields), "+"))
	}
	want := []string{
		"ServerProcessConfig:AutoMigrate+DatabaseUrl",
		"ApiConfig:Port+PreStopDelay+ShutdownTimeout",
		"GatewayConfig:Port+PprofAddr+ShutdownTimeout",
	}
	if !slices.Equal(got, want) {
		t.Errorf("inputs = %v, want %v", got, want)
	}
}

// F2 + F6: the scaffolded serve.go of a binary_config project reads each
// role's settings off the configs its command loaded, and compiles against
// a root config with none of them.
func TestServeGraphTemplate_ReadsTheCommandConfigs(t *testing.T) {
	dir := t.TempDir()
	writeRootGo(t, dir)
	msgs := reliantShapedMessages()
	data := CmdServerTemplateData{Module: "example.com/rbc", Name: "rbc", ConfigFields: RootConfigFieldNames(msgs, dir), ConfigInputs: ServeConfigInputs(msgs, dir)}
	out, err := templates.ProjectTemplates().Render("cmd-tree-serve-graph.go.tmpl", data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("serve.go does not format: %v\n%s", err, out)
	}
	src := string(out)
	flat := strings.Join(strings.Fields(src), " ")
	for _, want := range []string{
		"skCfg, err := serverkitConfig(cfg, root.Configs)",
		"func serverkitConfig(cfg *config.Config, configs rootConfigs) (serverkit.Config, error) {",
		`Addr: ":8080",`,
		"if c := configs.ApiConfig; c != nil { skCfg.Addr = fmt.Sprintf(\":%d\", c.Port) skCfg.PreStopDelay = c.PreStopDelay.AsDuration() skCfg.ShutdownTimeout = c.ShutdownTimeout.AsDuration() }",
		"if c := configs.GatewayConfig; c != nil { skCfg.Addr = fmt.Sprintf(\":%d\", c.Port) skCfg.PprofAddr = c.PprofAddr skCfg.ShutdownTimeout = c.ShutdownTimeout.AsDuration() }",
		"if c := configs.ServerProcessConfig; c != nil { skCfg.AutoMigrate = c.AutoMigrate skCfg.DatabaseURL = c.DatabaseUrl }",
		"} else if root.AwaitMigrations {",
		"migratekit.AwaitMigrations(forgedb.Source(), forgedb.SourceDir, migratekit.AwaitOptions{})",
		"drainbudget.FromEnv(os.Getenv)",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("serve.go lacks %q:\n%s", want, src)
		}
	}
	if m := regexp.MustCompile(`\bcfg\.([A-Z]\w*)`).FindAllString(src, -1); len(m) > 0 {
		t.Errorf("serve.go reads %v off a root config that declares none of them", m)
	}
	// The server-wide subtree config is applied before the command configs.
	if strings.Index(src, "configs.ServerProcessConfig") > strings.Index(src, "configs.ApiConfig") {
		t.Error("the most specific config's settings must be applied last")
	}
}

// Every serve field an input may declare is read off it, and nothing else:
// the override block and serveConfigFields are one set.
func TestServeGraphTemplate_InputOverridesAreTheServeFields(t *testing.T) {
	all := map[string]bool{}
	for _, f := range serveConfigFields(true) {
		all[f] = true
	}
	out, err := templates.ProjectTemplates().Render("cmd-tree-serve-graph.go.tmpl", CmdServerTemplateData{
		Module: "example.com/x", Name: "x", ConfigInputs: []ServeConfigInput{{Name: "RoleConfig", Doc: "the role config", Fields: all}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := format.Source(out); err != nil {
		t.Fatalf("serve.go does not format: %v\n%s", err, out)
	}
	read := map[string]bool{}
	for _, m := range regexp.MustCompile(`\bc\.([A-Z]\w*)`).FindAllStringSubmatch(string(out), -1) {
		read[m[1]] = true
	}
	if got, want := boolKeys(read), boolKeys(all); !slices.Equal(got, want) {
		t.Errorf("the input override reads %v, want the serve fields %v", got, want)
	}
}

// A project whose binary loads one config renders serve.go's config
// projection exactly as before: no rootConfigs, one-argument serverkitConfig.
func TestServeGraphTemplate_OneConfigRendersAsBefore(t *testing.T) {
	out, err := templates.ProjectTemplates().Render("cmd-tree-serve-graph.go.tmpl", CmdServerTemplateData{Module: "example.com/x", Name: "x", ConfigFields: DefaultConfigFieldNames()})
	if err != nil {
		t.Fatal(err)
	}
	src := string(out)
	if strings.Contains(src, "rootConfigs") || strings.Contains(src, "root.Configs") || !strings.Contains(src, "func serverkitConfig(cfg *config.Config) (serverkit.Config, error) {") {
		t.Errorf("a one-config serve.go changed shape:\n%s", src)
	}
}

// roots_gen.go hands each root's loaded configs to the policy (rootSpec
// Configs) as well as to the graph, and says when a root waits.
func TestRootsGen_HandsTheLoadedConfigsToThePolicy(t *testing.T) {
	views := binConfigViews()
	views[1].AwaitMigrations = true
	if err := markBinConfig(t, binConfigCompose, views); err != nil {
		t.Fatal(err)
	}
	for i := range views {
		views[i].TypeName = strings.ToUpper(views[i].Name[:1]) + views[i].Name[1:]
	}
	pc := processConfigsFrom(binConfigMessages(), "rbc")
	cmds, top := rootCommandViews(views, nil)
	src, err := renderGoTemplate("roots_gen.go.tmpl", rootsGenView{Module: "example.com/rbc", Commands: cmds, Top: top, Fmt: true, ConfigInputs: pc.inputViews()})
	if err != nil {
		t.Fatal(err)
	}
	gen := strings.Join(strings.Fields(string(src)), " ")
	for _, want := range []string{
		"Configs rootConfigs",
		"type rootConfigs struct {",
		"ApiConfig *config.ApiConfig",
		"CliConfig *config.CliConfig",
		"Configs: rootConfigs{ ServerConfig: serverConfig, ApiConfig: apiConfig, },",
		"Configs: rootConfigs{ ServerConfig: serverConfig, WorkerConfig: workerConfig, },",
		"AwaitMigrations: true,",
		"g.ApiConfig = apiConfig",
	} {
		if !strings.Contains(gen, want) {
			t.Errorf("roots_gen.go lacks %q:\n%s", want, src)
		}
	}
	if strings.Count(gen, "AwaitMigrations: true") != 1 {
		t.Errorf("only the worker waits:\n%s", src)
	}
}

// F2's reach: a command config's serve fields are reached by the root whose
// process loads it — so ROOTS hands that workload its own port — and by no
// other.
func TestProcessConfig_ACommandConfigsServeFieldsAreReached(t *testing.T) {
	pc := processConfigsFrom(reliantShapedMessages(), "rbc")
	views := []graphRootView{
		{Name: "api", Command: []string{"server", "api"}, Reach: []string{"DB"}, ReachesDB: true, Migrate: true},
		{Name: "gateway", Command: []string{"server", "gateway"}},
	}
	if err := markProcessConfig(views, pc, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := loadPaths(views[0]), map[string]string{"ServerProcessConfig": "database_url,auto_migrate", "ApiConfig": "port,pre_stop_delay,shutdown_timeout"}; !mapsEqual(got, want) {
		t.Errorf("api loads %v, want %v", got, want)
	}
	if got, want := loadPaths(views[1]), map[string]string{"ServerProcessConfig": "", "GatewayConfig": "port,shutdown_timeout,pprof_addr"}; !mapsEqual(got, want) {
		t.Errorf("gateway loads %v, want %v: a process that never opens the database reaches none of its fields", got, want)
	}
}

func boolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
