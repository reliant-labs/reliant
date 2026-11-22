// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/prrecords"
)

// `reliant github records` reads PR records (dev-docs/pr-records.md): the pull
// requests Reliant mirrors from GitHub's webhook deliveries. It reads Reliant,
// never GitHub, so it spends no GitHub quota.

// recordsPolicyFlags is whose review markers count and what the labels mean.
// The defaults are Reliant's built-in review pipeline's.
type recordsPolicyFlags struct {
	trusted  []string
	rereview []string
	passed   string
	changes  string
	held     []string
	check    string
	settle   time.Duration
}

func (f *recordsPolicyFlags) bind(cmd *cobra.Command, labels bool) {
	lp := prrecords.DefaultLabelPolicy()
	cmd.Flags().StringSliceVar(&f.trusted, "trusted-author", []string{"reliant-labs[bot]", "reliant-01"}, "A login or numeric id whose review markers decide a head's verdict (repeatable)")
	cmd.Flags().StringSliceVar(&f.rereview, "rereview-author", []string{"reliant-labs[bot]"}, "A login or numeric id whose evidence re-review re-decides a head (repeatable)")
	if !labels {
		return
	}
	cmd.Flags().StringVar(&f.passed, "passed-label", lp.Passed, "The label that shows a passed head")
	cmd.Flags().StringVar(&f.changes, "changes-label", lp.Changes, "The label that shows a head that got changes")
	cmd.Flags().StringSliceVar(&f.held, "held-label", lp.Held, "A label that takes a pull request out of the pipeline (repeatable)")
	cmd.Flags().StringVar(&f.check, "check", lp.Check, "The check run that reports a head's verdict")
	cmd.Flags().DurationVar(&f.settle, "settle", lp.Settle, "How long after a record changed a disagreement is still expected")
}

func (f *recordsPolicyFlags) policy() *reliantv1.VerdictPolicy {
	return &reliantv1.VerdictPolicy{TrustedAuthors: f.trusted, RereviewAuthors: f.rereview}
}

func (f *recordsPolicyFlags) labelPolicy() prrecords.LabelPolicy {
	return prrecords.LabelPolicy{Passed: f.passed, Changes: f.changes, Held: f.held, Check: f.check, Settle: f.settle}
}

func newPullRequestRecordClient(cmd *cobra.Command) (reliantv1connect.PullRequestRecordServiceClient, *connection, error) {
	conn, err := resolveConnection(cmd)
	if err != nil {
		return nil, nil, err
	}
	return reliantv1connect.NewPullRequestRecordServiceClient(conn.httpClient(), conn.ServerURL), conn, nil
}

func newGitHubRecordsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "records",
		Short: "Read the pull request records Reliant keeps from GitHub's webhooks",
		Long: `Read PR records: the pull requests of the repositories Reliant keeps records
for, mirrored from GitHub's webhook deliveries (state, head, labels, the
pipeline markers of reviews, the check runs on each head). A head's verdict is
computed under --trusted-author when it is read.

Records read Reliant, never GitHub: no GitHub quota is spent. You see a
repository's records when your own GitHub access reaches it.`,
	}
	cmd.AddCommand(newGitHubRecordsListCmd(), newGitHubRecordsGetCmd(), newGitHubRecordsDiffCmd())
	return cmd
}

// listAllRecords pages through a repository's records.
func listAllRecords(ctx context.Context, client reliantv1connect.PullRequestRecordServiceClient, repo, state string, policy *reliantv1.VerdictPolicy) ([]*reliantv1.PullRequestRecord, error) {
	var out []*reliantv1.PullRequestRecord
	token := ""
	for {
		req := &reliantv1.ListPullRequestRecordsRequest{Repository: repo, PageSize: 200, PageToken: token, Policy: policy}
		if state != "" {
			req.State = &state
		}
		resp, err := client.ListPullRequestRecords(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		out = append(out, resp.Msg.GetRecords()...)
		if token = resp.Msg.GetNextPageToken(); token == "" {
			return out, nil
		}
	}
}

func newGitHubRecordsListCmd() *cobra.Command {
	var repo, state string
	var asJSON bool
	var pf recordsPolicyFlags
	cmd := &cobra.Command{
		Use:          "list",
		Short:        "List a repository's pull request records",
		Example:      `  reliant github records list --repo reliant-labs/forge --state open`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, conn, err := newPullRequestRecordClient(cmd)
			if err != nil {
				return err
			}
			recs, err := listAllRecords(cmd.Context(), client, repo, state, pf.policy())
			if err != nil {
				return conn.annotate(fmt.Errorf("listing records: %w", err))
			}
			if asJSON {
				return writeRecordsJSON(cmd.OutOrStdout(), true, recs...)
			}
			printRecordTable(cmd.OutOrStdout(), recs)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "The repository, owner/name")
	cmd.Flags().StringVar(&state, "state", "open", "open, closed, merged, or empty for all")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the records as JSON")
	_ = cmd.MarkFlagRequired("repo")
	pf.bind(cmd, false)
	return cmd
}

func newGitHubRecordsGetCmd() *cobra.Command {
	var repo string
	var number int32
	var pf recordsPolicyFlags
	cmd := &cobra.Command{
		Use:          "get",
		Short:        "Show one pull request's record, heads, review markers and checks (JSON)",
		Example:      `  reliant github records get --repo reliant-labs/reliant --pr 3050`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, conn, err := newPullRequestRecordClient(cmd)
			if err != nil {
				return err
			}
			resp, err := client.GetPullRequestRecord(cmd.Context(), connect.NewRequest(&reliantv1.GetPullRequestRecordRequest{
				Repository: repo, Number: number, Policy: pf.policy(),
			}))
			if err != nil {
				return conn.annotate(fmt.Errorf("reading the record: %w", err))
			}
			return writeRecordsJSON(cmd.OutOrStdout(), false, resp.Msg)
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "The repository, owner/name")
	cmd.Flags().Int32Var(&number, "pr", 0, "The pull request number")
	_ = cmd.MarkFlagRequired("repo")
	_ = cmd.MarkFlagRequired("pr")
	pf.bind(cmd, false)
	return cmd
}

// recordsDiffReport is `records diff --json`.
type recordsDiffReport struct {
	Repository string              `json:"repository"`
	Counts     map[string]int      `json:"counts"`
	Rows       []prrecords.DiffRow `json:"rows"`
}

func newGitHubRecordsDiffCmd() *cobra.Command {
	var repo string
	var asJSON, all bool
	var pf recordsPolicyFlags
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Compare each open pull request's labels and verdict check with its head's verdict",
		Long: `Compare each open pull request's review labels, and its verdict check, with
the verdict its head's review markers carry: the shadow report of PR records.

Each open pull request is one of: agree, label_missing (the head has a verdict
its label does not show), label_stale (a verdict label the head's verdict does
not support), check_disagrees, settling (a disagreement younger than --settle),
held or draft. It exits 0 even when they disagree: it reports.`,
		Example:      `  reliant github records diff --repo reliant-labs/forge --json`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, conn, err := newPullRequestRecordClient(cmd)
			if err != nil {
				return err
			}
			recs, err := listAllRecords(cmd.Context(), client, repo, core.PRRecordOpen, pf.policy())
			if err != nil {
				return conn.annotate(fmt.Errorf("listing records: %w", err))
			}
			report := diffRecords(repo, recs, pf.labelPolicy(), time.Now())
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			printDiffReport(cmd.OutOrStdout(), report, all)
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "The repository, owner/name")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the report as JSON")
	cmd.Flags().BoolVar(&all, "all", false, "List every pull request, not only those that disagree")
	_ = cmd.MarkFlagRequired("repo")
	pf.bind(cmd, true)
	return cmd
}

