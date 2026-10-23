package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/deploytarget"
	"github.com/reliant-labs/forge/internal/devstack"
	"github.com/reliant-labs/forge/internal/secrets"
)

// readEnvConfigEntities is deliberately declaration-only. In particular, it
// must not open a secret provider before env exec has checked placement.
func readEnvConfigEntities(ctx context.Context, projectDir, env string) (*KCLEntities, error) {
	_, restore := activateDevStack(ctx, projectDir, env, renderToLaunch, inspectBlocks)
	defer restore()
	e, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, fmt.Errorf("read deploy/kcl/%s/: %w", env, err)
	}
	if e == nil {
		return nil, fmt.Errorf("deploy/kcl/%s/ rendered no workloads", env)
	}
	return e, nil
}

// hostWorkloadConfig is shared by env up and configuration readback/exec.
// Shell policy belongs to LayerHostEnv; these are only the declared layers.
func hostWorkloadConfig(cfg *config.ProjectConfig, w WorkloadEntity, secretValues map[string]string, env string) map[string]string {
	var defaults map[string]string
	if cfg != nil {
		defaults = loadProjectConfigEnv(cfg, env)
	}
	dev, _ := seedTargetIsDev(env)
	defaults = withDevRunDefaults(defaults, dev)
	defaults = withAirRunDefaults(defaults, hostRunnerSpec(w).Runner)
	out := make(map[string]string)
	for k, v := range defaults {
		out[k] = v
	}
	for k, v := range scopeSecretsToService(secretValues, &w) {
		out[k] = v
	}
	for k, v := range withoutDerivedOTLPIfShellSet(w.HostEnv(), os.Environ()) {
		out[k] = v
	}
	return out
}

// secretMode says what a missing declared secret means to a caller of the
// shared resolver.
//
// THE SPLIT IS READ VERSUS RUN. forge#859 routed `env config` through the
// resolver `env exec` uses, and with it the fail-closed secret check — so a
// readback of an env whose secret store lacked one declared key printed
// nothing and exited 1, though nothing was about to run with the gap. Running
// a process without a secret it declares is the real error, and stays one
// (secretsRequired: env exec, and env up's own launch check). Reading the
// configuration back is not: it reports the gap (secretsReported) and still
// prints everything that does resolve.
type secretMode int

const (
	// secretsReported lists missing declared secrets beside the values.
	secretsReported secretMode = iota
	// secretsRequired refuses when any declared secret is missing.
	secretsRequired
)

// localWorkloadConfig is the single value-resolution path for config and exec.
// Nonlocal callers must not enter here: it may read/pull LOCAL secrets.
//
// missing is the declared secrets the store cannot supply. Under
// secretsRequired it is always empty: a non-empty set is the error instead.
func localWorkloadConfig(ctx context.Context, projectDir, env string, e *KCLEntities, w WorkloadEntity, mode secretMode) (values map[string]string, missing []secrets.SecretRef, err error) {
	if w.Runtime.Type != RuntimeHost && w.Runtime.Type != RuntimeCompose {
		return nil, nil, fmt.Errorf("workload %q is not local", w.Name)
	}
	if err := armLocalSecretsForUp(ctx, env, e, func(string, ...any) {}); err != nil {
		return nil, nil, err
	}
	defer disarmPulledSecrets()
	prov, err := secretProviderFromEntities(e, projectDir)
	if err != nil {
		return nil, nil, fmt.Errorf("secret provider: %w", err)
	}
	missing = secrets.MissingDeclaredRefs(prov, secretRefsForService(&w))
	if len(missing) > 0 && mode == secretsRequired {
		return nil, nil, withEnvInSecretFix(secrets.MissingRefsError(prov, missing, secretStoreLabel(e)), env)
	}
	values, err = localWorkloadValues(projectDir, env, w, prov)
	return values, missing, err
}

// localWorkloadValues resolves a local workload's values once its secrets
// have been judged.
func localWorkloadValues(projectDir, env string, w WorkloadEntity, prov secrets.Provider) (map[string]string, error) {
	if w.Runtime.Type == RuntimeCompose {
		cm := w.Runtime.Compose
		if cm == nil {
			return nil, fmt.Errorf("workload %q has no Compose configuration", w.Name)
		}
		dir := projectDir
		if cm.Shared {
			dir = devstack.SharedProjectDir(projectDir)
		}
		// Pin relative env_file exactly as the Compose deploy path does for a
		// shared project, and to this project otherwise (exec retains the cwd).
		spec := &deploytarget.ComposeSpec{EnvFile: cm.EnvFile, Env: cm.Env, ProjectDirectory: dir}
		return deploytarget.ComposeProcessEnv(spec, prov.All())
	}
	var cfg *config.ProjectConfig
	if store, err := loadProjectStoreFrom(filepath.Join(projectDir, "forge.yaml")); err == nil {
		cfg = store.Config()
	} else {
		return nil, err
	}
	return hostWorkloadConfig(cfg, w, prov.All(), env), nil
}

// resolvedEnvConfigReport reads the configuration of every selected workload.
// Under secretsReported a workload whose store lacks declared secrets still
// reports everything that resolves, and lists the gap in MissingSecrets.
func resolvedEnvConfigReport(ctx context.Context, projectDir, env string, e *KCLEntities, filter string, mode secretMode) (envConfigReport, error) {
	report := envConfigReport{Env: env, Workloads: collectEnvConfigWorkloads(e)}
	if filter != "" {
		report.Workloads = filterEnvConfigWorkloads(report.Workloads, filter)
		if len(report.Workloads) == 0 {
			return report, fmt.Errorf("environment %q declares no workload named %q", env, filter)
		}
	}
	for i := range report.Workloads {
		row := &report.Workloads[i]
		for _, w := range e.Workloads {
			if row.Name != w.Name || row.Kind != w.Kind {
				continue
			}
			if w.Runtime.Type == RuntimeHost || w.Runtime.Type == RuntimeCompose {
				values, missing, err := localWorkloadConfig(ctx, projectDir, env, e, w, mode)
				if err != nil {
					return report, err
				}
				row.Env = values
				for _, r := range missing {
					row.MissingSecrets = append(row.MissingSecrets, envConfigMissingSecret{
						Name: r.EnvName, Secret: r.SecretName, Key: r.Key(),
					})
				}
			}
		}
	}
	overlayLaunchedEnv(&report, projectDir, env)
	report.SecretStore = secretStoreLabel(e)
	return report, nil
}
