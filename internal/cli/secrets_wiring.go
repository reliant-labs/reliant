package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/clickstack"
	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/internal/secrets"
)

// sharedSecretStorePath resolves relPath against the REPO ANCHOR — the
// primary checkout — returning "" when that is the same file the caller
// already has (the primary checkout itself, or a non-git directory).
//
// This is what makes a `git worktree add`'ed checkout usable without
// replaying every `forge secret set`: the store is gitignored, so a new
// worktree materializes none of it, but the values are sitting in the
// primary checkout. The anchor is git's own `--git-common-dir`, the same
// authoritative signal the port-block registry uses.
func sharedSecretStorePath(projectDir, relPath string) string {
	anchor := devstack.RepoAnchor(projectDir)
	if anchor == "" || sameDirPath(anchor, projectDir) {
		return "" // primary checkout / not a repo — one store, no layering
	}
	return filepath.Join(anchor, relPath)
}

// sameDirPath compares two directory paths tolerantly of symlinks (macOS
// /var vs /private/var) so the primary checkout is never mistaken for a
// linked worktree.
func sameDirPath(a, b string) bool {
	if a == b {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil {
		return filepath.Clean(ra) == filepath.Clean(rb)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// secretProviderFromEntities builds a secrets.Provider from the bundle's
// declared provider, resolving a dotenv relative path against projectDir.
// Returns a noop provider when none declared.
//
// The dotenv path is resolved here (the only place that knows projectDir)
// so the secrets package stays filesystem-location-agnostic — its
// ProviderConfig.Path is "already resolved" by contract.
func secretProviderFromEntities(e *KCLEntities, projectDir string) (secrets.Provider, error) {
	if e == nil || e.SecretProvider == nil {
		return secrets.NewProvider(nil)
	}
	// A "rendered" provider declares cluster Secrets explicitly (name +
	// per-key source) — it is NOT an env-var-keyed resolver like dir or
	// dotenv. Its cluster apply runs through applyRenderedSecretsPerGroup;
	// for the env-var-resolution consumers here (host secret-ref
	// validation / injection) it has nothing to offer, so return a noop.
	// The dedicated per-group path builds its own value source for
	// from="dir" / from="dotenv" keys.
	if e.SecretProvider.Type == "rendered" {
		return secrets.NewProvider(nil)
	}
	// A LOCAL env's hosted store is pullable: when `forge env up` has pulled
	// its values (armLocalSecretsForUp), they resolve here — in memory. Any
	// other hosted env (persistent, or no pull in this process) stays the
	// value-free hosted provider.
	if isHostedSecretEnv(e) {
		if values, ok := pulledSecretsFor(e); ok {
			return secrets.NewPulledProvider(values), nil
		}
	}
	cfg := &secrets.ProviderConfig{
		Type: e.SecretProvider.Type,
		Path: e.SecretProvider.Path,
	}
	if cfg.Path != "" && !filepath.IsAbs(cfg.Path) {
		cfg.SharedPath = sharedSecretStorePath(projectDir, cfg.Path)
		cfg.Path = filepath.Join(projectDir, cfg.Path)
	}
	return secrets.NewProvider(cfg)
}

// reloadSecretsAfterJobs re-reads a FILE secret store once the host jobs have
// run, and returns the provider the host services should start with.
//
// A job (or the host-identity step) is allowed to publish into the store — the
// login broker's token lives there — and `forge env up` read the store before any job
// ran. Without the re-read, the value a job just wrote reaches the services
// only on the NEXT run, which on a fresh project means a first `forge env up`
// whose sign-in does not work. Other providers (external, hosted, none) are
// not files a job can write, and a pulled hosted store was fetched for this
// run on purpose, so they are returned unchanged.
func reloadSecretsAfterJobs(prov secrets.Provider, e *KCLEntities, projectDir string) (secrets.Provider, error) {
	if prov == nil || prov.Kind() != "file" || e == nil || e.SecretProvider == nil || e.SecretProvider.Type != "file" {
		return prov, nil
	}
	reloaded, err := secretProviderFromEntities(e, projectDir)
	if err != nil {
		return nil, fmt.Errorf("secret provider: re-read after jobs: %w", err)
	}
	return reloaded, nil
}

// noteSecretLayering prints, on STDERR, where a linked worktree's secret
// values came from when any were inherited from the primary checkout.
//
// Inheriting credentials from another directory must never be silent: the
// KCL declares `path = "secrets/dev.yaml"`, which reads as project-relative,
// so a developer has to be able to see that it resolved elsewhere — and to
// see it BEFORE debugging why a worktree is talking to the wrong Stripe
// account. STDERR (not stdout) so it cannot corrupt the JSON document of a
// `--json` command.
func noteSecretLayering(prov secrets.Provider, out io.Writer) {
	l, ok := prov.(secrets.Layered)
	if !ok {
		return
	}
	sharedPath, _, inherited, overridden := l.Layering()
	if inherited == 0 && overridden == 0 {
		return
	}
	fmt.Fprintf(out, "[secrets] inherited %d value(s) from the primary checkout's store %s\n", inherited, sharedPath)
	if overridden > 0 {
		fmt.Fprintf(out, "[secrets] %d value(s) overridden by this worktree's own store\n", overridden)
	}
}

// secretRefsFromEntities walks every service's EnvVars and returns the
// declared secret references (those with a non-empty secret_ref).
// SecretKey falls back to EnvName when the KCL secret_key is empty.
// Used for validation and k8s Secret rendering.
func secretRefsFromEntities(e *KCLEntities) []secrets.SecretRef {
	if e == nil {
		return nil
	}
	var refs []secrets.SecretRef
	for i := range e.Workloads {
		refs = append(refs, secretRefsForService(&e.Workloads[i])...)
	}
	return refs
}

// secretRefsForK8sServices is like secretRefsFromEntities but ONLY for
// workloads bound to a cluster forge applies to — those are the refs that
// need rendered Secret objects. Host refs are resolved at launch, compose
// refs are injected as env values, and a HOSTED workload's secrets are the
// control plane's to materialize: none of them need a k8s Secret from forge.
func secretRefsForK8sServices(e *KCLEntities) []secrets.SecretRef {
	return secretRefsOnRuntime(e, RuntimeCluster)
}

// secretRefsForHostServices returns the declared secret refs for host-bound
// workloads only — the set a host launch must be able to resolve from the
// provider (for fail-fast validation before starting the process).
func secretRefsForHostServices(e *KCLEntities) []secrets.SecretRef {
	return secretRefsOnRuntime(e, RuntimeHost)
}

func secretRefsOnRuntime(e *KCLEntities, runtime string) []secrets.SecretRef {
	if e == nil {
		return nil
	}
	var refs []secrets.SecretRef
	for i := range e.Workloads {
		if e.Workloads[i].OnRuntime(runtime) {
			refs = append(refs, secretRefsForService(&e.Workloads[i])...)
		}
	}
	return refs
}

// secretRefsForLaunch is the set `forge env up` must be able to resolve
// before it starts anything: host services' declared refs plus every
// frontend's (a dev server receives its declared secret_refs too).
func secretRefsForLaunch(e *KCLEntities) []secrets.SecretRef {
	refs := secretRefsForHostServices(e)
	if e == nil {
		return refs
	}
	// The local forwarder sends to the destination with the headers in the
	// store; a missing key would start a collector that is refused by the
	// backend with nothing on screen saying why.
	if localCollectorService(e) == clickstack.ForwarderService && e.Observability != nil &&
		e.Observability.Destination != nil && e.Observability.Destination.HeadersSecret != nil {
		h := e.Observability.Destination.HeadersSecret
		refs = append(refs, secrets.SecretRef{EnvName: h.Key, SecretName: h.Name, SecretKey: h.Key})
	}
	for _, fe := range e.Frontends {
		for _, ev := range fe.EffectiveEnvVars() {
			if ev.SecretRef == "" || ev.Name == "" {
				continue
			}
			refs = append(refs, secrets.SecretRef{EnvName: ev.Name, SecretName: ev.SecretRef, SecretKey: ev.SecretKey, Optional: ev.SecretOptional})
		}
	}
	return refs
}

// withEnvInSecretFix substitutes the env name into a pre-flight error's
// generic `--env <env>` fix line, so the command it names is copy-pasteable.
func withEnvInSecretFix(err error, env string) error {
	if err == nil || env == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), "--env <env>", "--env "+env))
}

