package release

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func hardenedPolicy() *ShapeDeliveryPolicy {
	p := DefaultDeliveryPolicy()
	p.TrustedBuilder = ShapeTrustedBuilder{Mode: DeliveryEnforce, Builders: []string{"github:acme/api", "cloudbuild:acme/delivery"}}
	p.AdmissionVerify.Mode = DeliveryEnforce
	p.SignedPromotions.Mode = DeliveryEnforce
	p.VulnGate.Mode = DeliveryEnforce
	p.VulnGate.Exemptions = []ShapeVulnExempt{
		{ID: "GHSA-aaaa-bbbb-cccc", Reason: "unreachable", Expires: "2027-01-31"},
		{ID: "CVE-2026-0001", Reason: "fixed next base image", Expires: "2026-12-01"},
	}
	p.Approvals.Mode = DeliveryEnforce
	return &p
}

func TestDefaultDeliveryPolicyIsValidAndIsItsOwnDefault(t *testing.T) {
	d := DefaultDeliveryPolicy()
	if err := d.validate(); err != nil {
		t.Fatalf("the default policy must be valid: %v", err)
	}
	if !d.IsDefault() {
		t.Error("the default policy is not its own default")
	}
	var none *ShapeDeliveryPolicy
	if !none.IsDefault() || !reflect.DeepEqual(none.Resolved(), d) {
		t.Error("an absent policy must resolve to the defaults, not to off")
	}
}

// The hybrid rule, pinned: what cannot block a deploy enforces, what can audits.
func TestDefaultDeliveryPolicyFollowsTheHybridRule(t *testing.T) {
	d := DefaultDeliveryPolicy()
	for name, got := range map[string]DeliveryMode{
		"signing": d.Signing.Mode, "provenance_sbom": d.ProvenanceSBOM.Mode, "flux_verify": d.FluxVerify.Mode,
	} {
		if got != DeliveryEnforce {
			t.Errorf("%s defaults to %s, want enforce: it cannot block a deploy", name, got)
		}
	}
	for name, got := range map[string]DeliveryMode{
		"trusted_builder": d.TrustedBuilder.Mode, "admission_verify": d.AdmissionVerify.Mode,
		"signed_promotions": d.SignedPromotions.Mode, "vuln_gate": d.VulnGate.Mode,
		"bundle_kinds": d.BundleKinds.Mode, "approvals": d.Approvals.Mode,
	} {
		if got != DeliveryAudit {
			t.Errorf("%s defaults to %s, want audit: it can block a deploy", name, got)
		}
	}
	if d.Approvals.Required != 1 {
		t.Errorf("approvals default to %d people, want one-person", d.Approvals.Required)
	}
}

// A shape that declares no policy encodes without the field, so every shape
// recorded before it existed hashes and diffs exactly as it did.
func TestShape_DeliveryPolicyOmittedWhenAbsent(t *testing.T) {
	b, err := Shape{Kind: EnvLocal}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "delivery_policy") {
		t.Errorf("an absent policy must not be encoded: %s", b)
	}
}

// A policy that spells out every default is the same declaration as none, so
// it must encode and hash identically: a producer's choice to write the
// defaults down must not mint a new shape.
func TestShape_DeliveryPolicyOmittedWhenEveryValueIsTheDefault(t *testing.T) {
	d := DefaultDeliveryPolicy()
	// Spelled out, with the lists in a different shape (nil vs empty, unsorted
	// where it makes no difference).
	d.TrustedBuilder.Builders = nil
	d.BundleKinds.Allow = nil
	d.Approvals.Approvers = nil
	explicit, err := Shape{Kind: EnvSelfManaged, DeliveryPolicy: &d}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	absent, err := Shape{Kind: EnvSelfManaged}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(explicit) != string(absent) {
		t.Errorf("a default-valued policy changed the encoding:\n explicit %s\n absent   %s", explicit, absent)
	}
	if (Shape{Kind: EnvSelfManaged, DeliveryPolicy: &d}).Canonical().DeliveryPolicy != nil {
		t.Error("Canonical must drop a policy that is all defaults")
	}
}

