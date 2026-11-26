package cli

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// `forge env status --claimable --json` is a success: ok true, exit_code 0.
func TestClaimableStatusJSONReportsOKOnSuccess(t *testing.T) {
	testkit.Slow(t, "renders KCL and runs the whole deploy")
	fake, _ := claimableFixture(t)
	if out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if fake.starts != 1 {
		t.Fatalf("starts = %d", fake.starts)
	}
	out, err := runForge(t, "env", "status", "hosted", "--claimable", "--json")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	doc := decodeClaimDoc(t, out)
	if doc["ok"] != true || doc["exit_code"] != float64(0) {
		t.Errorf("status --json = ok:%v exit_code:%v, want ok:true exit_code:0", doc["ok"], doc["exit_code"])
	}
	if _, present := doc["error"]; present {
		t.Errorf("a successful status carries an error: %v", doc["error"])
	}
}
