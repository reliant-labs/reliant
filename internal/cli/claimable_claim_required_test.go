package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// claimRequiredErr is the platform's claim-required refusal as forge reads it.
func claimRequiredErr(cause string) error {
	h := http.Header{}
	h.Set(cloud.ReasonHeader, cloud.ReasonClaimableContentRefused)
	if cause != "" {
		h.Set(cloud.ClaimRequiredHeader, cause)
	}
	return &cloud.Error{HTTPStatus: http.StatusBadRequest, Code: cloud.CodeFailedPrecondition, Reason: cloud.ReasonClaimableContentRefused,
		Message: "this content can't be published from an unclaimed deploy: claim the deploy with its claim link, then publish again",
		Header:  h}
}

// The refusal the platform makes after the build names the claim link: the
// way forward the old message pointed at but never printed (new.txt §1 #3b).
func TestClaimContentRefusal_PrintsTheClaimLink(t *testing.T) {
	t.Parallel()
	const link = "https://app.reliant.dev/claim#rcl_9f3KQe"
	for _, cause := range []string{cloud.ClaimRequiredContent, "a_newer_cause"} {
		deployErr := claimRequiredErr(cause)
		r := claimContentRefusal("prod", link, deployErr)
		msg := r.Error()
		for _, want := range []string{"Claim:  " + link, "give the claim link", "forge login && ", "Nothing was published"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: refusal lacks %q:\n%s", cause, want, msg)
			}
		}
		if !strings.Contains(msg, "Claim to unlock") {
			t.Errorf("%s: does not say what claiming unlocks:\n%s", cause, msg)
		}
		if r.reason != cloud.ReasonClaimableContentRefused || r.exit != exitRefused {
			t.Errorf("%s: reason=%q exit=%d, want claimable_content_refused and exit 4", cause, r.reason, r.exit)
		}
	}
}

func TestClaimRequiredOf_ReadsTheHeaderNotTheMessage(t *testing.T) {
	t.Parallel()
	if cause, ok := cloud.ClaimRequiredOf(claimRequiredErr(cloud.ClaimRequiredContent)); !ok || cause != cloud.ClaimRequiredContent {
		t.Errorf("ClaimRequiredOf = %q, %v", cause, ok)
	}
	// A control plane that predates the header: the reason alone is a
	// content refusal, not one forge knows claiming lifts.
	if _, ok := cloud.ClaimRequiredOf(claimRequiredErr("")); ok {
		t.Error("a refusal with no claim-required header read as claim-required")
	}
	if _, ok := cloud.ClaimRequiredOf(errors.New("claim the deploy")); ok {
		t.Error("message text read as claim-required")
	}
}

// env status of a deploy refused until claimed gives the claim link out, and
// never says to hold it back (new.txt §1 #3b): claiming is the way forward.
func TestClaimDocument_ClaimRequiredGivesTheLinkOut(t *testing.T) {
	now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	prev := claimableNow
	claimableNow = func() time.Time { return now }
	defer func() { claimableNow = prev }()
	cred := credentials.Credential{Claimable: &credentials.Claimable{
		ClaimURL:  "https://app.reliant.dev/claim#rcl_9f3KQe",
		ExpiresAt: now.Add(71 * time.Hour), PurgeAfter: now.Add(239 * time.Hour),
		LastAttemptAt: now.Add(-time.Minute), LastAttemptError: "RecordBundle failed (HTTP 400 failed_precondition)",
		LastAttemptClaimRequired: cloud.ClaimRequiredContent,
	}}
	doc := claimDocument("prod", cred, now, false)
	if doc.Published || doc.ClaimRequired != cloud.ClaimRequiredContent {
		t.Fatalf("doc = %+v", doc)
	}
	for _, want := range []string{"Nothing is deployed yet", "Claim to unlock", "give claim_url to your user", "forge login && "} {
		if !strings.Contains(doc.AgentInstructions, want) {
			t.Errorf("agent_instructions lacks %q:\n%s", want, doc.AgentInstructions)
		}
	}
	if strings.Contains(doc.AgentInstructions, "Do NOT give") {
		t.Errorf("agent_instructions holds the link back:\n%s", doc.AgentInstructions)
	}
	b, _ := json.Marshal(doc)
	if !strings.Contains(string(b), `"claim_required":"content"`) {
		t.Errorf("claim_required is not serialised: %s", b)
	}

	var text strings.Builder
	printClaimBlock(&text, "prod", doc)
	out := text.String()
	for _, want := range []string{"Nothing is deployed", "Claim:  https://app.reliant.dev/claim#rcl_9f3KQe",
		"Claim to unlock", "give the claim link above to your user"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "do not give the claim link") || strings.Contains(out, "✓ Live") {
		t.Errorf("status contradicts the claim-required refusal:\n%s", out)
	}

	// The MCP relay says the same.
	res := &claimableDeployResult{Env: "prod"}
	fillClaim(res, doc)
	steps := claimNextSteps(res)
	if !strings.Contains(steps, "claim link "+cred.Claimable.ClaimURL) || strings.Contains(steps, "do not give them the claim link") {
		t.Errorf("next_steps:\n%s", steps)
	}
}

