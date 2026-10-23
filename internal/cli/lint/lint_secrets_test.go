package lint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/kclvendor"
	"github.com/reliant-labs/forge/internal/lint/secrets"
	"github.com/reliant-labs/forge/internal/linter/finding"
)

const secretsKCLMod = "[package]\nname = \"acme_deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n"

// prodMainK declares one store-backed Secret, one out of band, and a workload
// that reads a key the first does not list and a Secret nothing declares.
const prodMainK = `import forge
import forge.workloads as fw

_hub = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _hub
    openbao = forge.OpenBao {}
    external_secrets_operators = [forge.ExternalSecretsOperator {cluster = _hub}]
    required_secrets = [
        forge.ExternalSecret {
            name = "app-db"
            namespace = "acme-prod"
            keys = ["password"]
            source = forge.StoreSource {
                store = forge.OpenBaoStore {}
                data = {"password" = "APP_DB_PASSWORD"}
                immutable = ["APP_DB_PASSWORD"]
            }
        }
        forge.ExternalSecret {
            name = "cloudflare-api-token"
            namespace = "cert-manager"
            keys = ["api-token"]
            out_of_band_reason = "manual"
        }
    ]
    workloads = [
        fw.Workload {
            name = "api"
            image = "ghcr.io/acme/api:1"
            replicas = 1
            ports = [fw.Port {name = "http", port = 8080}]
            runtime = forge.OnCluster {target = _hub}
            env = {
                DB_PASSWORD = forge.SecretRef {name = "app-db", key = "password"}
                DB_URL = forge.SecretRef {name = "app-db", key = "url"}
                GHOST = forge.SecretRef {name = "ghost", key = "k"}
            }
        }
    ]
})
`

func writeSecretsProject(t *testing.T, mainK string) string {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"forge.yaml":             "name: acme\nmodule_path: github.com/acme/acme\n",
		"deploy/kcl/kcl.mod":     secretsKCLMod,
		"deploy/kcl/prod/main.k": mainK,
	})
	return root
}

func ruleSet(fs []finding.Finding) map[string][]finding.Finding {
	m := map[string][]finding.Finding{}
	for _, f := range fs {
		m[f.Rule] = append(m[f.Rule], f)
	}
	return m
}

// The lane renders each env, reads the Secrets its workloads project, and
// reports each defect with the file:line of the declaration that causes it.
func TestSecretsLaneJudgesARenderedEnv(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	root := writeSecretsProject(t, prodMainK)

	res, err := secretsFindings(root, &config.ProjectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.adopted || res.judged != 1 {
		t.Fatalf("adopted=%v judged=%d, want the one env judged", res.adopted, res.judged)
	}
	got := ruleSet(res.findings)

	// The render carries the out_of_band_reason, and the lane holds it to a standard.
	oob := got[secrets.RuleOutOfBandReason]
	if len(oob) != 1 || oob[0].File != "deploy/kcl/prod/main.k" || !strings.Contains(oob[0].Message, "cloudflare-api-token") {
		t.Fatalf("out_of_band_reason: %+v", oob)
	}
	if line := mainKLine(t, root, "name = \"cloudflare-api-token\""); oob[0].Line != line {
		t.Errorf("anchored at line %d, want the declaration at %d", oob[0].Line, line)
	}

	// The render carries source.immutable, and the lane sees the unpinned read.
	if len(got[secrets.RuleImmutablePin]) != 1 {
		t.Errorf("immutable pin: %+v", got[secrets.RuleImmutablePin])
	}

	// A key the declaration does not list, and a Secret nothing declares.
	cov := got[secrets.RuleKeyCoverage]
	if len(cov) != 2 {
		t.Fatalf("key coverage: want the dropped key and the ghost Secret, got %+v", cov)
	}
	all := cov[0].Message + cov[1].Message
	if !strings.Contains(all, "url") || !strings.Contains(all, "ghost") {
		t.Errorf("coverage findings name neither the key nor the Secret: %s", all)
	}

	// OpenBao is declared with no bootstrap_source: a warning, not a gate.
	esc := got[secrets.RuleEscrow]
	if len(esc) != 1 || esc[0].Severity != finding.SeverityWarning {
		t.Errorf("escrow: %+v", esc)
	}

	// Nothing the schema or the render already guarantees is reported again.
	for _, rule := range []string{secrets.RuleClusterKind, secrets.RuleESPolicy, secrets.RuleSingleOwner, secrets.RuleWildcardRead} {
		if len(got[rule]) != 0 {
			t.Errorf("%s fired on a clean render: %+v", rule, got[rule])
		}
	}

	// Every finding carries a place and an escape.
	for _, f := range res.findings {
		if f.File == "" || f.Line < 1 || !strings.Contains(f.Remediation, "forge:lint-disable-next-line "+f.Rule) {
			t.Errorf("finding without file:line or escape: %+v", f)
		}
	}

	// The gate: errors fail, and the JSON arm agrees.
	out, gated, err := collectSecretsJSON(&lintRunCtx{cwd: root, cfg: &config.ProjectConfig{}})
	if err != nil || !gated || len(out) != len(res.findings) {
		t.Errorf("collectSecretsJSON gated=%v n=%d err=%v", gated, len(out), err)
	}
}

func mainKLine(t *testing.T, root, needle string) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, "deploy/kcl/prod/main.k"))
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(raw), "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("%q not in main.k", needle)
	return 0
}

