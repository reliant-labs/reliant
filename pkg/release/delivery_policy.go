package release

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
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
	// HealthGate is the promotion health gate (forge.HealthGate). Nil is its
	// default — mode enforce, no environments named, so no gate — and is how
	// a policy that declares none encodes, so every shape recorded before
	// the field still hashes as it did. A reader that predates it never sees
	// it: it is the bundle feature [BundleFeatureHealthGate].
	HealthGate *ShapeHealthGate `json:"health_gate,omitempty"`
	// RequiredGates is forge.RequiredGates: the checks a promotion to this env
	// must carry as passed. Nil is its default — enforce, naming nothing, so
	// no requirement — and how a policy that declares none encodes. A reader
	// that predates it never sees it: it is the bundle feature
	// [BundleFeaturePromotionChecks].
	RequiredGates *ShapeRequiredGates `json:"required_gates,omitempty"`
	// ApprovalHook is forge.ApprovalHook: the team's own approval system,
	// asked approve / deny / pending before a promotion to this env is
	// written. Nil is no hook. Bundle feature [BundleFeaturePromotionChecks].
	ApprovalHook *ShapeApprovalHook `json:"approval_hook,omitempty"`
}

// ShapeRequiredGates is the required-gates control: a promotion to this env is
// admitted only when it carries a gate of every name in Names, each with
// status passed (`forge env deploy --gate name=e2e,status=passed`, or a forge
// `--gate-json` document). A gate that is missing refuses the promotion
// `required_gate_missing`; one carried as failed, error or skipped refuses it
// `required_gate_failed`. An empty Names requires nothing, whatever the mode,
// so naming a gate is the opt-in. `audit` admits and records what enforce
// would have refused.
type ShapeRequiredGates struct {
	Mode  DeliveryMode `json:"mode"`
	Names []string     `json:"names"`
}

// ShapeApprovalHook is the approval-hook control: before a promotion to this
// env is written, the hook is asked, with an [ApprovalRequest], and answers an
// [ApprovalDecision]. Exactly one of URL (an HTTPS endpoint the env's ledger
// POSTs to: the control plane for an env that has one, forge otherwise) or
// Argv (a command forge runs, with no shell, for an env without a control
// plane) is set.
//
// In `enforce`, anything but approve refuses: deny `approval_denied`, pending
// `approval_pending`, and a hook that times out, fails or answers something
// unreadable is a deny (fail closed). `audit` asks, records the answer, and
// admits. `--yes` never answers for the hook.
type ShapeApprovalHook struct {
	Mode DeliveryMode `json:"mode"`
	URL  string       `json:"url,omitempty"`
	Argv []string     `json:"argv,omitempty"`
	// TimeoutSeconds bounds one ask, 1..60. A hook that has not answered by
	// then is a deny in enforce.
	TimeoutSeconds int `json:"timeout_seconds"`
}

// ShapeHealthGate is the health-gate control: a promotion of release R to this
// env is admitted only once every env in After has proved R good — the control
// plane holds a release-health verdict of PASS for its newest promotion of R
// there (the verdict `forge obs check` reports as exit 0). The order is
// declared, never inferred: an empty After gates nothing, whatever the mode.
// `enforce` refuses an unproven promotion unless it carries the recorded
// override; `audit` admits it and records what enforce would have refused.
type ShapeHealthGate struct {
	Mode  DeliveryMode `json:"mode"`
	After []string     `json:"after"`
}

// ShapeControl is a control with no parameters beyond its mode.
type ShapeControl struct {
	Mode DeliveryMode `json:"mode"`
}

// ShapeTrustedBuilder is the trusted-builder control: only artifacts built by
// the platform's builder for the org, or by one of Builders (`<kind>:<id>`),
// are eligible.
type ShapeTrustedBuilder struct {
	Mode     DeliveryMode `json:"mode"`
	Builders []string     `json:"builders"`
}

// ShapeSigning is the signing control: images and the bundle are signed. Key
// is the key model; KeyRef is a customer key's PUBLIC reference (never key
// material); OIDCIssuer and OIDCSubject name the identity a keyless signature
// must come from.
type ShapeSigning struct {
	Mode        DeliveryMode `json:"mode"`
	Key         string       `json:"key"`
	KeyRef      string       `json:"key_ref,omitempty"`
	OIDCIssuer  string       `json:"oidc_issuer,omitempty"`
	OIDCSubject string       `json:"oidc_subject,omitempty"`
}

