package cli

// The discussion half of `forge obs issue`: the assignee, the incident and the
// timeline in the issue document, and the `comment` and `assign` verbs.

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/obsbackend"
	"github.com/reliant-labs/forge/internal/obsout"
)

const (
	defaultTimelineLimit = 10
	maxTimelineLimit     = 100
	// maxCommentCell bounds a comment body in the text form; --json carries it whole.
	maxCommentCell = 300
)

type obsUserDoc struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func userDoc(u *obsbackend.IssueUser) *obsUserDoc {
	if u == nil {
		return nil
	}
	return &obsUserDoc{ID: u.ID, Name: u.Name}
}

type obsActorDoc struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type obsStatusChangeDoc struct {
	From         string    `json:"from,omitempty"`
	To           string    `json:"to"`
	Version      string    `json:"version,omitempty"`
	IgnoredUntil time.Time `json:"ignored_until,omitzero"`
}

type obsCommentDoc struct {
	ID        string      `json:"id"`
	IssueID   string      `json:"issue_id"`
	Author    *obsUserDoc `json:"author,omitempty"`
	Body      string      `json:"body"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at,omitzero"`
}

func commentDoc(c obsbackend.IssueComment) obsCommentDoc {
	return obsCommentDoc{ID: c.ID, IssueID: c.IssueID, Author: userDoc(c.Author), Body: c.Body, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// obsAssignmentDoc is an assignment change; an absent side is "unassigned".
type obsAssignmentDoc struct {
	From *obsUserDoc `json:"from,omitempty"`
	To   *obsUserDoc `json:"to,omitempty"`
}

type obsChatLinkDoc struct {
	ChatID   string      `json:"chat_id"`
	Title    string      `json:"title,omitempty"`
	LinkedBy *obsUserDoc `json:"linked_by,omitempty"`
	LinkedAt time.Time   `json:"linked_at,omitzero"`
}

// obsTimelineEntryDoc is one timeline entry; Kind says which of Status,
// Comment, Assignment and Chat is set.
type obsTimelineEntryDoc struct {
	ID         string              `json:"id"`
	Time       time.Time           `json:"time"`
	IssueID    string              `json:"issue_id"`
	Kind       string              `json:"kind"`
	Actor      obsActorDoc         `json:"actor"`
	Status     *obsStatusChangeDoc `json:"status,omitempty"`
	Comment    *obsCommentDoc      `json:"comment,omitempty"`
	Assignment *obsAssignmentDoc   `json:"assignment,omitempty"`
	Chat       *obsChatLinkDoc     `json:"chat,omitempty"`
}

func timelineEntryDoc(e obsbackend.TimelineEntry) obsTimelineEntryDoc {
	d := obsTimelineEntryDoc{ID: e.ID, Time: e.Time, IssueID: e.IssueID, Kind: e.Kind, Actor: obsActorDoc(e.Actor)}
	if s := e.Status; s != nil {
		d.Status = &obsStatusChangeDoc{From: s.From, To: s.To, Version: s.Version, IgnoredUntil: s.IgnoredUntil}
	}
	if c := e.Comment; c != nil {
		cd := commentDoc(*c)
		d.Comment = &cd
	}
	if a := e.Assignment; a != nil {
		d.Assignment = &obsAssignmentDoc{From: userDoc(a.From), To: userDoc(a.To)}
	}
	if c := e.Chat; c != nil {
		d.Chat = &obsChatLinkDoc{ChatID: c.ChatID, Title: c.ChatTitle, LinkedBy: userDoc(c.LinkedBy), LinkedAt: c.LinkedAt}
	}
	return d
}

// obsIssueTimeline is the timeline section of an issue document, filled only
// when --timeline-limit asks for one.
type obsIssueTimeline struct {
	Timeline []obsTimelineEntryDoc `json:"timeline,omitempty"`
	// TimelineOmitted counts older entries --timeline-limit cut.
	TimelineOmitted int `json:"timeline_omitted,omitempty"`
	// TimelineIssueID is set when the timeline is of another issue than the one
	// named: the issue it was merged into.
	TimelineIssueID string `json:"timeline_issue_id,omitempty"`
	// TimelineNote says why a requested timeline is missing.
	TimelineNote string `json:"timeline_note,omitempty"`
}

// readTimeline fills the timeline. It never fails the verb: a control plane
// that does not serve the timeline yet, or refuses it, is a note on stderr and
// in the document, and the rest of the issue is still shown.
func readTimeline(ctx context.Context, errOut io.Writer, r obsbackend.IssueReader, id string, limit int) obsIssueTimeline {
	var out obsIssueTimeline
	if limit == 0 {
		return out
	}
	res, err := r.ListIssueTimeline(ctx, obsbackend.ListIssueTimelineRequest{ID: id, Limit: limit})
	if err != nil {
		out.TimelineNote = "timeline unavailable: " + err.Error()
		fmt.Fprintln(errOut, "note:", out.TimelineNote)
		return out
	}
	for _, e := range res.Entries {
		out.Timeline = append(out.Timeline, timelineEntryDoc(e))
	}
	out.TimelineOmitted = res.Omitted
	if res.IssueID != "" && res.IssueID != id {
		out.TimelineIssueID = res.IssueID
	}
	return out
}

func issueUserLabel(u *obsUserDoc) string {
	switch {
	case u == nil:
		return "none"
	case u.Name != "" && u.Name != u.ID:
		return u.Name + " (" + u.ID + ")"
	}
	return u.ID
}

func issueActorLabel(a obsActorDoc) string {
	switch {
	case a.Name != "":
		return a.Name
	case a.ID != "":
		return a.Kind + ":" + a.ID
	}
	return orDash(a.Kind)
}

// issueOneLine collapses a body to one line of at most max runes.
func issueOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		return string([]rune(s)[:max]) + "…"
	}
	return s
}

