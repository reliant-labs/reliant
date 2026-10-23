package cli

// This file blank-imports the dir-nested command-group subpackages so their
// init() runs and self-registers each group's command factory with
// internal/cli/factory. NewRootCmd ranges factory.Registered() to attach
// them. As commands migrate from the flat files in this package into group
// subpackages, add the group's import here.
//
// The import is one-directional: groups import internal/cli/factory (and may
// import internal/cli for shared helpers); internal/cli blank-imports the
// groups here. The registry indirection in the factory package is what keeps
// that from being an import cycle.
import (
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/generator"

	// Blank imports: each group's init() self-registers its command factory
	// with internal/cli/factory (see the file-level comment above).
	_ "github.com/reliant-labs/forge/internal/cli/audit"
	_ "github.com/reliant-labs/forge/internal/cli/component"
	_ "github.com/reliant-labs/forge/internal/cli/debug"
	_ "github.com/reliant-labs/forge/internal/cli/fix"
	_ "github.com/reliant-labs/forge/internal/cli/lint"
	_ "github.com/reliant-labs/forge/internal/cli/scaffold"
)

// init wires internal/cli's heavy shared loaders into the factory so the
// dir-nested command groups can reach them as function values without
// importing internal/cli (which would create an import cycle — internal/cli
// blank-imports the groups above).
func init() {
	factory.SetProjectStoreLoader(loadProjectStore)
	factory.SetGenAPI(factory.GenAPI{
		// The scaffold group holds the project's generate lock for each
		// command's whole run (HoldProjectLock), so the pipeline closures
		// below take only the in-process generateMu — in that order, the
		// same as withGenerateLock. Re-acquiring the project lock here would
		// deadlock: it is not reentrant.
		HoldProjectLock: acquireGenerateLock,
		// Full pipeline + bootstrap-only preset, each serialized under the
		// package-level generate mutex.
		RunPipeline: func(projectDir string) error {
			generateMu.Lock()
			defer generateMu.Unlock()
			return runGeneratePipeline(projectDir, false)
		},
		RunPipelineBootstrapOnly: func(projectDir string) error {
			generateMu.Lock()
			defer generateMu.Unlock()
			return runGeneratePipelineFlags(projectDir, pipelineFlags{Steps: "bootstrap-only"})
		},
		LoadServiceRegistry: func(projectDir string) (factory.ServiceRegistry, error) {
			reg, err := loadServiceRegistry(projectDir)
			if err != nil {
				return nil, err
			}
			return serviceRegistryAdapter{reg}, nil
		},
		ServiceRegistryRelPath: serviceRegistryRelPath,
		IsConnectServiceConfig: isConnectServiceConfig,
		WriteScenariosIndex:    generator.WriteScenariosIndex,
		RunPackageNew:          runPackageNew,
	})
	factory.SetAuditAPI(factory.AuditAPI{
		// KCL-entity-typed categories: computed cli-side (they need the KCL
		// render + entity structs shared by build/deploy/dev/doctor) and
		// returned to the audit group as neutral audittype.Category.
		Ingress:                       auditIngress,
		ExternalBuilds:                auditExternalBuilds,
		Prerequisites:                 auditPrerequisites,
		ListEnvs:                      ListEnvs,
		ProjectDefinesConnectServices: projectDefinesConnectServices,
		// scanProjectDrift returns []checksums.Tier1DriftEntry; the audit
		// group only needs the paths, so project them to []string here.
		ScanProjectDriftPaths: func(projectDir string, cs *generator.FileChecksums) []string {
			var out []string
			for _, d := range scanProjectDrift(projectDir, cs) {
				out = append(out, d.Path)
			}
			return out
		},
		LoadProjectStoreFrom: loadProjectStoreFrom,
	})
}

// serviceRegistryAdapter exposes the internal *serviceRegistry to the add
// group through the factory.ServiceRegistry interface, mapping the field +
// state methods onto the narrow exported contract.
type serviceRegistryAdapter struct{ reg *serviceRegistry }

func (a serviceRegistryAdapter) Exists() bool                { return a.reg.Exists }
func (a serviceRegistryAdapter) Registered(name string) bool { return a.reg.registered(name) }
func (a serviceRegistryAdapter) Tombstoned(name string) bool {
	return a.reg.state(name) == registrationTombstoned
}
