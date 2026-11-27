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
// --package, --artifact and --location (each repeatable) scope it: the
// exemption then covers the advisory only where every one given names the
// finding (release.ShapeVulnScopedExempt). An exemption's identity is its id AND
// its scope.
//
// Idempotent: the same exemption again changes nothing. The same id and
// scope with a new reason or expiry replaces the old entry (renewing an
// exemption is an edit of the one entry, not a second one, which the schema
// refuses). The same id with another scope is another declaration, added
// beside the first.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	// packages, artifacts and locations scope the exemption.
	packages  []string
	artifacts []string
	locations []string
}

// exemption is the exemption the options state, its scope lists sorted.
func (o vulnExemptOptions) exemption(id string) release.ShapeVulnScopedExempt {
	sorted := func(in []string) []string {
		if len(in) == 0 {
			return nil
		}
		out := append([]string(nil), in...)
		sort.Strings(out)
		return out
	}
	return release.ShapeVulnScopedExempt{
		ID: id, Reason: strings.TrimSpace(o.reason), Expires: o.expires,
		Packages: sorted(o.packages), Artifacts: sorted(o.artifacts), Locations: sorted(o.locations),
	}
}

// vulnExemptScopeLabel describes an exemption's scope for a message: ""
// when it has none.
func vulnExemptScopeLabel(ex release.ShapeVulnScopedExempt) string {
	var parts []string
	for _, f := range []struct {
		name string
		vals []string
	}{{"package", ex.Packages}, {"artifact", ex.Artifacts}, {"location", ex.Locations}} {
		if len(f.vals) > 0 {
			parts = append(parts, f.name+" "+strings.Join(f.vals, ", "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

func newVulnExemptCmd(deps vulnDeps) *cobra.Command {
	var opts vulnExemptOptions
	cmd := &cobra.Command{
		Use:   "exempt <env> <advisory-id> --reason \"<why>\" --expires YYYY-MM-DD [--package p] [--artifact a] [--location /path]",
		Short: "Accept one advisory for an env's vuln_gate, declared in the env's KCL",
		Long: `Accept one advisory for an env's vuln_gate: write a forge.VulnExemption into
deploy/kcl/<env>/main.k (or into the env document's delivery_policy.vuln_gate),
so the acceptance is a reviewed, committed change.

An exemption names the advisory (a CVE id covers the GHSA of the same advisory,
and the other way round), the reason a reviewer can check, and the date it
expires: from the day after, it exempts nothing and the gate judges the finding
again. --expires must not be in the past.

By default it covers the advisory in every package of every artifact of the
env. --package, --artifact and --location (each repeatable) narrow it: the
finding is exempt only where every flag given names it, and a finding found in
several places is exempt only when every place is covered.

  --package    a package name ("stdlib") or a purl prefix ("pkg:golang/stdlib")
  --artifact   a release artifact name, as forge vuln list names it
  --location   an absolute path inside the artifact ("/usr/local/bin/grype"),
               or every path under a directory that ends in "/"

A Go standard-library advisory is the case for --location: every Go binary's
package is "stdlib", so only the path tells an unreachable one in a vendored
tool from a reachable one in your own binary. ` + "`forge vuln list <env> --json`" + `
lists each finding's occurrences (artifact and path).

The env is rendered after the edit, and the edit is kept only if the rendered
policy carries the exemption. Running it again with the same values changes
nothing; with the same id and scope and a new reason or expiry it replaces the
entry; with another scope it adds a second one.

A control plane that predates scoped exemptions is sent the policy without
them, and judges the findings they cover as unexempted: a scoped exemption
never widens into an exemption of the advisory everywhere.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVulnExempt(cmd.Context(), cmd.OutOrStdout(), deps, args[0], args[1], opts)
		},
	}
	cmd.Flags().StringVar(&opts.reason, "reason", "", "why this advisory cannot hurt this env: the claim a reviewer checks (required)")
	cmd.Flags().StringVar(&opts.expires, "expires", "", "the last day the exemption applies, YYYY-MM-DD (required; not in the past)")
	cmd.Flags().StringArrayVar(&opts.packages, "package", nil, "cover only this package: a name (stdlib) or a purl prefix (pkg:golang/stdlib); repeatable")
	cmd.Flags().StringArrayVar(&opts.artifacts, "artifact", nil, "cover only this release artifact; repeatable")
	cmd.Flags().StringArrayVar(&opts.locations, "location", nil, "cover only this absolute path inside the artifact (/usr/local/bin/grype), or every path under one ending in /; repeatable")
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

// validateVulnExemptScope refuses a scope the schema would refuse.
func validateVulnExemptScope(o vulnExemptOptions) error {
	for _, f := range []struct {
		flag string
		vals []string
	}{{"--package", o.packages}, {"--artifact", o.artifacts}, {"--location", o.locations}} {
		seen := map[string]bool{}
		for _, v := range f.vals {
			switch {
			case v == "" || v != strings.TrimSpace(v) || strings.ContainsAny(v, "\t\r\n"):
				return fmt.Errorf("%s %q must be one non-empty line, not padded with spaces", f.flag, v)
			case strings.Contains(v, "${"):
				return fmt.Errorf("%s may not contain `${` (KCL would read it as interpolation)", f.flag)
			case f.flag == "--location" && !strings.HasPrefix(v, "/"):
				return fmt.Errorf("--location %q must be an absolute path inside the artifact (/usr/local/bin/grype)", v)
			case seen[v]:
				return fmt.Errorf("%s %q is given twice", f.flag, v)
			}
			seen[v] = true
		}
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
	if err := validateVulnExemptScope(opts); err != nil {
		return err
	}
	ex := opts.exemption(id)
	scope := vulnExemptScopeLabel(ex)
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
		next, changed, err = exemptInKCLOf(string(orig), ex)
	case "yaml":
		next, changed, err = exemptInEnvDocYAMLOf(orig, ex)
	default:
		entry, _ := json.Marshal(ex)
		key := "exemptions"
		if ex.Scoped() {
			key = "scoped_exemptions"
		}
		err = &errVulnExemptNoAnchor{
			why:     fmt.Sprintf("%s is a JSON env document, which forge does not rewrite", relOrAbs(projectDir, path)),
			snippet: fmt.Sprintf("    \"delivery_policy\": {\"vuln_gate\": {%q: [%s]}}\n", key, entry),
		}
	}
	if err != nil {
		return fmt.Errorf("forge vuln exempt %s %s: %w", env, id, err)
	}
	rel := filepath.ToSlash(relOrAbs(projectDir, path)) // the same spelling on every OS
	if !changed {
		_, _ = fmt.Fprintf(out, "%s already exempts %s%s (expires %s); nothing changed\n", rel, id, scope, opts.expires)
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
	if rerr == nil && !gateExempts(gate, ex) {
		rerr = fmt.Errorf("the edit to %s does not reach env %s's rendered vuln_gate (its policy may be built in another file)", rel, env)
	}
	if rerr != nil {
		if werr := atomicfile.WriteFile(path, orig, info.Mode().Perm()); werr != nil {
			return fmt.Errorf("forge vuln exempt: %v; and restoring %s failed: %w", rerr, rel, werr)
		}
		return fmt.Errorf("forge vuln exempt %s %s: %v\n  %s is unchanged. Add the exemption where env %s declares its vuln_gate:\n      %s",
			env, id, rerr, rel, env, vulnExemptionLiteralOf("forge", ex))
	}
	_, _ = fmt.Fprintf(out, "exempted %s%s in env %s until %s: %s\n", id, scope, env, opts.expires, rel)
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

// gateExempts reports whether gate carries exactly this exemption: its
// identity (id and scope), reason and expiry.
func gateExempts(gate release.ShapeVulnGate, ex release.ShapeVulnScopedExempt) bool {
	all := append([]release.ShapeVulnScopedExempt{}, gate.ScopedExemptions...)
	for _, e := range gate.Exemptions {
		all = append(all, release.ShapeVulnScopedExempt{ID: e.ID, Reason: e.Reason, Expires: e.Expires})
	}
	for _, e := range all {
		if e.Identity() == ex.Identity() && strings.TrimSpace(e.Reason) == ex.Reason && e.Expires == ex.Expires {
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
	return exemptInEnvDocYAMLOf(src, release.ShapeVulnScopedExempt{ID: id, Reason: reason, Expires: expires})
}

// exemptInEnvDocYAMLOf is exemptInEnvDocYAML for an exemption that may be
// scoped: an entry with the same id and scope is the one replaced.
func exemptInEnvDocYAMLOf(src []byte, ex release.ShapeVulnScopedExempt) (string, bool, error) {
	id, reason, expires := ex.ID, ex.Reason, ex.Expires
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return "", false, err
	}
	scopeYAML := ""
	scopeLists := []struct {
		key  string
		vals []string
	}{{"packages", ex.Packages}, {"artifacts", ex.Artifacts}, {"locations", ex.Locations}}
	for _, f := range scopeLists {
		if len(f.vals) > 0 {
			q := make([]string, len(f.vals))
			for i, v := range f.vals {
				q[i] = fmt.Sprintf("%q", v)
			}
			scopeYAML += fmt.Sprintf("            %s: [%s]\n", f.key, strings.Join(q, ", "))
		}
	}
	snippetKey := "exemptions"
	if ex.Scoped() {
		snippetKey = "scoped_exemptions"
	}
	snippet := fmt.Sprintf("    delivery_policy:\n      vuln_gate:\n        %s:\n          - id: %s\n            reason: %q\n            expires: %q\n%s", snippetKey, id, reason, expires, scopeYAML)
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
	for _, f := range scopeLists {
		if len(f.vals) > 0 {
			seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, v := range f.vals {
				seq.Content = append(seq.Content, yamlStr(v))
			}
			entry.Content = append(entry.Content, yamlStr(f.key), seq)
		}
	}
	// yamlScopeKey is an entry's scope (vulnExemptScopeKey); "" and ok
	// false when a scope key is not a list of strings, which forge does
	// not rewrite.
	yamlScopeKey := func(e *yaml.Node) (string, bool) {
		lists := map[string][]string{}
		for _, f := range scopeLists {
			n := yamlChild(e, f.key)
			if n == nil {
				continue
			}
			if n.Kind != yaml.SequenceNode {
				return "", false
			}
			for _, v := range n.Content {
				if v.Kind != yaml.ScalarNode {
					return "", false
				}
				lists[f.key] = append(lists[f.key], v.Value)
			}
		}
		return vulnExemptScopeKey(lists["packages"], lists["artifacts"], lists["locations"]), true
	}
	want := vulnExemptScopeKey(ex.Packages, ex.Artifacts, ex.Locations)
	// The document is the render's form: a scoped exemption is an entry of
	// scoped_exemptions, an unscoped one of exemptions. The entry of the
	// same declaration is replaced in whichever list holds it.
	listKey := "exemptions"
	if ex.Scoped() {
		listKey = "scoped_exemptions"
	}
	for _, key := range []string{"exemptions", "scoped_exemptions"} {
		list := yamlChild(gate, key)
		if list == nil {
			continue
		}
		if list.Kind != yaml.SequenceNode {
			return "", false, &errVulnExemptNoAnchor{why: "delivery_policy.vuln_gate." + key + " is not a list", snippet: snippet}
		}
		for i, e := range list.Content {
			if n := yamlChild(e, "id"); n == nil || n.Value != id {
				continue
			}
			scope, ok := yamlScopeKey(e)
			if !ok {
				return "", false, &errVulnExemptNoAnchor{why: fmt.Sprintf("an exemption of %s in delivery_policy.vuln_gate.%s has a scope that is not a list of strings", id, key), snippet: snippet}
			}
			if scope != want {
				continue // another declaration of the advisory
			}
			if v := yamlChild(e, "reason"); v != nil && v.Value == reason {
				if x := yamlChild(e, "expires"); x != nil && x.Value == expires {
					return string(src), false, nil
				}
			}
			list.Content[i], listKey = entry, ""
		}
	}
	switch list := yamlChild(gate, listKey); {
	case listKey == "": // replaced in place
	case list == nil:
		gate.Content = append(gate.Content, yamlStr(listKey), &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{entry}})
	default:
		list.Content = append(list.Content, entry)
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
