package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Managed identity: the part of an env's declaration (forge.ManagedIdentity)
// the control plane converges. Everything here is a NAME, a destination or a
// literal the author wrote; an issued client id, a broker token or a signing
// key is an OUTPUT the control plane delivers, and no shape can carry one.

// Where a managed identity runs ([ShapeIdentity.Runtime]).
const (
	// IdentityRuntimeHosted is an instance on the control plane's shared
	// identity deployment.
	IdentityRuntimeHosted = "hosted"
	// IdentityRuntimeHost is the env's own host-run IdP (forge.HostInfra,
	// engine zitadel): `forge env up`'s dev loop.
	IdentityRuntimeHost = "host"
)

// Login modes ([ShapeIdentityLogin.Mode]).
const (
	// IdentityLoginNative is the app's own sign-in form, with the server-side
	// broker. The default.
	IdentityLoginNative = "native"
	// IdentityLoginHosted is the identity provider's hosted login pages.
	// Accepted by the schema and recorded, but phase 2: nothing converges it
	// yet.
	IdentityLoginHosted = "hosted"
)

// Application kinds ([ShapeIdentityApplication.Kind]).
const (
	IdentityAppNativeBroker = "native_broker"
	IdentityAppOIDCWeb      = "oidc_web"
)

// Where a role's members are managed ([ShapeIdentityRole.MembersFrom]).
const (
	// IdentityMembersConsole: members are managed in the console and never
	// touched by a reconcile. The default.
	IdentityMembersConsole = "console"
	// IdentityMembersDeclared: members are a set converged from the
	// declaration, read-only in the console.
	IdentityMembersDeclared = "declared"
)

// Who may create an account from the sign-in screen ([ShapeIdentitySignup.Mode]).
const (
	// IdentitySignupInvite: only owners, declared members and invitations.
	// The default, and the pre-v2 behaviour.
	IdentitySignupInvite = "invite"
	// IdentitySignupOpen: anyone, once the address is verified.
	IdentitySignupOpen = "open"
	// IdentitySignupDomain: anyone whose VERIFIED address is in
	// [ShapeIdentitySignup.Domains].
	IdentitySignupDomain = "domain"
)

// First-factor sign-in methods ([ShapeIdentity.Methods]).
const (
	IdentityMethodPassword  = "password"
	IdentityMethodPasskey   = "passkey"
	IdentityMethodMagicLink = "magic_link"
)

// Social identity providers ([ShapeIdentitySocial.Type]).
const (
	IdentitySocialGoogle = "google"
	IdentitySocialGitHub = "github"
)

// How a hosted instance's mail is delivered when no smtp is declared
// ([ShapeIdentityEmail.Delivery]).
const (
	// IdentityMailPlatform is the control plane's identity mail relay. The
	// default: a nil [ShapeIdentity.Email] means this.
	IdentityMailPlatform = "platform"
	// IdentityMailNone sends nothing: the pre-v2 behaviour.
	IdentityMailNone = "none"
)

// IdentityLoginPolicyKey is the key the control plane delivers the versioned
// LOGIN_POLICY document under: in the forge-identity-<name> Secret, and as
// RELIANT_IDENTITY_<NAME>_LOGIN_POLICY for a hosted consumer. It is the
// `login_policy` output, which forge projects (always optional) into a native
// broker's config field idp_login_policy, and only for a declaration that
// [ShapeIdentity.DeclaresLoginPolicy].
const IdentityLoginPolicyKey = "LOGIN_POLICY"

// The names a managed identity reserves. A consumer reads its outputs from a
// Secret or a managed secret whose name starts with one of these, and the
// control plane is the only writer, so neither prefix is an env's own secret.
const (
	// IdentityDomain is the registrable domain every managed instance's
	// issuer lives under: https://<slug>.reliantidentity.com.
	IdentityDomain = "reliantidentity.com"
	// IdentitySecretPrefix starts the name of every Kubernetes Secret the
	// control plane delivers an identity's outputs in:
	// forge-identity-<name> (+ -jwks for the signing-key snapshot).
	IdentitySecretPrefix = "forge-identity-"
	// IdentityManagedSecretPrefix starts the name of every managed secret a
	// HOSTED consumer reads an identity output from.
	IdentityManagedSecretPrefix = "RELIANT_IDENTITY_"
)

// IdentityOutputs is the closed set of outputs a consumer's config can be
// filled from, and the keys of [ShapeIdentityConsumer.Fields].
var IdentityOutputs = []string{
	"audience", "broker_token", "client_id", "idp_base", "issuer",
	"jwks", "login_policy", "login_uri", "redirect_uri", "roles_claim", "scopes",
}

// IdentityOmittableOutputs are the outputs a consumer may decline to have
// projected ([ShapeIdentityConsumer.Omit]). The rest define the identity.
var IdentityOmittableOutputs = []string{"jwks", "login_policy", "login_uri", "roles_claim", "scopes"}

