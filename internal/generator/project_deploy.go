package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/naming"
	"github.com/reliant-labs/forge/internal/templates"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// hostedEnvNames are the deployed envs `forge project new` scaffolds, in
// promotion order. Each is born hosted on the forge control plane
// (deploy/kcl/cloud/main.k.tmpl).
func hostedEnvNames() []string { return []string{"staging", "prod"} }

func (g *ProjectGenerator) generateKCLDeploy() error {
	deployDir := filepath.Join(g.Path, "deploy", "kcl")

	// Generate kcl.mod at deploy/kcl/ — the KCL package root for the
	// deploy manifests, so the env main.k files' package-rooted imports
	// (`import config_projection`, `import .config`, `import ..ingress`)
	// resolve. It also declares the `forge` module dependency the env
	// files import — the schemas live upstream in `forge/kcl/`, not in
	// the project's tree.
	kclModData := struct {
		ProjectName string
	}{
		ProjectName: g.Name,
	}
	kclModContent, err := templates.DeployTemplates().Render("kcl/kcl.mod.tmpl", kclModData)
	if err != nil {
		return fmt.Errorf("render kcl.mod template: %w", err)
	}
	if err := os.MkdirAll(deployDir, 0755); err != nil {
		return err
	}
	kclModPath := filepath.Join(deployDir, "kcl.mod")
	if err := atomicfile.WriteFile(kclModPath, kclModContent, 0644); err != nil {
		return fmt.Errorf("write kcl.mod: %w", err)
	}

	// kcl.mod declares no `forge` dependency: every render supplies the
	// module from the running binary (internal/kclvendor), so a scaffold is
	// resolvable the instant it is written, on any build of forge, with
	// nothing materialized into the project.

	// The legacy in-tree `deploy/kcl/schema.k` + `base.k` + `render.k`
	// files were retired in favor of the upstream `forge` KCL module.
	// Projects now `import forge` from each env's main.k.

	// The per-env main.k files. An env BINDS each workload declared once in
	// deploy/kcl/workloads.k to where it runs — one line per workload, since
	// there is no env-level runtime (ADR 0002 §2) — and states its own
	// values. dev renders from the local-loop template (host processes, the
	// local k3d cluster, host-run postgres); staging and prod from the cloud
	// template: hosted on the forge control plane (every admissible workload
	// OnHosted, a managed database, managed secrets, static hosting), with
	// the binders for a cluster you operate declared beside them, unused.
	// Each one written is recorded in g.hostedEnvs, so the CI scaffolded
	// next is the hosted pipeline for exactly those envs.
	//
	// The project's binary mode does not reach these files. Every component
	// is a subcommand of the project binary in both modes (`<bin> <name>`),
	// and a workload's `args` select it on every runtime.
	//
	// Ingress is experimental but the wiring is scaffolded at `forge project
	// new` so an opt-in needs no rescaffold; the runtime gate reads
	// IngressEnabled() at call time.
	born := g.bornComponents()
	hasFrontend := g.forScaffold().HasFrontend
	hasBackend := len(born) > 0 || g.Features.MigrationsEnabled()
	hasDatabase := g.Features.MigrationsEnabled() || g.Features.ORMEnabled() || (hasBackend && hasFrontend)
	envs := []struct{ env, template string }{{codegen.DevEnvName, "kcl/dev/main.k.tmpl"}}
	for _, env := range hostedEnvNames() {
		envs = append(envs, struct{ env, template string }{env, "kcl/cloud/main.k.tmpl"})
	}
	for _, e := range envs {
		var apiSecrets []string
		data := templates.EnvTemplateData{
			// A new project composes with the graph, so its API is bound to
			// the `server` root and its env is what that root reaches.
			Roots:       true,
			ProjectName: g.Name,
			EnvName:     e.env,
			// The API holds the dev env's `-api` port key in every
			// project, born with a service or not: the frontend's dev
			// config reads that key, and `_api` is what serves it.
			PrimaryWorkload: codegen.APIWorkloadName,
			IngressEnabled:  true,
			HasFrontend:     hasFrontend,
			FrontendName:    g.FrontendName,
			FrontendIdent:   naming.KCLIdentifier(g.FrontendName),
			FrontendRoutes:  g.FrontendRoutes,
			Bindings:        scaffoldEnvBindings(e.env, born, g.Features.MigrationsEnabled()),
			APIWorkload:     codegen.APIWorkloadStanza(g.ModulePath, g.Name, apiSecrets...),
		}
		if !hasBackend && !hasDatabase {
			e.template = "kcl/frontend-only/main.k.tmpl"
		}
		data.DatabaseDisabled = !hasDatabase
		data.IngressEnabled = hasBackend
		content, err := templates.DeployTemplates().Render(e.template, data)
		if err != nil {
			return fmt.Errorf("render deploy template %s for %s: %w", e.template, e.env, err)
		}
		destPath := filepath.Join(deployDir, e.env, "main.k")
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(destPath, content, 0644); err != nil {
			return fmt.Errorf("write %s/main.k: %w", e.env, err)
		}
		if e.env != codegen.DevEnvName {
			g.hostedEnvs = append(g.hostedEnvs, e.env)
		}
	}
	ingressOn := hasBackend
	templateData := struct{ ProjectName string }{ProjectName: g.Name}

	// Gateway API ingress scaffolding. The base topology
	// (`deploy/kcl/ingress.k`) is user-owned and shared across envs;
	// each env's `deploy/kcl/<env>/ingress.k` re-exports the base
	// with optional overrides. Both render once at `forge project new`; not
	// regenerated on subsequent `forge generate` runs.
	if ingressOn {
		ingressFiles := []struct {
			templateName string
			dest         string
		}{
			{"kcl/ingress.k.tmpl", "ingress.k"},
			{"kcl/dev/ingress.k.tmpl", "dev/ingress.k"},
			{"kcl/staging/ingress.k.tmpl", "staging/ingress.k"},
			{"kcl/prod/ingress.k.tmpl", "prod/ingress.k"},
		}
		for _, f := range ingressFiles {
			content, err := templates.DeployTemplates().Render(f.templateName, templateData)
			if err != nil {
				return fmt.Errorf("render ingress template %s: %w", f.templateName, err)
			}
			destPath := filepath.Join(deployDir, f.dest)
			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return err
			}
			if err := atomicfile.WriteFile(destPath, content, 0644); err != nil {
				return fmt.Errorf("write %s: %w", f.dest, err)
			}
		}
	}

	// The component declaration — what this project is made of. SCAFFOLDED
	// ONCE and owned by the project from then on: forge writes it only when
	// it does not exist, and `forge scaffold <kind>` appends to it. Nothing
	// regenerates it, so a hand-edit survives every later forge run.
	//
	// The file is ALWAYS written, because every env's main.k imports it —
	// a missing workloads.k is an unresolvable import, not an empty list.
	//
	// Seeding it is the subtle part. This runs BEFORE the proto descriptor
	// exists, so discovery cannot yet see the services this very command is
	// scaffolding. Discovery is still asked first (it finds workers,
	// operators and secondary binaries on an upgrade path, and re-scaffolds
	// of an existing tree), and the services THIS generator knows it is
	// creating are unioned in — g.ServiceName and g.AdditionalServices are
	// that knowledge, and they are the only source for it at this point.
	//
	// Without the union, a project created with `--service billing` would be
	// born with an empty declaration; the generate pipeline that runs
	// moments later correctly refuses to rewrite a user-owned file, so
	// nothing would ever fill it in and the project could not deploy the
	// service it was created with.
	if err := ScaffoldWorkloadsKCL(g.Path, g.ModulePath, g.Name,
		g.bornComponents(), g.Features.MigrationsEnabled()); err != nil {
		return fmt.Errorf("scaffold %s: %w", codegen.WorkloadsKCLRelPath, err)
	}

	// The users, declared once for every env. Only a project with a frontend
	// has someone to sign in; a project without one declares no identity.
	if g.forScaffold().HasFrontend && hasBackend {
		if err := ScaffoldIdentityKCL(g.Path, g.Name); err != nil {
			return fmt.Errorf("scaffold %s: %w", codegen.IdentityKCLRelPath, err)
		}
	}

	return nil
}

