package obsbackend

// The discussion half of the cloud issues reader: IssuesService.ListIssueTimeline,
// AddIssueComment and AssignIssue (control plane cp#1351), by procedure name with
// locally declared protojson shapes, like cloud_issues.go.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
)

const (
	procListIssueTimeline = "controlplane.v1.IssuesService/ListIssueTimeline"
	procAddIssueComment   = "controlplane.v1.IssuesService/AddIssueComment"
	procAssignIssue       = "controlplane.v1.IssuesService/AssignIssue"

	timelinePageSize = 100
	// maxTimelinePages bounds the walk to the newest entries: the control plane
	// lists a timeline oldest first, so they are on the last page.
	maxTimelinePages = 50
)

type wireActor struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type wireComment struct {
	ID        string    `json:"id"`
	IssueID   string    `json:"issueId"`
	Author    *wireUser `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (w wireComment) comment() IssueComment {
	return IssueComment{
		ID: w.ID, IssueID: w.IssueID, Author: w.Author.user(), Body: w.Body,
		CreatedAt: w.CreatedAt.UTC(), UpdatedAt: unsetEpoch(w.UpdatedAt),
	}
}

type wireIssueTimelineEntry struct {
	ID      string    `json:"id"`
	Time    time.Time `json:"time"`
	IssueID string    `json:"issueId"`
	Actor   wireActor `json:"actor"`

	Status *struct {
		From         string    `json:"from"`
		To           string    `json:"to"`
		Version      string    `json:"version"`
		IgnoredUntil time.Time `json:"ignoredUntil"`
	} `json:"status"`
	Comment *struct {
		Comment wireComment `json:"comment"`
	} `json:"comment"`
	Assignment *struct {
		From *wireUser `json:"from"`
		To   *wireUser `json:"to"`
	} `json:"assignment"`
	Chat *struct {
		Link struct {
			ChatID    string    `json:"chatId"`
			ChatTitle string    `json:"chatTitle"`
			LinkedBy  *wireUser `json:"linkedBy"`
			LinkedAt  time.Time `json:"linkedAt"`
		} `json:"link"`
	} `json:"chat"`
}

var wireActorKind = map[string]string{"KIND_USER": ActorUser, "KIND_MACHINE": ActorMachine, "KIND_SYSTEM": ActorSystem}

// entry reports false for an entry of a kind this forge does not know (a
// newer control plane): it is skipped, never an error.
func (w wireIssueTimelineEntry) entry() (TimelineEntry, bool) {
	e := TimelineEntry{
		ID: w.ID, Time: w.Time.UTC(), IssueID: w.IssueID,
		Actor: IssueActor{Kind: wireActorKind[w.Actor.Kind], ID: w.Actor.ID, Name: w.Actor.Name},
	}
	switch {
	case w.Status != nil:
		e.Kind = TimelineStatus
		e.Status = &StatusChange{
			From: wireStatus[w.Status.From], To: wireStatus[w.Status.To],
			Version: w.Status.Version, IgnoredUntil: unsetEpoch(w.Status.IgnoredUntil),
		}
	case w.Comment != nil:
		e.Kind = TimelineComment
		c := w.Comment.Comment.comment()
		e.Comment = &c
	case w.Assignment != nil:
		e.Kind = TimelineAssignment
		e.Assignment = &AssignmentChange{From: w.Assignment.From.user(), To: w.Assignment.To.user()}
	case w.Chat != nil:
		l := w.Chat.Link
		e.Kind = TimelineChat
		e.Chat = &ChatLink{ChatID: l.ChatID, ChatTitle: l.ChatTitle, LinkedBy: l.LinkedBy.user(), LinkedAt: unsetEpoch(l.LinkedAt)}
	default:
		return e, false
	}
	return e, true
}

// ListIssueTimeline implements IssueReader over IssuesService.ListIssueTimeline.
// The control plane lists oldest first and exposes no way to start from the
// end, so the newest req.Limit entries are the tail of a walk over every page.
func (c Cloud) ListIssueTimeline(ctx context.Context, req ListIssueTimelineRequest) (ListIssueTimelineResult, error) {
	if req.Limit < 1 {
		return ListIssueTimelineResult{}, Wrong("the timeline limit must be at least 1")
	}
	res := ListIssueTimelineResult{IssueID: req.ID}
	token := ""
	for page := 0; ; page++ {
		if page == maxTimelinePages {
			return ListIssueTimelineResult{}, Undetermined("the timeline of %s is longer than %d entries; read it in the console", req.ID, maxTimelinePages*timelinePageSize)
		}
		in := struct {
			IssueID   string `json:"issueId"`
			PageSize  int    `json:"pageSize"`
			PageToken string `json:"pageToken,omitempty"`
		}{req.ID, timelinePageSize, token}
		var out struct {
			Entries       []wireIssueTimelineEntry `json:"entries"`
			NextPageToken string                   `json:"nextPageToken"`
			IssueID       string                   `json:"issueId"`
		}
		if err := c.callIssues(ctx, procListIssueTimeline, in, &out); err != nil {
			return ListIssueTimelineResult{}, err
		}
		if out.IssueID != "" {
			res.IssueID = out.IssueID
		}
		for _, w := range out.Entries {
			e, ok := w.entry()
			if !ok {
				continue
			}
			res.Entries = append(res.Entries, e)
			if len(res.Entries) > req.Limit {
				res.Entries = res.Entries[1:]
				res.Omitted++
			}
		}
		if out.NextPageToken == "" {
			return res, nil
		}
		token = out.NextPageToken
	}
}

// AddIssueComment implements IssueReader over IssuesService.AddIssueComment.
func (c Cloud) AddIssueComment(ctx context.Context, req AddIssueCommentRequest) (AddIssueCommentResult, error) {
	if strings.TrimSpace(req.Body) == "" {
		return AddIssueCommentResult{}, Wrong("a comment needs text")
	}
	in := struct {
		IssueID string `json:"issueId"`
		Body    string `json:"body"`
	}{req.ID, req.Body}
	var out struct {
		Comment wireComment `json:"comment"`
	}
	if err := c.callIssues(ctx, procAddIssueComment, in, &out); err != nil {
		return AddIssueCommentResult{}, err
	}
	return AddIssueCommentResult{Comment: out.Comment.comment()}, nil
}

// AssignIssue implements IssueReader over IssuesService.AssignIssue. Like
// UpdateIssueStatus it reads the issue first and sends the row version it read
// as expected_version: a write over a state the caller never saw is Aborted,
// reported as "changed since you read it", and never retried.
func (c Cloud) AssignIssue(ctx context.Context, req AssignIssueRequest) (AssignIssueResult, error) {
	now := time.Now().UTC()
	cur, err := c.getIssue(ctx, req.ID, Narrow{Range: Range{From: now.Add(-triageWindow), To: now}})
	if err != nil {
		return AssignIssueResult{}, err
	}
	if err := c.checkEnv(cur.Issue); err != nil {
		return AssignIssueResult{}, err
	}
	in := struct {
		IssueID         string `json:"issueId"`
		AssigneeUserID  string `json:"assigneeUserId,omitempty"`
		ExpectedVersion int32  `json:"expectedVersion,omitempty"`
	}{req.ID, req.Assignee, cur.Issue.Version}
	var out struct {
		Issue wireIssue `json:"issue"`
	}
	if err := c.callIssues(ctx, procAssignIssue, in, &out); err != nil {
		var ce *cloud.Error
		if errors.As(err, &ce) && ce.HasCode("aborted") {
			return AssignIssueResult{}, Wrong("issue %s changed since you read it (version %d); read it again before assigning", req.ID, cur.Issue.Version).Cause(err)
		}
		return AssignIssueResult{}, err
	}
	i := out.Issue.issue()
	i.EnvID = out.Issue.EnvID
	return AssignIssueResult{Issue: i}, nil
}
