package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// WorkloadsKCLRelPath is the project-relative path of the workload
// declaration — what the project DEPLOYS, one typed literal per deployable
// unit (an image + args + placement).
//
// It is SCAFFOLDED ONCE and owned by the project from then on: forge writes
// it when the file does not exist and appends to it when `forge scaffold`
// adds a component; it never rewrites or reformats what is already there.
// Deploy has exactly one source of truth, it is KCL, and it is tracked in
// git — so a fresh clone renders with nothing generated first.
const WorkloadsKCLRelPath = "deploy/kcl/workloads.k"

// IdentityKCLRelPath is the one ManagedIdentity declaration every env imports.
const IdentityKCLRelPath = "deploy/kcl/identity.k"

// WorkloadsKCLExists reports whether the project already declares its
// workloads. Callers use it to decide between scaffolding the file and
// appending to it; neither path ever overwrites existing content.
func WorkloadsKCLExists(projectDir string) bool {
	_, err := os.Stat(filepath.Join(projectDir, WorkloadsKCLRelPath))
	return err == nil
}

// MigrateArgs returns the subcommand of the project binary that applies its
// embedded migrations (`db migrate up`, cmd-tree-db.go.tmpl).
//
// It is the workload's `args`, not a container argv: the image's ENTRYPOINT
// is the binary (Dockerfile.tmpl), and the host runtime derives
// `go run ./cmd/<project> db migrate up` from the same build + args. One
// declaration selects the subcommand on every runtime, so it cannot drift
// from the image or the host process it runs in.
//
// This is a SCAFFOLD-TIME default only. It is written literally into
// deploy/kcl/workloads.k once, and the project owns it from then on: how a
// system migrates is an operational decision that differs per environment and
// changes over time (an env may migrate out of band, or run a different
// tool entirely). forge does not re-derive it.
func MigrateArgs() []string {
	return []string{"db", "migrate", "up"}
}

// MigrateWorkloadName is the name of the scaffolded migration workload. It
// is a workload name like any other, so it must be a legal DNS-1123 label —
// which is also why it can never collide with the `*` broadcast selector.
const MigrateWorkloadName = "migrate"

// MigrateWorkloadStanza renders the deploy-time migration step as the KCL
// literal that belongs in deploy/kcl/workloads.k.
//
// It is an ORDINARY WORKLOAD — `kind = "job"` with the broadcast `before`
// — and that is the entire point. Migration used to be a bespoke
// `migrate: [str]` field on WorkloadEnv that the render layer had special
// knowledge of. It is now one instance of the one-shot primitive, so it
// reads, refines, and can be dropped exactly like anything else the
// project ships.
//
// `before = [fw.BEFORE_ALL]` rather than an enumerated list of dependents
// is the load-bearing choice. An enumerated list is a list that goes
// STALE: add a workload six months from now and the list still renders,
// still passes every check, and silently does not gate the new one — a
// pod serving traffic against a schema it does not have. The broadcast
// selector has nothing to forget to update.
//
// The `build` names the project's own binary, the same GoBuild every
// component declares: forge builds a (cmd, output) pair once however many
// workloads name it, so this compiles nothing extra. It is what lets the
// host runtime derive `go run ./cmd/<project> db migrate up`, and the
// cluster runtime pick the image, from one declaration.
//
// `config_secrets = ["DATABASE_URL"]`: the job needs the DSN and nothing
// else, so it is the one credential projected into it.
func MigrateWorkloadStanza(modulePath, projectName string) string {
	var b strings.Builder
	b.WriteString("# The deploy-time schema migration — YOURS to change.\n")
	b.WriteString("#\n")
	b.WriteString("# `before = [fw.BEFORE_ALL]` is the BROADCAST form: it gates EVERY workload\n")
	b.WriteString("# in whichever environment deploys it, without naming any of them. On a\n")
	b.WriteString("# cluster that is an initContainer on each dependent, so the schema is\n")
	b.WriteString("# current BEFORE any new pod serves a request and a failed migration stalls\n")
	b.WriteString("# the rollout with the old pods still serving. On the host runtime forge\n")
	b.WriteString("# runs it to completion before starting anything it gates, and on the\n")
	b.WriteString("# hosted runtime the control plane does the same. Concurrent replicas are\n")
	b.WriteString("# safe — golang-migrate takes a postgres advisory lock.\n")
	b.WriteString("#\n")
	b.WriteString("# Do NOT replace the wildcard with a list of workload names. The list would\n")
	b.WriteString("# go stale the next time someone adds a workload, and the failure is silent:\n")
	b.WriteString("# it renders, it passes every check, and the new workload just is not gated.\n")
	b.WriteString("#\n")
	b.WriteString("# An environment that migrates OUT OF BAND (a DBA-run pipeline, a managed\n")
	b.WriteString("# database console, a separate release train) drops this workload from the\n")
	b.WriteString("# list it deploys, in deploy/kcl/<env>/main.k:\n")
	b.WriteString("#\n")
	b.WriteString("#     workloads = [w for w in wl.ALL if w.name != \"" + MigrateWorkloadName + "\"]\n")
	b.WriteString("#\n")
	b.WriteString("# To run a different tool entirely, set `command` (the argv, verbatim).\n")
	fmt.Fprintf(&b, "%s = fw.Workload {\n", naming.KCLIdentifier(MigrateWorkloadName))
	fmt.Fprintf(&b, "    name = %q\n", MigrateWorkloadName)
	fmt.Fprintf(&b, "    kind = %q\n", WorkloadKindJob)
	fmt.Fprintf(&b, "    image = %q\n", ScaffoldImageRef(modulePath, projectName))
	fmt.Fprintf(&b, "    build = %s\n", projectGoBuild(projectName))
	fmt.Fprintf(&b, "    args = %s\n", kclStringList(MigrateArgs()))
	b.WriteString("    before = [fw.BEFORE_ALL]\n")
	b.WriteString("    config_secrets = [\"DATABASE_URL\"]\n")
	b.WriteString("}\n")
	return b.String()
}

