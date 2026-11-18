package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/reliant-labs/forge/internal/deploytarget"
)

// localPrewriteInput is what the local stage's pre-apply writes read.
type localPrewriteInput struct {
	entities, fullEntities *KCLEntities
	groups                 []deploytarget.ServiceGroup
	namespace              string
	deployContext          string
	envName                string
	dryRun                 bool
}

// applyLocalPrewrites is everything the local stage writes into a cluster
// BEFORE the cluster apply, in the one order that is correct:
//
//  1. the release's missing Namespaces (deploy_namespaces_first.go), because
//     every step after this one writes INTO a namespace, and the release may
//     be the one that creates it (forge bug ac69bea3: the observer's Role in
//     claimable-dns was minted before claimable-dns existed);
//  2. the provider-projected Secrets, before the Deployments that reference
//     them roll out;
//  3. the secret store's bootstrap values, create-if-absent;
//  4. the minted kubeconfig credentials (a ServiceAccount, its Roles in every
//     namespace it is granted, its token Secret), before the workloads that
//     mount them.
//
// The order lives here, in one place, so a test can hold it.
func applyLocalPrewrites(ctx context.Context, in localPrewriteInput) error {
	if err := applyMissingRenderedNamespaces(ctx, in.entities, in.dryRun); err != nil {
		return err
	}
	if err := applyK8sSecretsFromProvider(ctx, in.entities, in.groups, in.namespace, in.deployContext, in.envName, in.dryRun); err != nil {
		return err
	}
	if err := ensureBootstrapSecrets(ctx, in.fullEntities, in.dryRun, os.Stdout); err != nil {
		return err
	}
	if err := mintDeployKubeconfigSecrets(ctx, in.entities, in.namespace, in.dryRun); err != nil {
		return fmt.Errorf("kubeconfig secrets: %w", err)
	}
	return nil
}
