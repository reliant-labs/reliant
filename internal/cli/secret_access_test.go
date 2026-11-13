package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

const (
	procAllow  = "controlplane.v1.SecretStoreService/AllowSecretUse"
	procDeny   = "controlplane.v1.SecretStoreService/DenySecretUse"
	procAccess = "controlplane.v1.SecretStoreService/ListSecretAccess"
)

// runSecretCLI drives the real cobra group, as TestHostedSecretCLIEndToEnd does.
func runSecretCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd := newSecretCmd()
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
}

func principalOf(t *testing.T, c recordedCall) map[string]any {
	t.Helper()
	p, ok := c.Body["principal"].(map[string]any)
	if !ok {
		t.Fatalf("request has no principal object: %s", c.Bytes)
	}
	return p
}

func TestSecretAllowSendsWorkflowPrincipal(t *testing.T) {
	fake, token := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	out, err := runSecretCLI(t, "allow", "--env", "prod", "--workflow", "trader-bot", "--project", "proj-1", "TRADER_KEY")
	if err != nil {
		t.Fatalf("allow: %v\n%s", err, out)
	}
	calls := fake.callsTo(procAllow)
	if len(calls) != 1 {
		t.Fatalf("AllowSecretUse calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Auth != "Bearer "+token || c.Body["environmentId"] != "env-1" || c.Body["secretName"] != "TRADER_KEY" {
		t.Errorf("call = %+v", c)
	}
	p := principalOf(t, c)
	if p["kind"] != "workflow" || p["projectId"] != "proj-1" || p["workflowName"] != "trader-bot" {
		t.Errorf("principal = %v, want workflow proj-1/trader-bot", p)
	}
	if !strings.Contains(out, "trader-bot") || !strings.Contains(out, "TRADER_KEY") {
		t.Errorf("output does not say what was allowed: %q", out)
	}
	// Idempotent: a second allow succeeds and the list still has one entry.
	if _, err := runSecretCLI(t, "allow", "--env", "prod", "--workflow", "trader-bot", "--project", "proj-1", "TRADER_KEY"); err != nil {
		t.Fatalf("second allow: %v", err)
	}
	if len(fake.access) != 1 {
		t.Errorf("access entries = %d after repeat allow, want 1", len(fake.access))
	}
}

func TestSecretAllowCreatesFreshEnvByName(t *testing.T) {
	fake, _ := hostedFixture(t, nil)
	if _, err := runSecretCLI(t, "allow", "--env", "prod", "--workflow", "w", "--project", "p", "K"); err != nil {
		t.Fatalf("allow on an unseen env: %v", err)
	}
	if n := len(fake.callsTo("controlplane.v1.DeployService/EnsureEnvironment")); n != 1 {
		t.Errorf("EnsureEnvironment calls = %d, want 1", n)
	}
	if c := fake.callsTo(procAllow); len(c) != 1 || c[0].Body["environmentId"] != "env-prod-created" {
		t.Errorf("AllowSecretUse = %+v, want one into the created env", c)
	}
}

func TestSecretDenyRevokesEntry(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	for _, wf := range []string{"trader-bot", "other"} {
		if _, err := runSecretCLI(t, "allow", "--env", "prod", "--workflow", wf, "--project", "proj-1", "K"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runSecretCLI(t, "deny", "--env", "prod", "--workflow", "trader-bot", "--project", "proj-1", "K")
	if err != nil {
		t.Fatalf("deny: %v\n%s", err, out)
	}
	calls := fake.callsTo(procDeny)
	if len(calls) != 1 || calls[0].Body["environmentId"] != "env-1" || calls[0].Body["secretName"] != "K" ||
		principalOf(t, calls[0])["workflowName"] != "trader-bot" || principalOf(t, calls[0])["kind"] != "workflow" {
		t.Fatalf("DenySecretUse calls = %+v", calls)
	}
	if len(fake.access) != 1 || fake.access[0]["principal"].(map[string]any)["workflowName"] != "other" {
		t.Errorf("remaining access = %v, want only 'other'", fake.access)
	}
	// Idempotent.
	if _, err := runSecretCLI(t, "deny", "--env", "prod", "--workflow", "trader-bot", "--project", "proj-1", "K"); err != nil {
		t.Errorf("repeat deny: %v", err)
	}
}

func TestSecretDenyOnUnseenEnvDoesNotCreateIt(t *testing.T) {
	fake, _ := hostedFixture(t, nil)
	if _, err := runSecretCLI(t, "deny", "--env", "prod", "--workflow", "w", "--project", "p", "K"); err != nil {
		t.Fatalf("deny on an unseen env: %v", err)
	}
	if n := len(fake.callsTo("controlplane.v1.DeployService/EnsureEnvironment")) + len(fake.callsTo(procDeny)); n != 0 {
		t.Errorf("made %d create/deny calls for an env the control plane has not seen", n)
	}
}

func TestSecretAccessListsEntries(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	for _, a := range [][2]string{{"B_KEY", "wf-b"}, {"A_KEY", "wf-z"}, {"A_KEY", "wf-a"}} {
		if _, err := runSecretCLI(t, "allow", "--env", "prod", "--workflow", a[1], "--project", "proj-1", a[0]); err != nil {
			t.Fatal(err)
		}
	}

	out, err := runSecretCLI(t, "access", "--env", "prod", "A_KEY")
	if err != nil {
		t.Fatalf("access: %v\n%s", err, out)
	}
	if c := fake.callsTo(procAccess); len(c) != 1 || c[0].Body["secretName"] != "A_KEY" || c[0].Body["environmentId"] != "env-1" {
		t.Fatalf("ListSecretAccess calls = %+v", c)
	}
	if !strings.Contains(out, "wf-a") || !strings.Contains(out, "wf-z") || strings.Contains(out, "wf-b") {
		t.Errorf("A_KEY access output:\n%s", out)
	}
	if strings.Index(out, "wf-a") > strings.Index(out, "wf-z") {
		t.Errorf("entries not sorted:\n%s", out)
	}

	// No name: the whole env, secret names included.
	out, err = runSecretCLI(t, "access", "--env", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report secretAccessReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if report.Env != "prod" || len(report.Entries) != 3 {
		t.Fatalf("report = %+v, want 3 entries", report)
	}
	first := report.Entries[0]
	if first.SecretName != "A_KEY" || first.WorkflowName != "wf-a" || first.ProjectID != "proj-1" || first.Kind != "workflow" ||
		first.CreatedBy != "owner@example.com" || first.CreatedAt == "" {
		t.Errorf("first entry = %+v", first)
	}
	if c := fake.callsTo(procAccess); len(c) != 2 || c[1].Body["secretName"] != "" {
		t.Errorf("an unnamed access must ask for every secret; calls = %+v", c)
	}
}

func TestSecretAccessEmptyAndUnseenEnv(t *testing.T) {
	hostedFixture(t, nil)
	out, err := runSecretCLI(t, "access", "--env", "prod", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"entries": []`) {
		t.Errorf("empty report must carry an empty array, got:\n%s", out)
	}
	out, err = runSecretCLI(t, "access", "--env", "prod", "K")
	if err != nil || !strings.Contains(out, "no workflow may use it") || !strings.Contains(out, "forge secret allow") {
		t.Errorf("empty text output (err %v):\n%s", err, out)
	}
}

func TestSecretListShowsAccessEntries(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	fake.stored["TRADER_KEY"] = 3
	fake.stored["PLAIN"] = 1
	fake.stored["OLD"] = 1
	for _, a := range [][2]string{{"TRADER_KEY", "trader-bot"}, {"PLAIN", "x"}} {
		if _, err := runSecretCLI(t, "allow", "--env", "prod", "--workflow", a[1], "--project", "proj-1", a[0]); err != nil {
			t.Fatal(err)
		}
	}
	e, _ := renderEntitiesForSecrets(context.Background(), "prod")
	e.Workloads = []WorkloadEntity{hostWL("api", withSecretRef("TRADER_KEY", "s", ""))}

	report, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil {
		t.Fatal(err)
	}
	if got := fake.callsTo(procAccess); len(got) != 1 || got[0].Body["secretName"] != "" {
		t.Fatalf("list should read the whole env's access in one call, got %+v", got)
	}
	if len(report.Secrets) != 1 || report.Secrets[0].Name != "TRADER_KEY" ||
		len(report.Secrets[0].Access) != 1 || report.Secrets[0].Access[0].WorkflowName != "trader-bot" {
		t.Errorf("declared secret access = %+v", report.Secrets)
	}
	// PLAIN is undeclared but workflow-used: reported with its access, not as inert.
	if containsString(report.Inert, "PLAIN") || len(report.AccessOnly["PLAIN"]) != 1 {
		t.Errorf("PLAIN: inert=%v access_only=%v", report.Inert, report.AccessOnly)
	}
	if !containsString(report.Inert, "OLD") {
		t.Errorf("OLD, with no access entry, must stay inert: %v", report.Inert)
	}

	out, err := runSecretCLI(t, "list", "--env", "prod")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"allowed: workflow \"trader-bot\" (project proj-1)", "allowed: workflow \"x\""} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

// A control plane that predates access lists must not break `secret list`.
func TestSecretListToleratesControlPlaneWithoutAccessLists(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	fake.stored["API_KEY"] = 1
	old := newHostedSecretWriter
	newHostedSecretWriter = func(ctx context.Context, env string, e *KCLEntities, ensure bool) (hostedSecretWriter, cloud.Endpoint, error) {
		w, ep, err := old(ctx, env, e, ensure)
		if err != nil {
			return w, ep, err
		}
		return unimplementedAccess{w.(cloudSecretWriter)}, ep, nil
	}
	t.Cleanup(func() { newHostedSecretWriter = old })
	report, err := collectSecretListFacts(context.Background(), "prod")
	if err != nil || !containsString(report.Inert, "API_KEY") {
		t.Fatalf("list = %+v, %v", report, err)
	}
}

type unimplementedAccess struct{ cloudSecretWriter }

func (u unimplementedAccess) Access(context.Context, string) ([]secretAccessEntry, error) {
	return nil, &cloud.Error{Code: cloud.CodeUnimplemented}
}

func TestSecretAccessErrorsSurfaceVerbatim(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	const msg = "only an org owner or admin may edit a secret's access list; step-up required for protected env prod"
	fake.refuse = msg
	for _, args := range [][]string{
		{"allow", "--env", "prod", "--workflow", "w", "--project", "p", "K"},
		{"deny", "--env", "prod", "--workflow", "w", "--project", "p", "K"},
		{"access", "--env", "prod", "K"},
	} {
		_, err := runSecretCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), msg) {
			t.Errorf("forge secret %s: err = %v, want the control plane's message verbatim", args[0], err)
		}
	}
}

func TestSecretAccessRequiresFlagsWithFix(t *testing.T) {
	fake, _ := hostedFixture(t, []cloudEnvironment{{ID: "env-1", Name: "prod"}})
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"allow", "--workflow", "w", "--project", "p", "K"}, "--env is required"},
		{[]string{"allow", "--env", "prod", "--project", "p", "K"}, "--workflow is required"},
		{[]string{"allow", "--env", "prod", "--workflow", "w", "K"}, "reliant project list"},
		{[]string{"deny", "--env", "prod", "--workflow", "w", "K"}, "--project is required"},
		{[]string{"access", "K"}, "--env is required"},
	}
	for _, tc := range cases {
		_, err := runSecretCLI(t, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
	if len(fake.calls) != 0 {
		t.Errorf("a usage error reached the control plane: %+v", fake.calls)
	}
}

func TestSecretAccessRefusesNonHostedEnv(t *testing.T) {
	prev := renderEntitiesForSecrets
	renderEntitiesForSecrets = func(context.Context, string) (*KCLEntities, error) {
		return &KCLEntities{SecretProvider: &SecretProviderEntity{Type: "file"}}, nil
	}
	t.Cleanup(func() { renderEntitiesForSecrets = prev })
	for _, args := range [][]string{
		{"allow", "--env", "dev", "--workflow", "w", "--project", "p", "K"},
		{"deny", "--env", "dev", "--workflow", "w", "--project", "p", "K"},
		{"access", "--env", "dev"},
	} {
		_, err := runSecretCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "access lists are a hosted feature") {
			t.Errorf("forge secret %s on a FileSecrets env: err = %v", args[0], err)
		}
	}
}