func timelineDetail(e obsTimelineEntryDoc) string {
	switch {
	case e.Status != nil:
		from := e.Status.From
		if from == "" {
			from = "opened"
		}
		s := from + "→" + e.Status.To
		if e.Status.Version != "" {
			s += " " + e.Status.Version
		}
		if !e.Status.IgnoredUntil.IsZero() {
			s += " until " + e.Status.IgnoredUntil.UTC().Format(time.RFC3339)
		}
		return s
	case e.Comment != nil:
		who := issueActorLabel(e.Actor)
		if a := e.Comment.Author; a != nil {
			who = issueUserLabel(a)
		}
		return fmt.Sprintf("%s  %s: %s", e.Comment.ID, who, issueOneLine(e.Comment.Body, maxCommentCell))
	case e.Assignment != nil:
		return issueUserLabel(e.Assignment.From) + "→" + issueUserLabel(e.Assignment.To)
	case e.Chat != nil:
		if e.Chat.Title != "" {
			return fmt.Sprintf("%s %q", e.Chat.ChatID, issueOneLine(e.Chat.Title, 80))
		}
		return e.Chat.ChatID
	}
	return ""
}

func renderTimeline(w io.Writer, id string, doc obsIssueDoc) {
	if doc.TimelineNote != "" {
		fmt.Fprintf(w, "\n%s\n", doc.TimelineNote)
		return
	}
	if len(doc.Timeline) == 0 {
		return
	}
	head := "timeline"
	if doc.TimelineIssueID != "" {
		head = fmt.Sprintf("timeline of %s (%s was merged into it)", doc.TimelineIssueID, id)
	}
	if doc.TimelineOmitted > 0 {
		head += fmt.Sprintf(", newest %d of %d", len(doc.Timeline), len(doc.Timeline)+doc.TimelineOmitted)
	}
	fmt.Fprintf(w, "\n%s:\n", head)
	for _, e := range doc.Timeline {
		by := ""
		if e.Kind != obsbackend.TimelineComment {
			by = "  by " + issueActorLabel(e.Actor)
		}
		fmt.Fprintf(w, "  %s  %-10s %s%s\n", e.Time.UTC().Format(time.RFC3339), e.Kind, timelineDetail(e), by)
	}
}

// ── comment ─────────────────────────────────────────────────────────────

type obsIssueCommentDoc struct {
	obsout.Head
	Scope     obsout.Scope  `json:"scope"`
	QueryMode string        `json:"query_mode"`
	Issue     obsIssueIDDoc `json:"issue"`
	Comment   obsCommentDoc `json:"comment"`
	Suggested []string      `json:"suggested,omitempty"`
}

// obsIssueIDDoc names the issue a write that does not read it was made on.
type obsIssueIDDoc struct {
	ID string `json:"id"`
}

type obsIssueWriteOpts struct {
	backend string
	out     obsout.Flags
}

func (o *obsIssueWriteOpts) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.BoolVar(&o.out.JSON, "json", false, "print one JSON document on stdout")
	f.StringVar(&o.out.Fields, "fields", "", "with --json: keep only these comma-separated dotted fields (the head is always kept)")
	f.StringVar(&o.backend, "backend", "", "local | cloud (default: from the env's declaration)")
}

func newObsIssueCommentCmd() *cobra.Command {
	var o obsIssueWriteOpts
	cmd := &cobra.Command{
		Use:   "comment <id> <text> [env]",
		Short: "Add a comment to an issue",
		Long: `Add a comment to an issue's timeline. The text is plain text or markdown, stored
verbatim (at most 10000 characters); it is shown to everyone who can see the
issue. A hosted env only: a local issue has no discussion, so this exits 2.

  ` + Name() + ` obs issue comment ISS-7f3a "reproduced on v1.4.0; the pool is exhausted" prod

The document carries the stored comment (comment.id, comment.created_at). A
credential that acts for no person cannot comment.

` + obsIssueSchema.Help(),
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsIssueComment(cmd.Context(), defaultObsDeps(cmd), args[0], args[1], obsEnvArg(args, 2), o)
		},
	}
	o.register(cmd)
	return cmd
}

