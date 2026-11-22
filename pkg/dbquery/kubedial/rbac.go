package kubedial

import (
	"sort"

	rbacv1 "k8s.io/api/rbac/v1"
)

// PolicyRules are the namespaced Kubernetes Role rules an identity needs to use this
// package against the named databases (CNPG Cluster names) in one namespace.
// Put them in a Role (or a ClusterRole bound by RoleBinding) in each database
// namespace; declare them from here so the grant and the code that needs it
// cannot drift.
//
//	pods/portforward  get, create  the websocket upgrade is a GET and, since
//	                               Kubernetes 1.35, also checks create; SPDY is a POST
//	endpointslices    get, list    discovery.k8s.io: find the ready pods
//	secrets           get          only <db>-forge-ro and <db>-ca, by name
//	                               (<db>-forge-dash for [PolicyRulesFor] [RoleDashboard])
//
// pods/portforward is not restrictable by port or pod: whoever holds it can
// reach any port of any pod in the namespace. Bind these rules to an identity
// that only runs database queries.
//
// With no names the secrets rule is omitted (a rule with an empty
// resourceNames would grant every Secret): an identity that gets no secret
// access cannot call [Client.Credentials].
func PolicyRules(databases ...string) []rbacv1.PolicyRule {
	return PolicyRulesFor(RoleReadOnly, databases...)
}

// PolicyRulesFor is [PolicyRules] for an identity that connects as role: the
// secrets rule names that role's Secret (<db>-forge-ro or <db>-forge-dash)
// and the CA, never the other role's. An identity that runs dashboard panels
// therefore cannot read forge_ro's password, and one that runs `forge db
// query` cannot read forge_dash's. Give an identity that needs both the union
// of the two calls' rules.
func PolicyRulesFor(role Role, databases ...string) []rbacv1.PolicyRule {
	secretName := role.spec().secretName
	rules := []rbacv1.PolicyRule{
		{
			APIGroups: []string{""},
			Resources: []string{"pods/portforward"},
			Verbs:     []string{"get", "create"},
		},
		{
			APIGroups: []string{"discovery.k8s.io"},
			Resources: []string{"endpointslices"},
			Verbs:     []string{"get", "list"},
		},
	}
	if len(databases) == 0 {
		return rules
	}
	seen := map[string]bool{}
	var names []string
	for _, db := range databases {
		for _, n := range []string{secretName(db), CASecretName(db)} {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	return append(rules, rbacv1.PolicyRule{
		APIGroups:     []string{""},
		Resources:     []string{"secrets"},
		Verbs:         []string{"get"},
		ResourceNames: names,
	})
}
