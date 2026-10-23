package codegen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// An env's deploy/kcl/<env>/main.k BINDS each workload to where it runs, one
// line per workload (ADR 0002 §2: there is no env-level runtime):
//
//	_workloads = [
//	    _on_host(wl.item)
//	    _on_host_job(wl.migrate)
//	    _on_k3d(wl.reaper)
//	]
//
// Each `_<binder>` is a lambda the env declares — it applies the env's own
// layer (env vars, capacity) and one runtime. The scaffold writes these
// lines, and `forge scaffold <kind>` appends one per env for a new workload,
// so a workload added later is bound exactly like one the project was born
// with.

// DevEnvName is the env the scaffold binds to the local loop (host processes
// plus the local k3d cluster). Every other env is hosted on the forge control
// plane as scaffolded (deploy/kcl/cloud/main.k.tmpl).
const DevEnvName = "dev"

// The binders a scaffolded deployed env declares for its workloads.
const (
	binderHosted    = "_hosted"
	binderOnCluster = "_on_cluster"
)

// EnvBinder names the binder a freshly scaffolded env uses for a workload of
// kind. In the local loop: a host process where one can run, and the local
// cluster where it cannot (a cron is scheduled by Kubernetes, an operator
// watches its API). Elsewhere: the forge control plane for every kind it
// admits, and a cluster you operate for the kinds it refuses (HostedRefusal).
// A tool is never run anywhere.
func EnvBinder(env, kind string) string {
	if kind == WorkloadKindTool {
		return "_build_only"
	}
	if env != DevEnvName {
		if HostedRefusal(kind) != "" {
			return binderOnCluster
		}
		return binderHosted
	}
	switch kind {
	case WorkloadKindCron, WorkloadKindOperator:
		return "_on_k3d"
	case WorkloadKindJob:
		return "_on_host_job"
	default:
		return "_on_host"
	}
}

// HostedRefusal is why the hosted runtime's Restricted profile refuses a
// workload of kind, or "" when it admits it. The control plane is the
// authority (deploytarget.PreflightHosted runs its admission at render); this
// names the kinds a SCAFFOLD must not bind there, so a new workload is never
// bound somewhere it cannot run.
func HostedRefusal(kind string) string {
	switch kind {
	case WorkloadKindCron:
		return "the platform does not run crons yet (they are not metered)"
	case WorkloadKindOperator:
		return "an operator needs the Kubernetes API, which hosted nodes do not expose"
	}
	return ""
}

// EnvBinding is one line of an env's `_workloads` list, as a fresh scaffold
// writes it.
func EnvBinding(env, kind, workloadName string) string {
	return bindingLine(EnvBinder(env, kind), workloadName)
}

// THE HOSTED API IS ONE WORKLOAD.
//
// A browser reaches a project's API at ONE origin: the scaffolded frontend's
// Connect transport has one base URL (a call is `/<package>.<Service>/<Method>`,
// so one origin serves every service), and sign-in answers with an HttpOnly
// session cookie the browser returns to that origin only. The hosted platform
// gives every workload its own hostname and routes no paths between them, so
// an env that hosts each service as its own workload leaves the frontend
// able to reach one of them — and signed in to none of the others. That is
// not fixable in the browser: per-service URLs would still strand the cookie.
//
// So a hosted env declares its API once, as `_api`: the project binary's
// all-in-one `server` command, which mounts every service on one Connect mux
// and supervises every worker beside it, in one process. It binds `_api`
// instead of its services and workers. workloads.k still declares each of
// them, and dev still runs each as its own process.
const (
	// APIWorkloadName is the hosted API workload's name: its platform
	// hostname, and what the frontend's API_URL references.
	APIWorkloadName = "api"
	// APIWorkloadIdent is the env-local KCL identifier it is declared as.
	APIWorkloadIdent = "_api"
	// ServerSubcommand is the project binary's all-in-one command
	// (cmd-tree-server.go.tmpl): every service mounted, every worker and
	// operator supervised.
	ServerSubcommand = "server"
)

// RunsInServer reports whether a workload of kind is part of what the project
// binary's `server` runs: a service (mounted on its mux) or a worker
// (supervised beside it). Jobs run to completion on their own; an operator
// needs the Kubernetes API, which a hosted `server` does not have (it logs
// that operators are disabled and serves without them); a tool never runs.
func RunsInServer(kind string) bool {
	return kind == WorkloadKindService || kind == WorkloadKindWorker
}

