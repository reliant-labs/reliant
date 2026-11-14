package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// projectMemoryData describes the project rooted at root for the framework
// memory: which of forge's pieces it uses (config.ReadSurface — the loader's
// own derivation) and what its own commands are. name is forge.yaml `name:`.
//
// A forge Go service — the project `forge project new` scaffolds, forge's own
// repositories included — renders the Go guide exactly as before; everything
// read here only adds to it where the project differs (unmanaged services, no
// Taskfile). An adopted app gets a memory composed of the pieces it uses.
func projectMemoryData(root, name string) (templates.ProjectMemoryData, error) {
	s, err := config.ReadSurface(root)
	if err != nil {
		return templates.ProjectMemoryData{}, err
	}
	d := templates.ProjectMemoryData{
		Name:         name,
		CLI:          Name(),
		Site:         s.RootFrontend,
		Derived:      true,
		GoModule:     s.GoModule,
		DeployEnvs:   s.DeployEnvs,
		Workloads:    s.Workloads,
		DeclaredEnvs: s.Envs,
		Monitoring:   s.Monitoring,
		Gate:         detectGate(root),
	}
	// Adopted: an app forge did not build. Not a site, none of a forge
	// service's sources, and either not Go at all or only what an
	// adoption writes (workloads.k, the monitoring/envs switches, a kept
	// migration tool). A Go module with none of those — forge itself, a
	// `--kind cli` or `--kind library` project — keeps the Go guide.
	// module_path counts as Go: forge writes it only for a Go module.
	goProject := s.GoModule || s.Config.ModulePath != ""
	adoptionOnly := s.Workloads || s.Monitoring || len(s.Envs) > 0 || s.MigrationTool != ""
	d.Adopted = !d.Site && !s.ServerSources && (!goProject || adoptionOnly)
	d.Container = d.Adopted && s.Workloads
	for _, fe := range s.Config.Frontends {
		dir, _ := fe.Dir(root)
		d.Frontends = append(d.Frontends, templates.MemoryFrontend{Name: fe.Name, Type: fe.Type, Dir: dir})
	}
	if d.Adopted {
		d.Database = detectMigrations(root, s)
	}
	if s.ServerSources {
		d.Unmanaged = unmanagedServices(root)
	}
	return d, nil
}

// unmanagedServices names the proto services marked `// forge:unmanaged`,
// read with the raw scan generate itself uses before the descriptor exists.
func unmanagedServices(root string) []string {
	scan, err := codegen.ScanRawProtoDir(filepath.Join(root, "proto", "services"))
	if err != nil {
		return nil
	}
	var out []string
	for _, svc := range scan.Services {
		if svc.Unmanaged {
			out = append(out, svc.Name)
		}
	}
	return out
}

// migrationProbe recognises a migration tool by a file or directory it keeps
// in the repo.
type migrationProbe struct {
	path  string // relative to the root; a trailing "/" means a directory
	tool  string
	apply string
}

// migrationProbes are the tools an adopted app is likeliest to bring, most
// specific first. Detection only names the tool; nothing here runs it.
func migrationProbes() []migrationProbe {
	return []migrationProbe{
		{"prisma/schema.prisma", "prisma", "npx prisma migrate deploy"},
		{"alembic.ini", "alembic", "alembic upgrade head"},
		{"manage.py", "django", "python manage.py migrate"},
		{"artisan", "laravel", "php artisan migrate"},
		{"db/migrate/", "rails", "bin/rails db:migrate"},
		{"atlas.hcl", "atlas", "atlas migrate apply"},
		{"flyway.toml", "flyway", "flyway migrate"},
		{"flyway.conf", "flyway", "flyway migrate"},
		{"liquibase.properties", "liquibase", "liquibase update"},
		{"drizzle.config.ts", "drizzle", "npx drizzle-kit migrate"},
		{"drizzle.config.js", "drizzle", "npx drizzle-kit migrate"},
		{"knexfile.ts", "knex", "npx knex migrate:latest"},
		{"knexfile.js", "knex", "npx knex migrate:latest"},
		{".sequelizerc", "sequelize", "npx sequelize-cli db:migrate"},
	}
}

// detectMigrations names how an adopted app's schema migrations are applied:
// the tool forge.yaml `database.migrations` declares, else one recognised by
// its files, else a db/migrations directory whose tool is unknown. nil when
// the project shows no migrations.
func detectMigrations(root string, s config.Surface) *templates.MemoryDatabase {
	if s.MigrationTool != "" {
		dir := s.MigrationsDir
		if dir == "" {
			dir = config.DefaultMigrationsDir
		}
		return &templates.MemoryDatabase{Tool: s.MigrationTool, Evidence: dir, Hook: s.MigrationTool != "forge"}
	}
	for _, p := range migrationProbes() {
		rel := strings.TrimSuffix(p.path, "/")
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil || info.IsDir() != strings.HasSuffix(p.path, "/") {
			continue
		}
		return &templates.MemoryDatabase{Tool: p.tool, Evidence: p.path, Apply: p.apply}
	}
	dir := s.MigrationsDir
	if dir == "" {
		dir = config.DefaultMigrationsDir
	}
	if !dirExists(filepath.Join(root, filepath.FromSlash(dir))) {
		return nil
	}
	return &templates.MemoryDatabase{Tool: sqlMigrationTool(filepath.Join(root, filepath.FromSlash(dir))), Evidence: dir + "/"}
}

