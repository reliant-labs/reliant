// Copyright (c) 2025 Reliant Labs
package github

import (
	"strings"

	"github.com/reliant-labs/reliant/internal/integrations/provenance"
)

// echoOf is what a delivery says about itself, for attributing it to a write
// Reliant made (internal/triggers' origin enricher). It is computed from the RAW
// body: the recorded payload truncates long text, and a digest of the
// truncation would never match the digest of what was written.
//
// Everything in it is data the sender controls, under a signature the provider
// verified. It is evidence for attribution and never authority for anything:
// the enricher matches it against the ledger, and the one field a stranger can
// write (RunTrailer) may only ever suppress.
func echoOf(typ string, raw map[string]any, body *payload) *provenance.Echo {
	e := &provenance.Echo{EventType: typ}
	if body.Repository != nil {
		e.Scope = provenance.ScopeOf(body.Repository.FullName)
	}
	if body.Sender != nil {
		e.SenderType = body.Sender.Type
		e.SenderLogin = body.Sender.Login
	}
	number := func(path ...string) string { return provenance.IDString(dig(raw, path...)) }
	target := func(n string) string { return provenance.TargetOf(e.Scope, n) }

	switch typ {
	case "issues.opened", "issues.closed", "issues.edited", "issues.reopened", "issues.assigned":
		e.ObjectKind = provenance.KindIssue
		e.ObjectID = number("issue", "number")
		e.Target = target(e.ObjectID)
		e.BodySHA256 = digestOf(dig(raw, "issue", "body"))
		e.State = text(dig(raw, "issue", "state"))
		e.AppSlug = appSlug(dig(raw, "issue", "performed_via_github_app"))
	case "issues.labeled", "issues.unlabeled":
		e.ObjectKind = provenance.KindIssue
		e.ObjectID = number("issue", "number")
		e.Target = target(e.ObjectID)
		e.Label = text(dig(raw, "label", "name"))
	case "issue_comment.created":
		e.ObjectKind = provenance.KindComment
		e.ObjectID = number("comment", "id")
		e.Target = target(number("issue", "number"))
		e.BodySHA256 = digestOf(dig(raw, "comment", "body"))
		e.AppSlug = appSlug(dig(raw, "comment", "performed_via_github_app"))
	case "pull_request.opened", "pull_request.reopened", "pull_request.closed",
		"pull_request.ready_for_review", "pull_request.review_requested",
		"pull_request.dequeued", "pull_request.auto_merge_disabled":
		e.ObjectKind = provenance.KindPullRequest
		e.ObjectID = number("pull_request", "number")
		e.Target = target(e.ObjectID)
		e.BodySHA256 = digestOf(dig(raw, "pull_request", "body"))
		e.Branch = text(dig(raw, "pull_request", "head", "ref"))
		e.HeadSHA = text(dig(raw, "pull_request", "head", "sha"))
		e.State = text(dig(raw, "pull_request", "state"))
	case "pull_request.labeled", "pull_request.unlabeled":
		e.ObjectKind = provenance.KindPullRequest
		e.ObjectID = number("pull_request", "number")
		e.Target = target(e.ObjectID)
		e.Label = text(dig(raw, "label", "name"))
		e.Branch = text(dig(raw, "pull_request", "head", "ref"))
		e.HeadSHA = text(dig(raw, "pull_request", "head", "sha"))
	case "pull_request.synchronize":
		e.ObjectKind = provenance.KindPullRequest
		e.ObjectID = number("pull_request", "number")
		e.Target = target(e.ObjectID)
		e.Branch = text(dig(raw, "pull_request", "head", "ref"))
		// The head AFTER the push: the commit a Reliant push produced.
		e.HeadSHA = firstNonEmpty(text(raw["after"]), text(dig(raw, "pull_request", "head", "sha")))
	case "pull_request_review.submitted":
		e.ObjectKind = provenance.KindReview
		e.ObjectID = number("review", "id")
		e.Target = target(number("pull_request", "number"))
		e.BodySHA256 = digestOf(dig(raw, "review", "body"))
		e.ReviewState = strings.ToLower(text(dig(raw, "review", "state")))
		e.HeadSHA = text(dig(raw, "review", "commit_id"))
		e.AppSlug = appSlug(dig(raw, "review", "performed_via_github_app"))
	case "pull_request_review_comment.created":
		e.ObjectKind = provenance.KindReviewComment
		e.ObjectID = number("comment", "id")
		e.Target = target(number("pull_request", "number"))
		e.BodySHA256 = digestOf(dig(raw, "comment", "body"))
		e.HeadSHA = text(dig(raw, "comment", "commit_id"))
		e.AppSlug = appSlug(dig(raw, "comment", "performed_via_github_app"))
	case "push":
		e.ObjectKind = provenance.KindBranch
		if ref := text(raw["ref"]); strings.HasPrefix(ref, "refs/heads/") {
			e.Branch = strings.TrimPrefix(ref, "refs/heads/")
			e.Target = provenance.BranchTargetOf(e.Scope, e.Branch)
		}
		e.HeadSHA = text(raw["after"])
		e.RunTrailer = provenance.RunTrailer(text(dig(raw, "head_commit", "message")))
	case "workflow_run.completed":
		e.ObjectKind = provenance.KindBranch
		e.Branch = text(dig(raw, "workflow_run", "head_branch"))
		e.HeadSHA = text(dig(raw, "workflow_run", "head_sha"))
		e.Target = provenance.BranchTargetOf(e.Scope, e.Branch)
	}
	return e
}

// appSlug is performed_via_github_app.slug: the GitHub App an object was
// created through, null when it was made by a person.
func appSlug(v any) string {
	return strings.ToLower(text(dig(v, "slug")))
}

func digestOf(v any) string {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return ""
	}
	return provenance.BodyDigest(s)
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