// scaffoldEnvBindings is the body of a scaffolded env's `_workloads` list:
// one binding per workload workloads.k declares, in the same order as its
// ALL list (migrate first — it gates the rest). The dev-IdP convergence job
// is bound only in dev, whose IdP it registers against.
func scaffoldEnvBindings(env string, components codegen.Inventory, migrations bool) string {
	var lines []string
	if migrations {
		lines = append(lines, codegen.EnvBinding(env, codegen.WorkloadKindJob, codegen.MigrateWorkloadName))
	}
	// Every env runs its services and workers as ONE workload, `_api` (the
	// binary's `server`), so the browser reaches every service at one origin
	// — see codegen.APIWorkloadName. It is bound once there is something for
	// it to run: a project born with no service runs (and, hosted, pays for)
	// no idle process, and `forge scaffold service` binds it with the first.
	var others []string
	served := false
	for _, c := range components {
		kind := codegen.WorkloadKindFor(c.EffectiveKind())
		if codegen.RunsInServer(kind) {
			served = true
			continue
		}
		others = append(others, codegen.EnvBinding(env, kind, c.Name))
	}
	if served {
		lines = append(lines, "    "+codegen.EnvBinder(env, codegen.WorkloadKindService)+"("+codegen.APIWorkloadIdent+")")
	}
	return strings.Join(append(lines, others...), "\n")
}