var (
	identityNameRE      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	identityRoleRE      = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)
	identityFieldRE     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	identityEmailRE     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	identityDomainRE    = regexp.MustCompile(`^([a-z0-9]([-a-z0-9]*[a-z0-9])?\.)+[a-z]{2,}$`)
	identityFromLocalRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	managedSecretRE     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	slugLabelRunRE      = regexp.MustCompile(`[^a-z0-9]+`)
)

// ShapeIdentity is the env's managed identity as DECLARED (forge.ManagedIdentity):
// what the control plane's identity reconciler converges an instance to. It
// records names only — a SMTP password, a seed user's password and a machine
// user's key are secret NAMES, and every issued value is delivered, never
// recorded.
//
// Shape.Identity is nil when the env declares no identity, so every shape
// recorded before the field encodes and hashes exactly as it did.
type ShapeIdentity struct {
	// Name names the outputs (forge-identity-<name>) and the declaration.
	Name string `json:"name"`
	// Runtime is where the instance runs: [IdentityRuntimeHosted] or
	// [IdentityRuntimeHost].
	Runtime string `json:"runtime"`
	// ShareWith is the env of the SAME project that owns the instance this
	// env's applications live in. Empty when this env owns its instance.
	ShareWith string `json:"share_with,omitempty"`
	// ProjectName is the identity project's name. Empty means the env's
	// project; an adopted instance keeps its legacy name here.
	ProjectName string             `json:"project_name,omitempty"`
	Login       ShapeIdentityLogin `json:"login"`
	// Public is the declaration's own claim that the instance needs a public
	// route (hosted login, an explicit public = True). The control plane also
	// raises it for a consumer outside the platform network, which only it
	// can see.
	Public bool `json:"public,omitempty"`
	// Domain is a custom issuer domain (login.acme.com), instead of the
	// assigned <slug>.reliantidentity.com.
	Domain       string                     `json:"domain,omitempty"`
	Roles        []ShapeIdentityRole        `json:"roles,omitempty"`
	Applications []ShapeIdentityApplication `json:"applications,omitempty"`
	Consumers    []ShapeIdentityConsumer    `json:"consumers,omitempty"`
	MachineUsers []ShapeIdentityMachineUser `json:"machine_users,omitempty"`
	SMTP         *ShapeIdentitySMTP         `json:"smtp,omitempty"`
	SeedUsers    []ShapeIdentitySeedUser    `json:"seed_users,omitempty"`
	// Deliveries are the Secrets the control plane writes this identity's
	// outputs into on a connected cluster. Each is ALSO an entry of
	// Shape.ControlPlaneSecrets, which is how the existing delivery path sees
	// it; this list says which of those entries are the identity's.
	Deliveries []ShapeIdentityDelivery `json:"deliveries,omitempty"`
	// ManagedSecrets are the reserved managed-secret names the control plane
	// writes the outputs under for HOSTED consumers. They are not the env's
	// secrets: nobody sets them.
	ManagedSecrets []string `json:"managed_secrets,omitempty"`

	// The v2 sign-in fields below are all omitempty and nil or empty by
	// default, so every shape recorded before them encodes and hashes exactly
	// as it did.

	// Owners are seeded on every pass (created email-verified, granted
	// OwnerRole, invited while credential-less). ADDITIVE AND STICKY: an
	// address removed here is never revoked. Lower-cased.
	Owners []string `json:"owners,omitempty"`
	// OwnerRole is set only when Owners is non-empty.
	OwnerRole string `json:"owner_role,omitempty"`
	// Signup is who may create an account. Nil = [IdentitySignupInvite].
	Signup *ShapeIdentitySignup `json:"signup,omitempty"`
	// Methods are the first-factor methods offered ([IdentityMethodPassword],
	// ...). When the declaration does not [ShapeIdentity.DeclaresLoginPolicy]
	// it is nil, meaning [password] and "leave the login policy alone". When
	// it does, the render always lists them, so nil there means none (social
	// sign-in only). Read it through [ShapeIdentity.SignInMethods].
	Methods []string `json:"methods,omitempty"`
	// PasskeyRPID is the WebAuthn relying-party id. Empty = the host of the
	// app's sign-in origin.
	PasskeyRPID string                `json:"passkey_rp_id,omitempty"`
	Social      []ShapeIdentitySocial `json:"social,omitempty"`
	// Email is how the instance's mail is addressed. Nil = {delivery:
	// platform}.
	Email *ShapeIdentityEmail `json:"email,omitempty"`
	// DevMail is the mailpit HostInfra a host-run instance's mail goes to.
	// Host runtime only.
	DevMail string `json:"dev_mail,omitempty"`
}

// ShapeIdentitySignup is who may create an account from the sign-in screen.
type ShapeIdentitySignup struct {
	// Mode is [IdentitySignupInvite], [IdentitySignupOpen] or
	// [IdentitySignupDomain].
	Mode string `json:"mode"`
	// Domains are the lower-case DNS names a domain-mode signup's VERIFIED
	// address must be in. A subdomain matches only if listed itself.
	Domains []string `json:"domains,omitempty"`
	// Role is granted to a self-signed-up account once verified. Empty =
	// none.
	Role string `json:"role,omitempty"`
}