// APIWorkloadStanza renders a hosted env's `_api` declaration: the project
// binary's `server` as one service workload. The facts are the ones
// WorkloadStanza gives every component of that binary — the image, the build,
// the `http` port, the credential the DI graph reads — with `server` as the
// subcommand, so `_api` and the services it runs cannot disagree about them.
//
// extraSecrets are credentials THIS env hands the API beyond the database —
// the dev env's login-broker token, which only an env running the dev IdP
// has. They are an env fact, not a workload fact, so the caller passes them.
func APIWorkloadStanza(modulePath, projectName string, extraSecrets ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s = fw.Workload {\n", APIWorkloadIdent)
	fmt.Fprintf(&b, "    name = %q\n", APIWorkloadName)
	fmt.Fprintf(&b, "    kind = %q\n", WorkloadKindService)
	fmt.Fprintf(&b, "    image = %q\n", ScaffoldImageRef(modulePath, projectName))
	fmt.Fprintf(&b, "    build = %s\n", projectGoBuild(projectName))
	fmt.Fprintf(&b, "    args = %s\n", kclStringList([]string{ServerSubcommand}))
	// The composition graph's default root: config_gen.workloadEnvMap gives
	// the API exactly the config `server` reaches.
	fmt.Fprintf(&b, "    root = %q\n", ServerSubcommand)
	fmt.Fprintf(&b, "    ports = [fw.Port {name = \"http\", port = %d, expose = True}]\n", config.DefaultServePort)
	fmt.Fprintf(&b, "    config_secrets = %s\n", kclStringList(append([]string{"DATABASE_URL"}, extraSecrets...)))
	b.WriteString("}")
	return b.String()
}

// workloadLiteralIdent is workloadLiteral with the declared identifier
// captured.
var workloadLiteralIdent = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)\s*=\s*fw\.Workload\s*\{`)

// EnvServerWorkload returns the identifier of a workload an env's main.k
// declares FOR ITSELF that runs the project binary's `server` — a hosted
// env's `_api`. Read from the source, like DeclaredWorkloads, so scaffold and
// `forge env new` need no render. Comments and docstrings are stripped first,
// so the worked examples in a scaffolded file are not mistaken for one.
func EnvServerWorkload(content, projectName string) (string, bool) {
	src := StripKCLProse(content)
	for _, m := range workloadLiteralIdent.FindAllStringSubmatchIndex(src, -1) {
		body, ok := kclBlockBody(src, m[1])
		if !ok {
			continue
		}
		args := kclStringListField(body, "args")
		build := goBuildCmd.FindStringSubmatch(body)
		if len(args) > 0 && args[0] == ServerSubcommand && build != nil && ProjectBinaryRuns(projectName, build[1]) {
			return src[m[2]:m[3]], true
		}
	}
	return "", false
}

// identRef matches a whole KCL identifier.
func identRef(ident string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(ident) + `\b`)
}

func bindingLine(binder, workloadName string) string {
	return fmt.Sprintf("    %s(wl.%s)", binder, naming.KCLIdentifier(workloadName))
}

// envBinderIn is the binder for a NEW workload of kind in an existing env,
// whose `_workloads` body is given: bound the way that env already binds its
// workloads, so a project keeps the shape it has.
//
// Only a hosted-admissible kind outside dev has a choice to make — everything
// else binds as EnvBinder says, because the alternative is a binding that
// cannot run. That kind follows the env's migrate job: the new workload reads
// the database migrate migrates, so it runs where migrate runs. An env
// scaffolded before hosting was the default binds migrate `_on_cluster`, and
// so keeps binding there; a hosted env that put one cron on its own cluster
// still binds a new service `_hosted`. With no migrate binding to follow, any
// hosted binding wins, then any cluster binding, then the scaffold default.
func envBinderIn(body, env, kind string) string {
	def := EnvBinder(env, kind)
	if def != binderHosted {
		return def
	}
	if m := migrateBinder.FindStringSubmatch(body); len(m) == 2 && (m[1] == binderHosted || m[1] == binderOnCluster) {
		return m[1]
	}
	switch {
	case strings.Contains(body, binderHosted+"(wl."):
		return binderHosted
	case strings.Contains(body, binderOnCluster+"(wl."):
		return binderOnCluster
	}
	return def
}

// migrateBinder captures the binder of an env's migrate job binding.
var migrateBinder = regexp.MustCompile(`(?m)^\s*(_[a-z_]+)\(wl\.` + MigrateWorkloadName + `\)`)

// envWorkloadsList matches the scaffolded `_workloads = [` ... `]` block,
// capturing its body. The closing bracket is the first line that is exactly
// `]`, which is how the templates write it.
//
//nolint:gocritic // badRegexp misreads the second `^`: under (?m) it anchors the closing `]` at a line start.
var envWorkloadsList = regexp.MustCompile(`(?ms)^_workloads = \[\n(.*?)^\]$`)

// EnvBindingResult is what AppendEnvBinding did to one env.
type EnvBindingResult struct {
	// Applied is true when a line was written.
	Applied bool
	// Binder is the binder of the line written.
	Binder string
	// Bound is what that line binds: `wl.<ident>` for the new workload, or
	// the env's own API workload (`_api`) when that is what runs it.
	Bound string
	// ServedBy names the env's own API workload when it is already bound and
	// already runs the new workload, so nothing was written.
	ServedBy string
}

// AppendEnvBinding adds the binding for a new workload to one env's main.k,
// at the end of its `_workloads` list, and reports what it wrote and with
// which binder (envBinderIn: the way that env binds its workloads).
//
// In an env that serves its API as one `server` workload (a hosted env's
// `_api`, see EnvServerWorkload), a new service or worker is already run by
// it, so it gets no line of its own — a second, separate workload would be
// one the browser cannot reach, or a worker run twice. If that API workload
// is not bound yet (a project born with no service), it is bound instead.
//
// Like AppendWorkloadStanza it never rewrites what is there: nothing is
// written when the file is missing, already binds the workload (`wl.<ident>`
// anywhere in the list), or has been restructured past the point where the
// list is unambiguous. The caller prints the line.
func AppendEnvBinding(projectDir, projectName, env, kind, workloadName string) (EnvBindingResult, error) {
	path := filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return EnvBindingResult{}, nil
		}
		return EnvBindingResult{}, err
	}
	content := string(raw)
	locs := envWorkloadsList.FindAllStringSubmatchIndex(content, -1)
	if len(locs) != 1 {
		return EnvBindingResult{}, nil
	}
	body := StripKCLProse(content[locs[0][2]:locs[0][3]])
	ref := regexp.MustCompile(`\bwl\.` + regexp.QuoteMeta(naming.KCLIdentifier(workloadName)) + `\b`)
	if ref.MatchString(body) {
		return EnvBindingResult{}, nil
	}
	res := EnvBindingResult{Binder: envBinderIn(body, env, kind), Bound: "wl." + naming.KCLIdentifier(workloadName)}
	line := bindingLine(res.Binder, workloadName)
	ownLine := true
	if api, ok := EnvServerWorkload(content, projectName); ok && RunsInServer(kind) {
		if identRef(api).MatchString(body) {
			return EnvBindingResult{ServedBy: api}, nil
		}
		res.Binder, res.Bound = envBinderIn(body, env, WorkloadKindService), api
		line = fmt.Sprintf("    %s(%s)", res.Binder, api)
		ownLine = false
	}
	updated := content[:locs[0][3]] + line + "\n" + content[locs[0][3]:]
	if kind == WorkloadKindService && ownLine {
		updated = claimAPIPortKey(updated, body, workloadName)
	}
	if err := atomicfile.WriteFile(path, []byte(updated), 0o644); err != nil {
		return EnvBindingResult{}, err
	}
	res.Applied = true
	return res, nil
}

// EnvDeclaresNoCluster reports whether an env's main.k still carries the
// scaffold's `_cluster = None`: a workload bound `_on_cluster` there fails
// the render, naming itself, until the author declares the cluster.
func EnvDeclaresNoCluster(projectDir, env string) bool {
	raw, err := os.ReadFile(filepath.Join(projectDir, "deploy", "kcl", env, "main.k"))
	if err != nil {
		return false
	}
	return noClusterDecl.MatchString(StripKCLProse(string(raw)))
}

var noClusterDecl = regexp.MustCompile(`(?m)^_cluster\s*=\s*None\s*$`)

// apiPortOwner matches the scaffolded dev env's `_port_of` lambda, capturing
// the workload that owns the `<project>-<env>-api` port key:
//
//	plugin.resolve_port("acme-dev-api" if name == "acme" else "acme-dev-" + name, 8085)
//
// That key is the one the frontend's dev config (config.k) resolves for its
// API origin, so whichever workload binds it is the API the frontend dials.
var apiPortOwner = regexp.MustCompile(`(resolve_port\("[^"]*-api" if name == )"([^"]+)"`)

// claimAPIPortKey hands the API port key to a newly bound service when the
// workload holding it is not bound in the env at all.
//
// A project created with no service names ITSELF as the key's owner — there
// is no workload of that name — so the frontend's config.k claimed the key
// alone (8085) and the first `forge scaffold service` bound the new service
// under its own key, which stepped to the next free port (8086). The
// frontend then dialled a port nothing listened on. Transferring the key to
// the first service that actually exists keeps the two on one number. An
// owner that IS bound (a project born with a service) is never displaced.
func claimAPIPortKey(content, priorBody, workloadName string) string {
	m := apiPortOwner.FindStringSubmatchIndex(content)
	if m == nil {
		return content
	}
	owner := content[m[4]:m[5]]
	ownerRef := regexp.MustCompile(`\bwl\.` + regexp.QuoteMeta(naming.KCLIdentifier(owner)) + `\b`)
	if ownerRef.MatchString(StripKCLProse(priorBody)) {
		return content
	}
	// The lambda compares the workload's declared `name`, not its
	// component name: `admin_server` is declared `name = "admin-server"`.
	return content[:m[4]] + WorkloadName(workloadName) + content[m[5]:]
}

// EnvBindingHint is the message shown when a binding could not be appended.
func EnvBindingHint(env, kind, workloadName string) string {
	return fmt.Sprintf("Bind workload %q in deploy/kcl/%s/main.k — add to its `_workloads` list:\n\n%s\n",
		workloadName, env, strings.TrimRight(EnvBinding(env, kind, workloadName), "\n"))
}
