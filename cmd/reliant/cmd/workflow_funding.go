// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
)

// A run of a chat in an organization asks before it moves from the
// organization's AI accounts to the person's own (control-plane
// docs/design/org-provider-accounts.md §10), and waits until it is answered.
//
// The CLI shows the question, and never answers it. Answering is a person's
// consent to spend their own money: the server takes it only from a person
// signed in to the app (ResolvePersonalFundingChoice, person_required), and the
// CLI login is an access token every agent shell inherits. So on a terminal or
// not, `workflow run --follow`, `workflow follow` and `workflow watch` print one
// line when the run starts waiting on the question, naming what it waits on and
// where to answer it; the run keeps waiting, as it does with nobody present
// (§10.3). `workflow funding <execution-id>` prints the question.

// fundingQuestionReader is the one ProviderAccountService read the CLI makes
// of a chat's question; reliantv1connect.ProviderAccountServiceClient
// satisfies it.
type fundingQuestionReader interface {
	GetPersonalFundingQuestion(context.Context, *connect.Request[reliantv1.GetPersonalFundingQuestionRequest]) (*connect.Response[reliantv1.GetPersonalFundingQuestionResponse], error)
}

// fundingPollInterval is how often a follow reads the chat's question.
var fundingPollInterval = 15 * time.Second

// fundingAnswerPlace is where the question is answered.
const fundingAnswerPlace = "open the chat in the Reliant app (web or desktop)"

// fundingReasonWords says why the chat asks, without the org's name (the CLI
// has only its id).
func fundingReasonWords(q *reliantv1.PersonalFundingQuestion) string {
	switch q.GetReason() {
	case reliantv1.PersonalFundingQuestion_REASON_LIMITED:
		if at := q.GetOrgResetAt(); at != "" {
			return "the organization's AI accounts are rate-limited until " + at
		}
		return "the organization's AI accounts are rate-limited"
	case reliantv1.PersonalFundingQuestion_REASON_UNSERVED:
		return "none of the organization's AI accounts serve " + q.GetModel()
	default:
		return "the organization's AI accounts can't serve this chat right now"
	}
}

// fundingNotice is the one line a follow prints when the run starts waiting
// on the question.
func fundingNotice(executionID string, q *reliantv1.PersonalFundingQuestion) string {
	own := q.GetPersonalProvider()
	if l := q.GetPersonalAccountLabel(); l != "" {
		own += " (" + l + ")"
	}
	return fmt.Sprintf("reliant: waiting: %s. The chat asks before it uses your own %s account; to answer, %s. "+
		"Until then it waits (reliant workflow funding %s).", fundingReasonWords(q), own, fundingAnswerPlace, executionID)
}

// watchFundingQuestion prints fundingNotice to w once each time the chat asks
// (a new asked_at), until ctx ends. A read that fails is skipped: the follow's
// own stream reports a run that cannot be read.
func watchFundingQuestion(ctx context.Context, c fundingQuestionReader, executionID string, w io.Writer, interval time.Duration) {
	var told string
	check := func() {
		resp, err := c.GetPersonalFundingQuestion(ctx, connect.NewRequest(&reliantv1.GetPersonalFundingQuestionRequest{ChatId: executionID}))
		if err != nil {
			return
		}
		q := resp.Msg.GetQuestion()
		if q == nil || q.GetState() != reliantv1.PersonalFundingQuestion_STATE_ASKED || q.GetAskedAt() == told {
			return
		}
		told = q.GetAskedAt()
		fmt.Fprintln(w, fundingNotice(executionID, q))
	}
	check()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		}
	}
}

// startFundingWatch runs watchFundingQuestion beside a follow and returns the
// function that stops it.
func startFundingWatch(ctx context.Context, c fundingQuestionReader, executionID string, w io.Writer) (stop func()) {
	if c == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		watchFundingQuestion(ctx, c, executionID, w, fundingPollInterval)
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}

func newWorkflowFundingCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "funding <execution-id>",
		Short: "Show whether a run waits to ask before using your own AI account",
		Long: `A run of a chat in an organization asks before it moves from the
organization's AI accounts to your own, and waits until it is answered. This
prints the question: why it asks, which of your accounts would serve, and
whether it was answered.

The answer is your consent to spend your own account, so only a person signed
in to the Reliant app can give it: ` + fundingAnswerPlace + `. The CLI login
is an access token any agent's shell can use, so the server does not take it
from here. To stop a chat asking, choose "Use my accounts" or "Wait" for the
organization in ` + providersSettingsPlace + `.

Exit codes:
  0  the run has no open question
  4  the run waits on an open question`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := reliantv1connect.NewProviderAccountServiceClient(conn.httpClient(), conn.ServerURL)
			open, err := writeFundingQuestion(cmd.Context(), client, args[0], cmd.OutOrStdout(), jsonOut)
			if err != nil {
				return conn.annotate(err)
			}
			if open {
				return fundingOpenError{executionID: args[0]}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print JSON")
	return cmd
}