// ShapeIdentitySocial is one social identity provider, with the app's own
// OAuth client. The client secret (and a secret client id) are managed-secret
// NAMES: only the identity reconciler reads them, and no workload is handed
// them.
type ShapeIdentitySocial struct {
	// Type is [IdentitySocialGoogle] or [IdentitySocialGitHub].
	Type string `json:"type"`
	// ClientID is the literal client id, OR ClientIDSecret names it.
	ClientID       string `json:"client_id,omitempty"`
	ClientIDSecret string `json:"client_id_secret,omitempty"`
	// ClientSecretSecret names the client secret, always.
	ClientSecretSecret string   `json:"client_secret_secret"`
	Scopes             []string `json:"scopes"`
}

// ShapeIdentityEmail is how the instance's mail is addressed.
type ShapeIdentityEmail struct {
	// Delivery is [IdentityMailPlatform] or [IdentityMailNone]. Ignored when
	// smtp is declared.
	Delivery string `json:"delivery"`
	// FromName is the From display name. Empty = the project.
	FromName string `json:"from_name,omitempty"`
	// FromLocal is the From local part; the domain is assigned by the
	// platform. Empty with smtp, whose sender is the From.
	FromLocal string `json:"from_local,omitempty"`
	ReplyTo   string `json:"reply_to,omitempty"`
}

// DeclaresLoginPolicy reports whether the declaration takes over the
// instance's login policy: Methods, Signup or Social is set. When it does not,
// the policy is left alone and no login_policy output is projected or
// delivered, which keeps an adopted instance and a pre-v2 broker as they were.
func (i ShapeIdentity) DeclaresLoginPolicy() bool {
	return i.Methods != nil || i.Signup != nil || len(i.Social) > 0
}

// SignInMethods is the first-factor methods the declaration offers: Methods
// when it [ShapeIdentity.DeclaresLoginPolicy] (possibly none: social only),
// else [IdentityMethodPassword].
func (i ShapeIdentity) SignInMethods() []string {
	if i.DeclaresLoginPolicy() {
		return i.Methods
	}
	return []string{IdentityMethodPassword}
}

// SignupMode is [ShapeIdentitySignup.Mode], or [IdentitySignupInvite] when
// Signup is nil.
func (i ShapeIdentity) SignupMode() string {
	if i.Signup == nil {
		return IdentitySignupInvite
	}
	return i.Signup.Mode
}

// MailAvailable reports whether the instance can send mail at all: smtp is
// declared, it is hosted with a delivery other than [IdentityMailNone], or it
// is host-run with a mailpit (DevMail).
func (i ShapeIdentity) MailAvailable() bool {
	switch {
	case i.SMTP != nil:
		return true
	case i.Runtime == IdentityRuntimeHosted:
		return i.Email == nil || i.Email.Delivery != IdentityMailNone
	default:
		return i.DevMail != ""
	}
}

// ShapeIdentityLogin is how users sign in.
type ShapeIdentityLogin struct {
	Mode string `json:"mode"`
}

// ShapeIdentityRole is one role of the identity project.
type ShapeIdentityRole struct {
	Key         string `json:"key"`
	DisplayName string `json:"display_name,omitempty"`
	Group       string `json:"group,omitempty"`
	// MembersFrom is [IdentityMembersConsole] or [IdentityMembersDeclared].
	MembersFrom string `json:"members_from"`
	// Members are the declared members' emails. Only with MembersFrom
	// declared.
	Members []string `json:"members,omitempty"`
}

// ShapeIdentityApplication is one OIDC application registered in the
// instance.
type ShapeIdentityApplication struct {
	Name string `json:"name"`
	// Workload is the broker: the workload that runs the sign-in endpoint.
	Workload     string   `json:"workload"`
	Kind         string   `json:"kind"`
	RedirectURIs []string `json:"redirect_uris,omitempty"`
	LogoutURIs   []string `json:"logout_uris,omitempty"`
	// LoginURI is the declared literal override. Empty means the control plane
	// derives it from the env's frontend.
	LoginURI string `json:"login_uri,omitempty"`
}

// ShapeIdentityConsumer is one workload whose typed config the identity's
// outputs fill.
type ShapeIdentityConsumer struct {
	Workload string `json:"workload"`
	// Application is the application this consumer brokers for. Empty for a
	// consumer that only validates tokens.
	Application string `json:"application,omitempty"`
	// Fields maps an output ([IdentityOutputs]) to the config field it fills
	// when that is not the standard one (the 20%: an app that validates two
	// issuers).
	Fields map[string]string `json:"fields,omitempty"`
	// DeliverOnly says the control plane delivers this consumer's Secrets but
	// forge projects nothing into the workload: it still reads its own config.
	DeliverOnly bool `json:"deliver_only,omitempty"`
	// Omit lists outputs ([IdentityOmittableOutputs]) forge does not project
	// into the workload, for an app whose config has no field for them. The
	// Secrets still carry them.
	Omit []string `json:"omit,omitempty"`
}