func runObsIssueComment(ctx context.Context, d obsDeps, id, text, env string, o obsIssueWriteOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsIssueSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsIssue, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsIssue, err) }
	r, b, err := openIssueWriter(ctx, d, env, "comment", o)
	if err != nil {
		return fail(err)
	}
	res, err := r.AddIssueComment(ctx, obsbackend.AddIssueCommentRequest{ID: id, Body: text})
	if err != nil {
		return fail(err)
	}
	doc := obsIssueCommentDoc{
		Scope: writeScope(d, b), QueryMode: QueryModeStructured, Issue: obsIssueIDDoc{ID: id}, Comment: commentDoc(res.Comment),
		Suggested: obsout.Suggest(obsout.Command("forge", "obs", "issue", id, env)),
	}
	doc.Stamp(schemaObsIssue, nil)
	if done, err := o.out.Emit(d.Stdout, obsIssueSchema, doc, fields); done || err != nil {
		return err
	}
	fmt.Fprintf(d.Stdout, "commented on %s (%s)\n", id, res.Comment.ID)
	return nil
}

// ── assign ──────────────────────────────────────────────────────────────

func newObsIssueAssignCmd() *cobra.Command {
	var o obsIssueWriteOpts
	cmd := &cobra.Command{
		Use:   "assign <id> <user-id|me|none> [env]",
		Short: "Assign an issue to a person, or clear its assignee",
		Long: `Set the issue's assignee to a user id, or clear it with "none". The user must be
a member of the issue's organization. A hosted env only: a local issue carries
no state, so this exits 2.

  ` + Name() + ` obs issue assign ISS-7f3a 7c1e2a90-0b1d-4a61-9e7d-5a1d3c9f2b10 prod
  ` + Name() + ` obs issue assign ISS-7f3a none prod

Use "me" for the person the verified credential acts for (including a user-owned
CLI login or helper token). The server resolves it; an organization automation
credential acts for nobody and is refused FailedPrecondition (exit 2).
  ` + Name() + ` obs issue assign ISS-7f3a me prod

This requires server-side AssignIssue "me" resolution; on a control plane that
only accepts literal user ids (including CP #1402 alone), pass a user id instead.

The write sends the row version it read, so a change made since is refused
("changed since you read it", exit 1) instead of overwriting it. It is never
retried: read the issue again, then decide.

` + obsIssueSchema.Help(),
		Args: cobra.RangeArgs(2, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runObsIssueAssign(cmd.Context(), defaultObsDeps(cmd), args[0], args[1], obsEnvArg(args, 2), o)
		},
	}
	o.register(cmd)
	return cmd
}

// assigneeArg reads the user id an assignment names: "none" clears it.
func assigneeArg(s string) (string, error) {
	switch s = strings.TrimSpace(s); strings.ToLower(s) {
	case "none":
		return "", nil
	case "me":
		return "me", nil
	case "":
		return "", obsbackend.Wrong(`the assignee is a user id, "me", or "none" to clear it`)
	}
	if len(s) > 128 {
		return "", obsbackend.Wrong("the assignee user id is longer than 128 characters")
	}
	return s, nil
}

func runObsIssueAssign(ctx context.Context, d obsDeps, id, who, env string, o obsIssueWriteOpts) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fields, err := o.out.Parsed(obsIssueSchema)
	if err != nil {
		return obsFail(d, o.out.JSON, schemaObsIssue, err)
	}
	fail := func(err error) error { return obsFail(d, o.out.JSON, schemaObsIssue, err) }
	assignee, err := assigneeArg(who)
	if err != nil {
		return fail(err)
	}
	r, b, err := openIssueWriter(ctx, d, env, "assign", o)
	if err != nil {
		return fail(err)
	}
	res, err := r.AssignIssue(ctx, obsbackend.AssignIssueRequest{ID: id, Assignee: assignee})
	if err != nil {
		return fail(err)
	}
	doc := obsIssueDoc{
		Scope: writeScope(d, b), QueryMode: QueryModeStructured, Issue: issueRowDoc(res.Issue), Suspects: []obsSuspectDoc{},
		Suggested: obsout.Suggest(obsout.Command("forge", "obs", "issue", res.Issue.ID, env)),
	}
	doc.Stamp(schemaObsIssue, nil)
	if done, err := o.out.Emit(d.Stdout, obsIssueSchema, doc, fields); done || err != nil {
		return err
	}
	if a := doc.Issue.Assignee; a != nil {
		fmt.Fprintf(d.Stdout, "%s is now assigned to %s\n", res.Issue.ID, issueUserLabel(a))
	} else {
		fmt.Fprintf(d.Stdout, "%s is now unassigned\n", res.Issue.ID)
	}
	return nil
}

func openIssueWriter(ctx context.Context, d obsDeps, env, verb string, o obsIssueWriteOpts) (obsbackend.IssueReader, obsbackend.Backend, error) {
	c := obsCommon{backend: o.backend, out: o.out}
	b, _, err := c.open(ctx, d, env, verb)
	if err != nil {
		return nil, nil, err
	}
	r, err := issueReader(b)
	return r, b, err
}

func writeScope(d obsDeps, b obsbackend.Backend) obsout.Scope {
	now := d.Now().UTC()
	return obsout.Scope{Env: b.Info().Env, Backend: b.Info().Backend, EnvID: b.Info().EnvID, From: now, To: now}
}