// fundingOpenError is `workflow funding`'s exit 4: the run waits on an open
// question. A script polls on it.
type fundingOpenError struct{ executionID string }

func (e fundingOpenError) Error() string {
	return "run " + e.executionID + " waits on your answer"
}

// ExitCode is the process exit code the CLI's main returns.
func (fundingOpenError) ExitCode() int { return 4 }

// fundingQuestionJSON is `workflow funding --json`.
type fundingQuestionJSON struct {
	ExecutionID     string `json:"execution_id"`
	State           string `json:"state"`
	Reason          string `json:"reason,omitempty"`
	OrgID           string `json:"org_id,omitempty"`
	Model           string `json:"model,omitempty"`
	PersonalAccount string `json:"personal_account,omitempty"`
	OrgResetAt      string `json:"org_reset_at,omitempty"`
	AskedAt         string `json:"asked_at,omitempty"`
}

var (
	fundingStateWords = map[reliantv1.PersonalFundingQuestion_State]string{
		reliantv1.PersonalFundingQuestion_STATE_ASKED:        "asked",
		reliantv1.PersonalFundingQuestion_STATE_USE_PERSONAL: "use_personal",
		reliantv1.PersonalFundingQuestion_STATE_KEEP_WAITING: "keep_waiting",
	}
	fundingReasonCodes = map[reliantv1.PersonalFundingQuestion_Reason]string{
		reliantv1.PersonalFundingQuestion_REASON_LIMITED:  "limited",
		reliantv1.PersonalFundingQuestion_REASON_UNUSABLE: "unusable",
		reliantv1.PersonalFundingQuestion_REASON_UNSERVED: "unserved",
	}
)

// writeFundingQuestion prints the run's question and reports whether it is
// open (asked and not answered).
func writeFundingQuestion(ctx context.Context, c fundingQuestionReader, executionID string, w io.Writer, jsonOut bool) (bool, error) {
	resp, err := c.GetPersonalFundingQuestion(ctx, connect.NewRequest(&reliantv1.GetPersonalFundingQuestionRequest{ChatId: executionID}))
	if err != nil {
		return false, fmt.Errorf("reading the run's question: %w", err)
	}
	q := resp.Msg.GetQuestion()
	open := q != nil && q.GetState() == reliantv1.PersonalFundingQuestion_STATE_ASKED
	if jsonOut {
		out := fundingQuestionJSON{ExecutionID: executionID, State: "none"}
		if q != nil {
			out.State = fundingStateWords[q.GetState()]
			out.Reason = fundingReasonCodes[q.GetReason()]
			out.OrgID, out.Model, out.OrgResetAt, out.AskedAt = q.GetOrgId(), q.GetModel(), q.GetOrgResetAt(), q.GetAskedAt()
			out.PersonalAccount = strings.TrimSpace(q.GetPersonalProvider() + " " + q.GetPersonalAccountLabel())
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return open, enc.Encode(out)
	}
	if q == nil {
		fmt.Fprintf(w, "Run %s is not waiting to ask about your own AI account.\n", executionID)
		return false, nil
	}
	switch q.GetState() {
	case reliantv1.PersonalFundingQuestion_STATE_USE_PERSONAL:
		fmt.Fprintf(w, "Run %s continues on your own %s account: you answered \"Use my account\" (%s).\n",
			executionID, q.GetPersonalProvider(), fundingReasonWords(q))
	case reliantv1.PersonalFundingQuestion_STATE_KEEP_WAITING:
		fmt.Fprintf(w, "Run %s waits for the organization's AI accounts: you answered \"Keep waiting\" (%s).\n",
			executionID, fundingReasonWords(q))
	default:
		fmt.Fprintf(w, "Run %s waits for your answer.\n", executionID)
		fmt.Fprintf(w, "  Why:           %s\n", fundingReasonWords(q))
		own := q.GetPersonalProvider()
		if l := q.GetPersonalAccountLabel(); l != "" {
			own += " (" + l + ")"
		}
		fmt.Fprintf(w, "  Would use:     your own %s account\n", own)
		fmt.Fprintf(w, "  Organization:  %s\n", q.GetOrgId())
		fmt.Fprintf(w, "  Asked:         %s\n", q.GetAskedAt())
		fmt.Fprintf(w, "To answer, %s. Only a person signed in to the app can: it spends your own account.\n", fundingAnswerPlace)
	}
	return open, nil
}
