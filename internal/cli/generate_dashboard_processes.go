package cli

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/obsdash"
	deployv1 "github.com/reliant-labs/forge/pkg/deploy/v1alpha1"
)

// dashboardSpecForEntities assembles from the already-rendered selected env.
// It does not render, write, retire artifacts, or inspect another environment.
func dashboardSpecForEntities(projectDir string, entities *KCLEntities) (*obsdash.Spec, error) {
	in, ok, err := obsdash.ReadFragments(projectDir)
	if err != nil || !ok {
		return nil, err
	}
	if entities == nil {
		return nil, fmt.Errorf("process dashboard assembly requires the selected environment's rendered workloads")
	}
	module, err := codegen.GetModulePath(projectDir)
	if err != nil {
		return nil, err
	}
	services, err := codegen.ParseDeclaredServices(projectDir)
	if err != nil {
		return nil, err
	}
	cfg := &config.ProjectConfig{Name: in.Project, ModulePath: module}
	ctx := &pipelineContext{AbsPath: projectDir, ProjectDir: projectDir, Cfg: cfg, Services: codegen.ManagedServices(services)}
	in.Processes, err = renderedDashboardProcesses(ctx, entities)
	if err != nil {
		return nil, err
	}
	// The frontend dashboard follows the frontends THIS environment serves, not
	// the project's frontends/ directories (an in-cluster operator console is a
	// container, not a served frontend).
	in.Frontends, in.FrontendServices = frontendIdentities(in.Project, entities.Frontends)
	sp := obsdash.Build(in)
	if len(in.Processes) == 0 {
		sp = obsdash.WithoutProcessDashboards(sp)
	}
	return &sp, nil
}

// frontendIdentities are the frontends a set of rendered frontends serve and
// the service.names they report.
//
// An in-repo frontend reports naming.FrontendServiceName(project, name) (its
// otel_gen.ts). A frontend built from another repository (Source set) reports
// the name THAT repository's generate baked, naming.FrontendServiceName of the
// source project and the source frontend: neither this project's name nor the
// name this environment deploys it under. forge cannot read it from here, so
// the frontend declares it (telemetry.service_name), and that exact name is
// used. One that declares none is guessed: its deployed name and the project
// derivation, which match only when the source repository happens to agree.
//
// frontends may come from several environments, and one deployment name can
// report a different service.name in each (in-repo in dev, sourced in prod).
// The two lists are therefore independent: names are the deployment names,
// deduplicated for display in first-seen order, and services are the sorted
// union of every entry's resolved names, so no environment's browser service
// is dropped because another environment already declared the same frontend
// name, and the result does not depend on the order environments are read in.
func frontendIdentities(project string, frontends []FrontendEntity) (names, services []string) {
	seenName, seenService := map[string]bool{}, map[string]bool{}
	for _, fe := range frontends {
		if fe.Type == "react-native" {
			continue
		}
		if !seenName[fe.Name] {
			seenName[fe.Name] = true
			names = append(names, fe.Name)
		}
		var resolved []string
		switch {
		case fe.Source == nil:
			resolved = []string{naming.FrontendServiceName(project, fe.Name)}
		case fe.Telemetry != nil && fe.Telemetry.ServiceName != "":
			resolved = []string{fe.Telemetry.ServiceName}
		default:
			resolved = []string{naming.FrontendServiceName(project, fe.Name), fe.Name}
		}
		for _, svc := range resolved {
			if !seenService[svc] {
				seenService[svc] = true
				services = append(services, svc)
			}
		}
	}
	sort.Strings(services)
	return names, services
}

// dashboardProcesses reads deployed process identities, never proto package
// names. Multiple environments declaring the same executable/role contribute
// workload aliases to one dashboard; roles of a shared executable stay apart.
func renderedDashboardProcesses(ctx *pipelineContext, entities *KCLEntities) ([]obsdash.Process, error) {
	roots := ctx.graphRoots
	if !ctx.graphRootsSet {
		var err error
		roots, err = codegen.GraphRoots(ctx.AbsPath)
		if err != nil {
			return nil, err
		}
	}
	processes, err := processesForWorkloads(bootstrapBinaryName(ctx.AbsPath), roots, entities.Workloads)
	if err != nil {
		return nil, err
	}
	if err := checkUniqueProcessNames(processes); err != nil {
		return nil, err
	}
	for i := range processes {
		p := &processes[i]
		if p.Root != "" {
			continue
		}
		p.RPCServices, err = codegen.CommandRPCServices(ctx.AbsPath, ctx.Cfg.ModulePath, p.Binary, p.Args, ctx.Services)
		if err != nil {
			return nil, err
		}
		// Legacy composition has no database reach model; do not claim a DB.
	}
	return processes, nil
}

