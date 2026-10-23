package release

import (
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// DeliveryMode is how one delivery control behaves.
type DeliveryMode string

const (
	// DeliveryOff does nothing.
	DeliveryOff DeliveryMode = "off"
	// DeliveryAudit records and alerts, and never blocks.
	DeliveryAudit DeliveryMode = "audit"
	// DeliveryEnforce blocks.
	DeliveryEnforce DeliveryMode = "enforce"
)

// Valid reports whether m is one of the three modes.
func (m DeliveryMode) Valid() bool {
	return m == DeliveryOff || m == DeliveryAudit || m == DeliveryEnforce
}

// Signing key models ([ShapeSigning.Key]).
const (
	SigningKeyOrgKMS   = "org_kms"
	SigningKeyCustomer = "customer"
	SigningKeyKeyless  = "keyless"
)

// Vulnerability severities, weakest first ([ShapeVulnGate.BlockSeverity]).
// "none" switches that band off.
var vulnSeverities = []string{"none", "low", "medium", "high", "critical"}

// ShapeDeliveryPolicy is what an env's releases must prove before they ship,
// one control per hardening control of the delivery design, each with a mode
// and its parameters. It is how the control plane learns what to enforce:
// recorded on every ensure, enforced at record (builder, signature,
// provenance, SBOM, vulnerabilities, kinds), at promote (approvals, the
// promotion's signature), at the converger (Flux verification) and at
// admission (pod images).
//
// Isolation controls between bindings (per-binding identity, kubeconfig-ref binding,
// narrowed impersonation, ledger and pointer integrity, ingest validation,
// head-of-line fairness, the hub's PSA and NetworkPolicy baseline) are NOT
// here: they are not an env's choice.
//
// Shape.DeliveryPolicy is nil when every value is the default
// ([DefaultDeliveryPolicy]), so every shape recorded before the field encodes
// and hashes as it did. A reader treats nil as the defaults, never as "off".
type ShapeDeliveryPolicy struct {
	TrustedBuilder   ShapeTrustedBuilder `json:"trusted_builder"`
	Signing          ShapeSigning        `json:"signing"`
	ProvenanceSBOM   ShapeControl        `json:"provenance_sbom"`
	FluxVerify       ShapeControl        `json:"flux_verify"`
	AdmissionVerify  ShapeControl        `json:"admission_verify"`
	SignedPromotions ShapeControl        `json:"signed_promotions"`
	VulnGate         ShapeVulnGate       `json:"vuln_gate"`
	BundleKinds      ShapeBundleKinds    `json:"bundle_kinds"`
	Approvals        ShapeApprovals      `json:"approvals"`
}

// ShapeControl is a control with no parameters beyond its mode.
type ShapeControl struct {
	Mode DeliveryMode `json:"mode"`
}

// ShapeTrustedBuilder: only artifacts built by the platform's builder for the
// org, or by one of Builders (`<kind>:<id>`), are eligible.
type ShapeTrustedBuilder struct {
	Mode     DeliveryMode `json:"mode"`
	Builders []string     `json:"builders"`
}

// ShapeSigning: images and the bundle are signed. Key is the key model;
// KeyRef is a customer key's PUBLIC reference (never key material);
// OIDCIssuer and OIDCSubject name the identity a keyless signature must come
// from.
type ShapeSigning struct {
	Mode        DeliveryMode `json:"mode"`
	Key         string       `json:"key"`
	KeyRef      string       `json:"key_ref,omitempty"`
	OIDCIssuer  string       `json:"oidc_issuer,omitempty"`
	OIDCSubject string       `json:"oidc_subject,omitempty"`
}

// ShapeVulnGate: a finding blocks when it is KEV-listed (BlockKEV), or when
// its severity is at or above BlockSeverity and, with FixableOnly, a fixed
// version exists. A non-blocking finding at or above WarnSeverity is warned.
type ShapeVulnGate struct {
	Mode          DeliveryMode      `json:"mode"`
	BlockSeverity string            `json:"block_severity"`
	WarnSeverity  string            `json:"warn_severity"`
	BlockKEV      bool              `json:"block_kev"`
	FixableOnly   bool              `json:"fixable_only"`
	Exemptions    []ShapeVulnExempt `json:"exemptions"`
}

// ShapeVulnExempt is one accepted advisory: time-boxed, with its reason.
type ShapeVulnExempt struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
}

// ShapeBundleKinds narrows, never widens, the mandatory ingest baseline:
// Allow non-empty means only those kinds; Deny refuses these. `Kind` or
// `group/Kind`.
type ShapeBundleKinds struct {
	Mode  DeliveryMode `json:"mode"`
	Allow []string     `json:"allow"`
	Deny  []string     `json:"deny"`
}

// ShapeApprovals: Required distinct people must approve a promotion, the
// requester included. 1 is one-person approval. Approvers are the org members
// who may approve; more than one required needs at least that many.
type ShapeApprovals struct {
	Mode      DeliveryMode `json:"mode"`
	Required  int          `json:"required"`
	Approvers []string     `json:"approvers"`
}

