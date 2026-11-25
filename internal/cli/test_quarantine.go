package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/testflakes"
	"github.com/reliant-labs/forge/pkg/testreport"
)

// newTestQuarantineCmd is `forge test quarantine`: the ci/quarantine/ list,
// control-plane's format (docs/flake-quarantine.md there) as forge's standard.
func newTestQuarantineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quarantine",
		Short: "Manage the flaky-test quarantine list (ci/quarantine/)",
		Long: `The quarantine list is one file per quarantined test,
ci/quarantine/<lang>/<package dir>/<Test>.yaml (` + "`_root`" + ` for the module's root
package), so two quarantine changes never conflict. Each holds flat
` + "`key: value`" + ` lines: lang, package, test, issue, reason, signature,
quarantined_at, expires_at, commit, run.

The project's lanes skip every quarantined Go test:

  go test -skip "$(forge test quarantine skip-regex go)" ./...

An entry expires 48h after it is written: the quarantine is the gap between
detection and deletion. An expired entry stays applied and is reported
overdue (list, forge test flakes). Delete the entry in the change that deletes
the test or rewrites it as a deterministic test.`,
	}
	cmd.AddCommand(newTestQuarantineAddCmd(), newTestQuarantineRemoveCmd(), newTestQuarantineListCmd(), newTestQuarantineSkipRegexCmd())
	return cmdutil.StrictGroup(cmd)
}

func quarantineModule(root string) (string, error) {
	module, err := testflakes.ModulePath(root)
	if err != nil {
		return "", exitCodeError{code: exitWrong, msg: fmt.Sprintf("forge test quarantine: no Go module at %s: %v", root, err)}
	}
	return module, nil
}

func newTestQuarantineAddCmd() *cobra.Command {
	var (
		issue, reason, signature, commit, run string
		expires                               time.Duration
		force                                 bool
	)
	cmd := &cobra.Command{
		Use:   "add <test-id> [--issue <url>] [--reason <text>] [--expires 48h]",
		Short: "Quarantine a test: write its ci/quarantine/ entry",
		Long: `Writes ci/quarantine/go/<package dir>/<Test>.yaml for the test.

<test-id> is the id forge prints (go:example.com/shop/internal/billing:TestRefund)
or <package>:<Test>, where <package> is the import path or the directory
relative to the module root (./internal/billing:TestRefund). A subtest
quarantines its top-level test, which is what -skip addresses.

A test already quarantined is left as it is (--force rewrites the entry).
--expires 0 writes an entry that is never overdue.`,
		Example: `  forge test quarantine add go:example.com/shop/internal/billing:TestRefund --issue https://github.com/acme/shop/issues/12
  forge test quarantine add ./internal/billing:TestRefund --reason "times out under -race"`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := testRoot()
			module, err := quarantineModule(root)
			if err != nil {
				return err
			}
			id, err := testflakes.ParseQuarantineTarget(args[0], module)
			if err != nil {
				return exitCodeError{code: exitWrong, msg: "forge test quarantine add: " + err.Error()}
			}
			now := time.Now().UTC().Truncate(time.Second)
			e := testflakes.Entry{Lang: id.Lang, Package: id.Package, Test: id.Name, Issue: issue, Reason: reason, Signature: signature, QuarantinedAt: now, Commit: commit, Run: run}
			if expires > 0 {
				e.ExpiresAt = now.Add(expires)
			}
			res, err := testflakes.QuarantineAdd(root, module, e, force)
			if err != nil {
				return exitCodeError{code: exitWrong, msg: "forge test quarantine add: " + err.Error()}
			}
			out := cmd.OutOrStdout()
			switch {
			case res.Existed && !res.Rewritten:
				fmt.Fprintf(out, "%s is already quarantined: %s (unchanged; --force rewrites it)\n", id, res.Entry.Path)
			case res.Rewritten:
				fmt.Fprintf(out, "rewrote %s\n", res.Entry.Path)
			default:
				fmt.Fprintf(out, "quarantined %s: %s\n", id, res.Entry.Path)
			}
			if !res.Entry.ExpiresAt.IsZero() {
				fmt.Fprintf(out, "expires %s: delete or rewrite the test, and remove this entry in the same change\n", res.Entry.ExpiresAt.UTC().Format(time.RFC3339))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&issue, "issue", "", "The issue tracking the flake (its URL)")
	f.StringVar(&reason, "reason", "", "Why the test is quarantined, in a few words")
	f.StringVar(&signature, "signature", "", "The failure signature it was quarantined for")
	f.StringVar(&commit, "commit", "", "The commit the flake was seen on")
	f.StringVar(&run, "run", "", "The run that showed it (a URL or run id)")
	f.DurationVar(&expires, "expires", testflakes.DefaultQuarantineTTL, "How long until the entry is overdue (0: never)")
	f.BoolVar(&force, "force", false, "Rewrite an existing entry")
	return cmd
}

func newTestQuarantineRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "remove <test-id>",
		Short:        "Remove a test's quarantine entry",
		Long:         "Deletes the test's ci/quarantine/ entry, and the package directories it leaves empty. Exit status 1 when the test is not quarantined.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := testRoot()
			module, err := quarantineModule(root)
			if err != nil {
				return err
			}
			id, err := testflakes.ParseQuarantineTarget(args[0], module)
			if err != nil {
				return exitCodeError{code: exitWrong, msg: "forge test quarantine remove: " + err.Error()}
			}
			rel, err := testflakes.QuarantineRemove(root, module, id)
			if err != nil {
				if errors.Is(err, testflakes.ErrNotQuarantined) {
					return exitCodeError{code: exitWrong, msg: "forge test quarantine remove: " + err.Error(), verdict: true}
				}
				return exitCodeError{code: exitWrong, msg: "forge test quarantine remove: " + err.Error()}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", rel)
			return nil
		},
	}
}

