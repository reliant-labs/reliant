package cli

// `forge db restore <env> --to <time>`: declare, in the env's main.k, a new
// managed database restored from another one's backups to a point in time
// (db_restore_kcl.go). It is a writer of a declaration, not an imperative
// restore: the deploy that ships the edit is what creates the database, so the
// restore is reviewed, committed and repeatable like any other change, and it
// never touches the database it restores from.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// dbRestoreRetention is how far back hosted Postgres keeps backups (7 days of
// point-in-time recovery). A cluster env's object store may keep more, so an
// older time is warned about, not refused.
const dbRestoreRetention = 7 * 24 * time.Hour

// dbRestoreGuide is the guide that walks the whole procedure.
const dbRestoreGuide = "https://docs.reliantlabs.io/guides/hosting/restore-postgres-point-in-time"

// dbRestoreRender renders env's managed databases; a var so a test states
// the render instead of evaluating KCL.
var dbRestoreRender = func(ctx context.Context, projectDir, env string) ([]DatabaseEntity, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return nil, err
	}
	return entities.Databases, nil
}

type dbRestoreOptions struct {
	to       string
	database string
	as       string
	dryRun   bool
	now      func() time.Time
}

func newDBRestoreCommand() *cobra.Command {
	opts := dbRestoreOptions{now: time.Now}
	cmd := &cobra.Command{
		Use:   "restore <env> --to <time>",
		Short: "Declare a managed database restored from backups to a point in time (writes the env's KCL)",
		Long: `Declare a new managed database, restored from an existing one's backups to a
point in time, in deploy/kcl/<env>/main.k.

  forge db restore prod --to 2026-11-20T15:02:00Z
  forge db restore prod --to 2026-11-20T15:02:00Z --database shop --as shop-before-inc12
  forge db restore prod --to 2026-11-20T15:02:00Z --dry-run    # print the edit, write nothing

A restore is a declaration: a forge.ManagedDatabase whose spec.restore names
the source database and the time. This command writes it beside the source
(same runtime and namespace), lists it in the same Bundle databases list, and
renders the env to prove the edit took. It never touches the source: a
restore always creates a NEW database, so a mistaken one cannot make things
worse. The deploy that ships the edit creates it, from the source's newest base
backup before --to, replaying archived changes up to it.

  --to        an RFC 3339 time (2026-11-20T15:02:00Z, or with an offset), not in
              the future. Recovery stops there: a change committed after it is
              not replayed. Hosted Postgres keeps 7 days of backups.
  --database  the database to restore from; default the env's only managed one
  --as        the new database's name; default <source>-restored

Then: review and commit the edit, build and deploy a release, check the
restored rows (forge db query <env> "<sql>" --database <name>), and point the
app at it by passing its name where the env passes the source's. The guide:
` + dbRestoreGuide,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := requireProjectDir("forge db restore")
			if err != nil {
				return err
			}
			return runDBRestore(cmd.Context(), cmd.OutOrStdout(), projectDir, args[0], opts)
		},
	}
	cmd.Flags().StringVar(&opts.to, "to", "", "the point in time to restore to, RFC 3339 (required)")
	cmd.Flags().StringVar(&opts.database, "database", "", "the managed database to restore from (default: the env's only one)")
	cmd.Flags().StringVar(&opts.as, "as", "", "the restored database's name (default: <source>-restored)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print the edit and write nothing")
	return cmd
}

