package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// missingSecretFixture is an OnHost api that declares two secrets; the file
// store holds only one of them.
const missingSecretFixture = `{"output":{"secret_provider":{"type":"file","path":"secrets.yaml"},"workloads":[{"name":"api","kind":"service","runtime":{"type":"host","launch_env":{"PORT":"8181"}},"spec":{"kind":"service","env":[{"name":"LOG_LEVEL","value":"debug"},{"name":"TOKEN","secretRef":{"name":"app","key":"TOKEN"}},{"name":"SIGNING_CERT","secretRef":{"name":"app","key":"signing_cert"}}]}}]}}`

func withMissingSecretFixture(t *testing.T) {
	t.Helper()
	withKCLFixture(t, missingSecretFixture)
	if err := os.WriteFile("secrets.yaml", []byte("TOKEN: local-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runEnvConfigSplit runs `env config` with stdout and stderr kept apart and
// returns the error rather than failing on it.
func runEnvConfigSplit(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newEnvConfigCmd()
	var out, errout bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errout)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errout.String(), err
}

// forge#859 routed `env config` through env exec's resolver and with it the
// fail-closed secret check, so reading back an env whose store lacked one
// declared key printed nothing and exited 1. A readback REPORTS the gap: it
// exits 0, prints what resolves, and names what is missing — on stderr, and
// as a field in --json, so stdout stays exactly the document.
func TestEnvConfigReportsMissingSecretsAndExitsZero(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates a KCL project natively; runs in task test")
	}
	withMissingSecretFixture(t)

	stdout, stderr, err := runEnvConfigSplit(t, "dev", "--json")
	if err != nil {
		t.Fatalf("env config with a missing secret must exit 0, got %v", err)
	}
	var report envConfigReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("stdout must be the JSON document alone: %v\n%s", err, stdout)
	}
	api := report.Workloads[0]
	if api.Env["LOG_LEVEL"] != "debug" || api.Env["TOKEN"] != "local-secret" {
		t.Errorf("what resolves must still be reported: %v", api.Env)
	}
	if _, present := api.Env["SIGNING_CERT"]; present {
		t.Errorf("a missing secret must be left out of env, not reported empty")
	}
	if len(api.MissingSecrets) != 1 || api.MissingSecrets[0] != (envConfigMissingSecret{Name: "SIGNING_CERT", Secret: "app", Key: "signing_cert"}) {
		t.Errorf("missing_secrets = %+v", api.MissingSecrets)
	}
	for _, want := range []string{"warning:", "SIGNING_CERT", "Secret app/signing_cert", "secret set --env dev"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "local-secret") || strings.Contains(stdout, "SIGNING_CERT\": \"") {
		t.Error("a value leaked, or the missing secret was reported with a value")
	}

	// The table view exits 0 too, with the warning on stderr only.
	table, stderr, err := runEnvConfigSplit(t, "dev")
	if err != nil {
		t.Fatalf("table view: %v", err)
	}
	if !strings.Contains(table, "LOG_LEVEL") || strings.Contains(table, "warning:") || !strings.Contains(stderr, "SIGNING_CERT") {
		t.Errorf("table stdout:\n%s\nstderr:\n%s", table, stderr)
	}
}

// --strict keeps the fail-closed behaviour for a caller that wants it.
func TestEnvConfigStrictFailsOnMissingSecrets(t *testing.T) {
	testkit.Slow(t, "execs git")
	withMissingSecretFixture(t)
	_, _, err := runEnvConfigSplit(t, "dev", "--strict")
	if err == nil || !strings.Contains(err.Error(), "SIGNING_CERT") || !strings.Contains(err.Error(), "secret set --env dev") {
		t.Fatalf("--strict must refuse naming the missing secret, got %v", err)
	}
}