// The escape every message names works, and a reasonless waiver of an error is
// itself a finding.
func TestSecretsLaneHonoursTheSharedWaiver(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	waived := strings.Replace(prodMainK, "            name = \"cloudflare-api-token\"\n",
		"            # forge:lint-disable-next-line secrets-out-of-band-reason: the Cloudflare token predates the store; tracked in SC20\n            name = \"cloudflare-api-token\"\n", 1)
	root := writeSecretsProject(t, waived)
	res, err := secretsFindings(root, &config.ProjectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got := ruleSet(res.findings)[secrets.RuleOutOfBandReason]; len(got) != 0 {
		t.Errorf("the waiver did not take: %+v", got)
	}

	bare := strings.Replace(prodMainK, "            name = \"cloudflare-api-token\"\n",
		"            # forge:lint-disable-next-line secrets-out-of-band-reason\n            name = \"cloudflare-api-token\"\n", 1)
	res, err = secretsFindings(writeSecretsProject(t, bare), &config.ProjectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ruleSet(res.findings)["forge-suppression-missing-reason"]) == 0 {
		t.Errorf("a waiver of an error with no reason must be a finding: %+v", res.findings)
	}
}

// forge.yaml lint.rules dials a rule, as it does for every other lane — in
// the outcome engine's one grading pass (lint_rules.go).
func TestSecretsLaneHonoursLintRules(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	root := writeSecretsProject(t, prodMainK)
	cfg := &config.ProjectConfig{}
	cfg.Lint.Rules = map[string]string{secrets.RuleOutOfBandReason: "warn", secrets.RuleKeyCoverage: "off"}
	res, err := secretsFindings(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	graded, _ := gradeLane(effectiveRules(cfg), findingsToJSON(res.findings), true)
	var coverage, reason []lintJSONFinding
	for _, f := range graded {
		switch f.Rule {
		case secrets.RuleKeyCoverage:
			coverage = append(coverage, f)
		case secrets.RuleOutOfBandReason:
			reason = append(reason, f)
		}
	}
	if len(coverage) != 0 {
		t.Errorf("a rule set to off still fired: %+v", coverage)
	}
	if len(reason) != 1 || reason[0].Severity != lintSevWarning {
		t.Errorf("a rule set to warn: %+v", reason)
	}
}

// A project that declares nothing of the model is not rendered and not judged.
func TestSecretsLaneIsInertForAProjectThatDidNotAdoptTheModel(t *testing.T) {
	root := writeSecretsProject(t, "import forge\n\noutput = forge.render(forge.Bundle {project = \"acme\"})\n")
	res, err := secretsFindings(root, &config.ProjectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if res.adopted || len(res.findings) != 0 {
		t.Errorf("adopted=%v findings=%+v", res.adopted, res.findings)
	}
}

func TestSecretsLaneIsRegisteredAsAGate(t *testing.T) {
	var found bool
	for _, s := range lintPipeline() {
		if s.name == "secrets lint" {
			found = true
			if !s.gates {
				t.Error("secrets lint does not gate")
			}
		}
	}
	if !found {
		t.Error("secrets lint is not in the sweep")
	}
	var f lintFlags
	f.secrets = true
	if got := selectedLanes(f); len(got) != 1 || got[0] != "secrets" {
		t.Errorf("--secrets selects %v", got)
	}
}

// A real forge.OpenBao render: its own pods read Secrets the component's
// prerequisites supply (no false coverage finding), and the access document it
// renders is judged for a glob read whose stated reason states nothing.
func TestSecretsLaneJudgesARealOpenBaoRender(t *testing.T) {
	if testing.Short() {
		t.Skip("renders KCL; runs in task test")
	}
	t.Cleanup(kclvendor.SetCacheDirForTest(t.TempDir()))
	const main = `import forge

_hub = forge.ClusterTarget {cluster = "gke_acme_prod", namespace = "acme-prod", platform = "amd64", connected_cluster = "hub"}

output = forge.render(forge.Bundle {
    project = "acme"
    env = "prod"
    cluster_target = _hub
    bootstrap_source = forge.GCPSecretManager {project = "acme-prod"}
    openbao = forge.OpenBao {
        consumers = [
            forge.OpenBaoConsumer {
                name = "deployer"
                workload = "deployer"
                grants = [forge.OpenBaoGrant {path = "secret/data/envs/*", capabilities = ["read"], wildcard_read_reason = "todo"}]
            }
        ]
    }
})
`
	root := writeSecretsProject(t, main)
	res, err := secretsFindings(root, &config.ProjectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	got := ruleSet(res.findings)
	w := got[secrets.RuleWildcardRead]
	if len(w) != 1 || !strings.Contains(w[0].Message, "secret/data/envs/*") || w[0].Severity != finding.SeverityError {
		t.Fatalf("wildcard read: %+v", res.findings)
	}
	if len(got[secrets.RuleKeyCoverage]) != 0 {
		t.Errorf("OpenBao's own pods flagged as reading Secrets nothing supplies: %+v", got[secrets.RuleKeyCoverage])
	}
	if len(got[secrets.RuleEscrow]) != 0 {
		t.Errorf("escrow warned though bootstrap_source is GSM: %+v", got[secrets.RuleEscrow])
	}

	// With a real reason, nothing is left to say.
	fixed := strings.Replace(main, `"todo"`, `"the deployer reads every hosted env until per-env grants land (SC1)"`, 1)
	res, err = secretsFindings(writeSecretsProject(t, fixed), &config.ProjectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range res.findings {
		if f.Severity == finding.SeverityError {
			t.Errorf("an error on a clean OpenBao: %+v", f)
		}
	}
}