// ShapeVulnGate is the vulnerability gate: a finding blocks when it is
// KEV-listed (BlockKEV), or when its severity is at or above BlockSeverity
// and, with FixableOnly, a fixed version exists. A non-blocking finding at or
// above WarnSeverity is warned.
type ShapeVulnGate struct {
	Mode          DeliveryMode      `json:"mode"`
	BlockSeverity string            `json:"block_severity"`
	WarnSeverity  string            `json:"warn_severity"`
	BlockKEV      bool              `json:"block_kev"`
	FixableOnly   bool              `json:"fixable_only"`
	Exemptions    []ShapeVulnExempt `json:"exemptions"`
	// ScopedExemptions are the exemptions that cover their advisory only
	// where their scope says. They are a list of their own, never entries
	// of Exemptions, so that a reader predating scopes ignores them rather
	// than reading each as an exemption of the advisory everywhere: an
	// unread scoped exemption leaves the finding in the gate (fail closed).
	// Absent when the env declares none, so such a policy encodes as it did
	// before the field. A control plane decodes it only once it names
	// [BundleFeatureVulnScopedExemptions].
	ScopedExemptions []ShapeVulnScopedExempt `json:"scoped_exemptions,omitempty"`
}

// ShapeVulnExempt is one accepted advisory: time-boxed, with its reason.
type ShapeVulnExempt struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
}

// ShapeVulnScopedExempt is one accepted advisory, covered only where its
// scope says: time-boxed, with its reason, as [ShapeVulnExempt].
//
// Packages, Artifacts and Locations are the scope. The exemption covers one
// occurrence of a finding ([VulnOccurrence]) only when every set field names
// it (AND across fields, OR within one). The three are what tells two
// findings of one advisory apart: the package it matched (a CVE matched
// against two libraries), the artifact (an image), and the path inside the
// artifact, which is what tells two Go binaries' "stdlib" findings apart,
// since their package name and purl are the same. With no scope it is
// [ShapeVulnExempt], and canonically recorded as one.
type ShapeVulnScopedExempt struct {
	ID      string `json:"id"`
	Reason  string `json:"reason"`
	Expires string `json:"expires"`
	// Packages are package names or purl prefixes ("stdlib",
	// "pkg:golang/stdlib", "pkg:npm/lodash@4.17.20"): [VulnPackageMatches].
	Packages []string `json:"packages,omitempty"`
	// Artifacts are release artifact names, matched exactly.
	Artifacts []string `json:"artifacts,omitempty"`
	// Locations are absolute paths inside the artifact
	// ("/usr/local/bin/grype"), matched exactly, or every path under one
	// that ends in "/".
	Locations []string `json:"locations,omitempty"`
}

// Scoped reports whether the exemption names a scope, so covers its advisory
// only where the scope says.
func (e ShapeVulnScopedExempt) Scoped() bool {
	return len(e.Packages) > 0 || len(e.Artifacts) > 0 || len(e.Locations) > 0
}

// Exempt is the exemption without its scope.
func (e ShapeVulnScopedExempt) Exempt() ShapeVulnExempt {
	return ShapeVulnExempt{ID: e.ID, Reason: e.Reason, Expires: e.Expires}
}

// Identity is what makes two exemptions the same declaration: the advisory
// and the scope, each list sorted. Two exemptions of one advisory with
// different scopes are two declarations; the reason and expiry are not part
// of it (a renewal edits the one declaration).
func (e ShapeVulnScopedExempt) Identity() string {
	return strings.Join([]string{
		strings.TrimSpace(e.ID),
		strings.Join(sortedNonNil(e.Packages), ","),
		strings.Join(sortedNonNil(e.Artifacts), ","),
		strings.Join(sortedNonNil(e.Locations), ","),
	}, "|")
}

// VulnOccurrence is one place a finding was found: the package the advisory
// matched, the artifact it is in, and the path inside that artifact. A
// finding merged across artifacts, or across binaries of one artifact, has
// one occurrence per (artifact, path). Artifact or Location is "" when the
// source does not say.
type VulnOccurrence struct {
	PackageName string
	PackagePURL string
	Artifact    string
	Location    string
}

// ScopeCovers reports whether the exemption's scope covers o. It does not
// look at the advisory or the expiry: the caller has already matched the
// exemption's id against the finding and dated it. An unscoped exemption
// covers every occurrence. A scope field the occurrence cannot answer (an
// empty Artifact or Location) does not cover it: an exemption is never
// widened by what its reader does not know.
func (e ShapeVulnScopedExempt) ScopeCovers(o VulnOccurrence) bool {
	if len(e.Packages) > 0 && !slices.ContainsFunc(e.Packages, func(p string) bool { return VulnPackageMatches(p, o.PackageName, o.PackagePURL) }) {
		return false
	}
	if len(e.Artifacts) > 0 && !slices.ContainsFunc(e.Artifacts, func(a string) bool { return o.Artifact != "" && a == o.Artifact }) {
		return false
	}
	if len(e.Locations) > 0 && !slices.ContainsFunc(e.Locations, func(l string) bool { return vulnLocationMatches(l, o.Location) }) {
		return false
	}
	return true
}

