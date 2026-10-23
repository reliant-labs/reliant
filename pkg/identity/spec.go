package identity

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Spec is the DECLARED content of one Zitadel instance. Reconcile makes the
// instance match it. Objects are matched by natural key (project name, role
// key, application name, username, email), never by internal id.
type Spec struct {
	Project      ProjectSpec
	Roles        []Role
	Applications []Application
	MachineUsers []MachineUser
	Policies     Policies
	// SMTP is the instance mail relay. Nil leaves the instance's mail
	// configuration alone.
	SMTP *SMTP
	// SeedUsers are throwaway accounts for local and ephemeral environments.
	// Reconcile refuses them unless Options.AllowSeedUsers is set.
	SeedUsers []SeedUser
}

// ProjectSpec is the Zitadel project. Its generated id is the token audience,
// which is why it is an OUTPUT (Result.Audience) and not something a spec can
// choose.
type ProjectSpec struct {
	// Name is the project's natural key.
	Name string
	// AdoptID pins the id the project must have. See the package comment.
	AdoptID string
	// MustHoldRole makes Zitadel refuse tokens for users who hold no role in the
	// project; ProjectCheck refuses tokens for users whose org was not granted
	// the project. Both default to off.
	MustHoldRole bool
	ProjectCheck bool
}

// Role is a project role. Roles are only ever created and relabelled, never
// deleted: a role may have grants, and removing it silently strips them.
type Role struct {
	Key         string
	DisplayName string
	Group       string
	// Declared makes the holders of this role a SET converged from the spec:
	// the humans in Members plus the machine users that list the role. A holder
	// not in that set loses THIS role (other roles in the same grant are kept).
	// Leave it false for a role whose members are managed in a console.
	Declared bool
	// Members are the emails that hold the role when Declared is set.
	Members []string
}

// AppKind selects the OIDC application shape.
type AppKind string

const (
	// AppSPA is a browser single-page app: a public client using PKCE.
	AppSPA AppKind = "spa"
	// AppWeb is a confidential server-side web app with a client secret,
	// which is generated once and held in the KeyStore.
	AppWeb AppKind = "web"
	// AppNative is a mobile or desktop app: a public client using PKCE.
	AppNative AppKind = "native"
	// AppNativeBroker is the app behind native sign-in: the product's own
	// login form, with a server-side broker that drives the OIDC flow. It is a
	// public PKCE client whose login pages are LoginURI, plus a broker machine
	// account holding IAM_LOGIN_CLIENT and an expiring access token.
	AppNativeBroker AppKind = "native_broker"
)

// Application is an OIDC application registered in the project.
type Application struct {
	// Name is the application's natural key.
	Name string
	Kind AppKind

	// RedirectURIs and PostLogoutURIs are compared LITERALLY by the issuer, so
	// a changed host silently breaks the callback until this converges. With
	// DevMode they are globs.
	RedirectURIs   []string
	PostLogoutURIs []string

	// LoginURI is the app's own sign-in route. When set the issuer redirects
	// /oauth/v2/authorize INTO the app (login V2) instead of rendering its own
	// pages. It is set per application, so two apps sharing an instance each
	// point at their own route. Empty restores the issuer-hosted login.
	LoginURI string

	// DevMode permits plain-http redirect URIs and turns them into globs. Only
	// for a loopback dev IdP.
	DevMode bool

	// IDTokenRoleAssertion / IDTokenUserinfoAssertion put the project roles
	// and the profile claims into the ID token. The access token always
	// carries the roles claim.
	IDTokenRoleAssertion     bool
	IDTokenUserinfoAssertion bool

	// AdoptClientID pins the client id the application must have; see the
	// package comment.
	AdoptClientID string

	// RegenerateSecret mints a new client secret for an AppWeb whose secret is
	// not in the KeyStore. It invalidates the previous secret, so it is never
	// done implicitly for an application that already existed.
	RegenerateSecret bool

	// Broker configures the broker account of an AppNativeBroker.
	Broker *BrokerAccount
}

// BrokerAccount is the machine account the login broker authenticates as. It is
// a privileged credential (IAM_LOGIN_CLIENT can complete an authorization
// request for any user of the instance), kept apart from the provisioning
// credential so the busiest surface of an app never holds the credential that
// administers the instance.
type BrokerAccount struct {
	// Username defaults to "broker-" plus a slug of the application name.
	Username string
}

// MachineUser is a service account.
type MachineUser struct {
	// Username is the natural key and a DNS-label-shaped name: it is part of a
	// secret name downstream.
	Username    string
	Name        string
	Description string
	// Roles are the project roles the account holds.
	Roles []string
	// InstanceRoles are instance-level memberships (for example
	// IAM_LOGIN_CLIENT). They are only ever added, never removed.
	InstanceRoles []string
	// Key issues a JSON machine key into the KeyStore and keeps it rotated.
	Key bool
	// Token issues an expiring personal access token into the KeyStore.
	Token bool
}

