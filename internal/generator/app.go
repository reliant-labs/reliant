package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
	"github.com/reliant-labs/forge/pkg/deploy"
)

// `forge project new --in-place --kind app` — an existing app in any
// language, adopted onto forge as it is: built from its own Dockerfile,
// declared in forge.yaml + deploy/kcl, and nothing else. No go.mod, no
// internal/, no Taskfile, no CI workflow, no memory file (unless a harness
// is asked for). The files are the ones the `adopt` skill's Tier 1
// templates describe, filled with what the CLI detected
// (internal/cli/new_app_detect.go); this file only lays them out.

// AppSpec is what an adopted app's forge files declare.
type AppSpec struct {
	ProjectName string
	// Workloads are what workloads.k declares and staging/prod host: the
	// app's own images (Build set) and third-party images forge copies into
	// the org's registry (Build nil).
	Workloads []AppWorkload
	// Migrate is the pre-rollout migration job, nil when none was found.
	Migrate *AppMigration
	// Database is the Postgres the app uses, nil when it uses none.
	Database *AppDatabase
	// ComposeFile is the compose file dev runs (relative to the root), ""
	// when dev runs each workload as a host process instead.
	ComposeFile string
	// DevOnly are compose services dev runs and staging/prod do not
	// declare: the app's Postgres (managed there), dev tools, and anything
	// forge cannot host for it.
	DevOnly []AppDevService
	// Claimable scaffolds prod for an anonymous deploy (`forge env deploy
	// prod --claimable`), sized to the claimable plan.
	Claimable bool
}

// AppWorkload is one thing the app runs.
type AppWorkload struct {
	// Name is the workload name (DNS label); Ident its KCL identifier in
	// workloads.k.
	Name  string
	Ident string
	// Kind is "service" (serves Port) or "worker".
	Kind string
	// Image is the bare image name for a build, or the full third-party
	// reference (its registry host included).
	Image string
	Build *AppBuild
	// Port is the port the process listens on (0: none); PortSource says
	// where that came from, and is written beside it.
	Port       int
	PortSource string
	// Expose publishes Port on a public URL (the app's own services); a
	// third-party image (a cache) is reached inside the env only.
	Expose bool
	// Env is the literal env workloads.k declares (every env).
	Env map[string]string
	// Secrets are credentials each env delivers from its secret store.
	Secrets []string
	// UsesDatabase delivers the database's URL in every env, under each
	// of DatabaseEnv (DATABASE_URL when empty).
	UsesDatabase bool
	DatabaseEnv  []string
	// ComposeService is the compose service dev runs it as ("" with no
	// compose file).
	ComposeService string
	// Command overrides the image's entrypoint+CMD on every runtime: a
	// compose service's `command`, which dev runs and hosting must run too
	// (a worker sharing the web Dockerfile). Nil runs the image's own.
	Command       []string
	CommandSource string
	// DevCommand is the host process dev runs (no compose file); DevDir its
	// working directory relative to the root ("" = the root).
	DevCommand       []string
	DevCommandSource string
	DevDir           string
	// Notes are `# check:` lines written above its declaration.
	Notes []string
}

// AppBuild is a forge.DockerBuild.
type AppBuild struct {
	Dockerfile string
	Context    string
	Target     string
	Args       map[string]string
}

// AppMigration is the job that migrates the schema before every rollout.
type AppMigration struct {
	// Workload is the AppWorkload whose image (and build) it runs.
	Workload string
	Command  []string
	// Source says where Command came from.
	Source string
}

// AppDatabase is the app's Postgres: a managed database on hosting, the
// compose service (or a forge host Postgres) in dev.
type AppDatabase struct {
	// Name is the managed database's name and the dev database.
	Name string
	// ComposeService is the compose service dev runs it as ("": a forge
	// host-native Postgres).
	ComposeService string
}

// AppDevService is a compose service only dev runs.
type AppDevService struct {
	Name           string
	ComposeService string
	Why            string
}

// AppFile is one file the adoption writes.
type AppFile struct {
	Path    string
	Content []byte
}

// AppGitignore is what an adoption adds to .gitignore: the machine-local
// state forge writes, and never the ledger it shares.
const AppGitignore = siteGitignore

