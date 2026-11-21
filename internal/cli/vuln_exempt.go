package cli

// `forge vuln exempt <env> <advisory-id> --reason "<why>" --expires YYYY-MM-DD`
// (design doc §1.7): accept one advisory for one env's vuln_gate, declared in
// the env's own file so the acceptance is reviewed like any other change.
//
// It writes a forge.VulnExemption into deploy/kcl/<env>/main.k
// (vuln_exempt_kcl.go), or an entry under delivery_policy.vuln_gate.exemptions
// in the env's YAML env document, then renders the env and keeps the edit
// only if the rendered policy carries the exemption. The vocabulary is the
// one ci.vuln_scan.exemptions already uses: an id, a reason a reviewer can
// check, and an expiry, after which the exemption exempts nothing.
//
// Idempotent: the same exemption again changes nothing. The same id with a
// new reason or expiry replaces the old entry (renewing an exemption is an
// edit of the one entry, not a second one, which the schema refuses).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
	"github.com/reliant-labs/forge/internal/envdoc"
	"github.com/reliant-labs/forge/pkg/release"
)

// vulnExemptIDRE is an advisory id the schema accepts (forge.VulnExemption's
// _DELIVERY_ID_RE, release's deliveryAdvisoryRE).
var vulnExemptIDRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[A-Za-z0-9-]+$`)

// vulnExemptRender renders env's resolved vuln_gate; a var so a test states
// the render instead of evaluating KCL.
var vulnExemptRender = func(ctx context.Context, projectDir, env string) (release.ShapeVulnGate, error) {
	entities, err := RenderKCL(ctx, projectDir, env)
	if err != nil {
		return release.ShapeVulnGate{}, err
	}
	return entities.DeliveryPolicy.Resolved().VulnGate, nil
}

type vulnExemptOptions struct {
	reason  string
	expires string
}

func newVulnExemptCmd(deps vulnDeps) *cobra.Command {
	var opts vulnExemptOptions
	cmd := &cobra.Command{
		Use:   "exempt <env> <advisory-id> --reason \"<why>\" --expires YYYY-MM-DD",
		Short: "Accept one advisory for an env's vuln_gate, declared in the env's KCL",
		Long: `Accept one advisory for an env's vuln_gate: write a forge.VulnExemption into
deploy/kcl/<env>/main.k (or into the env document's delivery_policy.vuln_gate),
so the acceptance is a reviewed, committed change.

An exemption names the advisory (a CVE id covers the GHSA of the same advisory,
and the other way round), the reason a reviewer can check, and the date it
expires: from the day after, it exempts nothing and the gate judges the finding
again. --expires must not be in the past.

The env is rendered after the edit, and the edit is kept only if the rendered
policy carries the exemption. Running it again with the same values changes
nothing; with a new reason or expiry it replaces the entry.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVulnExempt(cmd.Context(), cmd.OutOrStdout(), deps, args[0], args[1], opts)
		},
	}
	cmd.Flags().StringVar(&opts.reason, "reason", "", "why this advisory cannot hurt this env: the claim a reviewer checks (required)")
	cmd.Flags().StringVar(&opts.expires, "expires", "", "the last day the exemption applies, YYYY-MM-DD (required; not in the past)")
	return cmd
}

// validateVulnExemption refuses an exemption the schema would refuse, or one
// that expired before it was written.
func validateVulnExemption(id, reason, expires string, now time.Time) error {
	switch {
	case !vulnExemptIDRE.MatchString(id):
		return fmt.Errorf("%q is not an advisory id (CVE-2026-0001, GHSA-xxxx-xxxx-xxxx, GO-2026-0001)", id)
	case strings.TrimSpace(reason) == "":
		return fmt.Errorf("--reason is required: the claim a reviewer can check (\"unreachable: we never call the parser\")")
	case strings.ContainsAny(reason, "\r\n"):
		return fmt.Errorf("--reason must be one line")
	case strings.Contains(reason, "${"):
		return fmt.Errorf("--reason may not contain `${` (KCL would read it as interpolation)")
	case expires == "":
		return fmt.Errorf("--expires YYYY-MM-DD is required: an exemption is time-boxed")
	}
	exp, err := time.Parse("2006-01-02", expires)
	if err != nil {
		return fmt.Errorf("--expires %q is not a YYYY-MM-DD date", expires)
	}
	today, _ := time.Parse("2006-01-02", now.UTC().Format("2006-01-02"))
	if exp.Before(today) {
		return fmt.Errorf("--expires %s is in the past (today is %s): an exemption that has expired exempts nothing", expires, today.Format("2006-01-02"))
	}
	return nil
}