// bornComponents is the component set a freshly-scaffolded project declares:
// whatever discovery can see on disk, plus the services this generator is in
// the middle of creating (which discovery cannot see yet, because they are
// declared by a proto descriptor that does not exist until the generate
// pipeline extracts it).
func (g *ProjectGenerator) bornComponents() codegen.Inventory {
	inv := codegen.DiscoverProjectComponents(g.treeDir(), g.Name)
	for _, name := range append([]string{g.ServiceName}, g.AdditionalServices...) {
		if name == "" {
			continue
		}
		if _, exists := inv.Named(name); exists {
			continue
		}
		inv = append(inv, config.ComponentConfig{
			Name: name,
			Kind: config.ComponentKindServer,
		})
	}
	return inv
}

// ScaffoldWorkloadsKCL writes deploy/kcl/workloads.k when the project does
// not already have one, seeded with the workloads discovered so far.
//
// It is a NO-OP when the file exists. That is the whole contract: the file is
// user-owned, so the only safe automatic write is the FIRST one. Drift
// between the tree and this file afterwards is REPORTED by `forge lint`
// (which prints the exact stanza to paste) rather than silently repaired —
// forge cannot know whether a component missing from an env is an oversight
// or a deliberate choice.
//
// Callers that run before the proto descriptor exists should skip an empty
// inventory rather than write an empty file, or that empty file becomes the
// one automatic write and the real components never land (see the call in
// generateKCLDeploy).
func ScaffoldWorkloadsKCL(projectDir, modulePath, projectName string, components codegen.Inventory, migrations bool) error {
	if codegen.WorkloadsKCLExists(projectDir) {
		return nil
	}
	var stanzas strings.Builder
	for i, c := range components {
		if i > 0 {
			stanzas.WriteString("\n")
		}
		stanzas.WriteString(codegen.WorkloadStanza(modulePath, projectName, c))
	}

	// The deploy-time migration step, scaffolded as an ordinary one-shot
	// workload. It goes LAST in the file but FIRST in the ALL list below:
	// k8s runs initContainers in declaration order, so a project that later
	// adds a second one-shot (seed data, provision an IdP) gets it ordered
	// after the migration, and that job sees a current schema.
	if migrations {
		if len(components) > 0 {
			stanzas.WriteString("\n")
		}
		stanzas.WriteString(codegen.MigrateWorkloadStanza(modulePath, projectName))
	}

	// The identifier the docstring uses in its worked `wl.<name> | {...}`
	// example. Naming a workload the project ACTUALLY has makes the example
	// copy-pasteable; with no workloads yet it falls back to a placeholder
	// the alternate (empty) branch of the template explains.
	primaryIdent := "billing"
	componentIdents := make([]string, 0, len(components))
	for _, c := range components {
		componentIdents = append(componentIdents, naming.KCLIdentifier(c.Name))
	}
	if len(componentIdents) > 0 {
		primaryIdent = componentIdents[0]
	}

	// migrate FIRST in ALL. The list order is the init-container order on
	// every gated pod, so putting the migration ahead of everything is what
	// makes a later-added one-shot run against a current schema.
	var idents []string
	if migrations {
		idents = append(idents, naming.KCLIdentifier(codegen.MigrateWorkloadName))
	}
	idents = append(idents, componentIdents...)

	content, err := templates.DeployTemplates().Render("kcl/workloads.k.tmpl", struct {
		ProjectName    string
		Workloads      string
		PrimaryIdent   string
		WorkloadIdents string
	}{
		ProjectName:    projectName,
		Workloads:      strings.TrimRight(stanzas.String(), "\n"),
		PrimaryIdent:   primaryIdent,
		WorkloadIdents: strings.Join(idents, ", "),
	})
	if err != nil {
		return fmt.Errorf("render workloads.k template: %w", err)
	}
	dest := filepath.Join(projectDir, codegen.WorkloadsKCLRelPath)
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return atomicfile.WriteFile(dest, content, 0644)
}

