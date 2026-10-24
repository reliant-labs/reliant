// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/pkg/crypto"
	"github.com/reliant-labs/forge/pkg/observe/dbtrace"
	"github.com/reliant-labs/forge/pkg/orm"

	"github.com/reliant-labs/reliant/internal/app"
	"github.com/reliant-labs/reliant/internal/vault"
	"github.com/reliant-labs/reliant/pkg/config"
)

// newDBVaultCmd is `reliant db vault`: rotating the credential vault's
// key-encryption key. Run it where the servers run (`kubectl exec` into an
// api-server pod), so it reads the very RELIANT_VAULT_KEY and DATABASE_URL
// they serve with. It prints key ids and counts, never key material.
func newDBVaultCmd() *cobra.Command {
	vaultCmd := &cobra.Command{
		Use:   "vault",
		Short: "Rotate the credential vault's key-encryption key",
		Long: `Rotate the key-encryption key (KEK) in RELIANT_VAULT_KEY.

The KEK wraps one data key per user; the data keys seal the credentials. A
rotation re-wraps the data keys and leaves every sealed value as it is:

  1. Set RELIANT_VAULT_KEY to "<new-id>:<new>,<old-id>:<old>" on every server
     and roll them, so the new KEK is primary and the old one still unwraps.
  2. ` + "`reliant db vault rewrap`" + ` moves every data key onto the new KEK.
  3. ` + "`reliant db vault verify`" + ` proves none still needs the old KEK.
  4. Set RELIANT_VAULT_KEY to "<new-id>:<new>", roll the servers, and run
     ` + "`verify`" + ` again under it. Only then destroy the old key.`,
	}

	var dryRun bool
	rewrapCmd := &cobra.Command{
		Use:   "rewrap",
		Short: "Re-wrap every data key under the keyring's primary KEK",
		Long: `Re-wrap every stored data key under RELIANT_VAULT_KEY's primary (first)
KEK, in one transaction. The keyring must still hold every KEK a data key is
wrapped under; a key that does not open fails the run and nothing is written.
Keys already on the primary are skipped, so a rerun is a no-op.

--dry-run opens every data key and reports what would move, writing nothing.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVault(cmd, func(ctx context.Context, db orm.Context, ring *crypto.Keyring) error {
				rep, err := vault.Rewrap(ctx, db, ring, dryRun)
				if err != nil {
					return err
				}
				mode := "rewrap"
				if dryRun {
					mode = "rewrap (dry run, nothing written)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", mode, rep)
				return nil
			})
		},
	}
	rewrapCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Open every data key and report what would move; write nothing")
	vaultCmd.AddCommand(rewrapCmd)

	verifyCmd := &cobra.Command{
		Use:   "verify",
		Short: "Prove every data key is wrapped under the primary KEK and opens",
		Long: `Read every stored data key in one READ ONLY transaction and check it is
wrapped under RELIANT_VAULT_KEY's primary KEK and opens under it. Exits
non-zero, naming the first offending key id, otherwise. When it passes, every
other KEK in the keyring can be dropped.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withVault(cmd, func(ctx context.Context, db orm.Context, ring *crypto.Keyring) error {
				rep, err := vault.VerifyWrapped(ctx, db, ring)
				if rep.Primary != "" {
					// Also on failure: it counts the keys still off the primary.
					fmt.Fprintf(cmd.OutOrStdout(), "verify: %s\n", rep)
				}
				return err
			})
		},
	}
	vaultCmd.AddCommand(verifyCmd)

	// DATABASE_URL (DatabaseConfig) and RELIANT_VAULT_KEY (VaultMaintenanceConfig).
	config.RegisterDatabaseConfigFlags(vaultCmd)
	config.RegisterVaultMaintenanceConfigFlags(vaultCmd)
	return vaultCmd
}

// withVault loads the keyring and opens the database for one vault command.
func withVault(cmd *cobra.Command, f func(ctx context.Context, db orm.Context, ring *crypto.Keyring) error) error {
	vcfg, err := config.LoadVaultMaintenanceConfig(cmd)
	if err != nil {
		return err
	}
	ring, err := crypto.ParseKeyring(vcfg.GetVaultKey())
	if err != nil {
		return fmt.Errorf("%s is malformed: %w", vault.EnvKey, err)
	}
	dcfg, err := config.LoadDatabaseConfig(cmd)
	if err != nil {
		return err
	}
	_, url, _ := app.DatabaseURLs(dcfg)
	sqlDB, err := dbtrace.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()
	client, err := orm.NewClientWithDB(sqlDB, "postgres")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	return f(cmd.Context(), client, ring)
}
