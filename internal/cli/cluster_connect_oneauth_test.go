package cli

import (
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

// `connect` has ONE auth: an uploaded, scoped ServiceAccount token. The
// flags that selected between clouds are gone, not hidden.
func TestConnectFlags_OneAuthAndAnAddressOverride(t *testing.T) {
	flags := newClusterConnectCmd().Flags()
	for _, gone := range []string{"auth", "binding-gsa"} {
		if flags.Lookup(gone) != nil {
			t.Errorf("--%s still exists; there is no workload-identity path to select", gone)
		}
	}
	for _, want := range []string{"address", "ca-file"} {
		if flags.Lookup(want) == nil {
			t.Errorf("--%s does not exist", want)
		}
	}
	// A removed flag is an UNKNOWN flag, not a silently ignored one.
	cmd := newClusterConnectCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"c", "--auth", "token", "--env", "prod", "--context", "x"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --auth") {
		t.Errorf("--auth: err = %v, want unknown flag", err)
	}
	cmd = newClusterConnectCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs([]string{"c", "--binding-gsa", "x@y", "--env", "prod", "--context", "x"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "unknown flag: --binding-gsa") {
		t.Errorf("--binding-gsa: err = %v, want unknown flag", err)
	}
}

// Ports: 443 and 6443 (D4: k3s and kubeadm serve the API on 6443). Anything
// else is refused here, with the same rule the control plane applies, so the
// failure is local and names the fix.
func TestRefuseUnusableAddress_PortsAndUserinfo(t *testing.T) {
	for _, tc := range []struct {
		address string
		wantErr string
	}{
		{address: "https://203.0.113.10"},
		{address: "https://203.0.113.10:443"},
		{address: "https://203.0.113.10:6443"},
		{address: "https://api.example.com:6443"},
		{address: "https://203.0.113.10:8443", wantErr: "port 8443"},
		{address: "https://203.0.113.10:80", wantErr: "port 80"},
		{address: "https://api.example.com:16443", wantErr: "port 16443"},
		{address: "https://user:pw@203.0.113.10", wantErr: "credentials"},
	} {
		t.Run(tc.address, func(t *testing.T) {
			err := refuseUnusableAddress(tc.address)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("refuseUnusableAddress(%q) = %v, want nil", tc.address, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("refuseUnusableAddress(%q) = %v, want one containing %q", tc.address, err, tc.wantErr)
			}
		})
	}
}

const docRole = "apiVersion: rbac.authorization.k8s.io/v1\nkind: Role\nmetadata:\n  name: acme-editor\n  namespace: acme-prod\nrules: []\n"
const docRoleBindingToRole = "apiVersion: rbac.authorization.k8s.io/v1\nkind: RoleBinding\nmetadata:\n  name: acme-editor\n  namespace: acme-prod\n" +
	"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: Role\n  name: acme-editor\nsubjects: []\n"
const docClusterRoleBinding = "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRoleBinding\nmetadata:\n  name: acme-reader\n" +
	"roleRef:\n  apiGroup: rbac.authorization.k8s.io\n  kind: ClusterRole\n  name: view\nsubjects: []\n"

// decodeGrantRules returns every rule of every Role and ClusterRole connect
// would apply, decoded from the rendered YAML (not from the planner's structs).
func decodeGrantRules(t *testing.T, manifests string) []rbacv1.PolicyRule {
	t.Helper()
	var rules []rbacv1.PolicyRule
	for _, doc := range splitDocs(manifests) {
		var meta struct{ Kind string }
		if err := yaml.Unmarshal([]byte(doc), &meta); err != nil {
			t.Fatal(err)
		}
		switch meta.Kind {
		case "Role":
			var v rbacv1.Role
			if err := yaml.UnmarshalStrict([]byte(doc), &v); err != nil {
				t.Fatalf("%v\n%s", err, doc)
			}
			rules = append(rules, v.Rules...)
		case "ClusterRole":
			var v rbacv1.ClusterRole
			if err := yaml.UnmarshalStrict([]byte(doc), &v); err != nil {
				t.Fatalf("%v\n%s", err, doc)
			}
			rules = append(rules, v.Rules...)
		}
	}
	return rules
}

// THE RULE: an identity that holds `escalate` or `bind` without resourceNames
// can grant itself anything, whatever else the grant says. They are only ever
// pinned to the exact roles the render names, in either scope.
func TestGrant_BindAndEscalateAreAlwaysPinnedByResourceNames(t *testing.T) {
	renders := map[string][]string{
		"role and binding":             {onCtx(docRole), onCtx(docRoleBindingToRole)},
		"clusterrole and rolebinding":  {onCtx(docClusterRole), onCtx(docRoleBinding)},
		"clusterrolebinding to a role": {onCtx(docClusterRole), onCtx(docClusterRoleBinding)},
		"all of them": {onCtx(docRole), onCtx(docRoleBindingToRole), onCtx(docClusterRole),
			onCtx(docRoleBinding), onCtx(docClusterRoleBinding)},
	}
	for name, extra := range renders {
		for _, scope := range []applyScope{{Cluster: true}, {}} {
			if !scope.Cluster && strings.Contains(strings.Join(extra, ""), "kind: ClusterRole\n") {
				continue // namespace scope refuses cluster-scoped kinds; covered elsewhere
			}
			t.Run(name+"/"+scope.String(), func(t *testing.T) {
				r := testRBAC(t, scope, extra...)
				var escalating int
				for _, rule := range decodeGrantRules(t, r.bootstrapManifests()) {
					for _, v := range rule.Verbs {
						if v == "bind" || v == "escalate" || v == "*" {
							escalating++
							if len(rule.ResourceNames) == 0 {
								t.Errorf("rule %v %v holds %q with no resourceNames", rule.Resources, rule.Verbs, v)
							}
						}
						if v == "impersonate" {
							t.Errorf("rule %v impersonates", rule.Resources)
						}
					}
				}
				if escalating == 0 && name != "clusterrolebinding to a role" {
					t.Errorf("a render carrying RBAC produced no pinned bind/escalate rule at all; the apply would be Forbidden")
				}
			})
		}
	}
}