// kclStringList formats a Go string slice as a KCL list literal.
func kclStringList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, s := range items {
		quoted = append(quoted, fmt.Sprintf("%q", s))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// projectGoBuild is the typed build of the project's primary binary, as a
// KCL literal. The project name is used VERBATIM (hyphens preserved): `go
// build ./cmd/<hyphenated>` is valid, because it is a directory path rather
// than a package identifier.
func projectGoBuild(projectName string) string {
	return goBuild("./cmd/"+projectName, projectName)
}

// ScaffoldImageRef is the image reference a scaffolded workload declares: the
// FULL reference, registry host included, because that is the only place a
// registry is declared — no environment carries one.
//
// The registry is derived from the module path's GitHub owner
// (github.com/acme/shop → ghcr.io/acme/shop), which a GitHub-hosted project
// can push to with nothing but its workflow token.
//
// A module path that names no GitHub owner gets a placeholder HOST rather than
// a bare name, and the shape of that placeholder is load-bearing:
//
//   - It must contain a dot, so it parses as a registry host. A bare
//     `REPLACE_ME_REGISTRY/shop` does NOT — Docker's rule is that the first
//     segment is a host only if it has a `.` or `:` — so the render would
//     refuse the scaffold forge itself just wrote, and `forge env new --check`
//     could not compile a fresh project.
//   - It must be obviously unreal. `.invalid` is reserved by RFC 2606 and can
//     never resolve, so a scaffold that reaches a real push fails at the
//     placeholder instead of silently pushing somewhere that happens to exist.
//
// So the tree compiles, the value is visibly a placeholder, and the failure —
// if anyone tries to push it — names a host that provably belongs to nobody.
func ScaffoldImageRef(modulePath, projectName string) string {
	if owner := githubOwnerFromModulePath(modulePath); owner != "" {
		return "ghcr.io/" + strings.ToLower(owner) + "/" + projectName
	}
	return ScaffoldPlaceholderRegistry + "/" + projectName
}

// ScaffoldPlaceholderRegistry is the registry host a scaffolded image carries
// when forge cannot derive one. See ScaffoldImageRef for why it is shaped like
// a host and why the TLD is `.invalid`.
const ScaffoldPlaceholderRegistry = "REPLACE-ME-REGISTRY.invalid"

// githubOwnerFromModulePath is the owner segment of a github.com module path
// (github.com/acme/shop → "acme"), or "" for any other host or a path too
// short to name one.
func githubOwnerFromModulePath(modulePath string) string {
	const host = "github.com/"
	if !strings.HasPrefix(modulePath, host) {
		return ""
	}
	rest := modulePath[len(host):]
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return ""
	}
	return rest[:slash]
}

func goBuild(cmd, outputName string) string {
	return fmt.Sprintf("forge.GoBuild {cmd = %q, output_name = %q}", cmd, outputName)
}