// ShapeIdentityMachineUser is a non-human account whose key is delivered.
type ShapeIdentityMachineUser struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles,omitempty"`
	// KeySecret is where its key is delivered. Required for a hosted
	// instance; a host-run one keeps the key in the env's secret store.
	KeySecret *ShapeIdentityKeySecret `json:"key_secret,omitempty"`
}

// ShapeIdentityKeySecret is a delivery destination: a Secret on a connected
// cluster.
type ShapeIdentityKeySecret struct {
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	Cluster          string `json:"cluster"`
	ConnectedCluster string `json:"connected_cluster"`
}

// ShapeIdentitySMTP is a bring-your-own mail relay. PasswordSecret is a
// managed-secret NAME.
type ShapeIdentitySMTP struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	User           string `json:"user,omitempty"`
	Sender         string `json:"sender"`
	PasswordSecret string `json:"password_secret"`
}

// ShapeIdentitySeedUser is a user created in a local or ephemeral env.
// PasswordSecret is a secret NAME.
type ShapeIdentitySeedUser struct {
	Email          string   `json:"email"`
	Roles          []string `json:"roles,omitempty"`
	PasswordSecret string   `json:"password_secret,omitempty"`
}

// ShapeIdentityDelivery names one Secret of Shape.ControlPlaneSecrets as
// belonging to the identity.
type ShapeIdentityDelivery struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Namespace        string `json:"namespace"`
	Cluster          string `json:"cluster"`
	ConnectedCluster string `json:"connected_cluster"`
}

func (d ShapeIdentityDelivery) key() string {
	return d.Cluster + "/" + d.Namespace + "/" + d.Name
}

// IsIdentityKind reports whether a control-plane secret kind is one a managed
// identity derives.
func IsIdentityKind(kind string) bool {
	switch kind {
	case ControlPlaneSecretIdentity, ControlPlaneSecretIdentityMachineKey, ControlPlaneSecretIdentityJWKS:
		return true
	}
	return false
}

// canonical returns the identity with every list sorted and every empty list
// nil, so two projections of the same declaration encode to the same bytes.
func (i ShapeIdentity) canonical() ShapeIdentity {
	out := i
	if len(i.Roles) > 0 {
		out.Roles = make([]ShapeIdentityRole, len(i.Roles))
		for n, r := range i.Roles {
			r.Members = sortedNonEmpty(r.Members)
			out.Roles[n] = r
		}
		sort.Slice(out.Roles, func(a, b int) bool { return out.Roles[a].Key < out.Roles[b].Key })
	} else {
		out.Roles = nil
	}
	if len(i.Applications) > 0 {
		out.Applications = make([]ShapeIdentityApplication, len(i.Applications))
		for n, a := range i.Applications {
			a.RedirectURIs = sortedNonEmpty(a.RedirectURIs)
			a.LogoutURIs = sortedNonEmpty(a.LogoutURIs)
			out.Applications[n] = a
		}
		sort.Slice(out.Applications, func(a, b int) bool { return out.Applications[a].Name < out.Applications[b].Name })
	} else {
		out.Applications = nil
	}
	if len(i.Consumers) > 0 {
		out.Consumers = make([]ShapeIdentityConsumer, len(i.Consumers))
		for n, c := range i.Consumers {
			if len(c.Fields) == 0 {
				c.Fields = nil
			}
			c.Omit = sortedNonEmpty(c.Omit)
			out.Consumers[n] = c
		}
		sort.Slice(out.Consumers, func(a, b int) bool {
			x, y := out.Consumers[a], out.Consumers[b]
			if x.Workload != y.Workload {
				return x.Workload < y.Workload
			}
			return x.Application < y.Application
		})
	} else {
		out.Consumers = nil
	}
	if len(i.MachineUsers) > 0 {
		out.MachineUsers = make([]ShapeIdentityMachineUser, len(i.MachineUsers))
		for n, m := range i.MachineUsers {
			m.Roles = sortedNonEmpty(m.Roles)
			out.MachineUsers[n] = m
		}
		sort.Slice(out.MachineUsers, func(a, b int) bool { return out.MachineUsers[a].Name < out.MachineUsers[b].Name })
	} else {
		out.MachineUsers = nil
	}
	if len(i.SeedUsers) > 0 {
		out.SeedUsers = make([]ShapeIdentitySeedUser, len(i.SeedUsers))
		for n, u := range i.SeedUsers {
			u.Roles = sortedNonEmpty(u.Roles)
			out.SeedUsers[n] = u
		}
		sort.Slice(out.SeedUsers, func(a, b int) bool { return out.SeedUsers[a].Email < out.SeedUsers[b].Email })
	} else {
		out.SeedUsers = nil
	}
	if len(i.Deliveries) > 0 {
		out.Deliveries = append([]ShapeIdentityDelivery(nil), i.Deliveries...)
		sort.Slice(out.Deliveries, func(a, b int) bool {
			x, y := out.Deliveries[a], out.Deliveries[b]
			if x.key() != y.key() {
				return x.key() < y.key()
			}
			return x.Kind < y.Kind
		})
	} else {
		out.Deliveries = nil
	}
	out.ManagedSecrets = sortedNonEmpty(i.ManagedSecrets)
	if len(i.Owners) > 0 {
		lower := make([]string, len(i.Owners))
		for n, o := range i.Owners {
			lower[n] = strings.ToLower(o)
		}
		out.Owners = sortedStrings(lower)
	} else {
		out.Owners = nil
	}
	if i.Signup != nil {
		s := *i.Signup
		s.Domains = sortedNonEmpty(s.Domains)
		out.Signup = &s
	}
	out.Methods = sortedNonEmpty(i.Methods)
	if len(i.Social) > 0 {
		out.Social = make([]ShapeIdentitySocial, len(i.Social))
		for n, s := range i.Social {
			s.Scopes = sortedStrings(s.Scopes)
			out.Social[n] = s
		}
		sort.Slice(out.Social, func(a, b int) bool { return out.Social[a].Type < out.Social[b].Type })
	} else {
		out.Social = nil
	}
	if i.Email != nil {
		e := *i.Email
		out.Email = &e
	}
	return out
}

