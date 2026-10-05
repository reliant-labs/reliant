// Copyright (c) 2025 Reliant Labs
package github

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// The recorded payload is a TRIMMED copy of the webhook body. GitHub's are
// large (a push carries every commit's file lists; every object carries a
// dozen API URLs) and mostly irrelevant to a run, and the event row, the seed
// message and a CEL filter all read it. What is kept is what an agent acts
// on and a filter tests: numbers, titles, bodies, states, refs, logins and
// the html_url a human opens. Emails and API-only URLs are dropped, long text
// is capped, and the whole payload has a hard size bound.

const (
	// maxTextBytes caps one text field (an issue body, a comment).
	maxTextBytes = 16 << 10
	// maxDataBytes bounds the encoded payload; past it, list fields are cut
	// down until it fits.
	maxDataBytes = 64 << 10
	// maxCommits bounds a push's commit list (GitHub sends at most 20).
	maxCommits = 20
	// maxListItems bounds labels, assignees and the like.
	maxListItems = 50

	truncatedSuffix = "… [truncated]"
)

// mentionPattern finds an @reliant mention. A GitHub login is
// [A-Za-z0-9-], so the mention ends at the first character that cannot
// continue one: `@reliant-labs` (the org) and `@reliantbot` are other
// accounts, and `me@reliant.dev` is an email, not a mention.
var mentionPattern = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_@/.-])@reliant([^A-Za-z0-9-]|$)`)

// --- attributes -----------------------------------------------------------

func labelAttrs(raw map[string]any, attrs map[string]string) {
	attrs["label"] = text(dig(raw, "label", "name"))
}

func assigneeAttrs(raw map[string]any, attrs map[string]string) {
	attrs["assignee"] = text(dig(raw, "assignee", "login"))
}

func commentAttrs(raw map[string]any, attrs map[string]string) {
	attrs["mentions_reliant"] = strconv.FormatBool(mentionPattern.MatchString(text(dig(raw, "comment", "body"))))
	_, isPR := dig(raw, "issue", "pull_request").(map[string]any)
	attrs["is_pull_request"] = strconv.FormatBool(isPR)
}

func prAttrs(raw map[string]any, attrs map[string]string) {
	attrs["branch"] = text(dig(raw, "pull_request", "head", "ref"))
	attrs["base_branch"] = text(dig(raw, "pull_request", "base", "ref"))
}

func prClosedAttrs(raw map[string]any, attrs map[string]string) {
	prAttrs(raw, attrs)
	merged, _ := dig(raw, "pull_request", "merged").(bool)
	attrs["merged"] = strconv.FormatBool(merged)
}

func reviewRequestedAttrs(raw map[string]any, attrs map[string]string) {
	prAttrs(raw, attrs)
	// A request goes to a user or to a team; the attribute names whichever.
	reviewer := text(dig(raw, "requested_reviewer", "login"))
	if reviewer == "" {
		reviewer = text(dig(raw, "requested_team", "slug"))
	}
	attrs["requested_reviewer"] = reviewer
}

func reviewAttrs(raw map[string]any, attrs map[string]string) {
	prAttrs(raw, attrs)
	attrs["state"] = strings.ToLower(text(dig(raw, "review", "state")))
}

// pushAttrs splits the ref: a branch push sets `branch`, a tag push sets
// `tag`, never both — so a trigger matching branch: main can never fire on
// a tag that happens to be named main.
func pushAttrs(raw map[string]any, attrs map[string]string) {
	ref := text(raw["ref"])
	attrs["ref"] = ref
	switch {
	case strings.HasPrefix(ref, "refs/heads/"):
		attrs["branch"] = strings.TrimPrefix(ref, "refs/heads/")
	case strings.HasPrefix(ref, "refs/tags/"):
		attrs["tag"] = strings.TrimPrefix(ref, "refs/tags/")
	}
}

func workflowRunAttrs(raw map[string]any, attrs map[string]string) {
	attrs["conclusion"] = text(dig(raw, "workflow_run", "conclusion"))
	attrs["branch"] = text(dig(raw, "workflow_run", "head_branch"))
	attrs["workflow"] = text(dig(raw, "workflow_run", "name"))
}

// --- data -----------------------------------------------------------------

func issueData(raw map[string]any) map[string]any {
	return map[string]any{"issue": issueObject(raw["issue"])}
}

func issueEditedData(raw map[string]any) map[string]any {
	out := issueData(raw)
	if changes, ok := raw["changes"].(map[string]any); ok {
		kept := map[string]any{}
		for _, field := range []string{"title", "body"} {
			if from, ok := str(dig(changes, field, "from")); ok {
				kept[field] = map[string]any{"from": capText(from)}
			}
		}
		out["changes"] = kept
	}
	return out
}

func labelData(raw map[string]any) map[string]any {
	out := issueData(raw)
	out["label"] = text(dig(raw, "label", "name"))
	return out
}

func assigneeData(raw map[string]any) map[string]any {
	out := issueData(raw)
	out["assignee"] = userData(raw["assignee"])
	return out
}

func commentData(raw map[string]any) map[string]any {
	c, _ := raw["comment"].(map[string]any)
	return map[string]any{
		"issue": issueObject(raw["issue"]),
		"comment": map[string]any{
			"id":                 num(c["id"]),
			"body":               capText(text(c["body"])),
			"html_url":           text(c["html_url"]),
			"user":               userData(c["user"]),
			"author_association": text(c["author_association"]),
			"created_at":         text(c["created_at"]),
		},
	}
}

func prData(raw map[string]any) map[string]any {
	return map[string]any{"number": num(raw["number"]), "pull_request": prObject(raw["pull_request"])}
}

func prSyncData(raw map[string]any) map[string]any {
	out := prData(raw)
	out["before"] = text(raw["before"])
	out["after"] = text(raw["after"])
	return out
}

func reviewRequestedData(raw map[string]any) map[string]any {
	out := prData(raw)
	if r, ok := raw["requested_reviewer"].(map[string]any); ok {
		out["requested_reviewer"] = userData(r)
	}
	if team, ok := raw["requested_team"].(map[string]any); ok {
		out["requested_team"] = map[string]any{"name": text(team["name"]), "slug": text(team["slug"])}
	}
	return out
}

func reviewData(raw map[string]any) map[string]any {
	r, _ := raw["review"].(map[string]any)
	return map[string]any{
		"pull_request": prObject(raw["pull_request"]),
		"review": map[string]any{
			"id":                 num(r["id"]),
			"state":              strings.ToLower(text(r["state"])),
			"body":               capText(text(r["body"])),
			"html_url":           text(r["html_url"]),
			"user":               userData(r["user"]),
			"commit_id":          text(r["commit_id"]),
			"submitted_at":       text(r["submitted_at"]),
			"author_association": text(r["author_association"]),
		},
	}
}

func pushData(raw map[string]any) map[string]any {
	commits := list(raw["commits"])
	if len(commits) > maxCommits {
		commits = commits[:maxCommits]
	}
	kept := make([]any, 0, len(commits))
	for _, c := range commits {
		kept = append(kept, commitObject(c))
	}
	out := map[string]any{
		"ref":     text(raw["ref"]),
		"before":  text(raw["before"]),
		"after":   text(raw["after"]),
		"created": raw["created"] == true,
		"deleted": raw["deleted"] == true,
		"forced":  raw["forced"] == true,
		"compare": text(raw["compare"]),
		"commits": kept,
		"pusher":  map[string]any{"name": text(dig(raw, "pusher", "name"))},
	}
	if hc, ok := raw["head_commit"].(map[string]any); ok {
		out["head_commit"] = commitObject(hc)
	}
	return out
}

func workflowRunData(raw map[string]any) map[string]any {
	r, _ := raw["workflow_run"].(map[string]any)
	return map[string]any{
		"workflow": map[string]any{"id": num(dig(raw, "workflow", "id")), "name": text(dig(raw, "workflow", "name")), "path": text(dig(raw, "workflow", "path"))},
		"workflow_run": map[string]any{
			"id":             num(r["id"]),
			"name":           text(r["name"]),
			"run_number":     num(r["run_number"]),
			"run_attempt":    num(r["run_attempt"]),
			"event":          text(r["event"]),
			"status":         text(r["status"]),
			"conclusion":     text(r["conclusion"]),
			"head_branch":    text(r["head_branch"]),
			"head_sha":       text(r["head_sha"]),
			"html_url":       text(r["html_url"]),
			"actor":          userData(r["actor"]),
			"run_started_at": text(r["run_started_at"]),
			"updated_at":     text(r["updated_at"]),
			"head_commit":    map[string]any{"id": text(dig(r, "head_commit", "id")), "message": capText(text(dig(r, "head_commit", "message")))},
			"pull_requests":  prNumbers(r["pull_requests"]),
		},
	}
}

// --- objects --------------------------------------------------------------

func issueObject(v any) map[string]any {
	i, _ := v.(map[string]any)
	_, isPR := i["pull_request"].(map[string]any)
	out := map[string]any{
		"number":             num(i["number"]),
		"title":              text(i["title"]),
		"body":               capText(text(i["body"])),
		"state":              text(i["state"]),
		"html_url":           text(i["html_url"]),
		"user":               userData(i["user"]),
		"labels":             names(i["labels"], "name"),
		"assignees":          names(i["assignees"], "login"),
		"comments":           num(i["comments"]),
		"author_association": text(i["author_association"]),
		"created_at":         text(i["created_at"]),
		"updated_at":         text(i["updated_at"]),
		"is_pull_request":    isPR,
	}
	if s := text(i["state_reason"]); s != "" {
		out["state_reason"] = s
	}
	if s := text(i["closed_at"]); s != "" {
		out["closed_at"] = s
	}
	return out
}

func prObject(v any) map[string]any {
	p, _ := v.(map[string]any)
	ref := func(side string) map[string]any {
		return map[string]any{"ref": text(dig(p, side, "ref")), "sha": text(dig(p, side, "sha")), "label": text(dig(p, side, "label"))}
	}
	out := map[string]any{
		"number":              num(p["number"]),
		"title":               text(p["title"]),
		"body":                capText(text(p["body"])),
		"state":               text(p["state"]),
		"draft":               p["draft"] == true,
		"merged":              p["merged"] == true,
		"html_url":            text(p["html_url"]),
		"user":                userData(p["user"]),
		"head":                ref("head"),
		"base":                ref("base"),
		"labels":              names(p["labels"], "name"),
		"requested_reviewers": names(p["requested_reviewers"], "login"),
		"created_at":          text(p["created_at"]),
		"updated_at":          text(p["updated_at"]),
	}
	for _, k := range []string{"additions", "deletions", "changed_files", "commits"} {
		if n := num(p[k]); n != nil {
			out[k] = n
		}
	}
	if s := text(p["merged_at"]); s != "" {
		out["merged_at"] = s
	}
	if s := text(p["merge_commit_sha"]); s != "" {
		out["merge_commit_sha"] = s
	}
	return out
}

func commitObject(v any) map[string]any {
	c, _ := v.(map[string]any)
	return map[string]any{
		"id":        text(c["id"]),
		"message":   capText(text(c["message"])),
		"timestamp": text(c["timestamp"]),
		"url":       text(c["url"]),
		// Name and username only: the author email is personal data the run
		// has no use for.
		"author":   map[string]any{"name": text(dig(c, "author", "name")), "username": text(dig(c, "author", "username"))},
		"added":    capList(c["added"]),
		"removed":  capList(c["removed"]),
		"modified": capList(c["modified"]),
	}
}

func repositoryData(raw map[string]any) map[string]any {
	r, _ := raw["repository"].(map[string]any)
	return map[string]any{
		"id":             num(r["id"]),
		"full_name":      text(r["full_name"]),
		"name":           text(r["name"]),
		"owner":          text(dig(r, "owner", "login")),
		"private":        r["private"] == true,
		"html_url":       text(r["html_url"]),
		"default_branch": text(r["default_branch"]),
	}
}

func userData(v any) map[string]any {
	u, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return map[string]any{"login": text(u["login"]), "id": num(u["id"]), "type": text(u["type"])}
}

func prNumbers(v any) []any {
	out := []any{}
	for _, p := range list(v) {
		if n := num(dig(p, "number")); n != nil {
			out = append(out, n)
		}
	}
	return out
}

// --- caps -----------------------------------------------------------------

func capText(s string) string {
	if len(s) <= maxTextBytes {
		return s
	}
	cut := maxTextBytes
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + truncatedSuffix
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func capList(v any) []any {
	items := list(v)
	if len(items) > maxListItems {
		items = items[:maxListItems]
	}
	out := make([]any, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func names(v any, key string) []any {
	out := []any{}
	for _, it := range list(v) {
		if s := text(dig(it, key)); s != "" {
			out = append(out, s)
		}
		if len(out) == maxListItems {
			break
		}
	}
	return out
}

// capData bounds the encoded payload. Text is already capped; what can still
// grow is a push's commits with their file lists, so those are cut, then the
// commits themselves, until it fits.
func capData(data map[string]any) map[string]any {
	if size(data) <= maxDataBytes {
		return data
	}
	if commits, ok := data["commits"].([]any); ok {
		for _, c := range commits {
			if m, ok := c.(map[string]any); ok {
				delete(m, "added")
				delete(m, "removed")
				delete(m, "modified")
			}
		}
		for len(commits) > 0 && size(data) > maxDataBytes {
			commits = commits[:len(commits)-1]
			data["commits"] = commits
		}
		data["truncated"] = true
	}
	return data
}

func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// --- accessors over decoded JSON ------------------------------------------

func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func str(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && s != ""
}

func text(v any) string {
	s, _ := v.(string)
	return s
}

// num keeps a JSON number as an int64 when it is one (ids, counts), so the
// payload carries 42 rather than 4.2e1 and a filter compares it exactly.
func num(v any) any {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
	case float64:
		return n
	}
	return nil
}

// intString renders a JSON integer id as a string key.
func intString(v any) (string, bool) {
	switch n := v.(type) {
	case json.Number:
		if i, err := n.Int64(); err == nil && i != 0 {
			return strconv.FormatInt(i, 10), true
		}
	case float64:
		if n != 0 {
			return strconv.FormatInt(int64(n), 10), true
		}
	}
	return "", false
}