// renderedSecretsValueSource builds the provider that resolves
// `from="file"` keys of declared Secrets (Bundle.rendered_secrets and a
// RenderedSecrets provider's list).
//
// It is the env's ONE secret store. When the env declares FileSecrets —
// dev's usual shape, with rendered_secrets beside it for plain-manifest
// consumers — that is the provider's own file, so `forge secret set --env
// dev X` feeds a service's secret_ref and a rendered Secret's key alike.
// Otherwise it is the conventional secrets/<env>.yaml. Either way it layers
// over the primary checkout's store in a linked worktree, as the services'
// provider does.
func renderedSecretsValueSource(envName string, entities *KCLEntities) (secrets.Provider, error) {
	return secrets.NewProvider(renderedSecretsStoreConfig(envName, entities))
}

// renderedSecretsStoreConfig is renderedSecretsValueSource's path half, also
// used to REPORT where the values are read from.
func renderedSecretsStoreConfig(envName string, entities *KCLEntities) *secrets.ProviderConfig {
	projectDir := projectDirForKCL()
	rel := filepath.Join("secrets", envName+".yaml")
	if entities != nil && entities.SecretProvider != nil && entities.SecretProvider.Type == "file" && entities.SecretProvider.Path != "" {
		rel = entities.SecretProvider.Path
	}
	if filepath.IsAbs(rel) {
		return &secrets.ProviderConfig{Type: "file", Path: rel}
	}
	return &secrets.ProviderConfig{
		Type:       "file",
		Path:       filepath.Join(projectDir, rel),
		SharedPath: sharedSecretStorePath(projectDir, rel),
	}
}