// VulnPackageMatches reports whether an exemption's package entry names a
// finding's package: the package name exactly, or the purl exactly or by a
// prefix that ends at a purl boundary ("pkg:golang/stdlib" names
// "pkg:golang/stdlib@go1.24.1", never "pkg:golang/stdlibx@1").
func VulnPackageMatches(entry, name, purl string) bool {
	switch {
	case entry == "":
		return false
	case entry == name:
		return true
	case purl == "" || !strings.HasPrefix(purl, entry):
		return false
	case len(purl) == len(entry) || strings.HasSuffix(entry, "@") || strings.HasSuffix(entry, "/"):
		return true
	}
	return strings.ContainsRune("@?#/", rune(purl[len(entry)]))
}

// vulnLocationMatches reports whether an exemption's location entry names a
// path: exactly, or as a directory when the entry ends in "/".
func vulnLocationMatches(entry, path string) bool {
	if entry == "" || path == "" {
		return false
	}
	return entry == path || (strings.HasSuffix(entry, "/") && strings.HasPrefix(path, entry))
}

// ShapeBundleKinds narrows, never widens, the mandatory ingest baseline:
// Allow non-empty means only those kinds; Deny refuses these. `Kind` or
// `group/Kind`.
type ShapeBundleKinds struct {
	Mode  DeliveryMode `json:"mode"`
	Allow []string     `json:"allow"`
	Deny  []string     `json:"deny"`
}

// ShapeApprovals is the approvals control: Required distinct people must
// approve a promotion, the requester included. 1 is one-person approval.
// Approvers are the org members who may approve; more than one required needs
// at least that many.
type ShapeApprovals struct {
	Mode      DeliveryMode `json:"mode"`
	Required  int          `json:"required"`
	Approvers []string     `json:"approvers"`
}