// generateDevConfig writes the k3d cluster configuration for local development.
func (g *ProjectGenerator) generateDevConfig() error {
	data := struct {
		ProjectName string
	}{
		ProjectName: g.Name,
	}

	content, err := templates.DeployTemplates().Render("k3d.yaml.tmpl", data)
	if err != nil {
		return fmt.Errorf("render k3d.yaml: %w", err)
	}

	destPath := filepath.Join(g.Path, "deploy", "k3d.yaml")
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return err
	}
	return atomicfile.WriteFile(destPath, content, 0644)
}

// generateObservability writes the local ClickStack's two owned inputs: the
// collector pipeline file the compose service mounts, and the dashboard
// provisioner directory it watches.
func (g *ProjectGenerator) generateObservability() error {
	for _, f := range []struct{ tmpl, dest string }{
		{"otel-collector.yaml.tmpl", filepath.Join("deploy", "observability", "otel-collector.yaml")},
		{"dashboards-README.md.tmpl", filepath.Join("deploy", "observability", "dashboards", "README.md")},
		{"otel-profiles.yaml.tmpl", filepath.Join("deploy", "observability", "otel-profiles.yaml")},
		{"profiles-schema.sql.tmpl", filepath.Join("deploy", "observability", "profiles-schema.sql")},
	} {
		content, err := templates.ProjectTemplates().Render(f.tmpl, struct{}{})
		if err != nil {
			return fmt.Errorf("render %s: %w", f.dest, err)
		}
		destPath := filepath.Join(g.Path, f.dest)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(destPath, content, 0644); err != nil {
			return err
		}
	}
	return nil
}

func (g *ProjectGenerator) generateDockerCompose() error {
	// The shared scaffold payload rather than a local two-field struct: the
	// compose template now branches on HasFrontend (the dev IdP is only
	// scaffolded for a project that ships a browser), and forScaffold is
	// where that derivation lives for BOTH lanes. A local struct here would
	// be a third place to keep in sync with the upgrade lane's render.
	data := g.forScaffold()
	content, err := templates.ProjectTemplates().Render("docker-compose.yml.tmpl", data)
	if err != nil {
		return fmt.Errorf("render docker-compose.yml: %w", err)
	}
	destPath := filepath.Join(g.Path, "docker-compose.yml")
	if err := atomicfile.WriteFile(destPath, content, 0644); err != nil {
		return err
	}
	return g.generateIDPSteps()
}

// generateIDPSteps writes the dev IdP's declared state, which the `idp`
// compose service mounts as its setup steps. It rides alongside the
// compose file because the two are one artifact: the service is
// meaningless without the file it converges to.
func (g *ProjectGenerator) generateIDPSteps() error {
	data := struct {
		ProjectName string
	}{
		ProjectName: g.Name,
	}
	content, err := templates.ProjectTemplates().Render("idp-steps.yaml.tmpl", data)
	if err != nil {
		return fmt.Errorf("render idp-steps.yaml: %w", err)
	}
	return atomicfile.WriteFile(filepath.Join(g.Path, "idp-steps.yaml"), content, 0644)
}

// ScaffoldIdentityKCL writes deploy/kcl/identity.k, the one ManagedIdentity
// declaration every env imports. Write-if-absent: it is the project's from
// birth, and nothing regenerates it.
func ScaffoldIdentityKCL(projectDir, projectName string) error {
	dest := filepath.Join(projectDir, codegen.IdentityKCLRelPath)
	if _, err := os.Stat(dest); err == nil {
		return nil
	}
	content, err := templates.DeployTemplates().Render("kcl/identity.k.tmpl", struct{ ProjectName string }{projectName})
	if err != nil {
		return fmt.Errorf("render identity.k template: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}
	return atomicfile.WriteFile(dest, content, 0644)
}