// One value off the default is recorded, and ONLY that makes the difference.
func TestShape_DeliveryPolicyRecordedWhenAnyValueDiffers(t *testing.T) {
	for name, mut := range map[string]func(*ShapeDeliveryPolicy){
		"a mode":  func(p *ShapeDeliveryPolicy) { p.VulnGate.Mode = DeliveryEnforce },
		"a param": func(p *ShapeDeliveryPolicy) { p.VulnGate.BlockSeverity = "high" },
		"a list":  func(p *ShapeDeliveryPolicy) { p.TrustedBuilder.Builders = []string{"github:acme/api"} },
		"an exemption": func(p *ShapeDeliveryPolicy) {
			p.VulnGate.Exemptions = []ShapeVulnExempt{{ID: "CVE-2026-1", Reason: "r", Expires: "2026-12-01"}}
		},
		"signing model": func(p *ShapeDeliveryPolicy) { p.Signing.Key = SigningKeyCustomer; p.Signing.KeyRef = "gcpkms://p/k" },
	} {
		p := DefaultDeliveryPolicy()
		mut(&p)
		got := Shape{Kind: EnvSelfManaged, DeliveryPolicy: &p}.Canonical()
		if got.DeliveryPolicy == nil {
			t.Errorf("%s: a non-default policy was dropped", name)
		}
	}
}

func TestShape_DeliveryPolicyRoundTrip(t *testing.T) {
	in := Shape{Kind: EnvSelfManaged, DeliveryPolicy: hardenedPolicy()}
	b, err := in.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var out Shape
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if err := out.Validate(); err != nil {
		t.Fatalf("a round-tripped shape is invalid: %v", err)
	}
	if want := in.Canonical(); !reflect.DeepEqual(out.DeliveryPolicy, want.DeliveryPolicy) {
		t.Errorf("round trip changed the policy:\n got %+v\nwant %+v", out.DeliveryPolicy, want.DeliveryPolicy)
	}
	again, err := out.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(b) {
		t.Errorf("encode is not a fixed point:\n first  %s\n second %s", b, again)
	}
}

// The author's list order must never change the shape's bytes.
func TestShape_DeliveryPolicyIsCanonicalAcrossListOrder(t *testing.T) {
	a, b := hardenedPolicy(), hardenedPolicy()
	b.TrustedBuilder.Builders = []string{"cloudbuild:acme/delivery", "github:acme/api"}
	b.VulnGate.Exemptions = []ShapeVulnExempt{b.VulnGate.Exemptions[1], b.VulnGate.Exemptions[0]}
	ea, _ := Shape{Kind: EnvSelfManaged, DeliveryPolicy: a}.Encode()
	eb, _ := Shape{Kind: EnvSelfManaged, DeliveryPolicy: b}.Encode()
	if string(ea) != string(eb) {
		t.Errorf("list order changed the encoding:\n%s\n%s", ea, eb)
	}
}