// sortedNonEmpty is [sortedStrings] that returns nil for an empty list, so a
// field tagged omitempty omits it and a round trip compares equal.
func sortedNonEmpty(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return sortedStrings(in)
}

// validate re-checks the declaration structurally. KCL refuses the same cases
// at render; the shape re-checks because it is stored forever and read by a
// different program than the one that wrote it. controlPlaneSecrets is the
// shape's own list, which every delivery must appear in.
func (i ShapeIdentity) validate(controlPlaneSecrets []ShapeControlPlaneSecret) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: shape identity %q: %s", ErrInvalid, i.Name, fmt.Sprintf(format, args...))
	}
	if !identityNameRE.MatchString(i.Name) || len(i.Name) > 40 {
		return fmt.Errorf("%w: shape identity name %q must be an RFC-1123 label of at most 40 characters", ErrInvalid, i.Name)
	}
	if i.Runtime != IdentityRuntimeHosted && i.Runtime != IdentityRuntimeHost {
		return bad("runtime %q (expected %s or %s)", i.Runtime, IdentityRuntimeHosted, IdentityRuntimeHost)
	}
	if i.Login.Mode != IdentityLoginNative && i.Login.Mode != IdentityLoginHosted {
		return bad("login mode %q (expected %s or %s)", i.Login.Mode, IdentityLoginNative, IdentityLoginHosted)
	}
	if i.Domain != "" {
		if !identityDomainRE.MatchString(i.Domain) {
			return bad("domain %q is not a DNS name", i.Domain)
		}
		if !i.Public && i.Login.Mode != IdentityLoginHosted {
			return bad("a custom domain needs a public instance (public, or hosted login)")
		}
	}

	roles := map[string]ShapeIdentityRole{}
	for _, r := range i.Roles {
		if !identityRoleRE.MatchString(r.Key) {
			return bad("role key %q must match %s", r.Key, identityRoleRE)
		}
		if _, dup := roles[r.Key]; dup {
			return bad("role %q declared twice", r.Key)
		}
		roles[r.Key] = r
		switch r.MembersFrom {
		case IdentityMembersConsole:
			if len(r.Members) > 0 {
				return bad("role %q lists members but takes them from the %s", r.Key, IdentityMembersConsole)
			}
		case IdentityMembersDeclared:
			seen := map[string]bool{}
			for _, m := range r.Members {
				if !identityEmailRE.MatchString(m) {
					return bad("role %q: member %q is not an email address", r.Key, m)
				}
				if seen[m] {
					return bad("role %q: member %q listed twice", r.Key, m)
				}
				seen[m] = true
			}
		default:
			return bad("role %q: members_from %q (expected %s or %s)", r.Key, r.MembersFrom, IdentityMembersConsole, IdentityMembersDeclared)
		}
	}
	knownRole := func(what, who string, in []string) error {
		for _, role := range in {
			if _, ok := roles[role]; !ok {
				return bad("%s %q holds role %q, which is not declared", what, who, role)
			}
		}
		return nil
	}

	apps := map[string]bool{}
	for _, a := range i.Applications {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Workload) == "" {
			return bad("an application needs a name and a workload")
		}
		if a.Kind != IdentityAppNativeBroker && a.Kind != IdentityAppOIDCWeb {
			return bad("application %q: kind %q (expected %s or %s)", a.Name, a.Kind, IdentityAppNativeBroker, IdentityAppOIDCWeb)
		}
		if apps[a.Name] {
			return bad("application %q declared twice", a.Name)
		}
		apps[a.Name] = true
	}

	consumers := map[string]bool{}
	for _, c := range i.Consumers {
		if strings.TrimSpace(c.Workload) == "" {
			return bad("a consumer needs a workload")
		}
		if c.Application != "" && !apps[c.Application] {
			return bad("consumer %q names application %q, which is not declared", c.Workload, c.Application)
		}
		k := c.Workload + "\x00" + c.Application
		if consumers[k] {
			return bad("consumer %q declared twice", c.Workload)
		}
		consumers[k] = true
		for output, field := range c.Fields {
			if !containsString(IdentityOutputs, output) {
				return bad("consumer %q maps output %q (expected one of %s)", c.Workload, output, strings.Join(IdentityOutputs, ", "))
			}
			if !identityFieldRE.MatchString(field) {
				return bad("consumer %q maps %q to %q, which is not a config field name", c.Workload, output, field)
			}
		}
		omitted := map[string]bool{}
		for _, output := range c.Omit {
			if !containsString(IdentityOmittableOutputs, output) {
				return bad("consumer %q omits output %q (only %s may be omitted; deliver_only projects nothing)", c.Workload, output, strings.Join(IdentityOmittableOutputs, ", "))
			}
			if omitted[output] {
				return bad("consumer %q omits output %q twice", c.Workload, output)
			}
			omitted[output] = true
			if _, mapped := c.Fields[output]; mapped {
				return bad("consumer %q both maps and omits output %q", c.Workload, output)
			}
		}
	}

	machines := map[string]bool{}
	for _, m := range i.MachineUsers {
		if strings.TrimSpace(m.Name) == "" {
			return bad("a machine user needs a name")
		}
		if machines[m.Name] {
			return bad("machine user %q declared twice", m.Name)
		}
		machines[m.Name] = true
		if err := knownRole("machine user", m.Name, m.Roles); err != nil {
			return err
		}
		if k := m.KeySecret; k != nil {
			if strings.TrimSpace(k.Name) == "" || strings.TrimSpace(k.Namespace) == "" || strings.TrimSpace(k.Cluster) == "" || strings.TrimSpace(k.ConnectedCluster) == "" {
				return bad("machine user %q: key_secret needs a name, a namespace, a cluster and a connected cluster", m.Name)
			}
		} else if i.Runtime == IdentityRuntimeHosted {
			return bad("machine user %q: a hosted instance delivers the key to a Secret, so key_secret is required", m.Name)
		}
	}

	if s := i.SMTP; s != nil {
		switch {
		case strings.TrimSpace(s.Host) == "":
			return bad("smtp has no host")
		case s.Port < 1 || s.Port > 65535:
			return bad("smtp port %d is not 1..65535", s.Port)
		case strings.TrimSpace(s.Sender) == "":
			return bad("smtp has no sender")
		case !managedSecretRE.MatchString(s.PasswordSecret):
			return bad("smtp password_secret %q must be a managed-secret NAME, never the password", s.PasswordSecret)
		}
	}

	seeds := map[string]bool{}
	for _, u := range i.SeedUsers {
		if !identityEmailRE.MatchString(u.Email) {
			return bad("seed user %q is not an email address", u.Email)
		}
		if seeds[u.Email] {
			return bad("seed user %q declared twice", u.Email)
		}
		seeds[u.Email] = true
		if err := knownRole("seed user", u.Email, u.Roles); err != nil {
			return err
		}
		if u.PasswordSecret != "" && !managedSecretRE.MatchString(u.PasswordSecret) {
			return bad("seed user %q: password_secret %q must be a secret NAME, never the password", u.Email, u.PasswordSecret)
		}
	}

	if i.ShareWith != "" {
		switch {
		case !identityNameRE.MatchString(i.ShareWith):
			return bad("share_with %q must be the bare name of an env of the same project", i.ShareWith)
		case i.Runtime != IdentityRuntimeHosted:
			return bad("share_with needs a hosted instance to share")
		case len(i.Roles) > 0, i.SMTP != nil, len(i.SeedUsers) > 0, len(i.MachineUsers) > 0, i.Domain != "", i.ProjectName != "":
			return bad("share_with reuses %q's instance, so roles, smtp, seed_users, machine_users, domain and project_name belong to the owner and must be empty here", i.ShareWith)
		}
	}

	if err := i.validateSignIn(roles, bad); err != nil {
		return err
	}

	provided := map[string]ShapeControlPlaneSecret{}
	for _, c := range controlPlaneSecrets {
		provided[c.Key()] = c
	}
	delivered := map[string]bool{}
	for _, d := range i.Deliveries {
		if !IsIdentityKind(d.Kind) {
			return bad("delivery %s has kind %q, which is not an identity kind", d.key(), d.Kind)
		}
		if delivered[d.key()] {
			return bad("delivery %s listed twice", d.key())
		}
		delivered[d.key()] = true
		c, ok := provided[d.key()]
		if !ok || c.Kind != d.Kind {
			return bad("delivery %s (%s) is not among the shape's control-plane secrets: the delivery path would never see it", d.key(), d.Kind)
		}
	}

	if len(i.ManagedSecrets) > 0 && i.Runtime != IdentityRuntimeHosted {
		return bad("managed_secrets are what a hosted instance writes for hosted consumers; this instance runs on the %s", i.Runtime)
	}
	seenManaged := map[string]bool{}
	for _, n := range i.ManagedSecrets {
		if !managedSecretRE.MatchString(n) || !strings.HasPrefix(n, IdentityManagedSecretPrefix) {
			return bad("managed secret %q must be a name starting %s", n, IdentityManagedSecretPrefix)
		}
		if seenManaged[n] {
			return bad("managed secret %q listed twice", n)
		}
		seenManaged[n] = true
	}
	return nil
}