// RenderAppScaffold lays out an adopted app's forge files: forge.yaml,
// deploy/kcl/kcl.mod, deploy/kcl/workloads.k and the dev / staging / prod
// envs, in that order. It writes nothing (`--dry-run` prints them).
func RenderAppScaffold(spec AppSpec, harness Harness) ([]AppFile, error) {
	cfg := config.ProjectConfig{Name: spec.ProjectName, ForgeVersion: buildinfo.Version()}
	if harness != "" {
		cfg.Harness = string(harness.Normalized())
	}
	data, err := yaml.Marshal(config.NormalizeForWrite(&cfg))
	if err != nil {
		return nil, fmt.Errorf("marshal project config: %w", err)
	}
	header := "# Forge project manifest — see https://github.com/reliant-labs/forge.\n" +
		"# An adopted app: it builds from its own Dockerfile, and deploy/kcl/\n" +
		"# declares what it runs (workloads.k) and where (<env>/main.k). There is\n" +
		"# no Go module, so there is no module_path. `forge skill load adopt`.\n\n"
	kclMod, err := templates.DeployTemplates().Render("kcl/kcl.mod.tmpl", struct{ ProjectName string }{spec.ProjectName})
	if err != nil {
		return nil, fmt.Errorf("render kcl.mod template: %w", err)
	}
	files := []AppFile{
		{"forge.yaml", append([]byte(header), data...)},
		{"deploy/kcl/kcl.mod", kclMod},
		{kclModLockPath, nil},
		{"deploy/kcl/workloads.k", []byte(appWorkloadsK(spec))},
		{"deploy/kcl/" + codegen.DevEnvName + "/main.k", []byte(appDevMainK(spec))},
	}
	for _, env := range hostedEnvNames() {
		files = append(files, AppFile{"deploy/kcl/" + env + "/main.k", []byte(appHostedMainK(spec, env, spec.Claimable && env == "prod"))})
	}
	return files, nil
}

// WriteAppScaffold writes RenderAppScaffold's files into dir (a staging
// directory the caller merges into the app), its .gitignore block, and the
// harness's agent files when one was asked for. The app's memory is the
// any-language one (ProjectMemoryData.Container).
func WriteAppScaffold(dir string, spec AppSpec, harness Harness) error {
	files, err := RenderAppScaffold(spec, harness)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := writeSiteFile(dir, filepath.FromSlash(f.Path), f.Content); err != nil {
			return err
		}
	}
	if harness != "" {
		data := ProjectMemoryData{Name: spec.ProjectName, CLI: cliName(), Container: true}
		for _, f := range harness.Normalized().agentFiles() {
			dest := filepath.Join(dir, f.path)
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			if err := writeIfAbsent(dest, f.tmpl, data); err != nil {
				return fmt.Errorf("write %s: %w", f.path, err)
			}
		}
	}
	return appendSiteGitignore(dir)
}

// ---------------------------------------------------------------------------
// workloads.k
// ---------------------------------------------------------------------------