// DefaultDeliveryPolicy is every control at its default: the hybrid rule of
// the delivery design. A control that cannot block a deploy and costs the
// customer nothing is enforced (signing and provenance/SBOM are done by forge
// or the platform builder on the customer's behalf, and Flux verification
// only checks what that signing produced). A control that can refuse a deploy
// is audited until the env turns it up.
//
// It must agree with the defaults of forge.DeliveryPolicy in kcl/schema.k;
// TestDeliveryPolicyDefaultsAgreeWithKCL fails if they drift.
func DefaultDeliveryPolicy() ShapeDeliveryPolicy {
	return ShapeDeliveryPolicy{
		TrustedBuilder:   ShapeTrustedBuilder{Mode: DeliveryAudit, Builders: []string{}},
		Signing:          ShapeSigning{Mode: DeliveryEnforce, Key: SigningKeyOrgKMS},
		ProvenanceSBOM:   ShapeControl{Mode: DeliveryEnforce},
		FluxVerify:       ShapeControl{Mode: DeliveryEnforce},
		AdmissionVerify:  ShapeControl{Mode: DeliveryAudit},
		SignedPromotions: ShapeControl{Mode: DeliveryAudit},
		VulnGate: ShapeVulnGate{
			Mode: DeliveryAudit, BlockSeverity: "critical", WarnSeverity: "high",
			BlockKEV: true, FixableOnly: true, Exemptions: []ShapeVulnExempt{},
		},
		BundleKinds: ShapeBundleKinds{Mode: DeliveryAudit, Allow: []string{}, Deny: []string{}},
		Approvals:   ShapeApprovals{Mode: DeliveryAudit, Required: 1, Approvers: []string{}},
	}
}

// IsDefault reports whether p, once canonical, is [DefaultDeliveryPolicy]. A
// nil policy is the default.
func (p *ShapeDeliveryPolicy) IsDefault() bool {
	if p == nil {
		return true
	}
	return reflect.DeepEqual(p.canonical(), DefaultDeliveryPolicy())
}

// Resolved returns the policy in force: the defaults for nil.
func (p *ShapeDeliveryPolicy) Resolved() ShapeDeliveryPolicy {
	if p == nil {
		return DefaultDeliveryPolicy()
	}
	return p.canonical()
}

// canonical sorts every list and encodes an empty one as [] rather than null,
// so two projections of the same declaration encode to the same bytes.
func (p ShapeDeliveryPolicy) canonical() ShapeDeliveryPolicy {
	out := p
	out.TrustedBuilder.Builders = sortedNonNil(p.TrustedBuilder.Builders)
	out.BundleKinds.Allow = sortedNonNil(p.BundleKinds.Allow)
	out.BundleKinds.Deny = sortedNonNil(p.BundleKinds.Deny)
	out.Approvals.Approvers = sortedNonNil(p.Approvals.Approvers)
	ex := append([]ShapeVulnExempt{}, p.VulnGate.Exemptions...)
	sort.Slice(ex, func(i, j int) bool { return ex[i].ID < ex[j].ID })
	out.VulnGate.Exemptions = ex
	return out
}