// deployedIdentities is what forge generate scopes the overview with: the
// service.names and workload names of the long-running processes every
// environment declares (workloads, as rendered across all of them), derived
// exactly as a release bundle derives its one environment's processes. Jobs
// and crons are left out: they have their own dashboard. Without it the
// overview's identity filter matched nothing, and every panel of the local
// dashboards drew nothing.
//
// It is identities only. Per-process parts (process dashboards) stay the
// selected environment's, built at bundle time (dashboardSpecForEntities): an
// environment-wide part on disk would claim ownership no single environment
// has, and conflict whenever two branches add an rpc.
func deployedIdentities(ctx *pipelineContext, workloads []WorkloadEntity) (obsdash.Identities, error) {
	// Every environment's workloads together: only identities are wanted, and
	// the same workload name may run different commands in different
	// environments, so process names are not required to be unique here.
	roots := ctx.graphRoots
	if !ctx.graphRootsSet {
		var err error
		if roots, err = codegen.GraphRoots(ctx.AbsPath); err != nil {
			roots = nil // the graph is read for RPC ownership only; a project mid-edit still has identities
		}
	}
	processes, err := processesForWorkloads(bootstrapBinaryName(ctx.AbsPath), roots, workloads)
	if err != nil {
		return obsdash.Identities{}, err
	}
	for i := range processes {
		p := &processes[i]
		if p.Root == "" && len(p.RPCServices) == 0 {
			if svcs, err := codegen.CommandRPCServices(ctx.AbsPath, ctx.Cfg.ModulePath, p.Binary, p.Args, ctx.Services); err == nil {
				p.RPCServices = svcs
			}
		}
	}
	var id obsdash.Identities
	longRunning := map[string]bool{}
	for _, p := range processes {
		if p.IsJob() {
			continue
		}
		id.ServiceNames = append(id.ServiceNames, p.ServiceNames...)
		id.Workloads = append(id.Workloads, p.Workloads...)
		if p.ServesRPC() {
			id.RPCServiceNames = append(id.RPCServiceNames, p.ServiceNames...)
		}
		for _, w := range p.Workloads {
			longRunning[w] = true
		}
	}
	// A limit is declared per WORKLOAD, and a process groups the same command
	// under different workload names in different environments (a limit in prod
	// says nothing about dev's). Read each workload's own declaration instead of
	// the group's OR.
	for _, w := range workloads {
		if !longRunning[w.Name] {
			continue
		}
		if w.Spec.Resources.CPULimitMillicores > 0 {
			id.CPULimitWorkloads = append(id.CPULimitWorkloads, w.Name)
		}
		if w.Spec.Resources.MemoryLimitBytes > 0 {
			id.MemoryLimitWorkloads = append(id.MemoryLimitWorkloads, w.Name)
		}
	}
	id.ServiceNames, id.Workloads = uniqueSorted(id.ServiceNames), uniqueSorted(id.Workloads)
	id.RPCServiceNames = uniqueSorted(id.RPCServiceNames)
	id.CPULimitWorkloads, id.MemoryLimitWorkloads = uniqueSorted(id.CPULimitWorkloads), uniqueSorted(id.MemoryLimitWorkloads)
	return id, nil
}

