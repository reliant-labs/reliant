package scaffold

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
)

// declareWorkloadInKCL appends the freshly-scaffolded workload to the
// project's user-owned deploy/kcl/workloads.k, so what the project
// DEPLOYS stays a declaration the user can read, edit and diff.
//
// It re-reads the component inventory rather than trusting the scaffold verb,
// because discovery is what knows the component's real shape: a worker
// package carrying a Schedule const is a cron, and an operator's
// group/version/CRDs are read from its package. The verb only knows the name
// the user typed.
//
// FAILURE IS ADVISORY, on purpose. The component's code is already written
// and every other step has succeeded; a workloads.k that could not be
// appended to is a one-line paste, not a reason to fail a scaffold. Whatever
// happens, the user is told which of the two it was.
func declareWorkloadInKCL(root string, cfg *config.ProjectConfig, spec componentSpec) {
	if cfg == nil {
		return
	}
	inv := codegen.DiscoverProjectComponents(root, binaryName(cfg, root))
	comp, found := inv.Named(spec.name)
	if !found {
		// Discovery cannot see it yet — a service whose proto has not been
		// compiled into the descriptor, say. Print the stanza so the
		// declaration is never silently skipped.
		fmt.Printf("\n📝 %s\n", codegen.WorkloadStanzaHint(cfg.ModulePath, cfg.Name, config.ComponentConfig{
			Name: spec.name,
			Kind: kindFromCtxLabel(spec.ctxLabel),
		}))
		return
	}

	// A component another workload's process already runs gets no workload
	// of its own: one would deploy it a second time, as its own Deployment.
	// And in a project that groups components (`serves`), forge cannot see
	// which group a new one belongs to — that lives in the project's Go — so
	// it shows the choice rather than declaring the workload the project
	// most likely does not want. Lint reads the file the same way
	// (codegen.ServingWorkload), so what scaffold leaves out, lint does not
	// then report missing.
	if content, ok := readWorkloadsKCL(root); ok && !codegen.WorkloadDeclared(content, comp.Name) {
		declared := codegen.DeclaredWorkloads(content)
		if by, served := codegen.ServingWorkload(declared, cfg.Name, comp); served {
			fmt.Printf("   - %s (%s '%s' runs in workload '%s'; nothing to declare)\n",
				codegen.WorkloadsKCLRelPath, comp.EffectiveKind(), comp.Name, by)
			return
		}
		if codegen.DeclaresServes(declared) {
			fmt.Printf("\n📝 %s\n", codegen.ServesChoiceHint(cfg.ModulePath, cfg.Name, comp))
			return
		}
		// A project that runs its components of this kind in processes it
		// composes itself — control-plane mounts every service into
		// `public-api` or `admin-api` — gets no workload per component:
		// the new one belongs in one of those, which only its Go knows.
		if composed := codegen.ComposedProcesses(declared, cfg.Name, inv, comp); len(composed) > 0 {
			fmt.Printf("\n📝 %s\n", codegen.ComposedProcessHint(cfg.ModulePath, cfg.Name, comp, composed))
			return
		}
	}

	applied, err := codegen.AppendWorkloadStanza(root, cfg.ModulePath, cfg.Name, comp)
	content, _ := readWorkloadsKCL(root)
	declared := codegen.WorkloadDeclared(content, comp.Name)
	switch {
	case err != nil:
		fmt.Printf("\n⚠️  could not update %s: %v\n\n%s\n",
			codegen.WorkloadsKCLRelPath, err, codegen.WorkloadStanzaHint(cfg.ModulePath, cfg.Name, comp))
	case applied:
		fmt.Printf("   - %s (%s '%s' declared)\n",
			// Report the WORKLOAD kind that was written, not the component
			// kind it was derived from: the user is being told what is now in
			// the file, and `forge scaffold binary` writes kind="tool".
			codegen.WorkloadsKCLRelPath, codegen.WorkloadKindFor(comp.EffectiveKind()), comp.Name)
	case declared:
		// A re-run (--resume, --force): the declaration is already there.
		fmt.Printf("   - %s ('%s' already declared)\n", codegen.WorkloadsKCLRelPath, comp.Name)
	default:
		// The file has been restructured past the point where an append is
		// unambiguous. Show, do not guess.
		fmt.Printf("\n📝 %s\n", codegen.WorkloadStanzaHint(cfg.ModulePath, cfg.Name, comp))
	}
	// Bind only what is declared. An env binding names `wl.<ident>`, so
	// binding a workload whose declaration was printed rather than written
	// would leave every env's main.k referencing a name that does not exist.
	if declared {
		bindWorkloadInEnvs(root, cfg.Name, comp)
	}
}

// readWorkloadsKCL returns the project's deploy/kcl/workloads.k, and false
// when there is none to read.
func readWorkloadsKCL(root string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, codegen.WorkloadsKCLRelPath))
	if err != nil {
		return "", false
	}
	return string(raw), true
}

