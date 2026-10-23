package obsout

import (
	"time"

	"github.com/reliant-labs/forge/internal/exitcode"
)

// Head is the common head of every agent document. Embed it first in a verb's
// document so the schema name and the verdict lead the output.
type Head struct {
	// Schema names the document and its version, e.g. "forge.obs.logs/v1".
	// Field names under a schema are stable; a breaking change is a new
	// version.
	Schema string `json:"schema"`
	// OK is true exactly when the process will exit 0.
	OK bool `json:"ok"`
	// ExitCode is exactly the status the process will exit with.
	ExitCode int `json:"exit_code"`
	// Error is the failure message; absent on success.
	Error string `json:"error,omitempty"`
}

// Stamp fills the head from the error the verb is about to return. It takes the
// error rather than a code so the two cannot diverge: an error that carries an
// ExitCode() int (forge's exitCodeError) reports that code, any other error is
// 1 ("we looked, and it is wrong"); internal/exitcode is the one resolution.
func (h *Head) Stamp(schema string, err error) {
	h.Schema = schema
	h.OK = err == nil
	h.ExitCode = exitcode.Of(err)
	h.Error = ""
	if err != nil {
		h.Error = err.Error()
	}
}

// Scope echoes what a query was resolved against, so a document is
// self-describing: the same verb run against another env or backend is
// visibly a different question.
type Scope struct {
	Env     string `json:"env"`
	Backend string `json:"backend"`
	// EnvID is the control plane's id for the env, on the cloud backend.
	EnvID string `json:"env_id,omitempty"`
	// From and To are the half-open range queried, in UTC.
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Services []string  `json:"services,omitempty"`
}

// Failure is the document of a verb that failed before it had a result: the
// head and the commands that fix it. It is what --json prints on a refusal.
type Failure struct {
	Head
	Suggested []string `json:"suggested,omitempty"`
}

// Failed builds the failure document for err.
func Failed(schema string, err error, suggested ...string) Failure {
	f := Failure{Suggested: Suggest(suggested...)}
	f.Stamp(schema, err)
	return f
}