// processesForWorkloads groups the rendered workloads into dashboard processes.
//
// A process is named for its FORGE workload (the name KCL declares:
// "admin-api", "identity-canary"), never for the rendered Kubernetes object
// (a Job is "<name>-<hash>"), the command line or a hash of its arguments.
// Workloads that run the same executable with the same arguments (one per
// environment, under different names) are one process, named for the first of
// them alphabetically.
//
// A workload forge does not build has no executable forge can see; it is still
// a process, keyed by its own name. A ShellBuild is a sibling repository's Go
// binary (it keeps the Go runtime panels and, as a service with a port, the RPC
// ones); a Docker-built or third-party image is not known to be Go and gets
// neither. Build-only workloads and tools run nothing and are left out.
func processesForWorkloads(primary string, roots []codegen.GraphRoot, workloads []WorkloadEntity) ([]obsdash.Process, error) {
	type group struct {
		p      obsdash.Process
		legacy map[string]bool
	}
	byKey := map[string]*group{}
	var order []string
	for _, w := range workloads {
		if w.Runtime.Type == RuntimeBuildOnly || deployv1.WorkloadKind(w.Kind) == deployv1.KindTool {
			continue
		}
		binary := ""
		if g := w.GoBuild(); g != nil {
			binary = filepath.Base(filepath.Clean(g.Cmd))
		}
		// An explicit entrypoint identifies prebuilt/shell-built Go executables.
		// Do not infer one from an image name or from an RPC package.
		if len(w.Spec.Command) > 0 {
			binary = filepath.Base(w.Spec.Command[0])
		}
		args := w.Spec.Args
		var root *codegen.GraphRoot
		legacyID := ""
		// The kind is part of the identity: a job or cron and a long-running
		// workload that run the same command are different processes (one is
		// drawn on the Jobs dashboard, the other on its own), and workers,
		// operators and services keep the classification they were declared with.
		kind := string(w.Spec.EffectiveKind())
		if w.Kind != "" {
			kind = w.Kind
		}
		key := "ext\x00" + kind + "\x00" + w.Name
		if binary != "" {
			key = binary + "\x00" + kind + "\x00" + strings.Join(args, "\x00")
			role := strings.Join(args, "-")
			if binary == primary {
				for i := range roots {
					if equalArgs(roots[i].Args, args) {
						root = &roots[i]
						role = root.Name
						break
					}
				}
			}
			if role == "" {
				role = "default"
			}
			legacyID = dashboardProcessID(binary, role, args)
		}
		g := byKey[key]
		if g == nil {
			g = &group{p: obsdash.Process{Binary: binary, Args: append([]string(nil), args...)}, legacy: map[string]bool{}}
			if root != nil {
				g.p.Root = root.Name
				g.p.RPCServices = append([]string(nil), root.RPCServices...)
				g.p.HasDatabase = root.HasDatabase
			}
			byKey[key] = g
			order = append(order, key)
		}
		if legacyID != "" {
			g.legacy[legacyID] = true
		}
		p := &g.p
		p.Kind = kind
		p.CPULimit = p.CPULimit || w.Spec.Resources.CPULimitMillicores > 0
		p.MemoryLimit = p.MemoryLimit || w.Spec.Resources.MemoryLimitBytes > 0
		p.NodeMetadata = p.NodeMetadata || w.Runtime.Type == RuntimeCluster || w.Runtime.Type == RuntimeHosted
		// Forge cannot read a sibling-built process's mounts. A service with a
		// port serves something on it, and the RPC metrics are the only
		// server-side series it can have; a worker, a job or a service with no
		// port serves none.
		if binary == "" {
			if w.Build.Type == "shell" {
				p.ServesRPCDeclared = p.ServesRPCDeclared || (deployv1.WorkloadKind(w.Kind) == deployv1.KindService && len(w.Spec.Ports) > 0)
			} else {
				p.NonGo = true
			}
		}
		service := w.Name
		for _, e := range w.EnvVars() {
			if e.Name == "OTEL_SERVICE_NAME" && e.Value != "" {
				service = e.Value
			}
		}
		p.ServiceNames = append(p.ServiceNames, service)
		p.Workloads = append(p.Workloads, w.Name)
	}
	var out []obsdash.Process
	for _, key := range order {
		g := byKey[key]
		p := g.p
		p.ServiceNames = uniqueSorted(p.ServiceNames)
		p.Workloads = uniqueSorted(p.Workloads)
		p.Name = p.Workloads[0]
		for old := range g.legacy {
			if old != p.Name {
				p.Replaces = append(p.Replaces, old)
			}
		}
		sort.Strings(p.Replaces)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// checkUniqueProcessNames fails when two different executables or commands of
// ONE environment share a process name. A workload name is unique within an
// environment, so this cannot happen there; it can across environments (dev and
// prod may run the same workload name with different arguments), which is why
// only the single-environment path checks it and the cross-environment identity
// union (deployedIdentities) does not.
func checkUniqueProcessNames(ps []obsdash.Process) error {
	for i := 1; i < len(ps); i++ {
		if ps[i].Name == ps[i-1].Name {
			return fmt.Errorf("dashboard process name collision %q: two different executables or commands share the workload name", ps[i].Name)
		}
	}
	return nil
}

var processIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

func dashboardProcessID(binary, role string, args []string) string {
	raw := binary + "-" + role
	safe := processIDUnsafe.ReplaceAllString(raw, "-")
	if len(safe) > 100 {
		safe = safe[:100]
	}
	if safe != raw {
		sum := sha256.Sum256([]byte(binary + "\x00" + strings.Join(args, "\x00")))
		safe += fmt.Sprintf("-%x", sum[:6])
	}
	return safe
}

func equalArgs(a, b []string) bool { return strings.Join(a, "\x00") == strings.Join(b, "\x00") }
func uniqueSorted(values []string) []string {
	sort.Strings(values)
	out := values[:0]
	for _, v := range values {
		if len(out) == 0 || out[len(out)-1] != v {
			out = append(out, v)
		}
	}
	return out
}
