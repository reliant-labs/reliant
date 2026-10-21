// Copyright (c) 2025 Reliant Labs
package github

import "regexp"

// The hooks projection specs name (`hook:` on an attribute): the logic a
// path-and-kind projection cannot say. Each is func(any) bool, given the value
// at the attribute's `from` path.

// mentionPattern finds an @reliant mention. A GitHub login is
// [A-Za-z0-9-], so the mention ends at the first character that cannot
// continue one: `@reliant-labs` (the org) and `@reliantbot` are other
// accounts, and `me@reliant.dev` is an email, not a mention.
var mentionPattern = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_@/.-])@reliant([^A-Za-z0-9-]|$)`)

// mentionsReliant: the text mentions @reliant.
func mentionsReliant(v any) bool { return mentionPattern.MatchString(text(v)) }

// isObject: the value is a JSON object (an issue_comment's `issue.pull_request`
// is present exactly when the comment is on a pull request).
func isObject(v any) bool {
	_, ok := v.(map[string]any)
	return ok
}

// isSet: the value is a non-zero JSON integer id (a comment's in_reply_to_id).
func isSet(v any) bool {
	_, ok := intString(v)
	return ok
}