// scopeSecretsToEnvVars narrows the env-wide secret map to the keys a
// workload DECLARES via EnvVar.secret_ref, and DROPS any slot whose value is
// empty.
//
// The empty-slot rule is the important half. `forge secret ensure` scaffolds
// the store with every declared slot present and blank, so an untouched
// project's secrets/<env>.yaml reads `DATABASE_URL: ""`. Injected as-is that
// is not "no value", it is a REAL env var bound to the empty string — and it
// lands in a layer that outranks project config, so it beat the DSN the KCL
// composes and the migrate job died with
// `required config field database_url is not set`, pointing at a file that
// plainly declares it. A blank slot means "this machine holds no value for
// this name"; the declaration below it should win.
//
// An empty value that is genuinely meaningful (disable a feature by blanking
// it) is expressed in KCL, which is where declarations live and where the
// intent is visible in version control.
func scopeSecretsToEnvVars(all map[string]string, vars []KCLEnvVar) map[string]string {
	if len(all) == 0 {
		return nil
	}
	out := make(map[string]string)
	for _, ev := range vars {
		if ev.SecretRef == "" {
			continue
		}
		if v, ok := all[ev.Name]; ok && v != "" {
			out[ev.Name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// scopeSecretsToService narrows the env-wide secret map to the keys this
// workload DECLARES via a secretRef env var.
//
// This is the trust boundary for host services: the provider resolves the
// whole store once per run, but a process only ever sees what its own KCL
// asks for. A secret added to the store without a matching declaration
// reaches nothing — so KCL stays the single place a value becomes live,
// and one service cannot read another's credentials.
//
// A nil/empty store returns nil.
func scopeSecretsToService(all map[string]string, svc *WorkloadEntity) map[string]string {
	if len(all) == 0 || svc == nil {
		return nil
	}
	refs := secretRefsForService(svc)
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]string, len(refs))
	for _, r := range refs {
		// An EMPTY slot is not a value — see scopeSecretsToEnvVars for why
		// injecting one is worse than injecting nothing.
		if v, ok := all[r.EnvName]; ok && v != "" {
			out[r.EnvName] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// secretRefsForService extracts the declared secret references from one
// workload's spec.env. A ref is "declared" when SecretRef is non-empty.
// SecretKey falls back to EnvName.
func secretRefsForService(s *WorkloadEntity) []secrets.SecretRef {
	var refs []secrets.SecretRef
	for _, ev := range s.EnvVars() {
		if ev.SecretRef == "" {
			continue
		}
		key := ev.SecretKey
		if key == "" {
			key = ev.Name
		}
		refs = append(refs, secrets.SecretRef{
			EnvName:    ev.Name,
			SecretName: ev.SecretRef,
			SecretKey:  key,
			Optional:   ev.SecretOptional,
		})
	}
	return refs
}
