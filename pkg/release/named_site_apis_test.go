package release

import (
	"slices"
	"strings"
	"testing"
)

// A site with named same-origin APIs is refused to a control plane that does
// not serve them, never stripped: its /_api/@<name> calls would 404.
func TestBundleReader_CheckNamedSiteAPIs(t *testing.T) {
	sites := []string{"web"}
	for name, r := range map[string]*BundleReader{
		"machine ledger (nil reader)": nil,
		"serves them":                 {Features: []string{CapabilityServesNamedSiteAPIs}},
	} {
		if err := r.CheckNamedSiteAPIs(sites); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, r := range map[string]*BundleReader{
		"predates it":                      {},
		"a pin bump: every decode feature": {Features: BundleDocFeatures()},
	} {
		err := r.CheckNamedSiteAPIs(sites)
		if err == nil || !strings.Contains(err.Error(), `"serves-named-site-apis"`) || !strings.Contains(err.Error(), "web") {
			t.Errorf("%s: CheckNamedSiteAPIs = %v, want a refusal naming the site and the capability", name, err)
		}
	}
	if err := (&BundleReader{}).CheckNamedSiteAPIs(nil); err != nil {
		t.Errorf("no site declares named APIs, yet refused: %v", err)
	}
	if CapabilityServesNamedSiteAPIs != "serves-named-site-apis" {
		t.Errorf("CapabilityServesNamedSiteAPIs = %q; the wire string is frozen", CapabilityServesNamedSiteAPIs)
	}
	// Serving is added by hand, never claimed by a pin bump.
	if slices.Contains(BundleDocFeatures(), CapabilityServesNamedSiteAPIs) {
		t.Errorf("BundleDocFeatures claims %s", CapabilityServesNamedSiteAPIs)
	}
}