// diffRecords classifies each open record from the service's answer.
func diffRecords(repo string, recs []*reliantv1.PullRequestRecord, lp prrecords.LabelPolicy, now time.Time) recordsDiffReport {
	report := recordsDiffReport{Repository: repo, Counts: map[string]int{}, Rows: []prrecords.DiffRow{}}
	for _, r := range recs {
		rec := &core.PRRecord{
			Number: int(r.GetNumber()), HeadSHA: r.GetHeadSha(), State: r.GetState(), Draft: r.GetDraft(),
			Labels: r.GetLabels(), ObservedAt: r.GetObservedAt().AsTime(),
		}
		var checks []core.PRRecordCheck
		for _, c := range r.GetChecks() {
			pc := core.PRRecordCheck{HeadSHA: r.GetHeadSha(), Name: c.GetName(), CheckRunID: c.GetCheckRunId(), Conclusion: c.GetConclusion()}
			if c.GetCompletedAt() != nil {
				t := c.GetCompletedAt().AsTime()
				pc.CompletedAt = &t
			}
			checks = append(checks, pc)
		}
		row := prrecords.Classify(rec, prrecords.Verdict{Kind: r.GetVerdict().GetKind()}, checks, lp, now)
		report.Counts[row.Class]++
		report.Rows = append(report.Rows, row)
	}
	return report
}

func printDiffReport(out io.Writer, report recordsDiffReport, all bool) {
	var parts []string
	for _, class := range []string{prrecords.DiffAgree, prrecords.DiffLabelMissing, prrecords.DiffLabelStale,
		prrecords.DiffCheckDisagrees, prrecords.DiffSettling, prrecords.DiffHeld, prrecords.DiffDraft} {
		parts = append(parts, fmt.Sprintf("%s=%d", class, report.Counts[class]))
	}
	fmt.Fprintf(out, "%s: %d open; %s\n", report.Repository, len(report.Rows), strings.Join(parts, " "))
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PR\tCLASS\tVERDICT\tCHECK\tHEAD\tLABELS")
	for _, r := range report.Rows {
		if !all && (r.Class == prrecords.DiffAgree || r.Class == prrecords.DiffHeld || r.Class == prrecords.DiffDraft) {
			continue
		}
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\n", r.Number, r.Class, recordsDash(r.Verdict), recordsDash(r.Check), recordsShortSHA(r.Head), strings.Join(r.Labels, ","))
	}
	_ = w.Flush()
}

func printRecordTable(out io.Writer, recs []*reliantv1.PullRequestRecord) {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PR\tSTATE\tVERDICT\tHEAD\tOBSERVED\tTITLE")
	for _, r := range recs {
		state := r.GetState()
		if r.GetDraft() {
			state += " (draft)"
		}
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\n", r.GetNumber(), state, recordsDash(r.GetVerdict().GetKind()),
			recordsShortSHA(r.GetHeadSha()), r.GetObservedAt().AsTime().UTC().Format(time.RFC3339), r.GetTitle())
	}
	_ = w.Flush()
}

// writeRecordsJSON prints proto messages in their canonical JSON form: one
// object, or with list an array.
func writeRecordsJSON[M proto.Message](out io.Writer, list bool, msgs ...M) error {
	raw := make([]json.RawMessage, 0, len(msgs))
	for _, m := range msgs {
		b, err := protojson.Marshal(m)
		if err != nil {
			return err
		}
		raw = append(raw, b)
	}
	if !list && len(raw) == 1 {
		return writeJSON(out, raw[0])
	}
	return writeJSON(out, raw)
}

func recordsDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func recordsShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
