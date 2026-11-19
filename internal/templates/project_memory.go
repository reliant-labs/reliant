package templates

// ProjectMemoryData is what the framework memory template
// (project/reliant.md.tmpl) renders from — on disk for a harness that reads a
// file (`forge project new`), and live for reliant (cli.RenderProjectMemory).
//
// The zero value of every field past CLI renders the guide of the project
// `forge project new` scaffolds — a forge Go service — so a caller that only
// knows the name gets today's guide. The fields that describe a project AS IT
// IS are filled by cli.RenderProjectMemory from the tree and forge.yaml
// (Derived), and choose which sections render: the memory of an adopted app
// must not hand its agent forge's greenfield layout as the architecture, or
// `go build` and `task test` in a repo that has neither.
type ProjectMemoryData struct {
	// Name is the project's name (forge.yaml `name:`).
	Name string
	// CLI is how the user invokes forge: "forge", or "reliant forge" when
	// embedded.
	CLI string
	// Site reports a static site: a project with no Go module, made of the
	// app at its root and its deploy envs (`forge project new --kind site`).
	// Its memory is the frontend-only quick reference. Derived from the tree
	// (config.DiscoverRootFrontend), like every other fact about a project's
	// shape, so no flag can disagree with what is on disk.
	Site bool
	// Container reports an adopted app forge did not write, deployed from its
	// own Dockerfile: workloads in deploy/kcl/workloads.k (the `adopt`
	// skill, `forge project new --in-place --kind app`). It implies Adopted
	// and a deploy tree with workloads, so a caller that sets only this
	// (rendering before the tree exists) still gets the deploy sections.
	Container bool
	// Adopted reports an existing app forge did not build, adopted as it is:
	// not a site, none of a forge service's sources (pkg/app,
	// internal/handlers, proto/services), and either no Go module or only
	// the switches an adoption writes (deploy/kcl/workloads.k, forge.yaml
	// `monitoring:` / `envs:`). Its memory is composed from the pieces of
	// forge it uses, under a preamble that says the app keeps its own
	// language, layout and tooling.
	Adopted bool

	// Derived reports that the fields below were read off the project
	// (cli.RenderProjectMemory). Without it they are unknown, not absent,
	// and the guide describes the scaffold forge writes.
	Derived bool
	// GoModule reports a go.mod at the project root.
	GoModule bool
	// DeployEnvs are the envs deploy/kcl declares (deploy/kcl/<env>/main.k),
	// sorted; empty when there is no deploy tree.
	DeployEnvs []string
	// Workloads reports deploy/kcl/workloads.k: the app's workloads,
	// declared once and bound per env.
	Workloads bool
	// DeclaredEnvs are forge.yaml `envs:`: environments forge knows of but
	// does not deploy (no deploy/kcl/<env>/).
	DeclaredEnvs []string
	// Monitoring reports forge.yaml `monitoring:` switched on: forge's
	// observability for an app forge may not deploy.
	Monitoring bool
	// Database is how the project's schema migrations are applied, nil when
	// the project shows none.
	Database *MemoryDatabase
	// Frontends are the frontends in the tree (the load-time inventory).
	Frontends []MemoryFrontend
	// Unmanaged are the proto services marked `// forge:unmanaged`: their
	// handlers stay where the project already keeps them.
	Unmanaged []string
	// Gate is the project's own test / lint / build commands.
	Gate MemoryGate
}

// MemoryDatabase is the project's migration tool, as the memory names it.
type MemoryDatabase struct {
	// Tool names the migration tool: forge, goose, prisma, alembic, ...
	Tool string
	// Evidence is the file or directory it was recognised by, relative to
	// the project root.
	Evidence string
	// Apply is the command that applies pending migrations, "" when unknown.
	Apply string
	// Hook reports that forge.yaml `database.migrations` declares the tool,
	// so forge runs it wherever it would apply its own migrations.
	Hook bool
}

// MemoryFrontend is one frontend in the tree.
type MemoryFrontend struct {
	Name string
	Type string
	Dir  string
}

// MemoryGate is the project's own commands, from the first source that
// declares any: a Taskfile, then the root package.json's scripts, then a
// Makefile. Empty when none does.
type MemoryGate struct {
	// Source is the file the commands come from (Taskfile.yml,
	// package.json, Makefile); "" when none was found.
	Source string
	// Runner is the program that runs them: task, npm, pnpm, yarn, bun,
	// make.
	Runner string
	// Commands are the gate-relevant ones, tests first.
	Commands []GateCommand
}

// GateCommand is one command and what it is for.
type GateCommand struct {
	// Run is the command line, e.g. `npm run lint`.
	Run string
	// Does says what it is for: tests, lint, build, ...
	Does string
}