// DefaultDeliveryPolicy is every control at its default for an env that
// delivers through a control plane (the only reader of a recorded policy,
// so it is what a nil policy in a shape means). A control that cannot block
// a deploy and costs the customer nothing is enforced (provenance/SBOM); a
// control that can refuse a deploy is audited until the env turns it up.
//
// Signing and flux_verify are audit: the platform signs every recorded bundle
// with the org's key and verifies it server-side, recording the verdict, and
// enforce (spec.verify, and Promote refusing an unsigned bundle) is the env's
// opt-in once every rollback target is signed.
//
// It must agree with the defaults of forge.DeliveryPolicy in kcl/schema.k for
// an env with a control plane; TestDeliveryPolicyDefaultsAgreeWithKCL fails if
// they drift.
func DefaultDeliveryPolicy() ShapeDeliveryPolicy {
	return ShapeDeliveryPolicy{
		TrustedBuilder:   ShapeTrustedBuilder{Mode: DeliveryAudit, Builders: []string{}},
		Signing:          ShapeSigning{Mode: DeliveryAudit, Key: SigningKeyOrgKMS},
		ProvenanceSBOM:   ShapeControl{Mode: DeliveryAudit},
		FluxVerify:       ShapeControl{Mode: DeliveryAudit},
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

// DefaultSelfHostedDeliveryPolicy is the default of an env with no control
// plane: as DefaultDeliveryPolicy, except that signing and flux_verify are
// off, because nothing signs a self-hosted env's bundles yet. Such an env
// that declares forge.DeliveryPolicy {} records this, which is not the
// shape-level default, so the shape says plainly that nothing is signed.
func DefaultSelfHostedDeliveryPolicy() ShapeDeliveryPolicy {
	p := DefaultDeliveryPolicy()
	p.Signing.Mode = DeliveryOff
	p.FluxVerify.Mode = DeliveryOff
	return p
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
	// An exemption with no scope is canonically an entry of Exemptions,
	// whichever list a producer put it in; each scope list is sorted.
	ex := append([]ShapeVulnExempt{}, p.VulnGate.Exemptions...)
	var scoped []ShapeVulnScopedExempt
	for _, e := range p.VulnGate.ScopedExemptions {
		if !e.Scoped() {
			ex = append(ex, e.Exempt())
			continue
		}
		e.Packages, e.Artifacts, e.Locations = sortedOrNil(e.Packages), sortedOrNil(e.Artifacts), sortedOrNil(e.Locations)
		scoped = append(scoped, e)
	}
	sort.Slice(ex, func(i, j int) bool { return ex[i].ID < ex[j].ID })
	sort.Slice(scoped, func(i, j int) bool { return scoped[i].Identity() < scoped[j].Identity() })
	out.VulnGate.Exemptions, out.VulnGate.ScopedExemptions = ex, scoped
	// The default gate (enforce, naming nothing) is the absence of one, so a
	// policy that spells it out hashes as one that does not.
	if p.HealthGate != nil {
		g := ShapeHealthGate{Mode: p.HealthGate.Mode, After: sortedNonNil(p.HealthGate.After)}
		out.HealthGate = &g
		if g.Mode == DeliveryEnforce && len(g.After) == 0 {
			out.HealthGate = nil
		}
	}
	// Likewise the default requirement (enforce, naming nothing).
	if p.RequiredGates != nil {
		g := ShapeRequiredGates{Mode: p.RequiredGates.Mode, Names: sortedNonNil(p.RequiredGates.Names)}
		out.RequiredGates = &g
		if g.Mode == DeliveryEnforce && len(g.Names) == 0 {
			out.RequiredGates = nil
		}
	}
	// A hook's argv is ordered (it is a command line), so it is copied, not
	// sorted.
	if p.ApprovalHook != nil {
		h := *p.ApprovalHook
		h.Argv = append([]string(nil), p.ApprovalHook.Argv...)
		out.ApprovalHook = &h
	}
	return out
}

// validateVulnScope is why an exemption's scope is unusable, "" when it is
// not: the rules forge.VulnExemption's checks hold in kcl/schema.k.
func validateVulnScope(e ShapeVulnScopedExempt) string {
	for _, f := range []struct {
		name string
		vals []string
	}{{"packages", e.Packages}, {"artifacts", e.Artifacts}, {"locations", e.Locations}} {
		seen := map[string]bool{}
		for _, v := range f.vals {
			switch {
			case v == "" || v != strings.TrimSpace(v) || strings.ContainsAny(v, "\t\r\n"):
				return fmt.Sprintf("%s entry %q is empty, padded with spaces, or not one line", f.name, v)
			case f.name == "locations" && !strings.HasPrefix(v, "/"):
				return fmt.Sprintf("location %q is not an absolute path inside the artifact", v)
			case seen[v]:
				return fmt.Sprintf("%s names %q twice", f.name, v)
			}
			seen[v] = true
		}
	}
	return ""
}

func sortedNonNil(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// sortedOrNil is sortedNonNil, except that an empty list stays nil (an
// omitempty field that is absent stays absent).
func sortedOrNil(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return sortedNonNil(in)
}

var (
	deliveryAdvisoryRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[A-Za-z0-9-]+$`)
	deliveryDateRE     = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])$`)
	deliveryBuilderRE  = regexp.MustCompile(`^[a-z][a-z0-9-]*:[^\s:]\S*$`)
	deliveryKindRE     = regexp.MustCompile(`^([a-z0-9][a-z0-9.-]*/)?[A-Z][A-Za-z0-9]*$`)
	// deliveryEnvRE is an env name as health_gate.after names it: the
	// directory under deploy/kcl, an RFC 1123 label.
	deliveryEnvRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
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
	// v is canonical (Shape.Validate), so every entry here is scoped.
	scoped := map[string]bool{}
	for _, e := range v.ScopedExemptions {
		switch {
		case !deliveryAdvisoryRE.MatchString(e.ID):
			return bad("vuln_gate exemption %q is not an advisory ID", e.ID)
		case strings.TrimSpace(e.Reason) == "":
			return bad("vuln_gate exemption %s has no reason", e.ID)
		case !deliveryDateRE.MatchString(e.Expires):
			return bad("vuln_gate exemption %s expires %q (expected YYYY-MM-DD): an exemption is time-boxed", e.ID, e.Expires)
		case scoped[e.Identity()]:
			return bad("vuln_gate exempts %s twice with the same scope", e.ID)
		}
		if err := validateVulnScope(e); err != "" {
			return bad("vuln_gate exemption %s: %s", e.ID, err)
		}
		scoped[e.Identity()] = true
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

	if g := p.HealthGate; g != nil {
		if !g.Mode.Valid() {
			return bad("health_gate mode %q (expected off, audit or enforce)", g.Mode)
		}
		envs := map[string]bool{}
		for _, env := range g.After {
			if !deliveryEnvRE.MatchString(env) {
				return bad("health_gate.after names %q, which is not an env name", env)
			}
			if envs[env] {
				return bad("health_gate.after names %q twice", env)
			}
			envs[env] = true
		}
	}

	if g := p.RequiredGates; g != nil {
		if !g.Mode.Valid() {
			return bad("required_gates mode %q (expected off, audit or enforce)", g.Mode)
		}
		names := map[string]bool{}
		for _, name := range g.Names {
			if err := ValidateRequiredGateName(name); err != nil {
				return bad("required_gates: %v", err)
			}
			if names[name] {
				return bad("required_gates names %q twice", name)
			}
			names[name] = true
		}
	}
	if h := p.ApprovalHook; h != nil {
		if err := h.validate(); err != nil {
			return bad("approval_hook: %v", err)
		}
	}
	return nil
}