type quarantineListDoc struct {
	testreport.Head
	Entries []quarantineListEntry `json:"entries"`
}

type quarantineListEntry struct {
	Test          testreport.TestID `json:"test"`
	Path          string            `json:"path"`
	Issue         string            `json:"issue,omitempty"`
	Reason        string            `json:"reason,omitempty"`
	Signature     string            `json:"signature,omitempty"`
	QuarantinedAt *time.Time        `json:"quarantined_at,omitempty"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
	Overdue       bool              `json:"overdue"`
	Commit        string            `json:"commit,omitempty"`
	Run           string            `json:"run,omitempty"`
}

func newTestQuarantineListCmd() *cobra.Command {
	var overdue, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list [--overdue] [--json]",
		Short: "List the quarantined tests, overdue ones flagged",
		Long: `Lists every entry under ci/quarantine/: one row per test with its expiry and
issue. An entry past its expires_at is OVERDUE: it stays applied (the test
is still skipped) until the change that deletes or rewrites the test removes
it. --overdue lists only those (the port of quarantine.sh expired).`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runQuarantineList(cmd.OutOrStdout(), testRoot(), time.Now(), overdue, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&overdue, "overdue", false, "Only entries past their expires_at")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the entries as JSON")
	return cmd
}

func runQuarantineList(w io.Writer, root string, now time.Time, overdueOnly, jsonOut bool) error {
	entries, errs := testflakes.QuarantineList(root)
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "forge test quarantine list: warning: %v\n", e)
	}
	doc := quarantineListDoc{Head: testreport.Head{Schema: "forge.test.quarantine/v1", OK: true}, Entries: []quarantineListEntry{}}
	for _, e := range entries {
		if overdueOnly && !e.Overdue(now) {
			continue
		}
		le := quarantineListEntry{Test: e.ID(), Path: e.Path, Issue: e.Issue, Reason: e.Reason, Signature: e.Signature, Overdue: e.Overdue(now), Commit: e.Commit, Run: e.Run}
		if !e.QuarantinedAt.IsZero() {
			t := e.QuarantinedAt
			le.QuarantinedAt = &t
		}
		if !e.ExpiresAt.IsZero() {
			t := e.ExpiresAt
			le.ExpiresAt = &t
		}
		doc.Entries = append(doc.Entries, le)
	}
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}
	if len(doc.Entries) == 0 {
		if overdueOnly {
			fmt.Fprintln(w, "No overdue quarantine entries.")
		} else {
			fmt.Fprintln(w, "No quarantined tests (ci/quarantine/).")
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TEST\tEXPIRES\tISSUE\tFILE")
	for _, e := range doc.Entries {
		exp := "never"
		if e.ExpiresAt != nil {
			exp = e.ExpiresAt.UTC().Format(time.RFC3339)
		}
		if e.Overdue {
			exp += " OVERDUE"
		}
		issue := e.Issue
		if issue == "" {
			issue = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Test, exp, issue, e.Path)
	}
	return tw.Flush()
}

func newTestQuarantineSkipRegexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "skip-regex <lang>",
		Short: "Print the -skip pattern that skips every quarantined test (go)",
		Long: `Prints the ` + "`go test -skip`" + ` value that skips every quarantined Go test,
^(TestA|TestB)$, or nothing when none is quarantined (the port of
quarantine.sh go-skip). The names come from the entry file names alone. An
overlay file named by $CI_QUARANTINE_EXTRA ("<package>\t<Test>" lines) adds
to them. It always exits 0 for go: an unreadable list is an empty one, and the
lanes then run every test, the safe direction.`,
		Example:      `  go test -skip "$(forge test quarantine skip-regex go)" ./...`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if lang := strings.ToLower(args[0]); lang != string(testreport.LangGo) {
				return exitCodeError{code: exitWrong, msg: fmt.Sprintf("forge test quarantine skip-regex: %q: only go is supported today", args[0])}
			}
			if re := testflakes.GoSkipRegex(testRoot(), os.Getenv(testflakes.EnvQuarantineExtra)); re != "" {
				fmt.Fprintln(cmd.OutOrStdout(), re)
			}
			return nil
		},
	}
}