// sqlMigrationTool reads the tool off a SQL migrations directory by the
// markers its files carry: goose's `-- +goose Up`, dbmate's `-- migrate:up`,
// forge's `-- forge:` declarations. "" when no file says.
func sqlMigrationTool(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		switch text := string(body); {
		case strings.Contains(text, "+goose Up"):
			return "goose"
		case strings.Contains(text, "-- migrate:up"):
			return "dbmate"
		case strings.Contains(text, "-- forge:"):
			return "forge"
		}
	}
	return ""
}

// gateRole is one kind of command the gate section names, with the names a
// Taskfile task, a package.json script or a Makefile target carries for it.
type gateRole struct {
	does  string
	names []string
}

func gateRoles() []gateRole {
	return []gateRole{
		{"tests", []string{"test"}},
		{"fast tests while iterating", []string{"test:short", "test:unit", "test-unit", "test:fast"}},
		{"lint", []string{"lint"}},
		{"typecheck", []string{"typecheck", "type-check", "tsc"}},
		{"build", []string{"build"}},
		{"the whole gate", []string{"ci", "check", "verify"}},
	}
}

// detectGate finds the project's own test / lint / build commands: a
// Taskfile's tasks, else the root package.json's scripts, else a Makefile's
// targets — the first source that names any of them. A source that exists
// but names none is still reported (its Source set, no Commands), so the
// memory can point at it rather than claim there is nothing.
func detectGate(root string) templates.MemoryGate {
	var fallback templates.MemoryGate
	for _, src := range []func(string) (templates.MemoryGate, map[string]bool){taskfileSource, packageJSONSource, makefileSource} {
		g, names := src(root)
		if g.Source == "" {
			continue
		}
		for _, role := range gateRoles() {
			for _, n := range role.names {
				if names[n] {
					g.Commands = append(g.Commands, templates.GateCommand{Run: gateInvocation(g.Runner, n), Does: role.does})
					break
				}
			}
		}
		if len(g.Commands) > 0 {
			return g
		}
		if fallback.Source == "" {
			fallback = g
		}
	}
	return fallback
}

// gateInvocation is how runner runs the task/script/target named n.
func gateInvocation(runner, n string) string {
	switch runner {
	case "task", "make":
		return runner + " " + n
	case "bun":
		return "bun run " + n
	default: // npm, pnpm, yarn
		if n == "test" {
			return runner + " test"
		}
		return runner + " run " + n
	}
}

func taskfileSource(root string) (templates.MemoryGate, map[string]bool) {
	for _, f := range []string{"Taskfile.yml", "Taskfile.yaml", "taskfile.yml", "taskfile.yaml", "Taskfile.dist.yml", "Taskfile.dist.yaml"} {
		body, err := os.ReadFile(filepath.Join(root, f)) //nolint:gosec // project root + fixed filename
		if err != nil {
			continue
		}
		var doc struct {
			Tasks map[string]yaml.Node `yaml:"tasks"`
		}
		_ = yaml.Unmarshal(body, &doc) // a Taskfile we cannot parse still names the runner
		names := make(map[string]bool, len(doc.Tasks))
		for n := range doc.Tasks {
			names[n] = true
		}
		return templates.MemoryGate{Source: f, Runner: "task"}, names
	}
	return templates.MemoryGate{}, nil
}

// npmDefaultTest is the `test` script `npm init` writes: a placeholder that
// fails, not a test suite.
const npmDefaultTest = `echo "Error: no test specified" && exit 1`

func packageJSONSource(root string) (templates.MemoryGate, map[string]bool) {
	body, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return templates.MemoryGate{}, nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	_ = json.Unmarshal(body, &pkg)
	names := make(map[string]bool, len(pkg.Scripts))
	for n, cmd := range pkg.Scripts {
		if n == "test" && strings.TrimSpace(cmd) == npmDefaultTest {
			continue
		}
		names[n] = true
	}
	return templates.MemoryGate{Source: "package.json", Runner: nodeRunner(root)}, names
}

// nodeRunner is the package manager the root's lockfile names; npm when none.
func nodeRunner(root string) string {
	for _, lf := range []struct{ file, runner string }{
		{"pnpm-lock.yaml", "pnpm"}, {"yarn.lock", "yarn"}, {"bun.lockb", "bun"}, {"bun.lock", "bun"},
	} {
		if fileExists(filepath.Join(root, lf.file)) {
			return lf.runner
		}
	}
	return "npm"
}

var makeTargetRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_.:/-]*)\s*:([^=]|$)`)

func makefileSource(root string) (templates.MemoryGate, map[string]bool) {
	for _, f := range []string{"GNUmakefile", "makefile", "Makefile"} {
		fh, err := os.Open(filepath.Join(root, f)) //nolint:gosec // project root + fixed filename
		if err != nil {
			continue
		}
		names := map[string]bool{}
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			if m := makeTargetRE.FindStringSubmatch(sc.Text()); m != nil {
				names[m[1]] = true
			}
		}
		_ = fh.Close()
		return templates.MemoryGate{Source: f, Runner: "make"}, names
	}
	return templates.MemoryGate{}, nil
}
