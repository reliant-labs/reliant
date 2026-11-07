package cli

// `forge obs changes [env] [--since 2h]` — what changed? The release-timeline
// rows (promotions, rollbacks, failed deploys) merged with the env's activity
// feed, newest first. Cloud: the deploy ledger and ActivityService through the
// control plane; local: this machine's ledger.

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

const schemaObsChanges = "forge.obs.changes/v1"

var obsChangesSchema = obsout.Schema{
	Name: schemaObsChanges,
	Fields: []string{
		"scope", "scope.env", "scope.backend", "scope.env_id", "scope.from", "scope.to",
		"query_mode", "changes", "changes.kind", "changes.at", "changes.summary", "changes.version", "changes.promotion_id",
		"changes.direction", "changes.verb", "changes.target", "changes.actor", "changes.via",
		"warnings", "suggested",
	},
}

type obsChangesDoc struct {
	obsout.Head
	Scope     obsout.Scope        `json:"scope"`
	QueryMode string              `json:"query_mode"`
	Changes   []obsbackend.Change `json:"changes"`
	obsout.Page
	Warnings  []string `json:"warnings,omitempty"`
	Suggested []string `json:"suggested,omitempty"`
}

type obsChangesOpts struct {
	out      obsout.Flags
	backend  string
	since    time.Duration
	from, to string
}

func newObsChangesCmd() *cobra.Command {
	var o obsChangesOpts
	cmd := &cobra.Command{
		Use:   "changes [env]",
		Short: "What changed? Promotions, rollbacks, failed deploys and activity, newest first",
		Long: `List what changed in [env] (default dev) over --since (default 2h): releases
promoted or rolled back, deploys that failed, and the activity feed (restarts,
secret rotations, triage). Locally it is this machine's ledger: promotions,
failed applies and the times the dev stack started.

  ` + Name() + ` obs changes prod --since 6h
  ` + Name() + ` obs changes dev --json

` + obsChangesSchema.Help(),
		Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsChanges(cmd.Context(), defaultObsDeps(cmd), obsEnvArg(args, 0), o)
		},
	}
	f := cmd.Flags()
	o.out.Register(f, 50)
	f.StringVar(&o.backend, "backend", "", "local | cloud (default: from the env's declaration)")
	f.DurationVar(&o.since, "since", 2*time.Hour, "how far back to read (ignored with --from)")
	f.StringVar(&o.from, "from", "", "range start, RFC 3339")
	f.StringVar(&o.to, "to", "", "range end, RFC 3339 (default now)")
	return cmd
}

func runObsChanges(ctx context.Context, d obsDeps, env string, o obsChangesOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsChangesSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsChanges, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsChanges, err) }
	rng, err := resolveRange(d.Now(), o.since, o.from, o.to)
	if err != nil {
		return fail(err)
	}
	b, _, err := (&obsCommon{backend: o.backend}).open(ctx, d, env, "changes")
	if err != nil {
		return fail(err)
	}
	reader, ok := b.(obsbackend.ChangesReader)
	if !ok {
		return fail(obsbackend.Undetermined("the %s backend cannot list changes", b.Info().Backend))
	}
	res, err := reader.Changes(ctx, obsbackend.ChangesRequest{Env: env, Range: rng, Limit: o.out.Limit, Project: hostedProjectName()})
	if err != nil {
		return fail(err)
	}
	info := b.Info()
	doc := obsChangesDoc{
		Scope:     obsout.Scope{Env: env, Backend: info.Backend, EnvID: info.EnvID, From: rng.From, To: rng.To},
		QueryMode: QueryModeStructured, Changes: res.Changes, Warnings: res.Warnings,
		Page: obsout.Page{Truncated: res.Omitted > 0, Omitted: res.Omitted},
	}
	if doc.Changes == nil {
		doc.Changes = []obsbackend.Change{}
	}
	for _, c := range res.Changes {
		if c.Kind == obsbackend.ChangePromotion || c.Kind == obsbackend.ChangeFailedDeploy {
			doc.Suggested = obsout.Suggest(
				obsout.Command("forge", "obs", "check", env, "--promotion", c.PromotionID),
				obsout.Command("forge", "env", "releases", "show", env, firstNonEmptyStr(c.PromotionID, c.Version)),
			)
			if c.PromotionID == "" {
				doc.Suggested = obsout.Suggest(obsout.Command("forge", "env", "releases", "show", env, c.Version))
			}
			break
		}
	}
	doc.Stamp(schemaObsChanges, nil)
	if done, err := o.out.Emit(d.Stdout, obsChangesSchema, doc, fields); done || err != nil {
		return err
	}
	rows := make([][]string, len(res.Changes))
	for i, c := range res.Changes {
		rows[i] = []string{c.At.UTC().Format("01-02 15:04:05"), c.Kind, c.Summary, firstNonEmptyStr(c.Actor, c.Via)}
	}
	obsout.Table(d.Stdout, []string{"TIME", "KIND", "WHAT", "BY"}, rows, 80)
	if res.Omitted > 0 {
		fmt.Fprintf(d.Stdout, "\n(%d older; --limit to show them)\n", res.Omitted)
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(d.Stderr, "warning: %s\n", w)
	}
	return nil
}

func firstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