// A clean store reports nothing missing and prints no warning.
func TestEnvConfigNoWarningWhenEverySecretResolves(t *testing.T) {
	testkit.Slow(t, "execs git")
	withMissingSecretFixture(t)
	if err := os.WriteFile("secrets.yaml", []byte("TOKEN: t\nSIGNING_CERT: c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := runEnvConfigSplit(t, "dev", "--json")
	if err != nil || stderr != "" || strings.Contains(stdout, "missing_secrets") {
		t.Fatalf("err=%v stderr=%q stdout=%s", err, stderr, stdout)
	}
}

// Running a process without a secret it declares is the real error: env exec
// stays strict, and refuses before the child starts.
func TestEnvExecRefusesMissingSecrets(t *testing.T) {
	testkit.Slow(t, "execs git")
	withMissingSecretFixture(t)
	t.Setenv("FORGE_EXEC_HELPER", "1")
	out, _, err := executeEnvFixture(t, []string{"dev"}, execHelperArgs(t))
	if err == nil || !strings.Contains(err.Error(), "SIGNING_CERT") || !strings.Contains(err.Error(), "secret set --env dev") {
		t.Fatalf("env exec must refuse naming the missing secret, got %v", err)
	}
	if out != "" {
		t.Fatalf("the child must not run: stdout %q", out)
	}
}

func TestEnvConfigMasksUnreferencedProviderValues(t *testing.T) {
	testkit.Slow(t, "evaluates a KCL project natively")
	withKCLFixture(t, `{"output":{"secret_provider":{"type":"file","path":"secrets.yaml"},"workloads":[
		{"name":"compose-app","kind":"service","runtime":{"type":"compose","env_file":"local.env"},"spec":{"kind":"service"}},
		{"name":"host-app","kind":"service","runtime":{"type":"host"},"spec":{"kind":"service","env":[{"name":"ALIAS","value":"dummy-alias-value"},{"name":"PUBLIC","value":"visible"}]}}
	]}}`)
	if err := os.WriteFile("secrets.yaml", []byte("UNREFERENCED: dummy-provider-value\nORIGINAL: dummy-alias-value\nOVERRIDDEN: dummy-replaced-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("local.env", []byte("OVERRIDDEN=public-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	table, _, err := runEnvConfigSplit(t, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(table, "dummy-provider-value") || strings.Contains(table, "dummy-alias-value") {
		t.Error("default table exposed a value present in the secret store")
	}
	if !strings.Contains(table, "public-value") || !strings.Contains(table, "visible") {
		t.Error("default table hid public values")
	}
	shown, _, err := runEnvConfigSplit(t, "dev", "--show-secrets")
	if err != nil || !strings.Contains(shown, "dummy-provider-value") || !strings.Contains(shown, "dummy-alias-value") {
		t.Fatalf("--show-secrets did not preserve the opt-in display (err %v)", err)
	}
	stdout, _, err := runEnvConfigSplit(t, "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report envConfigReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"compose-app": "ORIGINAL UNREFERENCED", "host-app": "ALIAS"}
	if len(report.Workloads) != len(want) {
		t.Fatalf("got %d workloads, want %d", len(report.Workloads), len(want))
	}
	for _, row := range report.Workloads {
		if got := strings.Join(row.SecretVars, " "); got != want[row.Name] {
			t.Errorf("%s: secret_vars %q, want %q", row.Name, got, want[row.Name])
		}
	}
}

// A readback must not put a secret in a terminal or an agent's transcript
// (forge report a7cfb588): the table masks a secret-sourced value unless
// --show-secrets is given, and --json carries it and names it in secret_vars.
func TestEnvConfigMasksSecretValuesInTheTable(t *testing.T) {
	testkit.Slow(t, "evaluates a KCL project natively")
	withMissingSecretFixture(t)

	table, _, err := runEnvConfigSplit(t, "dev")
	if err != nil {
		t.Fatalf("table view: %v", err)
	}
	if strings.Contains(table, "local-secret") || !strings.Contains(table, "<secret, 12 bytes>") || !strings.Contains(table, "debug") {
		t.Errorf("the table must mask TOKEN and show LOG_LEVEL:\n%s", table)
	}
	shown, _, err := runEnvConfigSplit(t, "dev", "--show-secrets")
	if err != nil || !strings.Contains(shown, "local-secret") {
		t.Errorf("--show-secrets must print the value (err %v):\n%s", err, shown)
	}
	stdout, _, err := runEnvConfigSplit(t, "dev", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var report envConfigReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	api := report.Workloads[0]
	if api.Env["TOKEN"] != "local-secret" || len(api.SecretVars) != 1 || api.SecretVars[0] != "TOKEN" {
		t.Errorf("--json must carry the value and name it in secret_vars: env %v, secret_vars %v", api.Env, api.SecretVars)
	}
}