// bindWorkloadInEnvs adds the new workload's binding to every env's
// `_workloads` list. There is no env-level runtime (ADR 0002 §2), so a
// workload declared in workloads.k runs nowhere until an env binds it; the
// scaffold binds it the way that env binds its other workloads.
// Advisory like the declaration: an env that cannot be edited
// unambiguously gets the line printed instead.
//
// A kind the hosted platform refuses (a cron, an operator) is bound to a
// cluster you operate in every deployed env — the one binding that can run
// it. In an env that declares no cluster yet (the scaffold's `_cluster =
// None`) that binding fails the render, naming the workload, until the
// author declares one or drops the line; the scaffold says so here, so the
// first anyone hears of it is not a red CI run.
//
// An env that serves its API as one `server` workload (a hosted env's
// `_api`) runs a new service or worker there already: it gets no line of its
// own, and that API workload is bound if it was not yet.
func bindWorkloadInEnvs(root, projectName string, comp config.ComponentConfig) {
	envs, err := os.ReadDir(filepath.Join(root, "deploy", "kcl"))
	if err != nil {
		return
	}
	kind := codegen.WorkloadKindFor(comp.EffectiveKind())
	var needCluster []string
	for _, e := range envs {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "deploy", "kcl", e.Name(), "main.k")); err != nil {
			continue
		}
		res, err := codegen.AppendEnvBinding(root, projectName, e.Name(), kind, comp.Name)
		switch {
		case err != nil:
			fmt.Printf("\n⚠️  could not update deploy/kcl/%s/main.k: %v\n\n%s\n", e.Name(), err, codegen.EnvBindingHint(e.Name(), kind, comp.Name))
		case res.ServedBy != "":
			fmt.Printf("   - deploy/kcl/%s/main.k (%s '%s' runs in %s, the binary's `server`; nothing to bind)\n", e.Name(), kind, comp.Name, res.ServedBy)
		case res.Applied && res.Bound != "wl."+naming.KCLIdentifier(comp.Name):
			fmt.Printf("   - deploy/kcl/%s/main.k (%s '%s' runs in %s, the binary's `server`, now bound: %s)\n", e.Name(), kind, comp.Name, res.Bound, res.Binder)
		case res.Applied:
			binder := res.Binder
			fmt.Printf("   - deploy/kcl/%s/main.k (%s bound: %s)\n", e.Name(), comp.Name, binder)
			if codegen.HostedRefusal(kind) != "" && binder == "_on_cluster" && codegen.EnvDeclaresNoCluster(root, e.Name()) {
				needCluster = append(needCluster, "deploy/kcl/"+e.Name()+"/main.k")
			}
		default:
			if !envBindsWorkload(root, e.Name(), comp.Name) {
				fmt.Printf("\n📝 %s\n", codegen.EnvBindingHint(e.Name(), kind, comp.Name))
			}
		}
	}
	if len(needCluster) > 0 {
		fmt.Print(clusterNeededNotice(comp.Name, kind, needCluster))
	}
}

// clusterNeededNotice tells the author that a workload is bound to a cluster
// the env has not declared, why, and the ways out.
func clusterNeededNotice(name, kind string, envFiles []string) string {
	return fmt.Sprintf("\n⚠️  %s '%s' cannot run on Reliant hosting: %s.\n"+
		"   It is bound to a cluster you operate, `_on_cluster(wl.%s)`, in %s —\n"+
		"   and `forge env render` refuses those envs until you declare `_cluster` there\n"+
		"   (its kubectl context, namespace and platform, registered once with\n"+
		"   `forge cluster connect`; the file shows the shape).\n"+
		"   Not running one? Drop the line from that env.\n",
		kind, name, codegen.HostedRefusal(kind), naming.KCLIdentifier(name), strings.Join(envFiles, ", "))
}

// envBindsWorkload reports whether an env's main.k already names the
// workload (`wl.<ident>`), so an already-bound workload prints no hint.
func envBindsWorkload(root, env, name string) bool {
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "kcl", env, "main.k"))
	if err != nil {
		return false
	}
	return strings.Contains(codegen.StripKCLProse(string(raw)), "wl."+naming.KCLIdentifier(name))
}

// kindFromCtxLabel recovers the component kind from the "forge scaffold
// <kind> <name>" boundary label. Used only on the path where discovery has
// not caught up yet, to label a printed stanza. Server is the fallback: it is
// the kind whose expansion is a plain Deployment+Service, which is the least
// surprising thing to suggest.
func kindFromCtxLabel(ctxLabel string) string {
	for _, kind := range []string{
		config.ComponentKindWorker,
		config.ComponentKindCron,
		config.ComponentKindOperator,
		config.ComponentKindBinary,
	} {
		if slices.Contains(strings.Fields(ctxLabel), kind) {
			return kind
		}
	}
	return config.ComponentKindServer
}