// dbRestorePointInTime validates --to and spells it in UTC, as the
// ManagedDatabase spec's RFC 3339 check accepts it.
func dbRestorePointInTime(to string, now time.Time) (string, error) {
	if strings.TrimSpace(to) == "" {
		return "", fmt.Errorf("--to <time> is required: the RFC 3339 moment to restore to, e.g. %s", now.UTC().Add(-time.Hour).Truncate(time.Minute).Format(time.RFC3339))
	}
	t, err := time.Parse(time.RFC3339, to)
	if err != nil {
		return "", fmt.Errorf("--to %q is not an RFC 3339 time: write it in full with a zone, e.g. 2026-11-20T15:02:00Z", to)
	}
	if t.After(now) {
		return "", fmt.Errorf("--to %s is in the future (now is %s): a restore replays backups up to a moment that has happened", t.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	}
	return t.UTC().Format(time.RFC3339), nil
}

func runDBRestore(ctx context.Context, out io.Writer, projectDir, env string, opts dbRestoreOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.now == nil {
		opts.now = time.Now
	}
	now := opts.now()
	pit, err := dbRestorePointInTime(opts.to, now)
	if err != nil {
		return err
	}
	if env == "" || strings.ContainsAny(env, `/\`) || strings.HasPrefix(env, ".") {
		return fmt.Errorf("%q is not an env name", env)
	}
	path := filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
	orig, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("env %q has no deploy/kcl/%s/main.k in this checkout: forge db restore writes the restore into an env's main.k", env, env)
		}
		return err
	}
	rel := filepath.ToSlash(relOrAbs(projectDir, path))
	next, source, inserts, err := restoreInKCLInserts(string(orig), opts.database, opts.as, pit)
	if err != nil {
		return fmt.Errorf("forge db restore %s: %w", env, err)
	}
	name := opts.as
	if name == "" {
		name = source + "-restored"
	}
	if t, _ := time.Parse(time.RFC3339, pit); now.Sub(t) > dbRestoreRetention {
		_, _ = fmt.Fprintf(out, "warning: %s is more than 7 days ago; hosted Postgres keeps 7 days of backups, so a hosted restore to it fails at deploy\n", pit)
	}
	if opts.dryRun {
		_, _ = fmt.Fprintf(out, "would declare database %s in %s: %s as it was at %s (nothing written)\n\n", name, rel, source, pit)
		_, _ = fmt.Fprint(out, insertPreview(string(orig), next, inserts))
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, []byte(next), info.Mode().Perm()); err != nil {
		return err
	}
	if back, err := os.ReadFile(path); err != nil || string(back) != next {
		_ = atomicfile.WriteFile(path, orig, info.Mode().Perm())
		return fmt.Errorf("forge db restore: %s did not read back as written; restored it", rel)
	}
	dbs, rerr := dbRestoreRender(ctx, projectDir, env)
	if rerr == nil {
		rerr = renderedRestore(dbs, source, name, pit)
	}
	if rerr != nil {
		if werr := atomicfile.WriteFile(path, orig, info.Mode().Perm()); werr != nil {
			return fmt.Errorf("forge db restore: %v; and restoring %s failed: %w", rerr, rel, werr)
		}
		return fmt.Errorf("forge db restore %s: %v\n  %s is unchanged", env, rerr, rel)
	}
	_, _ = fmt.Fprintf(out, "declared database %s in %s: %s as it was at %s\n", name, rel, source, pit)
	_, _ = fmt.Fprintf(out, "  the app still reads %s. Next:\n", source)
	_, _ = fmt.Fprintf(out, "    1. review and commit it, then build and deploy a release (forge env build %[1]s --release <v>; forge env deploy %[1]s <v>): the deploy creates %[2]s\n", env, name)
	_, _ = fmt.Fprintf(out, "    2. check the rows: forge db query %s \"<select ...>\" --database %s\n", env, name)
	_, _ = fmt.Fprintf(out, "    3. point the app at it: pass %s's name where the env passes %s's, and deploy again\n", name, source)
	_, _ = fmt.Fprintf(out, "  guide: %s\n", dbRestoreGuide)
	return nil
}

// renderedRestore checks the env's render carries the restore as declared.
func renderedRestore(dbs []DatabaseEntity, source, name, pit string) error {
	var gotSource, gotNew bool
	for _, d := range dbs {
		switch d.Name {
		case source:
			gotSource = true
		case name:
			r := d.Spec.Restore
			if r == nil || r.SourceDatabase != source || r.PointInTime != pit {
				return fmt.Errorf("the rendered database %s does not carry restore {sourceDatabase = %q, pointInTime = %q}", name, source, pit)
			}
			gotNew = true
		}
	}
	// Both must render: the copy, and the source it restores from. Neither
	// alone proves the edit reached the env the restore is for.
	if !gotNew {
		return fmt.Errorf("the edit does not reach the env's rendered databases (the Bundle may be built in another file)")
	}
	if !gotSource {
		return fmt.Errorf("the env renders no database %q to restore from", source)
	}
	return nil
}

// insertPreview shows each insertion as the edited file's lines that hold
// it, prefixed "+ ", with the line of the original it follows.
func insertPreview(orig, next string, ins []kclInsert) string {
	var b strings.Builder
	shift := 0
	for i, in := range ins {
		if i > 0 {
			b.WriteString("\n")
		}
		text := strings.Trim(in.text, "\n")
		lo := in.at + shift + strings.Index(in.text, text)
		hi := lo + len(text)
		shift += len(in.text)
		start := strings.LastIndexByte(next[:lo], '\n') + 1
		end := len(next)
		if j := strings.IndexByte(next[hi:], '\n'); j >= 0 {
			end = hi + j
		}
		fmt.Fprintf(&b, "at line %d:\n", strings.Count(orig[:in.at], "\n")+1)
		for _, l := range strings.Split(next[start:end], "\n") {
			b.WriteString("+ " + l + "\n")
		}
	}
	return b.String()
}
