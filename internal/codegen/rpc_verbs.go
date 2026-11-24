// File: internal/codegen/rpc_verbs.go
//
// What an rpc's NAME says about whether it reads or writes, for the passes
// that ask the proto to state it: `forge lint --idempotency` (does the
// idempotency_level agree with the name?) and `forge scaffold rpc` (which
// prints the declaration in the snippet it hands the author).
//
// The first word of the name decides, matched as a whole word (CheckIn
// starts with "Check"; Checkout does not). No Batch or Bulk prefix is
// stripped: the only batch form forge reads is the activity interceptor's
// BatchGet, so BatchSearch, BatchUpdate and BulkGet are judged on their own
// first word, exactly as the hook generator judges them.

package codegen

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/reliant-labs/forge/pkg/activity"
)

// rpcWriteVerbs are the rpc-name verbs whose contract ALWAYS changes state:
// the CRUD writes and the verbs that name a definite change to a record or
// a grant. Sorted.
//
// The list is deliberately short. A verb that a read-only rpc can honestly
// open with — Run (RunSavedQuery), Execute, Ensure, Start (StartPreview),
// Check, Sync, Refresh, Export, Validate, Preview, Resolve, Generate — is
// not judged, because rule 2 of the lint then calls a correct
// NO_SIDE_EFFECTS wrong. A write verb missing here only leaves that rpc to
// review; a read-capable verb here makes a correct declaration a finding.
var rpcWriteVerbs = []string{
	"Add", "Archive", "Cancel", "Create", "Delete", "Grant", "Insert", "Move",
	"Patch", "Purge", "Remove", "Rename", "Replace", "Restore", "Revoke", "Send",
	"Set", "Update", "Upsert",
}

// WriteVerbs returns the write verbs WriteVerb recognizes, sorted.
func WriteVerbs() []string { return append([]string(nil), rpcWriteVerbs...) }

// ReadVerb is the read verb an rpc name opens with, or "" when it opens with
// none forge treats as a read.
//
// It is exactly the union of the two classifiers forge already runs on rpc
// names, with their own rules and nothing added: the hook generator's
// (IsQueryMethodName: Get, List, Search, Find, Check, Has, Is, Count,
// Exists, whole word) and the activity interceptor's
// (activity.IsMutatingName: BatchGet, Get, List, Watch, Search, Query,
// Count, Describe). So asking every rpc ReadVerb names to declare
// NO_SIDE_EFFECTS never asks it of a name forge itself treats as a write.
func ReadVerb(name string) string {
	switch {
	case IsQueryMethodName(name):
		return leadingWord(name)
	case activity.IsMutatingName(name):
		return ""
	case strings.HasPrefix(name, "BatchGet"):
		return "BatchGet"
	default:
		return leadingWord(name)
	}
}

// WriteVerb is the write verb (rpcWriteVerbs) an rpc name opens with, or "".
func WriteVerb(name string) string {
	for _, v := range rpcWriteVerbs {
		if rest, ok := strings.CutPrefix(name, v); ok && (rest == "" || startsWord(rest)) {
			return v
		}
	}
	return ""
}

// startsWord reports whether s begins a new PascalCase word.
func startsWord(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return s != "" && (unicode.IsUpper(r) || unicode.IsDigit(r))
}

// leadingWord is the first PascalCase word of name.
func leadingWord(name string) string {
	for i, r := range name {
		if i > 0 && (unicode.IsUpper(r) || unicode.IsDigit(r)) {
			return name[:i]
		}
	}
	return name
}