// validateSignIn re-checks the v2 sign-in fields: rules V1-V15 of the
// identity v2 contract, kept identical to the KCL checks (V15's "names a
// mailpit HostInfra" needs Bundle.infra and is KCL's alone; the shape records
// only the resolved name). roles is the declaration's roles by key.
func (i ShapeIdentity) validateSignIn(roles map[string]ShapeIdentityRole, bad func(string, ...any) error) error {
	consoleRole := func(key string) bool {
		r, ok := roles[key]
		return ok && r.MembersFrom == IdentityMembersConsole
	}
	methods := i.SignInMethods()

	// V7, V8: owners.
	seenOwner := map[string]bool{}
	for _, o := range i.Owners {
		if !identityEmailRE.MatchString(o) {
			return bad("owner %q is not an email address", o)
		}
		if seenOwner[strings.ToLower(o)] {
			return bad("owner %q listed twice (compared case-insensitively)", o)
		}
		seenOwner[strings.ToLower(o)] = true
	}
	switch {
	case len(i.Owners) == 0 && i.OwnerRole != "":
		return bad("owner_role %q is set without owners", i.OwnerRole)
	case len(i.Owners) > 0 && !consoleRole(i.OwnerRole):
		return bad("owner_role %q must be a declared role with members_from %s: owners are never demoted, which a declared (converged) role cannot promise", i.OwnerRole, IdentityMembersConsole)
	}

	// V1, V2, V3: signup.
	if s := i.Signup; s != nil {
		switch s.Mode {
		case IdentitySignupInvite, IdentitySignupOpen, IdentitySignupDomain:
		default:
			return bad("signup mode %q (expected %s, %s or %s)", s.Mode, IdentitySignupInvite, IdentitySignupOpen, IdentitySignupDomain)
		}
		if (s.Mode == IdentitySignupDomain) != (len(s.Domains) > 0) {
			return bad("signup mode %s needs domains, and domains need mode %s", IdentitySignupDomain, IdentitySignupDomain)
		}
		seen := map[string]bool{}
		for _, d := range s.Domains {
			if !identityDomainRE.MatchString(d) {
				return bad("signup domain %q is not a lower-case DNS name (no wildcard: a subdomain matches only if listed itself)", d)
			}
			if seen[d] {
				return bad("signup domain %q listed twice", d)
			}
			seen[d] = true
		}
		if s.Role != "" && !consoleRole(s.Role) {
			return bad("signup role %q must be a declared role with members_from %s: a declared role is a converged set, so self-signups would be revoked", s.Role, IdentityMembersConsole)
		}
	}

	// V4: methods.
	seenMethod := map[string]bool{}
	for _, m := range i.Methods {
		switch m {
		case IdentityMethodPassword, IdentityMethodPasskey, IdentityMethodMagicLink:
		default:
			return bad("method %q (expected %s, %s or %s)", m, IdentityMethodPassword, IdentityMethodPasskey, IdentityMethodMagicLink)
		}
		if seenMethod[m] {
			return bad("method %q listed twice", m)
		}
		seenMethod[m] = true
	}
	if len(methods)+len(i.Social) == 0 {
		return bad("no sign-in method and no social provider: someone must be able to sign in")
	}
	// V5, V6: what needs mail.
	if containsString(methods, IdentityMethodMagicLink) && !i.MailAvailable() {
		return bad("method %s is an email, but the instance can send no mail", IdentityMethodMagicLink)
	}
	if i.SignupMode() != IdentitySignupInvite && !i.MailAvailable() {
		return bad("signup %s verifies every new address by email, but the instance can send no mail", i.SignupMode())
	}

	// V9, V10: social.
	seenSocial := map[string]bool{}
	for _, s := range i.Social {
		if s.Type != IdentitySocialGoogle && s.Type != IdentitySocialGitHub {
			return bad("social type %q (expected %s or %s)", s.Type, IdentitySocialGoogle, IdentitySocialGitHub)
		}
		if seenSocial[s.Type] {
			return bad("social %s declared twice", s.Type)
		}
		seenSocial[s.Type] = true
		switch {
		case (strings.TrimSpace(s.ClientID) == "") == (s.ClientIDSecret == ""):
			return bad("social %s: give exactly one of client_id (a literal) and client_id_secret (a managed-secret name)", s.Type)
		case s.ClientIDSecret != "" && !managedSecretRE.MatchString(s.ClientIDSecret):
			return bad("social %s: client_id_secret %q must be a managed-secret NAME", s.Type, s.ClientIDSecret)
		case !managedSecretRE.MatchString(s.ClientSecretSecret):
			return bad("social %s: client_secret_secret %q must be a managed-secret NAME, never the secret", s.Type, s.ClientSecretSecret)
		case len(s.Scopes) == 0:
			return bad("social %s has no scopes", s.Type)
		}
	}

	// V11: passkey relying party.
	if i.PasskeyRPID != "" {
		if !identityDomainRE.MatchString(i.PasskeyRPID) {
			return bad("passkey_rp_id %q is not a lower-case DNS name", i.PasskeyRPID)
		}
		if !containsString(methods, IdentityMethodPasskey) {
			return bad("passkey_rp_id is set but the methods do not offer %s", IdentityMethodPasskey)
		}
	}

	// V12, V13: mail addressing.
	if e := i.Email; e != nil {
		switch {
		case e.Delivery != IdentityMailPlatform && e.Delivery != IdentityMailNone:
			return bad("email delivery %q (expected %s or %s)", e.Delivery, IdentityMailPlatform, IdentityMailNone)
		case e.FromLocal != "" && !identityFromLocalRE.MatchString(e.FromLocal):
			return bad("email from_local %q must match %s", e.FromLocal, identityFromLocalRE)
		case len(e.FromName) > 64 || strings.ContainsAny(e.FromName, "\r\n<>"):
			return bad("email from_name must be at most 64 characters with no CR, LF, '<' or '>'")
		case e.ReplyTo != "" && !identityEmailRE.MatchString(e.ReplyTo):
			return bad("email reply_to %q is not an email address", e.ReplyTo)
		case i.SMTP != nil && e.FromLocal != "":
			return bad("email from_local is set with smtp, whose sender is the From")
		}
	}

	// V14: a sharer's sign-in belongs to the owner.
	if i.ShareWith != "" && (len(i.Owners) > 0 || i.OwnerRole != "" || i.Signup != nil || i.Methods != nil || i.PasskeyRPID != "" || len(i.Social) > 0 || i.Email != nil) {
		return bad("share_with reuses %q's instance, so owners, signup, methods, passkey_rp_id, social and email belong to the owner and must be at their defaults here", i.ShareWith)
	}

	// V15: the dev mail server.
	if i.DevMail != "" {
		if i.Runtime != IdentityRuntimeHost {
			return bad("dev_mail names a mailpit HostInfra, which only a host-run instance uses; this instance runs on the %s", i.Runtime)
		}
		if !identityNameRE.MatchString(i.DevMail) {
			return bad("dev_mail %q must be the name of a HostInfra (an RFC-1123 label)", i.DevMail)
		}
	}
	return nil
}