func sortedNonNil(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

var (
	deliveryAdvisoryRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[A-Za-z0-9-]+$`)
	deliveryDateRE     = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$`)
	deliveryBuilderRE  = regexp.MustCompile(`^[a-z][a-z0-9-]*:[^\s:]\S*$`)
	deliveryKindRE     = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*/)?[A-Z][A-Za-z0-9]*$`)
)

func vulnRank(s string) int {
	for i, v := range vulnSeverities {
		if v == s {
			return i
		}
	}
	return -1
}

// validate refuses a policy no env could satisfy, or one this version cannot
// read. The same rules as forge.DeliveryPolicy's checks in kcl/schema.k, so a
// shape written by another producer is held to them too.
func (p ShapeDeliveryPolicy) validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: shape delivery_policy: %s", ErrInvalid, fmt.Sprintf(format, a...))
	}
	type named struct {
		name string
		mode DeliveryMode
	}
	for _, c := range []named{
		{"trusted_builder", p.TrustedBuilder.Mode}, {"signing", p.Signing.Mode},
		{"provenance_sbom", p.ProvenanceSBOM.Mode}, {"flux_verify", p.FluxVerify.Mode},
		{"admission_verify", p.AdmissionVerify.Mode}, {"signed_promotions", p.SignedPromotions.Mode},
		{"vuln_gate", p.VulnGate.Mode}, {"bundle_kinds", p.BundleKinds.Mode}, {"approvals", p.Approvals.Mode},
	} {
		if !c.mode.Valid() {
			return bad("%s mode %q (expected off, audit or enforce)", c.name, c.mode)
		}
	}

	// An ENFORCING control cannot be satisfied when what it checks is never
	// produced. Audit over a missing prerequisite only records "unverifiable".
	if p.Signing.Mode == DeliveryOff {
		for _, c := range []named{
			{"flux_verify", p.FluxVerify.Mode}, {"admission_verify", p.AdmissionVerify.Mode},
			{"signed_promotions", p.SignedPromotions.Mode},
		} {
			if c.mode == DeliveryEnforce {
				return bad("%s is enforce but signing is off: nothing would carry a signature, so nothing could pass", c.name)
			}
		}
	}
	if p.ProvenanceSBOM.Mode == DeliveryOff {
		if p.TrustedBuilder.Mode == DeliveryEnforce {
			return bad("trusted_builder is enforce but provenance_sbom is off: the builder is read from the provenance")
		}
		if p.VulnGate.Mode == DeliveryEnforce {
			return bad("vuln_gate is enforce but provenance_sbom is off: the gate scans the SBOM")
		}
	}

	seen := map[string]bool{}
	for _, b := range p.TrustedBuilder.Builders {
		if !deliveryBuilderRE.MatchString(b) {
			return bad("trusted_builder names %q, which is not a <kind>:<id> identity", b)
		}
		if seen[b] {
			return bad("trusted_builder names %q twice", b)
		}
		seen[b] = true
	}

	s := p.Signing
	switch s.Key {
	case SigningKeyOrgKMS:
		if s.KeyRef != "" || s.OIDCIssuer != "" || s.OIDCSubject != "" {
			return bad("signing key org_kms takes no key_ref or oidc identity")
		}
	case SigningKeyCustomer:
		if s.KeyRef == "" || !strings.Contains(s.KeyRef, "://") || strings.ContainsAny(s.KeyRef, " \t\n") {
			return bad("signing key customer needs a key_ref of the form <scheme>://<path>")
		}
		if s.OIDCIssuer != "" || s.OIDCSubject != "" {
			return bad("signing key customer takes no oidc identity")
		}
	case SigningKeyKeyless:
		if s.OIDCIssuer == "" || s.OIDCSubject == "" {
			return bad("signing key keyless needs oidc_issuer and oidc_subject")
		}
		if !strings.HasPrefix(s.OIDCIssuer, "https://") {
			return bad("signing oidc_issuer %q must be an https:// URL", s.OIDCIssuer)
		}
		if s.KeyRef != "" {
			return bad("signing key keyless takes no key_ref")
		}
	default:
		return bad("signing key %q (expected %s, %s or %s)", s.Key, SigningKeyOrgKMS, SigningKeyCustomer, SigningKeyKeyless)
	}

	v := p.VulnGate
	if vulnRank(v.BlockSeverity) < 0 || vulnRank(v.WarnSeverity) < 0 {
		return bad("vuln_gate severities %q / %q (expected one of %s)", v.BlockSeverity, v.WarnSeverity, strings.Join(vulnSeverities, ", "))
	}
	if v.Mode != DeliveryOff && !v.BlockKEV && v.BlockSeverity == "none" {
		return bad("vuln_gate is %s but blocks nothing (block_kev false, block_severity none)", v.Mode)
	}
	ids := map[string]bool{}
	for _, e := range v.Exemptions {
		switch {
		case !deliveryAdvisoryRE.MatchString(e.ID):
			return bad("vuln_gate exemption %q is not an advisory ID", e.ID)
		case strings.TrimSpace(e.Reason) == "":
			return bad("vuln_gate exemption %s has no reason", e.ID)
		case !deliveryDateRE.MatchString(e.Expires):
			return bad("vuln_gate exemption %s expires %q (expected YYYY-MM-DD): an exemption is time-boxed", e.ID, e.Expires)
		case ids[e.ID]:
			return bad("vuln_gate exempts %s twice", e.ID)
		}
		ids[e.ID] = true
	}

	deny := map[string]bool{}
	for _, k := range p.BundleKinds.Deny {
		if !deliveryKindRE.MatchString(k) {
			return bad("bundle_kinds names %q, which is not a kind", k)
		}
		deny[k] = true
	}
	for _, k := range p.BundleKinds.Allow {
		if !deliveryKindRE.MatchString(k) {
			return bad("bundle_kinds names %q, which is not a kind", k)
		}
		if deny[k] {
			return bad("bundle_kinds has %q in both allow and deny", k)
		}
	}

	a := p.Approvals
	if a.Required < 1 || a.Required > 5 {
		return bad("approvals required %d is out of range (1 to 5)", a.Required)
	}
	if a.Required > 1 {
		if a.Mode == DeliveryOff {
			return bad("approvals requires %d people but is off: off asks nobody", a.Required)
		}
		if len(a.Approvers) == 0 {
			return bad("approvals requires %d people but declares no approvers", a.Required)
		}
		if len(a.Approvers) < a.Required {
			return bad("approvals requires %d people but declares only %d approver(s)", a.Required, len(a.Approvers))
		}
	}
	people := map[string]bool{}
	for _, who := range a.Approvers {
		if strings.TrimSpace(who) == "" || strings.ContainsAny(who, " \t\n") {
			return bad("approvals names an approver %q (expected a non-empty identifier with no spaces)", who)
		}
		if people[who] {
			return bad("approvals names %q twice", who)
		}
		people[who] = true
	}
	return nil
}
