package bundle

import (
	"bytes"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

func deliveryPolicyBuild(t *testing.T, p *release.ShapeDeliveryPolicy) Bundle {
	t.Helper()
	in := buildFixture()
	in.Shape.DeliveryPolicy = p
	return mustBuild(t, in)
}

// A bundle built with no policy, and one built with a policy that spells out
// every default, are the same bundle: the digest is the identity, so writing
// the defaults down must not mint a release.
func TestDeliveryPolicyAllDefaultsLeavesTheBundleDigestAlone(t *testing.T) {
	def := release.DefaultDeliveryPolicy()
	plain := deliveryPolicyBuild(t, nil)
	if got := deliveryPolicyBuild(t, &def); got.Digest != plain.Digest {
		t.Errorf("a default-valued policy changed the bundle digest:\n plain %s\n with  %s", plain.Digest, got.Digest)
	}
}

// And a real policy does change the digest and reaches the recorded shape, so
// the control plane reads what the env declared.
func TestDeliveryPolicyIsSealedInTheBundleShape(t *testing.T) {
	p := release.DefaultDeliveryPolicy()
	p.VulnGate.Mode = release.DeliveryEnforce
	plain := deliveryPolicyBuild(t, nil)
	built := deliveryPolicyBuild(t, &p)
	if built.Digest == plain.Digest {
		t.Error("a non-default policy did not change the bundle digest")
	}
	cfg := built.ConfigBlob()
	if len(cfg) == 0 {
		t.Fatal("no config blob")
	}
	if !bytes.Contains(cfg, []byte(`"delivery_policy"`)) || !bytes.Contains(cfg, []byte(`"vuln_gate":{"mode":"enforce"`)) {
		t.Errorf("the bundle's recorded shape lacks the policy: %s", cfg)
	}
	if bytes.Contains(plain.ConfigBlob(), []byte("delivery_policy")) {
		t.Error("a bundle with no policy recorded one")
	}
}

func TestProjectShapeCarriesTheDeliveryPolicy(t *testing.T) {
	p := release.DefaultDeliveryPolicy()
	p.Approvals.Mode = release.DeliveryEnforce
	in := fixtureInput()
	in.DeliveryPolicy = &p
	if got := projectFixture(t, in).DeliveryPolicy; got == nil || got.Approvals.Mode != release.DeliveryEnforce {
		t.Errorf("ProjectShape dropped the policy: %+v", got)
	}
	in.DeliveryPolicy = nil
	if got := projectFixture(t, in).DeliveryPolicy; got != nil {
		t.Errorf("ProjectShape invented a policy: %+v", got)
	}
	bad := release.DefaultDeliveryPolicy()
	bad.Approvals.Required = 2
	in.DeliveryPolicy = &bad
	if _, err := ProjectShape(in); err == nil {
		t.Error("ProjectShape accepted a two-person rule with no approvers")
	}
}
