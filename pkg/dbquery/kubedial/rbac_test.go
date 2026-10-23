package kubedial

import (
	"reflect"
	"testing"
)

func TestPolicyRules(t *testing.T) {
	rules := PolicyRules("ledger", "billing", "ledger")
	if len(rules) != 3 {
		t.Fatalf("%d rules: %+v", len(rules), rules)
	}
	pf, es, sec := rules[0], rules[1], rules[2]
	if pf.Resources[0] != "pods/portforward" || !reflect.DeepEqual(pf.Verbs, []string{"get", "create"}) || pf.APIGroups[0] != "" {
		t.Errorf("portforward rule %+v", pf)
	}
	if es.APIGroups[0] != "discovery.k8s.io" || es.Resources[0] != "endpointslices" || !reflect.DeepEqual(es.Verbs, []string{"get", "list"}) {
		t.Errorf("endpointslices rule %+v", es)
	}
	wantNames := []string{"billing-ca", "billing-forge-ro", "ledger-ca", "ledger-forge-ro"}
	if sec.Resources[0] != "secrets" || !reflect.DeepEqual(sec.Verbs, []string{"get"}) || !reflect.DeepEqual(sec.ResourceNames, wantNames) {
		t.Errorf("secrets rule %+v, want names %v", sec, wantNames)
	}
}

// A secrets rule with no resourceNames grants every Secret, so no names means
// no rule at all.
func TestPolicyRulesNeverGrantsAllSecrets(t *testing.T) {
	for _, r := range PolicyRules() {
		if r.Resources[0] == "secrets" {
			t.Errorf("secrets rule without names: %+v", r)
		}
	}
}