func appWorkloadsK(spec AppSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\"\"\"What %s runs, declared once. Each env's main.k binds it.\n\n", spec.ProjectName)
	b.WriteString("Written by `forge project new --in-place --kind app` from what it found in\n" +
		"this repo, and yours from here: forge never rewrites it. A `# check:` line is\n" +
		"something it assumed; read each one. `forge skill load adopt` explains every\n" +
		"field (Tier 1), and what to add next.\n\"\"\"\n")
	b.WriteString("import forge\nimport forge.workloads as fw\nimport forge.tiers\n")
	for _, w := range spec.Workloads {
		b.WriteString("\n")
		for _, n := range w.Notes {
			fmt.Fprintf(&b, "# check: %s\n", n)
		}
		fmt.Fprintf(&b, "%s = fw.Workload {\n", w.Ident)
		fmt.Fprintf(&b, "    name = %s\n", kclStr(w.Name))
		fmt.Fprintf(&b, "    kind = %s\n", kclStr(w.Kind))
		if w.Build != nil {
			fmt.Fprintf(&b, "    # Bare: hosting puts the build under <registry>/<org>/%s/%s.\n", spec.ProjectName, w.Image)
			fmt.Fprintf(&b, "    image = %s\n", kclStr(w.Image))
			fmt.Fprintf(&b, "    build = %s\n", kclDockerBuild(*w.Build))
		} else {
			b.WriteString("    # A third-party image, registry host included: at release cut hosting\n" +
				"    # copies it into your org's registry and pins it by digest.\n")
			fmt.Fprintf(&b, "    image = %s\n", kclStr(w.Image))
		}
		if len(w.Command) > 0 {
			// args, not command: compose's `command` replaces the image's CMD
			// and keeps its ENTRYPOINT, and so does a workload's args.
			fmt.Fprintf(&b, "    args = %s  # check: %s\n", kclList(w.Command), w.CommandSource)
		}
		if w.Port > 0 {
			fmt.Fprintf(&b, "    ports = [fw.Port {name = \"http\", port = %d, expose = %s}]  # %s\n", w.Port, kclBool(w.Expose), w.PortSource)
		}
		if env := w.declaredEnv(); len(env) > 0 {
			fmt.Fprintf(&b, "    env = %s\n", kclEnvDict(env, "    "))
		}
		if w.Port > 0 {
			b.WriteString("    # Up once it accepts connections. With a health endpoint, probe it:\n" +
				"    # tiers.Probes {readinessPath = \"/health\"}.\n" +
				"    probes = tiers.Probes {tcp = True}\n")
		}
		b.WriteString("}\n")
	}
	if m := spec.Migrate; m != nil {
		owner := spec.workload(m.Workload)
		b.WriteString("\n# Schema migrations, from the same image, before every rollout. The image\n" +
			"# must carry the migration tool. Keep migrations backward-compatible: the\n" +
			"# running release serves while they apply.\n")
		b.WriteString("migrate = fw.Workload {\n    name = \"migrate\"\n    kind = \"job\"\n")
		fmt.Fprintf(&b, "    image = %s.image\n    build = %s.build\n", owner.Ident, owner.Ident)
		fmt.Fprintf(&b, "    command = %s  # check: %s\n", kclList(m.Command), m.Source)
		if len(owner.declaredEnv()) > 0 {
			// The job runs the owner's image with the owner's configuration
			// (FLASK_APP, DJANGO_SETTINGS_MODULE, APP_ENV, ...).
			fmt.Fprintf(&b, "    env = %s.env\n", owner.Ident)
		}
		b.WriteString("    before = [fw.BEFORE_ALL]\n}\n")
	}
	return b.String()
}