// ProjectBinaryRuns reports whether a workload declared with build command
// buildCmd runs the project's primary binary — the `./cmd/<project>` GoBuild
// every scaffolded stanza names (projectGoBuild).
func ProjectBinaryRuns(projectName, buildCmd string) bool {
	return strings.TrimPrefix(strings.TrimSuffix(buildCmd, "/"), "./") == "cmd/"+projectName
}

// RunsBuiltinSubcommand reports whether args, handed to the project binary,
// select a command the generated cmd tree itself owns rather than a
// component: `server` (every component in one process), `version`, `db …`
// (the scaffolded migrate job),
//
// Judged by the FIRST arg, the top-level subcommand, so every leaf under a
// built-in (`db migrate status`, a later `auth` verb) counts without being
// listed. The set is derived, not restated: the top-level names the cmd tree
// reserves (reservedSubcommandNames) plus the command each scaffolded job's
// args select (MigrateArgs).
func RunsBuiltinSubcommand(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return reservedSubcommandNames[args[0]] || args[0] == MigrateArgs()[0]
}

// WorkloadStanza renders ONE workload as the typed KCL literal that belongs
// in deploy/kcl/workloads.k. It is the single formatter behind both the
// initial scaffold and `forge scaffold <kind>`'s append, so a workload added
// later is indistinguishable from one the project was born with.
//
// The emitted declaration is COMPLETE and RUNTIME-INDEPENDENT (ADR 0002): the
// build, the subcommand in `args`, the ports, the credentials it reads. It
// says nothing about WHERE it runs — each env binds it to a runtime
// (`forge.OnHost`, `OnCluster`, `OnHosted`, ...) and the same `args` select the
// subcommand on every one: `go run ./cmd/<p> <args>` on the host, the image's
// ENTRYPOINT `/app/<p>` plus `<args>` in a pod. Per-env facts (replicas,
// resources, env values) are refined in deploy/kcl/<env>/main.k.
//
// No probes: the lowering writes `/readyz` + `/healthz` on the `http` port
// into the spec of every service forge builds, on every runtime, which is
// exactly what serverkit serves.
func WorkloadStanza(modulePath, projectName string, c config.ComponentConfig) string {
	var b strings.Builder
	kind := WorkloadKindFor(c.EffectiveKind())

	fmt.Fprintf(&b, "%s = fw.Workload {\n", naming.KCLIdentifier(c.Name))
	fmt.Fprintf(&b, "    name = %q\n", WorkloadName(c.Name))
	fmt.Fprintf(&b, "    kind = %q\n", kind)

	// The image, registry INCLUDED — a workload declares its own registry,
	// and no environment declares one. A secondary binary is its own program
	// and so its own image; everything else ships the project image and
	// selects its behaviour with `args`.
	if c.EffectiveKind() == config.ComponentKindBinary {
		fmt.Fprintf(&b, "    image = %q\n", ScaffoldImageRef(modulePath, naming.ServicePackage(c.Name)))
	} else {
		fmt.Fprintf(&b, "    image = %q\n", ScaffoldImageRef(modulePath, projectName))
	}

	// Build target and subcommand. A secondary binary is its OWN program:
	// it builds its own cmd/<binpkg> package and runs with no subcommand.
	// Every other component is a subcommand of the project binary
	// (`<bin> <component>`, generated under cmd/<bin>/cmd/{services,workers,
	// operators}/), which runs exactly that component — one service's
	// routes, one worker, one operator — rather than `server`, which runs
	// them all.
	if c.EffectiveKind() == config.ComponentKindBinary {
		binPkg := naming.ServicePackage(c.Name)
		fmt.Fprintf(&b, "    build = %s\n", goBuild("./cmd/"+binPkg, binPkg))
	} else {
		fmt.Fprintf(&b, "    build = %s\n", projectGoBuild(projectName))
		fmt.Fprintf(&b, "    args = %s\n", kclStringList([]string{componentSubcommand(c)}))
	}
	if kind == WorkloadKindOperator {
		if c.Group != "" {
			fmt.Fprintf(&b, "    group = %q\n", c.Group)
		}
		if c.Version != "" {
			fmt.Fprintf(&b, "    version = %q\n", c.Version)
		}
		crds := make([]string, 0, len(c.CRDs))
		for _, crd := range c.CRDs {
			crds = append(crds, fmt.Sprintf("%q", crd.Name))
		}
		fmt.Fprintf(&b, "    crds = [%s]\n", strings.Join(crds, ", "))
	}

	// A service serves the standard mux on the one port forge itself knows
	// (config.DefaultServePort), named `http` — the port the default probes
	// target and the one an env's routes address.
	if kind == WorkloadKindService {
		fmt.Fprintf(&b, "    ports = [fw.Port {name = \"http\", port = %d, expose = True}]\n",
			config.DefaultServePort)
	}
	// Every scheduled component of the project binary reads the database:
	// the binary constructs the whole DI graph whichever subcommand runs.
	// A tool is never scheduled, so it reads nothing.
	if kind != WorkloadKindTool {
		b.WriteString("    config_secrets = [\"DATABASE_URL\"]\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// WorkloadName is the `name` a component's workload declares in
// deploy/kcl/workloads.k: the component name as a DNS-1123 label, which
// fw.Workload requires because it becomes every object's name. A component
// is named like a Go package (`engine_shadow`, `admin_server`); its
// workload is `engine-shadow`, declared under the KCL identifier
// `engine_shadow`.
//
// KCLIdentifier(WorkloadName(c)) == KCLIdentifier(c) for every component
// name, so code that matches a declared workload to a component compares
// the two through KCLIdentifier (SameWorkload) and never depends on the
// spelling.
func WorkloadName(component string) string {
	return strings.ReplaceAll(naming.KCLIdentifier(component), "_", "-")
}

// SameWorkload reports whether two names spell the same workload: a
// declared `name` and a component name, in any of their forms.
func SameWorkload(a, b string) bool {
	return naming.KCLIdentifier(a) == naming.KCLIdentifier(b)
}

// componentSubcommand is the cobra subcommand the project binary registers
// for a component: CmdServiceCommand's kebab form for a service (the same
// derivation the cmd tree uses), and the component's own name for a worker
// or operator (cmd-worker-group / cmd-operator-group `Use:`).
func componentSubcommand(c config.ComponentConfig) string {
	if c.EffectiveKind() == config.ComponentKindServer || c.EffectiveKind() == "" {
		if runtime, _, ok := CmdServiceCommand(c.Name); ok {
			return runtime
		}
	}
	return c.Name
}

// The KCL workload kinds — how a deployable unit RUNS. These are the values
// of `forge.workloads.Workload.kind` and are deliberately a different axis
// from config.ComponentKind*, which describes what forge SCAFFOLDS (whether
// a component gets a proto, a cmd/ dir, bootstrap wiring). Two components of
// different Go kinds can deploy identically; WorkloadKindFor is the mapping.
const (
	WorkloadKindService  = "service"
	WorkloadKindWorker   = "worker"
	WorkloadKindCron     = "cron"
	WorkloadKindJob      = "job"
	WorkloadKindOperator = "operator"
	WorkloadKindTool     = "tool"
)

// WorkloadKindFor maps a scaffolded component kind onto the KCL workload
// kind that deploys it.
//
// The two axes are NOT the same, which is the whole reason this function
// exists rather than a cast:
//
//   - config.ComponentKindServer names a Connect-RPC surface — a code fact.
//     It deploys as a `service` (Deployment + Service).
//
//   - config.ComponentKindBinary names a second cmd/<name>/main.go — a build
//     fact. It deploys as a `tool`: forge builds it into the image, but
//     nothing schedules it. It is run on demand (`kubectl exec`, a CI step),
//     which is exactly what the old `binary` deploy kind meant in practice —
//     its manifests were an unscheduled Deployment nobody addressed.
//
//   - config.ComponentKindCron is a worker whose process runs an IN-PROCESS
//     scheduler (worker-cron: robfig/cron, Start blocks until shutdown). It
//     deploys as a long-running `worker`. A Kubernetes CronJob would start
//     that process on the schedule and wait for an exit that never comes.
//
// An unrecognized kind falls back to `service`: a plain Deployment+Service is
// the least surprising thing to render for something forge has no name for.
func WorkloadKindFor(componentKind string) string {
	switch componentKind {
	case config.ComponentKindWorker:
		return WorkloadKindWorker
	case config.ComponentKindCron:
		// See above: the scheduler lives in the process.
		return WorkloadKindWorker
	case config.ComponentKindJob:
		return WorkloadKindJob
	case config.ComponentKindOperator:
		return WorkloadKindOperator
	case config.ComponentKindBinary:
		return WorkloadKindTool
	default:
		return WorkloadKindService
	}
}