func runVulnExempt(ctx context.Context, out io.Writer, deps vulnDeps, env, id string, opts vulnExemptOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reason := strings.TrimSpace(opts.reason)
	if err := validateVulnExemption(id, reason, opts.expires, deps.now()); err != nil {
		return err
	}
	projectDir := deps.projectDir()
	path, kind, err := vulnExemptTarget(projectDir, env)
	if err != nil {
		return err
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var next string
	var changed bool
	switch kind {
	case "kcl":
		next, changed, err = exemptInKCL(string(orig), id, reason, opts.expires)
	case "yaml":
		next, changed, err = exemptInEnvDocYAML(orig, id, reason, opts.expires)
	default:
		err = &errVulnExemptNoAnchor{
			why:     fmt.Sprintf("%s is a JSON env document, which forge does not rewrite", relOrAbs(projectDir, path)),
			snippet: fmt.Sprintf("    \"delivery_policy\": {\"vuln_gate\": {\"exemptions\": [{\"id\": %q, \"reason\": %q, \"expires\": %q}]}}\n", id, reason, opts.expires),
		}
	}
	if err != nil {
		return fmt.Errorf("forge vuln exempt %s %s: %w", env, id, err)
	}
	rel := filepath.ToSlash(relOrAbs(projectDir, path)) // the same spelling on every OS
	if !changed {
		_, _ = fmt.Fprintf(out, "%s already exempts %s (expires %s); nothing changed\n", rel, id, opts.expires)
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, []byte(next), info.Mode().Perm()); err != nil {
		return err
	}
	// Nothing else changed: what is on disk is the verified edit, byte for
	// byte (exemptInKCL proved it differs from the original only inside the
	// inserted or updated literal).
	if back, err := os.ReadFile(path); err != nil || string(back) != next {
		_ = atomicfile.WriteFile(path, orig, info.Mode().Perm())
		return fmt.Errorf("forge vuln exempt: %s did not read back as written; restored it", relOrAbs(projectDir, path))
	}
	gate, rerr := vulnExemptRender(ctx, projectDir, env)
	if rerr == nil && !gateExempts(gate, id, reason, opts.expires) {
		rerr = fmt.Errorf("the edit to %s does not reach env %s's rendered vuln_gate (its policy may be built in another file)", rel, env)
	}
	if rerr != nil {
		if werr := atomicfile.WriteFile(path, orig, info.Mode().Perm()); werr != nil {
			return fmt.Errorf("forge vuln exempt: %v; and restoring %s failed: %w", rerr, rel, werr)
		}
		return fmt.Errorf("forge vuln exempt %s %s: %v\n  %s is unchanged. Add the exemption where env %s declares its vuln_gate:\n      %s",
			env, id, rerr, rel, env, vulnExemptionLiteral("forge", id, reason, opts.expires))
	}
	_, _ = fmt.Fprintf(out, "exempted %s in env %s until %s: %s\n", id, env, opts.expires, rel)
	_, _ = fmt.Fprintf(out, "  review and commit it; `forge vuln list %s --include-exempt` shows what it covers\n", env)
	return nil
}

// vulnExemptTarget is the file that declares env: its main.k, else its env
// document.
func vulnExemptTarget(projectDir, env string) (path, kind string, err error) {
	if env == "" || strings.ContainsAny(env, `/\`) || strings.HasPrefix(env, ".") {
		return "", "", fmt.Errorf("%q is not an env name", env)
	}
	mainK := filepath.Join(projectDir, "deploy", "kcl", env, "main.k")
	if _, err := os.Stat(mainK); err == nil {
		return mainK, "kcl", nil
	}
	doc, err := envdoc.Path(projectDir, env)
	if err != nil {
		return "", "", err
	}
	if doc == "" {
		return "", "", fmt.Errorf("env %q has no declaration in this checkout (no deploy/kcl/%s/main.k, no %s/%s.yaml)", env, env, envdoc.Dir, env)
	}
	if strings.HasSuffix(doc, ".json") {
		return doc, "json", nil
	}
	return doc, "yaml", nil
}

// gateExempts reports whether gate carries exactly this exemption.
func gateExempts(gate release.ShapeVulnGate, id, reason, expires string) bool {
	for _, e := range gate.Exemptions {
		if e.ID == id && strings.TrimSpace(e.Reason) == reason && e.Expires == expires {
			return true
		}
	}
	return false
}

// exemptInEnvDocYAML states the exemption in a YAML env document's
// delivery_policy.vuln_gate.exemptions, keeping its comments (yaml.v3 node
// edit). The document must already declare delivery_policy.vuln_gate: a
// hand-written policy is the author's whole statement, and inventing the
// rest of one is not this verb's call.
func exemptInEnvDocYAML(src []byte, id, reason, expires string) (string, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return "", false, err
	}
	snippet := fmt.Sprintf("    delivery_policy:\n      vuln_gate:\n        exemptions:\n          - id: %s\n            reason: %q\n            expires: %q\n", id, reason, expires)
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", false, &errVulnExemptNoAnchor{why: "the env document is not a YAML mapping", snippet: snippet}
	}
	gate := yamlChild(yamlChild(doc.Content[0], "delivery_policy"), "vuln_gate")
	if gate == nil || gate.Kind != yaml.MappingNode {
		return "", false, &errVulnExemptNoAnchor{why: "the env document declares no delivery_policy.vuln_gate", snippet: snippet}
	}
	entry := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		yamlStr("id"), yamlStr(id), yamlStr("reason"), yamlStr(reason), yamlStr("expires"), yamlStr(expires),
	}}
	list := yamlChild(gate, "exemptions")
	switch {
	case list == nil:
		gate.Content = append(gate.Content, yamlStr("exemptions"), &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{entry}})
	case list.Kind != yaml.SequenceNode:
		return "", false, &errVulnExemptNoAnchor{why: "delivery_policy.vuln_gate.exemptions is not a list", snippet: snippet}
	default:
		replaced := false
		for i, e := range list.Content {
			if n := yamlChild(e, "id"); n != nil && n.Value == id {
				if v := yamlChild(e, "reason"); v != nil && v.Value == reason {
					if x := yamlChild(e, "expires"); x != nil && x.Value == expires {
						return string(src), false, nil
					}
				}
				list.Content[i], replaced = entry, true
			}
		}
		if !replaced {
			list.Content = append(list.Content, entry)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", false, err
	}
	if err := enc.Close(); err != nil {
		return "", false, err
	}
	return buf.String(), true, nil
}

func yamlChild(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func yamlStr(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
}
