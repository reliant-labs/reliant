package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/reliant-labs/forge/internal/exitcode"
)

// The ONE --json envelope every hosted deploy verb carries.
//
// `deploy --json` established the pattern and the reason for it: ok and
// exit_code are not a second opinion about what happened, they are computed
// from the SAME error the text path returns. Two independent switches
// someone has to keep in agreement is exactly the bug this avoids — a
// pipeline that reads ok:true from a document whose process exited 1 has no
// way to discover which half is lying.
//
// So a verb does not assemble these three fields itself. It embeds
// jsonEnvelope in its own document and calls stamp once, with the error it
// is about to return.

// jsonEnvelope is the common head of every hosted --json document. Embed it;
// the verb's own fields follow.
type jsonEnvelope struct {
	// OK is true exactly when the process will exit 0.
	OK bool `json:"ok"`
	// ExitCode is exactly the code the process will exit with — one of the
	// exit* constants, resolved the same way main() resolves it.
	ExitCode int `json:"exit_code"`
	// Error is the failure message when OK is false, and absent when the
	// verb succeeded. Omitted rather than empty so a consumer can test for
	// the key.
	Error string `json:"error,omitempty"`
	// The error's structure — code, reason, scopes (json_error_facts.go).
	// Absent on success and when the error carries none.
	jsonErrorFacts
}

// stamp fills the envelope from the error the verb is returning. err == nil
// is success; anything else carries its message and its resolved code.
//
// Takes the error rather than a code so the two cannot diverge: there is no
// way to call this and report a code the error does not produce.
func (e *jsonEnvelope) stamp(err error) {
	e.OK = err == nil
	e.ExitCode = exitCodeForError(err)
	if err != nil {
		e.Error = err.Error()
	} else {
		e.Error = ""
	}
	e.jsonErrorFacts = errorFactsOf(err)
}

// exitCodeForError is the process status an error produces — the SAME
// resolution main() performs, so a reported code is the one the shell sees
// rather than a guess that happens to agree most of the time.
//
// An error carrying no explicit code is exitWrong, not exitUndetermined: an
// unclassified failure is a failure we DID observe. Defaulting the other way
// would let an ordinary bug read as "could not look", which is the one
// answer CI treats as retryable.
func exitCodeForError(err error) int { return exitcode.Of(err) }

// emitJSONDocument writes one document to stdout, indented, per the house
// convention every other --json command follows.
//
// STDOUT CARRIES EXACTLY ONE DOCUMENT. That is why human progress goes to
// progressWriter (stderr) in JSON mode: a verb that printed a status line to
// stdout would produce a stream no `jq` invocation can read, and the failure
// looks like malformed JSON rather than like misrouted logging.
func emitJSONDocument(doc any) error {
	return writeJSONDocument(os.Stdout, doc)
}

// writeJSONDocument is emitJSONDocument to a caller-chosen writer, for a verb
// that renders to cmd.OutOrStdout() so a test can read what it printed.
func writeJSONDocument(w io.Writer, doc any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("write json document: %w", err)
	}
	return nil
}

// progressWriter is where a verb's human progress goes: stdout normally,
// stderr in JSON mode. One call at the top of a verb, and every subsequent
// print is routed correctly without an `if jsonMode` at each site.
func progressWriter(jsonMode bool) io.Writer {
	if jsonMode {
		return os.Stderr
	}
	return os.Stdout
}