// The wire form is a contract with the control plane: pin it.
func TestShape_DeliveryPolicyGolden(t *testing.T) {
	p := DefaultDeliveryPolicy()
	p.VulnGate.Mode = DeliveryEnforce
	p.Approvals = ShapeApprovals{Mode: DeliveryEnforce, Required: 1}
	b, err := Shape{Kind: EnvSelfManaged, DeliveryPolicy: &p}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		DeliveryPolicy json.RawMessage `json:"delivery_policy"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	const want = `{"trusted_builder":{"mode":"audit","builders":[]},` +
		`"signing":{"mode":"enforce","key":"org_kms"},` +
		`"provenance_sbom":{"mode":"enforce"},` +
		`"flux_verify":{"mode":"enforce"},` +
		`"admission_verify":{"mode":"audit"},` +
		`"signed_promotions":{"mode":"audit"},` +
		`"vuln_gate":{"mode":"enforce","block_severity":"critical","warn_severity":"high","block_kev":true,"fixable_only":true,"exemptions":[]},` +
		`"bundle_kinds":{"mode":"audit","allow":[],"deny":[]},` +
		`"approvals":{"mode":"enforce","required":1,"approvers":[]}}`
	if string(doc.DeliveryPolicy) != want {
		t.Errorf("delivery_policy wire form changed:\n got  %s\n want %s", doc.DeliveryPolicy, want)
	}
}

func TestShape_DeliveryPolicyRefusals(t *testing.T) {
	mut := func(f func(*ShapeDeliveryPolicy)) *ShapeDeliveryPolicy {
		p := DefaultDeliveryPolicy()
		f(&p)
		return &p
	}
	for name, tc := range map[string]struct {
		p    *ShapeDeliveryPolicy
		want string
	}{
		"unknown mode": {mut(func(p *ShapeDeliveryPolicy) { p.FluxVerify.Mode = "block" }), "flux_verify mode"},
		"empty mode":   {mut(func(p *ShapeDeliveryPolicy) { p.Approvals.Mode = "" }), "approvals mode"},
		"admission enforce, no sign": {mut(func(p *ShapeDeliveryPolicy) {
			p.Signing.Mode = DeliveryOff
			p.FluxVerify.Mode = DeliveryOff
			p.AdmissionVerify.Mode = DeliveryEnforce
		}), "admission_verify is enforce but signing is off"},
		"flux enforce, no sign": {mut(func(p *ShapeDeliveryPolicy) { p.Signing.Mode = DeliveryOff }), "flux_verify is enforce but signing is off"},
		"promotions enforce, no sign": {mut(func(p *ShapeDeliveryPolicy) {
			p.Signing.Mode = DeliveryOff
			p.FluxVerify.Mode = DeliveryOff
			p.SignedPromotions.Mode = DeliveryEnforce
		}), "signed_promotions is enforce but signing is off"},
		"builder enforce, no prov": {mut(func(p *ShapeDeliveryPolicy) {
			p.ProvenanceSBOM.Mode = DeliveryOff
			p.TrustedBuilder.Mode = DeliveryEnforce
		}), "trusted_builder is enforce but provenance_sbom is off"},
		"vuln enforce, no sbom":   {mut(func(p *ShapeDeliveryPolicy) { p.ProvenanceSBOM.Mode = DeliveryOff; p.VulnGate.Mode = DeliveryEnforce }), "vuln_gate is enforce but provenance_sbom is off"},
		"builder not an identity": {mut(func(p *ShapeDeliveryPolicy) { p.TrustedBuilder.Builders = []string{"my laptop"} }), "not a <kind>:<id>"},
		"builder twice":           {mut(func(p *ShapeDeliveryPolicy) { p.TrustedBuilder.Builders = []string{"github:a/b", "github:a/b"} }), "twice"},
		"unknown key model":       {mut(func(p *ShapeDeliveryPolicy) { p.Signing.Key = "hsm" }), "signing key"},
		"customer key, no ref":    {mut(func(p *ShapeDeliveryPolicy) { p.Signing.Key = SigningKeyCustomer }), "needs a key_ref"},
		"org key with ref":        {mut(func(p *ShapeDeliveryPolicy) { p.Signing.KeyRef = "gcpkms://p/k" }), "org_kms takes no key_ref"},
		"keyless, no identity":    {mut(func(p *ShapeDeliveryPolicy) { p.Signing.Key = SigningKeyKeyless }), "needs oidc_issuer and oidc_subject"},
		"keyless, http issuer": {mut(func(p *ShapeDeliveryPolicy) {
			p.Signing.Key, p.Signing.OIDCIssuer, p.Signing.OIDCSubject = SigningKeyKeyless, "http://x", ".*"
		}), "https://"},
		"unknown severity": {mut(func(p *ShapeDeliveryPolicy) { p.VulnGate.BlockSeverity = "severe" }), "severities"},
		"blocks nothing":   {mut(func(p *ShapeDeliveryPolicy) { p.VulnGate.BlockKEV, p.VulnGate.BlockSeverity = false, "none" }), "blocks nothing"},
		"exemption not an id": {mut(func(p *ShapeDeliveryPolicy) {
			p.VulnGate.Exemptions = []ShapeVulnExempt{{ID: "x", Reason: "r", Expires: "2026-12-01"}}
		}), "not an advisory ID"},
		"exemption no reason": {mut(func(p *ShapeDeliveryPolicy) {
			p.VulnGate.Exemptions = []ShapeVulnExempt{{ID: "CVE-2026-1", Reason: " ", Expires: "2026-12-01"}}
		}), "no reason"},
		"exemption open ended": {mut(func(p *ShapeDeliveryPolicy) {
			p.VulnGate.Exemptions = []ShapeVulnExempt{{ID: "CVE-2026-1", Reason: "r"}}
		}), "time-boxed"},
		"exemption twice": {mut(func(p *ShapeDeliveryPolicy) {
			p.VulnGate.Exemptions = []ShapeVulnExempt{{ID: "CVE-2026-1", Reason: "r", Expires: "2026-12-01"}, {ID: "CVE-2026-1", Reason: "r", Expires: "2026-12-02"}}
		}), "twice"},
		"kind not a kind": {mut(func(p *ShapeDeliveryPolicy) { p.BundleKinds.Allow = []string{"deployments"} }), "not a kind"},
		"kind allowed and denied": {mut(func(p *ShapeDeliveryPolicy) {
			p.BundleKinds.Allow, p.BundleKinds.Deny = []string{"Pod"}, []string{"Pod"}
		}), "both allow and deny"},
		"required zero":            {mut(func(p *ShapeDeliveryPolicy) { p.Approvals.Required = 0 }), "out of range"},
		"required six":             {mut(func(p *ShapeDeliveryPolicy) { p.Approvals.Required = 6 }), "out of range"},
		"two-person, no approvers": {mut(func(p *ShapeDeliveryPolicy) { p.Approvals.Required = 2 }), "declares no approvers"},
		"two-person, one approver": {mut(func(p *ShapeDeliveryPolicy) { p.Approvals.Required, p.Approvals.Approvers = 2, []string{"ana"} }), "only 1 approver"},
		"two-person, off": {mut(func(p *ShapeDeliveryPolicy) {
			p.Approvals.Required, p.Approvals.Mode, p.Approvals.Approvers = 2, DeliveryOff, []string{"ana", "bo"}
		}), "off asks nobody"},
		"approver twice":        {mut(func(p *ShapeDeliveryPolicy) { p.Approvals.Required, p.Approvals.Approvers = 2, []string{"ana", "ana"} }), "twice"},
		"approver with a space": {mut(func(p *ShapeDeliveryPolicy) { p.Approvals.Approvers = []string{"ana b"} }), "approver"},
	} {
		err := Shape{Kind: EnvLocal, DeliveryPolicy: tc.p}.Canonical().Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want ErrInvalid containing %q", name, err, tc.want)
		}
	}
}

// Audit over a missing prerequisite is NOT refused: it records "unverifiable",
// it cannot make a deploy impossible. Only enforce is.
func TestShape_DeliveryPolicyAuditOverMissingPrerequisiteIsAllowed(t *testing.T) {
	p := DefaultDeliveryPolicy()
	p.Signing.Mode = DeliveryOff
	p.FluxVerify.Mode = DeliveryOff
	p.ProvenanceSBOM.Mode = DeliveryOff
	// admission, signed_promotions, trusted_builder and vuln_gate stay audit.
	if err := (Shape{Kind: EnvLocal, DeliveryPolicy: &p}.Canonical().Validate()); err != nil {
		t.Fatalf("audit over a missing prerequisite must be valid: %v", err)
	}
}

func TestShape_DeliveryPolicyAllOffIsValid(t *testing.T) {
	p := DefaultDeliveryPolicy()
	p.TrustedBuilder.Mode, p.Signing.Mode, p.ProvenanceSBOM.Mode = DeliveryOff, DeliveryOff, DeliveryOff
	p.FluxVerify.Mode, p.AdmissionVerify.Mode, p.SignedPromotions.Mode = DeliveryOff, DeliveryOff, DeliveryOff
	p.VulnGate.Mode, p.BundleKinds.Mode, p.Approvals.Mode = DeliveryOff, DeliveryOff, DeliveryOff
	if err := (Shape{Kind: EnvLocal, DeliveryPolicy: &p}.Canonical().Validate()); err != nil {
		t.Fatalf("a customer may turn every control off: %v", err)
	}
}
