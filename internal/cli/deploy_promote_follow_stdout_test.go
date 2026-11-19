package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// captureStdout is dev_status_test.go's helper, reused deliberately: one
// redirect helper per package, not one per test file.

// TestFollowNoticeKeepsStdoutDecodableUnderJSON pins `--json`'s contract: the
// whole promise of the flag is that stdout carries EXACTLY ONE JSON document
// (see the --json help text on env deploy). A human progress line printed
// beside it does not degrade the output, it destroys it — json.Unmarshal fails
// on the first byte of prose and the consumer gets nothing, including the
// fields it needed most.
//
// Found live: `forge env deploy <env> <version> --no-wait --json` against a
// hosted env printed
//
//	Recorded. <env> is converged by its control plane; gate on it with: …
//
// and then a correct document, so a caller reading stdout as JSON failed with
// `invalid character 'R' looking for beginning of value` — naming the R of
// "Recorded" rather than anything about the deploy.
//
// MUTATION VERIFIED RED: point notice() at os.Stdout unconditionally and the
// json subtest fails to decode, exactly as the live run did.
func TestFollowNoticeKeepsStdoutDecodableUnderJSON(t *testing.T) {
	for _, tc := range []struct {
		name          string
		jsonOut       bool
		wantOnStdout  bool
		stdoutDecodes bool
	}{
		// Under --json the notice must NOT be on stdout, and what IS on
		// stdout must still decode as the document.
		{"json: notice to stderr", true, false, true},
		// Without --json there is no document to protect, and the human
		// must still see the line.
		{"text: notice to stdout", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout := captureStdout(t, func() {
				o := promoteFollowOptions{jsonOut: tc.jsonOut}
				o.notice("\nRecorded. %s is converged by its control plane\n", "staging")
				if tc.jsonOut {
					// What the real command writes to stdout under --json.
					enc := json.NewEncoder(os.Stdout)
					_ = enc.Encode(map[string]any{"ok": true, "applied": true})
				}
			})

			onStdout := strings.Contains(stdout, "Recorded.")
			if onStdout != tc.wantOnStdout {
				t.Errorf("notice on stdout = %v, want %v (stdout: %q)", onStdout, tc.wantOnStdout, stdout)
			}
			if !tc.stdoutDecodes {
				return
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &doc); err != nil {
				t.Fatalf("stdout is not one decodable JSON document: %v\nstdout: %q", err, stdout)
			}
			if doc["applied"] != true {
				t.Errorf("decoded document = %v, want the deploy's own fields", doc)
			}
		})
	}
}
