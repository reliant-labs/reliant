package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// generate_frontend_seams.go — the owned modules generated code imports.
//
// The generated React Query hooks import `connectClient` from @/lib/connect,
// and their scaffold-once tests import `setTransport` from it and
// `mockTransport` from @/lib/test-utils. Those modules are SEAMS: owned by
// the app ("yours"), because how an app reaches its API — which origin,
// which interceptors, which service goes to which backend — is its decision.
// The frontend scaffold writes them, and every frontend forge scaffolded has
// them.
//
// An adopted frontend never had them. Adopting reliant's web app generated
// 35 hook modules importing a @/lib/connect that did not exist (35× TS2307)
// and 35 tests importing a @/lib/test-utils that did not exist either. So an
// adopted frontend gets each missing seam scaffolded ONCE, by the birth
// ledger — written when it was never born here, left alone when present
// (the app's own connect.ts is the seam), and when the app deleted forge's
// copy on purpose, not written again but named, since the generated code
// importing it no longer compiles.
//
// A frontend forge scaffolded is not touched: its seams came with it, and a
// deletion there is the same decision the ledger already honours.

// frontendSeam is one owned module and the template it is scaffolded from.
type frontendSeam struct {
	rel      string // relative to the frontend directory
	template string // FrontendTemplates path
}

// frontendSeamsFor lists the seams a frontend kind's generated code imports,
// in dependency order: connect.ts imports ./api-base.
func frontendSeamsFor(feType string) []frontendSeam {
	switch strings.ToLower(feType) {
	case "vite-spa":
		return []frontendSeam{
			{rel: "src/lib/connect.ts", template: "vite-spa/src/lib/connect.ts.tmpl"},
			{rel: "src/lib/api-base.ts", template: "vite-spa/src/lib/api-base.ts"},
			{rel: "src/lib/test-utils.tsx", template: "shared-web/src/lib/test-utils.tsx.tmpl"},
		}
	case "nextjs":
		return []frontendSeam{
			{rel: "src/lib/connect.ts", template: "nextjs/src/lib/connect.ts.tmpl"},
			{rel: "src/lib/api-base.ts", template: "nextjs/src/lib/api-base.ts"},
			{rel: "src/lib/test-utils.tsx", template: "shared-web/src/lib/test-utils.tsx.tmpl"},
		}
	}
	return nil
}

// ensureAdoptedFrontendSeams scaffolds the missing seams into every adopted
// frontend that gets generated hooks.
func ensureAdoptedFrontendSeams(cfg *config.ProjectConfig, services []codegen.ServiceDef, projectDir string) error {
	if cfg == nil || cfg.IsFrontendWorkspacesEnabled() {
		return nil
	}
	for _, fe := range cfg.Frontends {
		if !fe.Adopted {
			continue
		}
		seams := frontendSeamsFor(fe.Type)
		if len(seams) == 0 || !frontendGetsHooks(fe, services) {
			continue
		}
		feDir, ok := fe.Dir(projectDir)
		if !ok {
			continue
		}
		data := templates.FrontendTemplateData{
			FrontendName: fe.Name,
			ProjectName:  cfg.Name,
			Platform:     strings.ToLower(fe.Type),
		}
		typed := frontendTypedConfig(projectDir, fe.Name)
		data.HasTypedConfig, data.HasMockAPI = typed.Bound, typed.HasMockAPI

		var born []string
		for _, seam := range seams {
			rel := filepath.ToSlash(filepath.Join(feDir, filepath.FromSlash(seam.rel)))
			abs := filepath.Join(projectDir, filepath.FromSlash(rel))
			if _, err := os.Stat(abs); err == nil {
				continue // the app's own module is the seam
			}
			if checksums.ScaffoldRecorded(projectDir, rel) {
				checksums.NoteLeftAlone(rel, fmt.Sprintf("generated code imports @/%s, which forge scaffolded "+
					"here once and was then deleted, so forge did not write it again. Write your own, or `%s "+
					"project rescaffold %s` to get forge's back", strings.TrimSuffix(strings.TrimPrefix(seam.rel, "src/"),
					filepath.Ext(seam.rel)), Name(), rel))
				continue
			}
			content, err := templates.FrontendTemplates().Render(seam.template, data)
			if err != nil {
				return fmt.Errorf("render %s for frontend %s: %w", seam.rel, fe.Name, err)
			}
			wrote, err := checksums.WriteScaffoldIfMissing(projectDir, rel, content)
			if err != nil {
				return fmt.Errorf("write %s: %w", rel, err)
			}
			if wrote {
				formatScaffoldedFrontendFile(abs)
				born = append(born, seam.rel)
			}
		}
		if len(born) > 0 {
			fmt.Printf("  ✅ frontend %s: scaffolded %s (yours — the generated hooks import them; see the comments in "+
				"src/lib/connect.ts to route each service to your own transport)\n", fe.Name, strings.Join(born, ", "))
		}
	}
	return nil
}

// frontendGetsHooks reports whether generate writes any hook module into fe.
func frontendGetsHooks(fe config.FrontendConfig, services []codegen.ServiceDef) bool {
	for _, svc := range services {
		if fe.WantsHooksFor(svc.Name, qualifiedServiceName(svc)) && len(svc.Methods) > 0 {
			return true
		}
	}
	return false
}

// frontendTypedConfig reports which typed config fields the project binds to
// the named frontend (forge.v1.frontend_config), so a scaffolded connect.ts
// reads its origin from the same place the scaffold's would. A parse failure
// degrades to the env-var form: this picks a template variant, and the
// sensitive-field refusal runs in its own step, which fails loudly.
func frontendTypedConfig(projectDir, feName string) generator.FrontendTypedConfig {
	messages, err := codegen.ParseConfigProtosFromDir(filepath.Join(projectDir, "proto", "config"))
	if err != nil {
		return generator.FrontendTypedConfig{}
	}
	for _, fc := range codegen.FrontendConfigsFromMessages(messages) {
		if fc.Frontend != feName {
			continue
		}
		envVars := make([]string, 0, len(fc.Fields))
		for _, f := range fc.Fields {
			if f.EnvVar != "" {
				envVars = append(envVars, f.EnvVar)
			}
		}
		return generator.FrontendTypedConfigFrom(envVars)
	}
	return generator.FrontendTypedConfig{}
}