// Policies are the instance-level login policies. A nil policy is left alone.
type Policies struct {
	Login              *LoginPolicy
	PasswordComplexity *PasswordComplexity
	Lockout            *Lockout
}

// LoginPolicy is the instance login policy. Fields not exposed here (check
// lifetimes, factor lists) keep their current values.
type LoginPolicy struct {
	AllowUsernamePassword bool
	AllowRegister         bool
	AllowExternalIDP      bool
	ForceMFA              bool
	HidePasswordReset     bool
	AllowDomainDiscovery  bool
	// PasswordlessType is "PASSWORDLESS_TYPE_ALLOWED" or
	// "PASSWORDLESS_TYPE_NOT_ALLOWED"; empty means not allowed.
	PasswordlessType string
	// MFAInitSkipLifetime is how long a user may skip enrolling a second
	// factor; zero never prompts.
	MFAInitSkipLifetime time.Duration
}

// PasswordComplexity is the instance password complexity policy.
type PasswordComplexity struct {
	MinLength        int
	RequireUppercase bool
	RequireLowercase bool
	RequireNumber    bool
	RequireSymbol    bool
}

// Lockout is the instance lockout policy; zero disables that lockout.
type Lockout struct {
	MaxPasswordAttempts int
	MaxOTPAttempts      int
}

// SMTP is the instance mail relay.
type SMTP struct {
	// Host is host:port.
	Host          string
	User          string
	Password      string `json:"-"`
	SenderAddress string
	SenderName    string
	ReplyTo       string
	TLS           bool
}

// SeedUser is a throwaway account. Its password is set when the account is
// created and never again, so a user who changed it is not reset.
type SeedUser struct {
	Email      string
	Password   string `json:"-"`
	GivenName  string
	FamilyName string
	Roles      []string
}

// Rotation tunes credential lifetimes. Zero values take the defaults.
type Rotation struct {
	// BrokerTokenTTL is the lifetime of a minted broker token (default 30d); a
	// new one is minted once less than BrokerRenewWithin (default 10d) is left.
	BrokerTokenTTL    time.Duration
	BrokerRenewWithin time.Duration
	// BrokerOverlap is how long a superseded token keeps working (default 10d).
	BrokerOverlap time.Duration
	// MachineKeyTTL (default 90d), MachineKeyRenewWithin (default 30d) and
	// MachineKeyOverlap (default 7d) are the same three for machine keys.
	MachineKeyTTL         time.Duration
	MachineKeyRenewWithin time.Duration
	MachineKeyOverlap     time.Duration
}

const day = 24 * time.Hour

func (r Rotation) withDefaults() Rotation {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&r.BrokerTokenTTL, 30*day)
	def(&r.BrokerRenewWithin, 10*day)
	def(&r.BrokerOverlap, 10*day)
	def(&r.MachineKeyTTL, 90*day)
	def(&r.MachineKeyRenewWithin, 30*day)
	def(&r.MachineKeyOverlap, 7*day)
	return r
}

var (
	roleKeyRE  = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,63}$`)
	usernameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{1,61}[a-z0-9]$`)
	emailRE    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
)

// ValidMachineUsername reports why name cannot be a machine user's username.
// It is deliberately narrower than Zitadel: the name becomes part of a secret
// name, must be a DNS-label fragment, and starting with a letter keeps it from
// ever equalling one of Zitadel's numeric ids.
func ValidMachineUsername(name string) error {
	if !usernameRE.MatchString(name) {
		return fmt.Errorf("machine username %q is invalid: 3-63 characters, starting with a letter, ending with a letter or digit, only lowercase letters, digits and '-'", name)
	}
	return nil
}