// The platform refuses the upload until the deploy is claimed (content the
// local check does not see): the deploy prints the claim link, and env
// status gives it out rather than holding it back.
func TestClaimableDeploy_PlatformClaimRequiredPrintsTheClaimLink(t *testing.T) {
	testkit.Slow(t, "renders KCL and runs the whole CLI")
	fake, _ := claimableFixture(t)
	fake.refuseBundle = cloud.ClaimRequiredContent

	out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json")
	if err == nil {
		t.Fatalf("a refused upload exited 0:\n%s", out)
	}
	doc := decodeClaimDoc(t, out)
	claim, _ := doc["claim"].(map[string]any)
	if doc["error_reason"] != cloud.ReasonClaimableContentRefused || claim == nil ||
		claim["claim_url"] != "https://app.reliant.dev/claim#rcl_9f3KQe" || claim["claim_required"] != cloud.ClaimRequiredContent {
		t.Fatalf("deploy doc = %v", doc)
	}
	if !strings.Contains(err.Error(), "Claim:  https://app.reliant.dev/claim#rcl_9f3KQe") {
		t.Errorf("the refusal does not print the claim link:\n%v", err)
	}

	statusOut, err := runForge(t, "env", "status", "hosted", "--claimable")
	if err != nil {
		t.Fatalf("status --claimable: %v\n%s", err, statusOut)
	}
	if !strings.Contains(statusOut, "Claim:  https://app.reliant.dev/claim#rcl_9f3KQe") || strings.Contains(statusOut, "do not give the claim link") {
		t.Errorf("status --claimable after a claim-required refusal:\n%s", statusOut)
	}
}

// The scan's other refusals (one claiming does not lift, and an unavailable
// scan) exit 4 with a --json envelope naming the reason, instead of exit 1
// and no document (forge examples/express-postgres, 2026-11-25).
func TestClaimableDeploy_ScanRefusalExits4WithItsEnvelope(t *testing.T) {
	testkit.Slow(t, "renders KCL and runs the whole CLI")
	for _, reason := range []string{cloud.ReasonClaimableContentRefused, cloud.ReasonClaimableScanUnavailable} {
		t.Run(reason, func(t *testing.T) {
			fake, _ := claimableFixture(t)
			fake.refuseBundleReason = reason
			out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json")
			if err == nil || exitCodeForError(err) != exitRefused {
				t.Fatalf("want exit 4; err=%v\n%s", err, out)
			}
			doc := decodeClaimDoc(t, out)
			if doc["ok"] != false || doc["exit_code"] != float64(exitRefused) || doc["error_reason"] != reason {
				t.Fatalf("doc = %v", doc)
			}
			if next, _ := doc["next_actions"].([]any); len(next) == 0 {
				t.Errorf("no next_actions: %v", doc)
			}
			if !strings.Contains(err.Error(), "evidence id: ev-123") || !strings.Contains(err.Error(), "fix: ") {
				t.Errorf("refusal text:\n%v", err)
			}
			if strings.Contains(err.Error(), "Claim:  ") {
				t.Errorf("offers the claim link for a refusal claiming does not lift:\n%v", err)
			}
		})
	}
}

// A claim-required refusal AFTER the promotion is written (the content scan at
// publish) reaches the deploy's own --json document: one document, whose
// exit_code is the process's and whose claim carries the link and the cause,
// so MCP's deploy_claimable can relay them.
func TestClaimableDeploy_ClaimRequiredAtPublishIsInTheOneDocument(t *testing.T) {
	testkit.Slow(t, "renders KCL and runs the whole CLI")
	for _, proc := range []string{procRecordBundle, "controlplane.v1.DeployService/Promote"} {
		t.Run(proc, func(t *testing.T) {
			fake, _ := claimableFixture(t)
			fake.refuseBundle, fake.refuseProc = cloud.ClaimRequiredContent, proc
			if proc == procRecordBundle {
				fake.refuseFromCall = 2 // the build records it once; refuse the next
			}
			out, err := runForge(t, "env", "deploy", "hosted", "--claimable", "--no-wait", "--json")
			if err == nil || exitCodeForError(err) != exitRefused {
				t.Fatalf("want exit 4; err=%v\n%s", err, out)
			}
			if n := strings.Count(out, "\n{\n") + boolInt(strings.HasPrefix(out, "{")); n != 1 {
				t.Fatalf("stdout carries %d JSON documents, want 1:\n%s", n, out)
			}
			doc := decodeClaimDoc(t, out)
			claim, _ := doc["claim"].(map[string]any)
			if doc["exit_code"] != float64(exitRefused) || doc["error_reason"] != cloud.ReasonClaimableContentRefused || claim == nil ||
				claim["claim_url"] != "https://app.reliant.dev/claim#rcl_9f3KQe" || claim["claim_required"] != cloud.ClaimRequiredContent {
				t.Fatalf("doc = %v", doc)
			}
			res := &claimableDeployResult{Env: "hosted"}
			fillClaim(res, decodeClaimJSON(t, claim))
			if res.ClaimURL == "" || res.ClaimRequired == "" {
				t.Fatalf("MCP result lacks the claim: %+v", res)
			}
		})
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func decodeClaimJSON(t *testing.T, m map[string]any) *deployClaimJSON {
	t.Helper()
	b, _ := json.Marshal(m)
	var c deployClaimJSON
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return &c
}
