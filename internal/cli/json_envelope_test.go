package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
)

// TestExitCodes_AreTheSharedTable pins §3.A's table by value, not by
// construction. CI pipelines branch on these numbers literally, so a
// renumbering is a breaking change to every pipeline that reads them — and
// the only thing that can catch it is a test that states the numbers.
func TestExitCodes_AreTheSharedTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{"ok", exitOK, 0},
		{"looked and it is wrong", exitWrong, 1},
		{"could not determine", exitUndetermined, 2},
		{"conflict", exitConflict, 3},
		{"refused", exitRefused, 4},
		// 5 is the O-13 confirmation gate: the plan was printed and nobody
		// approved it, so NOTHING was promoted. It took 5 from the timeout
		// because it is the outcome that must be impossible to misread as
		// success — see the header in exitcodes.go.
		{"plan unconfirmed", exitPlanUnconfirmed, 5},
		{"superseded", exitSuperseded, 6},
		{"timed out", exitTimedOut, 8},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestExitCodeForError_MatchesMainsResolution is the parity that makes the
// envelope safe in a pipeline: the code reported in JSON is resolved exactly
// as cmd/forge/main.go resolves the process status.
func TestExitCodeForError_MatchesMainsResolution(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"nil is success", nil, exitOK},
		// An unclassified failure is one we OBSERVED, so it is 1. Reading
		// as 2 would make an ordinary bug look retryable to CI.
		{"a plain error is wrong, not undetermined", errors.New("boom"), exitWrong},
		{"a coded error keeps its code", exitCodeError{code: exitConflict, msg: "prod moved"}, exitConflict},
		{"refused", exitCodeError{code: exitRefused, msg: "rollout in flight"}, exitRefused},
		{"timed out", exitCodeError{code: exitTimedOut, msg: "still progressing"}, exitTimedOut},
		{"superseded", exitCodeError{code: exitSuperseded, msg: "overtaken"}, exitSuperseded},
		{"undetermined", exitCodeError{code: exitUndetermined, msg: "unreachable"}, exitUndetermined},
		// The code must survive wrapping: a verb that adds context with
		// %w must not lose the classification it chose.
		{"a wrapped coded error keeps its code", fmt.Errorf("wait on prod: %w",
			exitCodeError{code: exitSuperseded, msg: "overtaken"}), exitSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeForError(tc.err); got != tc.want {
				t.Errorf("exitCodeForError(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestJSONEnvelope_StampDerivesEveryFieldFromTheError: ok, exit_code and
// error all come from one error, so a document cannot claim success while
// the process fails.
func TestJSONEnvelope_StampDerivesEveryFieldFromTheError(t *testing.T) {
	var ok jsonEnvelope
	ok.stamp(nil)
	if !ok.OK || ok.ExitCode != exitOK || ok.Error != "" {
		t.Errorf("success envelope = %+v", ok)
	}

	refusal := exitCodeError{code: exitRefused, msg: "a rollout is in flight"}
	var bad jsonEnvelope
	bad.stamp(refusal)
	if bad.OK {
		t.Error("a failure must not report ok")
	}
	if bad.ExitCode != exitRefused {
		t.Errorf("exit_code = %d, want %d", bad.ExitCode, exitRefused)
	}
	if bad.Error != refusal.msg {
		t.Errorf("error = %q, want %q", bad.Error, refusal.msg)
	}
	// Stamping again with nil must CLEAR the message, not leave a stale
	// failure on a document that now reports success.
	bad.stamp(nil)
	if !bad.OK || bad.Error != "" {
		t.Errorf("re-stamped envelope kept stale failure state: %+v", bad)
	}
}

// TestJSONEnvelope_EmbedsIntoAVerbsDocument: the three fields are present at
// the TOP level of a verb's own document, not nested under a key. §3.A's
// shape is `{ "ok": …, "exit_code": …, "error": …, … }`.
func TestJSONEnvelope_EmbedsIntoAVerbsDocument(t *testing.T) {
	type waitDoc struct {
		jsonEnvelope
		Env   string `json:"env"`
		Phase string `json:"phase"`
	}
	doc := waitDoc{Env: "prod", Phase: "degraded"}
	doc.stamp(exitCodeError{code: exitWrong, msg: "api: CrashLoopBackOff"})

	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ok", "exit_code", "error", "env", "phase"} {
		if _, present := got[key]; !present {
			t.Errorf("document is missing top-level %q: %s", key, raw)
		}
	}
	if got["ok"] != false {
		t.Errorf("ok = %v", got["ok"])
	}
	if got["exit_code"] != float64(exitWrong) {
		t.Errorf("exit_code = %v", got["exit_code"])
	}

	// A SUCCESSFUL document omits error entirely, so a consumer can test
	// for the key rather than for an empty string.
	doc.stamp(nil)
	raw, _ = json.Marshal(doc)
	var okDoc map[string]any
	_ = json.Unmarshal(raw, &okDoc)
	if _, present := okDoc["error"]; present {
		t.Errorf("a successful document must omit error: %s", raw)
	}
}

// TestProgressWriter_JSONModeKeepsStdoutForTheDocument: human progress goes
// to stderr in JSON mode, so stdout is exactly one document. A status line
// on stdout produces a stream no consumer can parse, and the failure looks
// like malformed JSON rather than like misrouted logging.
func TestProgressWriter_JSONModeKeepsStdoutForTheDocument(t *testing.T) {
	if got := progressWriter(true); got != os.Stderr {
		t.Errorf("json mode progress must go to stderr, got %v", got)
	}
	if got := progressWriter(false); got != os.Stdout {
		t.Errorf("text mode progress must go to stdout, got %v", got)
	}
}

// TestEmitJSONDocument_IsOneIndentedDocument pins the house convention: one
// document, indented two spaces, newline-terminated.
func TestEmitJSONDocument_IsOneIndentedDocument(t *testing.T) {
	// emitJSONDocument writes to os.Stdout by design (it is the verb's
	// output), so capture it through a pipe.
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	type doc struct {
		jsonEnvelope
		Env string `json:"env"`
	}
	d := doc{Env: "prod"}
	d.stamp(nil)
	emitErr := emitJSONDocument(d)
	_ = w.Close()
	os.Stdout = original
	if emitErr != nil {
		t.Fatalf("emitJSONDocument: %v", emitErr)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("\n  \"ok\": true")) {
		t.Errorf("document should be indented two spaces; got:\n%s", out)
	}
	// Exactly one document: decoding once must consume the whole stream.
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.More() {
		t.Errorf("stdout carried more than one document:\n%s", out)
	}
}