// BrokerUsername is the machine username of an application's broker account.
func (a Application) BrokerUsername() string {
	if a.Broker != nil && a.Broker.Username != "" {
		return a.Broker.Username
	}
	s := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(a.Name), "-"), "-")
	if len(s) > 50 {
		s = strings.Trim(s[:50], "-")
	}
	return "broker-" + s
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Validate checks the spec offline and reports every problem, each naming the
// object it concerns.
func (s Spec) Validate() error {
	var errs []error
	bad := func(kind, name, format string, args ...any) {
		errs = append(errs, objErr(kind, name, fmt.Errorf(format, args...)))
	}

	if strings.TrimSpace(s.Project.Name) == "" {
		bad("project", "", "a name is required")
	}

	roles := map[string]Role{}
	for _, r := range s.Roles {
		switch {
		case !roleKeyRE.MatchString(r.Key):
			bad("role", r.Key, "key must match %s", roleKeyRE)
		case roles[r.Key].Key != "":
			bad("role", r.Key, "declared twice")
		}
		roles[r.Key] = r
		if len(r.Members) > 0 && !r.Declared {
			bad("role", r.Key, "members are listed but the role is not Declared; set Declared or drop the members")
		}
		for _, m := range r.Members {
			if !emailRE.MatchString(m) {
				bad("role", r.Key, "member %q is not an email address", m)
			}
		}
	}
	knownRole := func(kind, name, role string) {
		if _, ok := roles[role]; !ok {
			bad(kind, name, "refers to role %q, which is not declared in Spec.Roles", role)
		}
	}

	apps := map[string]bool{}
	brokers := map[string]string{}
	for _, a := range s.Applications {
		if strings.TrimSpace(a.Name) == "" {
			bad("application", "", "a name is required")
			continue
		}
		if apps[a.Name] {
			bad("application", a.Name, "declared twice")
		}
		apps[a.Name] = true
		switch a.Kind {
		case AppSPA, AppWeb, AppNative, AppNativeBroker:
		default:
			bad("application", a.Name, "kind %q is not one of spa, web, native, native_broker", a.Kind)
		}
		if len(a.RedirectURIs) == 0 {
			bad("application", a.Name, "at least one redirect URI is required")
		}
		for _, u := range append(append([]string{}, a.RedirectURIs...), a.PostLogoutURIs...) {
			if !strings.Contains(u, "://") && !strings.Contains(u, ":/") {
				bad("application", a.Name, "uri %q is not absolute", u)
			}
		}
		if a.LoginURI != "" {
			if u, err := url.Parse(a.LoginURI); err != nil || u.Scheme == "" || u.Host == "" {
				bad("application", a.Name, "login URI %q is not an absolute URL", a.LoginURI)
			}
		}
		if a.Kind == AppNativeBroker {
			if a.LoginURI == "" {
				bad("application", a.Name, "a native_broker application needs a LoginURI: it is the app's own sign-in route")
			}
			bu := a.BrokerUsername()
			if err := ValidMachineUsername(bu); err != nil {
				bad("application", a.Name, "%v", err)
			}
			if other, dup := brokers[bu]; dup {
				bad("application", a.Name, "broker account %q is already the broker of %q", bu, other)
			}
			brokers[bu] = a.Name
		} else if a.Broker != nil {
			bad("application", a.Name, "a broker account is only for kind native_broker")
		}
		if a.RegenerateSecret && a.Kind != AppWeb {
			bad("application", a.Name, "RegenerateSecret is only for kind web")
		}
	}

	users := map[string]bool{}
	for _, m := range s.MachineUsers {
		if err := ValidMachineUsername(m.Username); err != nil {
			errs = append(errs, objErr("machine user", m.Username, err))
			continue
		}
		if users[m.Username] {
			bad("machine user", m.Username, "declared twice")
		}
		if _, clash := brokers[m.Username]; clash {
			bad("machine user", m.Username, "is also the broker account of application %q", brokers[m.Username])
		}
		users[m.Username] = true
		for _, r := range m.Roles {
			knownRole("machine user", m.Username, r)
		}
	}

	if p := s.Policies.PasswordComplexity; p != nil && (p.MinLength < 1 || p.MinLength > 72) {
		bad("password complexity policy", "", "minimum length %d is outside 1-72", p.MinLength)
	}
	if l := s.Policies.Lockout; l != nil && (l.MaxPasswordAttempts < 0 || l.MaxOTPAttempts < 0) {
		bad("lockout policy", "", "attempt limits cannot be negative")
	}
	if l := s.Policies.Login; l != nil {
		switch l.PasswordlessType {
		case "", "PASSWORDLESS_TYPE_ALLOWED", "PASSWORDLESS_TYPE_NOT_ALLOWED":
		default:
			bad("login policy", "", "passwordless type %q is not PASSWORDLESS_TYPE_ALLOWED or PASSWORDLESS_TYPE_NOT_ALLOWED", l.PasswordlessType)
		}
	}
	if m := s.SMTP; m != nil {
		if !strings.Contains(m.Host, ":") {
			bad("smtp", m.Host, "host must be host:port")
		}
		if !emailRE.MatchString(m.SenderAddress) {
			bad("smtp", m.Host, "sender address %q is not an email address", m.SenderAddress)
		}
	}

	seeds := map[string]bool{}
	for _, u := range s.SeedUsers {
		key := strings.ToLower(u.Email)
		switch {
		case !emailRE.MatchString(u.Email):
			bad("seed user", u.Email, "not an email address")
		case seeds[key]:
			bad("seed user", u.Email, "declared twice")
		case u.Password == "":
			bad("seed user", u.Email, "a password is required")
		}
		seeds[key] = true
		for _, r := range u.Roles {
			knownRole("seed user", u.Email, r)
		}
	}
	return errors.Join(errs...)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