// Issuer slugs.
//
// An instance's issuer is https://<slug>.reliantidentity.com, and an issuer is
// the one thing a deployed app cannot change without invalidating every token
// it has issued. So the slug is ASSIGNED ONCE, by the control plane, when the
// env's instance is first provisioned, and stored: nothing recomputes it
// afterwards, which is what makes a later env rename harmless. forge records
// the declaration, not the slug; this is the rule the control plane assigns
// by, kept here so every program that predicts or checks one agrees.
//
//	<project>-<env>-<hash>
//
// The two labels make an issuer readable in a log line. The hash makes it
// unique: the first N hex digits of sha256(envID), where envID is the control
// plane's immutable id for the env (never its name, which can be reused after
// a delete). A label is lower-case [a-z0-9] with every other run folded to one
// '-', at most 24 characters. N starts at IdentitySlugMinHash; the control
// plane's unique index decides uniqueness, and on a collision it retries with
// the next N, so uniqueness never rests on the hash's luck. The longest slug is
// 24+1+24+1+12 = 62 characters, inside the 63 of a DNS label.
const (
	identitySlugLabelMax = 24
	// IdentitySlugMinHash is the hash length an assignment starts at.
	IdentitySlugMinHash = 6
	// IdentitySlugMaxHash is the longest hash a retry reaches.
	IdentitySlugMaxHash = 12
	identitySlugMax     = 63
)

