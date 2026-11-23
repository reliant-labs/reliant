package cli

// forge.OnExternal on the CLI side: turning a rendered external workload into
// the deploytarget.ExternalSpec its hooks run with. The deploy dispatch and
// `forge env verify` both build it here, so the two hand a hook the same
// environment for the same workload.

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/kclrender"
	"github.com/reliant-labs/forge/internal/secrets"
)

// hookSecretResolver is what pass_env needs of the env's secret provider:
// a value by env-var name.
type hookSecretResolver interface {
	Resolve(name string) (string, bool)
}

// externalSpecFor is the spec a workload bound to forge.OnExternal runs its
// hooks with. Image is the rendered spec image — `repo@sha256:…` when the
// render had the deploy's digests — and Version and Release are stamped by
// the caller that knows them (stampExternalRelease). warn names every
// pass_env secret the provider could not resolve; nil discards.
func externalSpecFor(envName string, w WorkloadEntity, entities *KCLEntities, prov hookSecretResolver, warn io.Writer) *deploytarget.ExternalSpec {
	rt := w.Runtime.External
	spec := &deploytarget.ExternalSpec{
		Runtime:    *rt,
		EnvName:    envName,
		Workload:   w.Name,
		ProjectDir: projectDirForKCL(),
		Image:      w.Spec.Image,
	}
	if entities != nil {
		spec.Project = entities.Project
	}
	if rt.PassEnv {
		spec.WorkloadEnv = externalWorkloadEnv(w, prov, warn)
	}
	return spec
}

// externalWorkloadEnv is the env pass_env hands the hooks: every literal
// value, and every secret-backed entry the env's secret provider resolves by
// name (the provider's key IS the env var name). An entry it cannot resolve
// is named on warn and left out — never passed empty, which a hook could not
// tell from a deliberately blank value.
func externalWorkloadEnv(w WorkloadEntity, prov hookSecretResolver, warn io.Writer) map[string]string {
	env := map[string]string{}
	var missing []string
	for _, e := range w.Spec.Env {
		switch {
		case e.SecretRef != nil || e.ManagedSecret != nil:
			if prov == nil {
				missing = append(missing, e.Name)
				continue
			}
			if v, ok := prov.Resolve(e.Name); ok {
				env[e.Name] = v
			} else {
				missing = append(missing, e.Name)
			}
		case e.Value != "":
			env[e.Name] = e.Value
		}
	}
	if len(missing) > 0 && warn != nil {
		sort.Strings(missing)
		fmt.Fprintf(warn, "warning: %s: pass_env: the env's secret provider resolves no value for %s; not passed to its hooks\n",
			w.Name, strings.Join(missing, ", "))
	}
	return env
}

// stampExternalRelease fills in what only the deploy knows: the image tag
// (FORGE_VERSION) and the release the env is promoted to (FORGE_RELEASE, from
// the render context the deploy armed with kclrender.WithRelease).
func stampExternalRelease(ctx context.Context, groups []deploytarget.ServiceGroup) {
	release := kclrender.Release(ctx)
	for gi := range groups {
		for si := range groups[gi].Services {
			s := groups[gi].Services[si].External
			if s == nil {
				continue
			}
			if s.Version == "" {
				s.Version = groups[gi].ImageTag
			}
			if s.Release == "" {
				s.Release = release
			}
		}
	}
}

// externalHookSecretProvider is the env's secret provider for pass_env, or nil
// (with a warning) when it cannot be built: a deploy of an external workload
// must not fail because a store it may never read is broken.
func externalHookSecretProvider(entities *KCLEntities) secrets.Provider {
	prov, err := secretProviderFromEntities(entities, projectDirForKCL())
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: secret provider: %v (pass_env secrets will not be passed)\n", err)
		return nil
	}
	return prov
}
