package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// newSecretsCmd is the `forge env secrets` command group. Today it exposes a
// single `sync` subcommand that materializes the k8s Secrets an env's
// dotenv secret_provider implies — the standalone primitive CI lanes need
// so they can provision cluster secrets WITHOUT a full `forge env deploy`
// (which would also bring up compose/host targets). External-provider
// envs are a no-op (forge never renders their values).
func newSecretsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Project the env's secret_provider into the cluster",
		Long: `Work with the secrets an environment's bundle secret_provider implies.

A secret is declared once as a reference (EnvVar.secret_ref) and its value
comes from the env's bundle secret_provider. For a DotenvSecrets provider,
forge can render the declared cluster secret_refs into k8s Secret objects
and apply them. ExternalSecrets envs are a no-op (their values are
provisioned out-of-band).

For a Secret ESO syncs from a store (forge.StoreSource), the migration steps are:

  forge secret import      --env prod --from <ns>/<secret> --map key=NAME   # copy in, create-only
  forge env secrets verify prod <secret>                                    # live vs the store's copy: match / no-match
  forge env secrets adopt  prod <ns>/<secret>                               # hand the verified Secret to ESO`,
	}
	cmd.AddCommand(newSecretsSyncCmd(), newSecretsVerifyCmd(), newSecretsAdoptCmd())
	return cmdutil.StrictGroup(cmd)
}

// newSecretsSyncCmd renders + applies the k8s Secrets for an env whose
// bundle declares a DotenvSecrets provider. It is the same projection the
// deploy phase runs (applyK8sSecretsFromProvider) lifted into a standalone
// command, so a CI lane can do `forge env secrets sync dev` before its
// `kcl run | kubectl apply` and retire a hand-rolled secret-bootstrap
// script. Guarded to LOCAL clusters (it renders plaintext); fail-fasts on
// a declared ref the dotenv can't supply.
func newSecretsSyncCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "sync <environment>",
		Short: "Render + apply the k8s Secrets for an env's dotenv secret_provider (local clusters only)",
		Args:  cobra.ExactArgs(1),
		Long: `Render the k8s Secret objects implied by an environment's bundle
secret_provider and apply them to the current kubectl context.

Only DotenvSecrets providers produce output — forge reads the gitignored
dotenv (keyed by env-var name), builds a Secret per declared cluster
secret_ref, and applies it. ExternalSecrets / no provider are a no-op.
The dotenv renders PLAINTEXT, so this refuses any non-local cluster.

Use it in a CI test lane (k3d/kind) to provision cluster secrets the
forge-native way instead of a bespoke create-secret script:

  forge env secrets sync dev
  kcl run deploy/kcl/dev/main.k -D image_tag=ci | kubectl apply -f -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSecretsSync(cmd.Context(), args[0], dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print the Secret manifests instead of applying them")
	return cmd
}

// runSecretsSync renders the env's KCL, derives its namespace + deploy
// groups (the groups drive the local-cluster guard), and delegates to the
// shared applyK8sSecretsFromProvider projection.
func runSecretsSync(ctx context.Context, envName string, dryRun bool) error {
	store, err := loadProjectStore()
	if err != nil {
		return err
	}
	projectDir := projectDirForKCL()

	entities, err := RenderKCL(ctx, projectDir, envName)
	if err != nil {
		return fmt.Errorf("render KCL: %w", err)
	}

	// Namespace resolution mirrors runDeploy: per-env cluster namespace,
	// falling back to "<project>-<env>".
	namespace := k8sClusterNamespaceForEnv(ctx, envName)
	if namespace == "" {
		namespace = store.Meta().Name + "-" + envName
	}

	groups, gerr := buildDeployGroups(envName, entities, namespace)
	if gerr != nil {
		return fmt.Errorf("group services: %w", gerr)
	}

	// secrets sync is local-cluster-only and targets the current kubectl
	// context (no --context flag), so the context override is empty.
	return applyK8sSecretsFromProvider(ctx, entities, groups, namespace, "", envName, dryRun)
}