// migrateEnvAdds is the env the migration job gets in an env's main.k: the
// database URL under DATABASE_URL and under every name its owning workload
// reads it from (SQLALCHEMY_DATABASE_URI), plus the owner's managed secrets
// when the env delivers them. The job runs the owner's image with the owner's
// configuration, so it must see the same names the owner does.
func migrateEnvAdds(spec AppSpec, secrets bool) []string {
	owner := spec.workload(spec.Migrate.Workload)
	names := []string{"DATABASE_URL"}
	if owner.UsesDatabase {
		for _, n := range owner.databaseEnv() {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	adds := make([]string, 0, len(names)+len(owner.Secrets))
	for _, n := range names {
		adds = append(adds, "env."+n+" = _database_url")
	}
	if secrets {
		for _, s := range owner.Secrets {
			adds = append(adds, fmt.Sprintf("env.%s = forge.ManagedSecret {name = %s}", s, kclStr(s)))
		}
	}
	return adds
}

// declaredEnv is the literal env workloads.k declares for w: its own, plus
// PORT for a built service that listens.
func (w AppWorkload) declaredEnv() map[string]string {
	env := map[string]string{}
	for k, v := range w.Env {
		env[k] = v
	}
	if w.Port > 0 && w.Build != nil {
		if _, ok := env["PORT"]; !ok {
			env["PORT"] = fmt.Sprint(w.Port)
		}
	}
	return env
}

// databaseEnv is where w reads the database's URL.
func (w AppWorkload) databaseEnv() []string {
	if len(w.DatabaseEnv) == 0 {
		return []string{"DATABASE_URL"}
	}
	return w.DatabaseEnv
}

func (s AppSpec) workload(name string) AppWorkload {
	for _, w := range s.Workloads {
		if w.Name == name {
			return w
		}
	}
	return AppWorkload{Ident: name}
}

func kclDockerBuild(bld AppBuild) string {
	parts := []string{"dockerfile = " + kclStr(bld.Dockerfile), "context = " + kclStr(emptyAsDot(bld.Context))}
	if bld.Target != "" {
		parts = append(parts, "target = "+kclStr(bld.Target))
	}
	if len(bld.Args) > 0 {
		parts = append(parts, "build_args = "+kclInlineDict(bld.Args))
	}
	return "forge.DockerBuild {" + strings.Join(parts, ", ") + "}"
}

func emptyAsDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

// ---------------------------------------------------------------------------
// dev
// ---------------------------------------------------------------------------

func appDevMainK(spec AppSpec) string {
	if spec.ComposeFile != "" {
		return appDevComposeMainK(spec)
	}
	return appDevHostMainK(spec)
}

// appDevComposeMainK runs the app's own compose file: dev is what `docker
// compose up` ran before forge, every service included.
func appDevComposeMainK(spec AppSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\"\"\"dev: the app's own %s, as `docker compose up` runs it.\n\n", spec.ComposeFile)
	b.WriteString("`forge env up dev` brings up every service below through compose, which\n" +
		"builds and runs the containers exactly as before forge: the file stays the\n" +
		"one place their dev settings live. Logs land in .forge/logs/dev/.\n\"\"\"\n")
	b.WriteString("import forge\nimport forge.workloads as fw\nimport ..workloads as wl\n\n")
	fmt.Fprintf(&b, "# The compose file owns the container (build, ports, healthcheck, command,\n"+
		"# env), so dev takes only the workload's name from workloads.k.\n"+
		"_compose = lambda w: fw.Workload, service: str -> fw.Workload {\n"+
		"    fw.Workload {name = w.name, runtime = forge.OnCompose {file = %s, service = service}}\n}\n\n", kclStr(spec.ComposeFile))
	b.WriteString("_bundle = forge.Bundle {\n")
	fmt.Fprintf(&b, "    project = %s\n    lifecycle = \"local\"\n    workloads = [\n", kclStr(spec.ProjectName))
	for _, d := range spec.DevOnly {
		fmt.Fprintf(&b, "        fw.Workload {name = %s, runtime = forge.OnCompose {file = %s, service = %s}}  # %s\n",
			kclStr(d.Name), kclStr(spec.ComposeFile), kclStr(d.ComposeService), d.Why)
	}
	for _, w := range spec.Workloads {
		fmt.Fprintf(&b, "        _compose(wl.%s, %s)\n", w.Ident, kclStr(w.ComposeService))
	}
	b.WriteString("    ]\n}\noutput = forge.render(_bundle)\n")
	return b.String()
}

// appDevHostMainK runs each workload as a host process (the app's own dev
// command) beside a forge host-native Postgres: the adopt skill's dev.
func appDevHostMainK(spec AppSpec) string {
	var b strings.Builder
	what := "the app as a host process"
	if spec.Database != nil {
		what += ", beside a host-native Postgres"
	}
	fmt.Fprintf(&b, "\"\"\"dev: %s.\n\n", what)
	b.WriteString("`forge env up dev` runs each workload's own dev command on this machine\n" +
		"(install the app's dependencies first), with PORT set to a free port")
	if spec.Database != nil {
		b.WriteString(" and\nDATABASE_URL to the Postgres forge runs for it")
	}
	b.WriteString(".\nLogs land in .forge/logs/dev/.\n\"\"\"\n")
	b.WriteString("import forge\nimport forge.workloads as fw\nimport kcl_plugin.forge as plugin\nimport ..workloads as wl\n\n")
	if db := spec.Database; db != nil {
		fmt.Fprintf(&b, "_pg_port = plugin.resolve_port(%s, 5432)\n", kclStr(spec.ProjectName+"-dev-postgres"))
		fmt.Fprintf(&b, "_database_url = \"postgres://postgres:postgres@localhost:${_pg_port}/%s?sslmode=disable\"\n\n", db.Name)
	}
	b.WriteString("_on_host = lambda w: fw.Workload, command: [str], port: int, cwd: str -> fw.Workload {\n" +
		"    w | {\n" +
		"        command = command\n" +
		"        env = w.env | ({PORT = str(port)} if port else {})\n" +
		"        runtime = forge.OnHost {listen_ports = [port] if port else [], working_dir = cwd}\n" +
		"    }\n}\n\n")
	for _, w := range spec.Workloads {
		if w.Port > 0 {
			fmt.Fprintf(&b, "_%s_port = plugin.resolve_port(%s, %d)\n", w.Ident, kclStr(spec.ProjectName+"-dev-"+w.Name), w.Port)
		}
	}
	b.WriteString("\n_bundle = forge.Bundle {\n")
	fmt.Fprintf(&b, "    project = %s\n    lifecycle = \"local\"\n    workloads = [\n", kclStr(spec.ProjectName))
	if m := spec.Migrate; m != nil {
		owner := spec.workload(m.Workload)
		fmt.Fprintf(&b, "        _on_host(wl.migrate, %s, 0, %s) | {%s}  # check: %s\n",
			kclList(m.Command), kclStr(emptyAsDot(owner.DevDir)), strings.Join(migrateEnvAdds(spec, true), ", "), m.Source)
	}
	for _, w := range spec.Workloads {
		port := "0"
		if w.Port > 0 {
			port = "_" + w.Ident + "_port"
		}
		line := fmt.Sprintf("        _on_host(wl.%s, %s, %s, %s)", w.Ident, kclList(w.DevCommand), port, kclStr(emptyAsDot(w.DevDir)))
		var adds []string
		if w.UsesDatabase && spec.Database != nil {
			for _, n := range w.databaseEnv() {
				adds = append(adds, "env."+n+" = _database_url")
			}
		}
		for _, s := range w.Secrets {
			adds = append(adds, fmt.Sprintf("env.%s = forge.ManagedSecret {name = %s}", s, kclStr(s)))
		}
		if len(adds) > 0 {
			line += " | {" + strings.Join(adds, ", ") + "}"
		}
		fmt.Fprintf(&b, "%s  # check: %s\n", line, w.DevCommandSource)
	}
	b.WriteString("    ]\n")
	if db := spec.Database; db != nil {
		fmt.Fprintf(&b, "    infra = [forge.HostInfra {name = \"postgres\", port = _pg_port, database = %s}]\n", kclStr(db.Name))
	}
	b.WriteString("    # Dev credentials: the gitignored secrets/dev.yaml (`NAME: value`).\n" +
		"    secret_provider = forge.FileSecrets {path = \"secrets/dev.yaml\"}\n}\noutput = forge.render(_bundle)\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// staging / prod
// ---------------------------------------------------------------------------

func appHostedMainK(spec AppSpec, env string, claimable bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\"\"\"%s: on Reliant hosting", env)
	if spec.Database != nil {
		b.WriteString(", with a managed Postgres")
	}
	b.WriteString(".\n\n")
	b.WriteString("Each workload in workloads.k runs on the platform (forge.OnHosted), built\n" +
		"once and promoted between envs by digest. Credentials come from the\n" +
		"platform's write-only store: `forge secret set --env " + env + " <NAME>` (the\n" +
		"value on stdin). Hosted workloads and databases need billing on your\n" +
		"organization; `forge env deploy` prints the link when it is missing.\n")
	if claimable {
		caps := deploy.ClaimableLimitsFallback()
		r := caps.Resources()
		fmt.Fprintf(&b, "\nANONYMOUS, AS SCAFFOLDED (--claimable): `forge env deploy %s --claimable`\n"+
			"publishes with no account and prints a claim link for a human. An unclaimed\n"+
			"deploy is one service with its migration job and one database, each sized\n"+
			"to the plan (%dm / %d MiB), takes no secret values, and has NO outbound\n"+
			"internet: DNS and its own Postgres only. Once it is claimed, delete\n"+
			"`claimable = True`, the `resources` line and the database's `spec`.\n",
			env, r.CPURequestMillicores, r.MemoryRequestBytes>>20)
	}
	b.WriteString("\"\"\"\nimport forge\nimport forge.workloads as fw\nimport ..workloads as wl\n\n")
	if db := spec.Database; db != nil {
		b.WriteString("# Its name may not be a workload's. RETAIN: deleting it never drops the data.\n")
		fmt.Fprintf(&b, "_db = forge.ManagedDatabase {\n    name = %s\n    runtime = forge.OnHosted {}\n", kclStr(db.Name))
		if claimable {
			fmt.Fprintf(&b, "    spec = {storageGiB = %d}\n", deploy.ClaimableLimitsFallback().DatabaseDeclaredStorageGiB)
		}
		b.WriteString("}\n")
		b.WriteString("_database_url = forge.DatabaseRef {name = _db.name}\n\n")
	}
	if claimable {
		r := deploy.ClaimableLimitsFallback().Resources()
		fmt.Fprintf(&b, "# The claimable plan's largest shape, request and limit, no burst.\n"+
			"_claimable_resources = fw.Resources {\n"+
			"    cpuRequestMillicores = %d\n    cpuLimitMillicores = %d\n"+
			"    memoryRequestBytes = forge.mib(%d)\n    memoryLimitBytes = forge.mib(%d)\n}\n\n",
			r.CPURequestMillicores, r.CPULimitMillicores, r.MemoryRequestBytes>>20, r.MemoryLimitBytes>>20)
		b.WriteString("_hosted = lambda w: fw.Workload -> fw.Workload {\n" +
			"    w | {\n        runtime = forge.OnHosted {}\n        resources = _claimable_resources\n    }\n}\n\n")
	} else {
		b.WriteString("_hosted = lambda w: fw.Workload -> fw.Workload {\n" +
			"    w | {runtime = forge.OnHosted {}}\n}\n\n")
	}
	b.WriteString("_bundle = forge.Bundle {\n")
	fmt.Fprintf(&b, "    project = %s\n", kclStr(spec.ProjectName))
	if claimable {
		b.WriteString("    control_plane = forge.ControlPlane {claimable = True}\n")
	} else {
		b.WriteString("    control_plane = forge.ControlPlane {}\n")
	}
	b.WriteString("    workloads = [\n")
	if m := spec.Migrate; m != nil {
		b.WriteString("        _hosted(wl.migrate) | {" + strings.Join(migrateEnvAdds(spec, !claimable), ", ") + "}\n")
	}
	for _, w := range spec.Workloads {
		var adds []string
		if w.UsesDatabase && spec.Database != nil {
			for _, n := range w.databaseEnv() {
				adds = append(adds, "env."+n+" = _database_url")
			}
		}
		if !claimable {
			for _, s := range w.Secrets {
				adds = append(adds, fmt.Sprintf("env.%s = forge.ManagedSecret {name = %s}", s, kclStr(s)))
			}
		}
		line := fmt.Sprintf("        _hosted(wl.%s)", w.Ident)
		if len(adds) > 0 {
			line += " | {" + strings.Join(adds, ", ") + "}"
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("    ]\n")
	if spec.Database != nil {
		b.WriteString("    databases = [_db]\n")
	}
	b.WriteString("    secret_provider = forge.HostedSecrets {}\n}\noutput = forge.render(_bundle)\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// KCL literals
// ---------------------------------------------------------------------------

// kclStr is s as a KCL string expression. KCL interpolates `${…}` inside a
// string, so a literal "${" is spliced in from two strings.
func kclStr(s string) string {
	parts := strings.Split(s, "${")
	quoted := make([]string, len(parts))
	for i, p := range parts {
		quoted[i] = kclQuote(p)
	}
	return strings.Join(quoted, ` + "$" + "{" + `)
}

func kclQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	return `"` + r.Replace(s) + `"`
}

func kclBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

func kclList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = kclStr(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// kclKey is an env name as a KCL dict key: bare when it is an identifier,
// quoted otherwise.
func kclKey(k string) string {
	for i, r := range k {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return kclQuote(k)
		}
	}
	return k
}

func kclInlineDict(m map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, kclKey(k)+" = "+kclStr(m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func kclEnvDict(m map[string]string, indent string) string {
	if len(m) <= 2 {
		return kclInlineDict(m)
	}
	var b strings.Builder
	b.WriteString("{\n")
	for _, k := range sortedKeys(m) {
		fmt.Fprintf(&b, "%s    %s = %s\n", indent, kclKey(k), kclStr(m[k]))
	}
	b.WriteString(indent + "}")
	return b.String()
}
