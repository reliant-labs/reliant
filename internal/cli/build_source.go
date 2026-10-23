package cli

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/internal/config"
)

// resolveShellBuildSource keeps cwd and source mutually exclusive and preserves
// the declared pin. Every consumer (execution, plan, release guard) reads the
// same EffectiveBuildCwd after this pass. A failed fetch never falls back to cwd.
func resolveShellBuildSource(ctx context.Context, projectDir string, sh *ShellBuild) error {
	if sh == nil || sh.Source == nil {
		return nil
	}
	if sh.Cwd != "" {
		return fmt.Errorf("ShellBuild.cwd and source are mutually exclusive")
	}
	if err := sh.Source.Validate(); err != nil {
		return err
	}
	if sh.SourceDir != "" {
		return nil
	}
	resolver, err := frontendSourceResolver(projectDir)
	if err != nil {
		return err
	}
	res, err := resolver.Resolve(ctx, toGitSource(sh.Source))
	if err != nil {
		return err
	}
	sh.SourceDir = res.Dir
	return nil
}

// resolveNamedBuildContexts materializes only pins, leaving paths and BuildKit
// references alone. The declaration remains available for diagnostics and
// release provenance; resolved directories never leak into forge.yaml.
func resolveNamedBuildContexts(ctx context.Context, projectDir string, contexts map[string]config.BuildContext) error {
	for _, name := range sortedKeys(contexts) {
		c := contexts[name]
		if err := c.Validate(); err != nil {
			return fmt.Errorf("build context %q: %w", name, err)
		}
		if c.Source == nil || c.SourceDir != "" {
			continue
		}
		resolver, err := frontendSourceResolver(projectDir)
		if err != nil {
			return err
		}
		res, err := resolver.Resolve(ctx, toGitSource(c.Source))
		if err != nil {
			return fmt.Errorf("build context %q: %w", name, err)
		}
		c.SourceDir = res.Dir
		contexts[name] = c
	}
	return nil
}

func resolveBuildSources(ctx context.Context, projectDir string, cfg *config.ProjectConfig, entities *KCLEntities) error {
	if cfg != nil {
		if err := resolveNamedBuildContexts(ctx, projectDir, cfg.Docker.BuildContexts); err != nil {
			return err
		}
	}
	if entities == nil {
		return nil
	}
	for _, w := range entities.Workloads {
		if err := resolveShellBuildSource(ctx, projectDir, w.Build.Shell); err != nil {
			return fmt.Errorf("workload %q: %w", w.Name, err)
		}
		if d := w.Build.Docker; d != nil {
			if err := resolveNamedBuildContexts(ctx, projectDir, d.BuildContexts); err != nil {
				return fmt.Errorf("workload %q: %w", w.Name, err)
			}
		}
	}
	return nil
}
