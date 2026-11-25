// Frontend source resolution — the bridge between a DECLARED cross-repo
// source and a directory a build can shell into.
//
// A frontend declares its code one of two ways: a `path` (a directory in
// this repository) or a `source` (another repository, pinned to a ref).
// Every command that shells into a frontend — build, deploy, install,
// typecheck — needs a real directory, so this file resolves the second
// form to the first and leaves the first untouched.
//
// The design constraint that shapes everything here: existing projects
// must be byte-identical. A project with no `source:` anywhere never
// constructs a resolver, never touches the cache, and never changes
// behavior — resolveFrontendSources returns immediately when nothing
// declares a source.
//
// See internal/gitsource for the cache, the fetch, and the
// .forge/source-overrides.yaml local-override rules.
package cli

import (
	"context"
	"fmt"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
)

// toGitSource converts the config/KCL schema type to the resolver's own.
// The two are deliberately separate types: internal/config is a pure
// schema package with no internal imports, so the conversion happens at
// this boundary rather than by making config depend on the resolver.
func toGitSource(s *config.GitSource) gitsource.Source {
	if s == nil {
		return gitsource.Source{}
	}
	return gitsource.Source{Repo: s.Repo, Ref: s.Ref, Subdir: s.Subdir}
}

// frontendSourceResolver builds a resolver rooted at the project. Split
// out so the (few) call sites share one construction — including the
// override-file load, which must be identical everywhere or a developer's
// override would apply to `forge build` and not to `forge env deploy`.
func frontendSourceResolver(projectDir string) (*gitsource.Resolver, error) {
	return gitsource.NewResolver(projectDir,
		gitsource.WithLogger(func(line string) { fmt.Println(line) }))
}

// resolveFrontendSources rewrites the Path of every source-declared
// frontend in cfg to the directory its pin resolves to, fetching into the
// machine-local cache when needed.
//
// It is a no-op — no resolver, no cache access, no output — for a project
// where no frontend declares a source, which is every project that exists
// today.
//
// Rewriting Path (rather than adding a second field every consumer must
// learn) is deliberate: by the time a build shells into a frontend, "where
// is the code" has ONE answer, and the ~20 call sites that read fe.Path
// should not each have to re-derive it. The declaration keeps both fields;
// only this resolved, in-memory copy collapses them.
func resolveFrontendSources(ctx context.Context, projectDir string, cfg *config.ProjectConfig) error {
	if cfg == nil {
		return nil
	}
	needed := false
	for _, fe := range cfg.Frontends {
		if fe.HasGitSource() {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}

	resolver, err := frontendSourceResolver(projectDir)
	if err != nil {
		return err
	}
	fmt.Println("Resolving cross-repo frontend sources...")
	for i := range cfg.Frontends {
		fe := &cfg.Frontends[i]
		if !fe.HasGitSource() {
			continue
		}
		res, err := resolver.Resolve(ctx, toGitSource(fe.Source))
		if err != nil {
			return fmt.Errorf("frontend %q: %w", fe.Name, err)
		}
		*fe = fe.WithDir(res.Dir)
	}
	return nil
}

// resolveBuildFrontendSources materializes cross-repo frontend sources
// for `forge build`, which reads BOTH structures: the project config
// drives the build lane (which frontends to `npm run build`) and the KCL
// entities drive the deploy lane. Both must resolve through the same
// resolver, or a developer's local override would apply to one lane and
// not the other and the two would build different code.
func resolveBuildFrontendSources(ctx context.Context, cfg *config.ProjectConfig, entities *KCLEntities) error {
	projectDir := projectDirForKCL()
	if err := resolveFrontendSources(ctx, projectDir, cfg); err != nil {
		return err
	}
	return resolveFrontendEntitySources(ctx, projectDir, entities)
}

// resolveFrontendEntitySources does the same for the KCL-rendered
// frontend entities, which are what the deploy path reads. Same no-op
// guarantee for projects that declare no sources.
//
// Deploy and build read different structures (KCLEntities vs
// ProjectConfig) but must agree on where a frontend's code is, so both
// route through the same resolver with the same overrides.
func resolveFrontendEntitySources(ctx context.Context, projectDir string, entities *KCLEntities) error {
	if entities == nil {
		return nil
	}
	needed := false
	for _, fe := range entities.Frontends {
		if fe.Source != nil && fe.Source.Repo != "" {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}

	resolver, err := frontendSourceResolver(projectDir)
	if err != nil {
		return err
	}
	fmt.Println("Resolving cross-repo frontend sources...")
	for i := range entities.Frontends {
		fe := &entities.Frontends[i]
		if fe.Source == nil || fe.Source.Repo == "" {
			continue
		}
		res, err := resolver.Resolve(ctx, toGitSource(fe.Source))
		if err != nil {
			return fmt.Errorf("frontend %q: %w", fe.Name, err)
		}
		fe.Path = res.Dir
		// What the build is told it was built from (frontendBuildIdentityEnv).
		// The fetch is shallow and carries no tags, so without this a build
		// pinned to `v1.8.1` cannot name its own version.
		fe.SourceCommit = res.Commit
		fe.SourceOverridden = res.Overridden
		// The render could not check this frontend's runtime_config keys:
		// its code, and so its config schema, was not on disk until now.
		if err := checkFrontendRuntimeConfigKeys(projectDir, []FrontendEntity{*fe}); err != nil {
			return err
		}
	}
	return nil
}