// IdentitySlug is the issuer slug for an env: <project>-<env>-<hash>. hashChars
// is clamped to [IdentitySlugMinHash, IdentitySlugMaxHash]. The result is
// always a valid DNS label ([ValidIdentitySlug]).
func IdentitySlug(envID, project, env string, hashChars int) string {
	hashChars = max(IdentitySlugMinHash, min(hashChars, IdentitySlugMaxHash))
	sum := sha256.Sum256([]byte(envID))
	return slugLabel(project) + "-" + slugLabel(env) + "-" + hex.EncodeToString(sum[:])[:hashChars]
}

func slugLabel(s string) string {
	s = strings.Trim(slugLabelRunRE.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "x"
	}
	if len(s) > identitySlugLabelMax {
		s = strings.Trim(s[:identitySlugLabelMax], "-")
	}
	return s
}

// ValidIdentitySlug reports whether s is a slug [IdentitySlug] could have
// produced: a DNS label of at most 63 characters.
func ValidIdentitySlug(s string) bool {
	return len(s) <= identitySlugMax && identityNameRE.MatchString(s)
}

// IdentityIssuer is the issuer of a slug: https://<slug>.reliantidentity.com.
func IdentityIssuer(slug string) string {
	return "https://" + slug + "." + IdentityDomain
}
