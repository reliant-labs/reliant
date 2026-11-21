package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/vulnscan"
)

// vulnDBDocumentSchema names `forge vuln db status --json`.
const vulnDBDocumentSchema = "forge.dev/vuln-db/v1"

// vulnDBDocument is `forge vuln db status|pull|import --json`.
type vulnDBDocument struct {
	Schema string `json:"schema"`
	// Database is the database as it is on disk.
	Database vulnscan.Status `json:"database"`
	// Fresh: younger than a day, so nothing refreshes it. Stale: older than
	// five days (or absent), so every verdict against it is "stale".
	Fresh bool `json:"fresh"`
	Stale bool `json:"stale"`
	// AgeSeconds is its age now; 0 when absent.
	AgeSeconds int64 `json:"age_seconds"`
	// CacheKey is the key a CI cache of Dir should use.
	CacheKey string `json:"cache_key,omitempty"`
}

func newVulnDBCmd(deps vulnDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "The vulnerability database every forge on this machine shares",
		Long: `The grype vulnerability database: one per machine, shared by every project
(~200 MB download, 3.3 GiB on disk), in $FORGE_VULN_DB_DIR (default
<user cache dir>/forge/vulndb).

` + "`forge vuln scan`" + ` refreshes it when it is more than a day old; ` + "`list`" + ` and ` + "`show`" + ` never
download it. Older than five days, it is stale: every verdict against it exits 2.

  FORGE_VULN_DB_URL   a mirror of https://grype.anchore.io/databases (same layout:
                      <url>/v6/latest.json and the archives it names). grype
                      verifies each archive against the listing's sha256.
  FORGE_VULN_DB_DIR   where the database lives.

CI: cache $FORGE_VULN_DB_DIR keyed by ` + "`forge vuln db status --json | jq -r .cache_key`" + `
after the job (forge-vulndb-v6-<build date>), restoring with the prefix
forge-vulndb-v6- so a job downloads at most once a day.`,
	}
	cmd.AddCommand(newVulnDBStatusCmd(deps), newVulnDBPullCmd(deps), newVulnDBImportCmd(deps))
	return cmdutil.StrictGroup(cmd)
}

func newVulnDBStatusCmd(deps vulnDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Where the vulnerability database is, how old it is, and its CI cache key",
		Long: `Report the shared vulnerability database without downloading anything.

Exit codes: 0 present and not stale; 2 absent, stale, or grype not installed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := deps.openDB(cmd.ErrOrStderr(), false)
			if err != nil {
				return dbExit(err)
			}
			st, err := db.Status(cmd.Context())
			if err != nil {
				return dbExit(err)
			}
			return reportVulnDB(cmd.OutOrStdout(), st, deps.now(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the "+vulnDBDocumentSchema+" document")
	return cmd
}

func newVulnDBPullCmd(deps vulnDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "pull",
		Short: "Download the newest vulnerability database now (and grype, when missing)",
		Long: `Read the database listing now and download the newest build when it is newer
than the one here, whatever the local database's age. Installs grype first when
it is missing.

Exit codes: 0 a current database is in place; 2 the download failed and no usable
database remains.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := deps.openDB(cmd.ErrOrStderr(), true)
			if err != nil {
				return dbExit(err)
			}
			st, err := db.Ensure(cmd.Context(), vulnscan.EnsureOptions{Fetch: true, Force: true})
			if err != nil {
				return dbExit(err)
			}
			return reportVulnDB(cmd.OutOrStdout(), st, deps.now(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the "+vulnDBDocumentSchema+" document")
	return cmd
}

func newVulnDBImportCmd(deps vulnDeps) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Load a vulnerability database archive on a machine with no network",
		Long: `Import a grype database archive (vulnerability-db_v6.*.tar.zst, the file
https://grype.anchore.io/databases/v6/latest.json names) downloaded elsewhere.
Takes about two minutes of CPU. Installs grype first when it is missing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := deps.openDB(cmd.ErrOrStderr(), true)
			if err != nil {
				return dbExit(err)
			}
			st, err := db.Import(cmd.Context(), args[0])
			if err != nil {
				return dbExit(err)
			}
			return reportVulnDB(cmd.OutOrStdout(), st, deps.now(), asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the "+vulnDBDocumentSchema+" document")
	return cmd
}

func reportVulnDB(w io.Writer, st vulnscan.Status, now time.Time, asJSON bool) error {
	doc := vulnDBDocument{
		Schema: vulnDBDocumentSchema, Database: st,
		Fresh: st.Fresh(now), Stale: st.Stale(now), AgeSeconds: int64(st.Age(now) / time.Second), CacheKey: st.CacheKey(),
	}
	if asJSON {
		if err := writeVulnJSON(w, doc); err != nil {
			return err
		}
	} else {
		writeVulnDBText(w, doc, now)
	}
	if doc.Stale {
		return &exitCodeError{code: exitUndetermined, verdict: true, msg: "the vulnerability database is absent or older than 5 days: run `forge vuln db pull` (or `forge vuln db import <file>` offline)"}
	}
	return nil
}

func writeVulnDBText(w io.Writer, doc vulnDBDocument, now time.Time) {
	d := doc.Database
	_, _ = fmt.Fprintf(w, "vulnerability database  %s\n", d.Dir)
	if !d.Present {
		problem := d.Problem
		if problem == "" {
			problem = "absent"
		}
		_, _ = fmt.Fprintf(w, "  none: %s\n", problem)
	} else {
		state := "current"
		switch {
		case doc.Stale:
			state = "STALE (older than 5 days: verdicts against it exit 2)"
		case !doc.Fresh:
			state = "due a refresh (older than a day)"
		}
		_, _ = fmt.Fprintf(w, "  schema %s, built %s (%s ago): %s\n", d.Schema, d.BuiltAt.Format(time.RFC3339), roundAge(now.Sub(d.BuiltAt)), state)
		if d.Source != "" {
			_, _ = fmt.Fprintf(w, "  from %s\n", d.Source)
		}
		_, _ = fmt.Fprintf(w, "  CI cache key: %s\n", doc.CacheKey)
	}
	_, _ = fmt.Fprintf(w, "  listing %s/%s/latest.json", d.URL, vulnscan.SchemaMajor)
	if !d.CheckedAt.IsZero() {
		_, _ = fmt.Fprintf(w, ", last checked %s (newest listed: %s)", d.CheckedAt.Format(time.RFC3339), d.LatestBuiltAt.Format(time.RFC3339))
	}
	_, _ = fmt.Fprintln(w)
}
