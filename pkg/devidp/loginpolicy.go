package devidp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/idna"
)

// The LOGIN_POLICY document: which sign-in methods and which sign-up mode a
// login broker enforces.
//
// The broker cannot read the identity instance's settings itself (a hosted
// instance's gateway routes only the sign-in APIs), and the ids of the social
// providers exist only after the instance was converged. So whoever converges
// the instance — the control plane for a hosted one, `forge env up` for the dev
// one — DELIVERS this document, and the broker reads it from its
// idp_login_policy config field.
//
// ABSENT IS NORMAL. An app whose identity declares nothing new, or whose
// control plane predates the document, gets none. The broker then serves the
// legacy policy: password only, sign-up only where it always was (development,
// or auth_self_signup). A document that is PRESENT but unreadable — malformed,
// or a version this package does not know — also yields the legacy policy,
// with an error. The broker must then fail CLOSED, not back to the legacy
// switches: whatever the document restricted (invite-only, verified email) is
// unknown, so the scaffolded broker turns sign-up off and requires a verified
// address until the document is fixed. Never open to something nobody
// converged, and never more open than what was.

// LoginPolicyVersion is the only LOGIN_POLICY version this package reads.
const LoginPolicyVersion = 1

// Signup modes.
const (
	SignupInvite = "invite"
	SignupOpen   = "open"
	SignupDomain = "domain"
)

// First-factor methods.
const (
	MethodPassword  = "password"
	MethodPasskey   = "passkey"
	MethodMagicLink = "magic_link"
)

// InviteMethodSocial is the method an invitee names to accept an invitation
// by connecting one of the policy's social accounts. It is not a first-factor
// method of the document: the social buttons are its Social list.
const InviteMethodSocial = "social"

// Mail delivery states the document reports.
const (
	MailPlatform = "platform"
	MailSMTP     = "smtp"
	MailDev      = "dev"
	MailSandbox  = "sandbox"
	MailPaused   = "paused"
	MailNone     = "none"
)

// LoginPolicy is the parsed document. The zero value is NOT the legacy
// policy; use LegacyLoginPolicy.
type LoginPolicy struct {
	// Present is false for the legacy policy: no document, or one that
	// could not be read.
	Present bool `json:"-"`

	Version       int                 `json:"version"`
	Signup        string              `json:"signup"`
	SignupDomains []string            `json:"signup_domains"`
	SignupRole    string              `json:"signup_role"`
	Methods       []string            `json:"methods"`
	PasskeyRPID   string              `json:"passkey_rp_id"`
	Social        []LoginPolicySocial `json:"social"`
	Mail          string              `json:"mail"`
	// RequireVerifiedEmail: no session is issued for an unverified address.
	RequireVerifiedEmail bool `json:"require_verified_email"`
	// Password is the instance's complexity policy as last converged, for
	// the sign-up checklist. Nil when the document carries none.
	Password *LoginPolicyPassword `json:"password,omitempty"`
}

// LoginPolicySocial is one social sign-in button.
type LoginPolicySocial struct {
	Type  string `json:"type"`
	IdPID string `json:"idp_id"`
	Label string `json:"label"`
}

// LoginPolicyPassword is the password complexity a sign-up form checks.
type LoginPolicyPassword struct {
	MinLength int  `json:"min_length"`
	Uppercase bool `json:"uppercase"`
	Lowercase bool `json:"lowercase"`
	Number    bool `json:"number"`
	Symbol    bool `json:"symbol"`
}

// LegacyLoginPolicy is the policy every broker served before the document
// existed: password only, invite-only (the broker's own development and
// auth_self_signup switches decide sign-up), no verified-email gate.
func LegacyLoginPolicy() LoginPolicy {
	return LoginPolicy{Signup: SignupInvite, Methods: []string{MethodPassword}}
}

// ErrLoginPolicyUnreadable wraps every reason a present document was not used.
var ErrLoginPolicyUnreadable = errors.New("the LOGIN_POLICY document could not be used; serving the legacy policy (password only, invite-only)")

// ParseLoginPolicy reads the idp_login_policy value.
//
// Empty (or whitespace) is the legacy policy with a nil error. Anything that
// is not a valid version-1 document is the legacy policy AND an error
// wrapping ErrLoginPolicyUnreadable, which the caller logs at ERROR.
func ParseLoginPolicy(raw string) (LoginPolicy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return LegacyLoginPolicy(), nil
	}
	var p LoginPolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return LegacyLoginPolicy(), fmt.Errorf("%w: %v", ErrLoginPolicyUnreadable, err)
	}
	if p.Version != LoginPolicyVersion {
		return LegacyLoginPolicy(), fmt.Errorf("%w: version %d (this broker reads version %d)", ErrLoginPolicyUnreadable, p.Version, LoginPolicyVersion)
	}
	if err := p.validate(); err != nil {
		return LegacyLoginPolicy(), fmt.Errorf("%w: %v", ErrLoginPolicyUnreadable, err)
	}
	for i, d := range p.SignupDomains {
		p.SignupDomains[i] = normalizeDomain(d)
	}
	p.Present = true
	return p, nil
}

func (p LoginPolicy) validate() error {
	switch p.Signup {
	case SignupInvite, SignupOpen:
	case SignupDomain:
		if len(p.SignupDomains) == 0 {
			return fmt.Errorf("signup %q lists no signup_domains", p.Signup)
		}
	default:
		return fmt.Errorf("unknown signup mode %q", p.Signup)
	}
	for _, m := range p.Methods {
		switch m {
		case MethodPassword, MethodPasskey, MethodMagicLink:
		default:
			return fmt.Errorf("unknown method %q", m)
		}
	}
	for _, s := range p.Social {
		if s.Type == "" || s.IdPID == "" {
			return fmt.Errorf("a social entry has no type or idp_id")
		}
	}
	return nil
}

// Offers reports whether method is one the sign-in screen offers.
func (p LoginPolicy) Offers(method string) bool {
	for _, m := range p.Methods {
		if m == method {
			return true
		}
	}
	return false
}

// SelfSignup reports whether the document lets anyone create an account
// from the sign-in screen (open, or domain-gated).
func (p LoginPolicy) SelfSignup() bool {
	return p.Present && (p.Signup == SignupOpen || p.Signup == SignupDomain)
}

// RegisterServed reports whether the sign-up FORM (POST /auth/register) can
// end in a session under this policy, which is when it is offered at all.
// Sign-up by mail needs mail that arrives. Where none does (mail "none" or
// "paused"), only an open sign-up with a password and no verified-email gate
// can complete: the account is signed in at once, as before the document. A
// domain sign-up never runs without mail, because a typed address proves no
// domain. Social sign-up is not the form, and SelfSignup governs it.
func (p LoginPolicy) RegisterServed() bool {
	switch {
	case !p.SelfSignup():
		return false
	case p.MailDelivers():
		return true
	}
	return p.Signup == SignupOpen && !p.RequireVerifiedEmail && p.Offers(MethodPassword)
}

// SignupAllows reports whether an address may create an account under this
// policy. In domain mode its domain must be LISTED: a sub-domain matches only
// if it is listed itself. Case-insensitive, after IDNA normalisation.
//
// Call it with an address that is (or is about to be) VERIFIED. A typed
// address proves nothing; the verification that follows does, and the
// session gate refuses the account until it has happened.
func (p LoginPolicy) SignupAllows(email string) bool {
	switch {
	case !p.SelfSignup():
		return false
	case p.Signup == SignupOpen:
		return true
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	domain := normalizeDomain(email[at+1:])
	for _, d := range p.SignupDomains {
		if d != "" && d == domain {
			return true
		}
	}
	return false
}

// SocialByType returns the button for a provider type.
func (p LoginPolicy) SocialByType(typ string) (LoginPolicySocial, bool) {
	for _, s := range p.Social {
		if s.Type == typ {
			return s, true
		}
	}
	return LoginPolicySocial{}, false
}

// SocialByIdPID returns the button whose identity-provider id is id.
func (p LoginPolicy) SocialByIdPID(id string) (LoginPolicySocial, bool) {
	for _, s := range p.Social {
		if s.IdPID == id {
			return s, true
		}
	}
	return LoginPolicySocial{}, false
}

// MailDelivers reports whether the instance's mail reaches anyone: the
// relay, a declared SMTP server, the dev mail catcher, or the sandbox (where
// an org admin reads it). Paused and none deliver nothing.
func (p LoginPolicy) MailDelivers() bool {
	switch p.Mail {
	case MailPlatform, MailSMTP, MailDev, MailSandbox:
		return true
	}
	return false
}

// PasswordPolicy returns the document's complexity policy in the shape the
// sign-up checklist and the rejection body use; ok is false when it carries
// none.
func (p LoginPolicy) PasswordPolicy() (PasswordPolicy, bool) {
	if p.Password == nil {
		return PasswordPolicy{}, false
	}
	return PasswordPolicy{
		MinLength:    p.Password.MinLength,
		HasUppercase: p.Password.Uppercase,
		HasLowercase: p.Password.Lowercase,
		HasNumber:    p.Password.Number,
		HasSymbol:    p.Password.Symbol,
	}, true
}

// normalizeDomain lower-cases a DNS name and converts it to its ASCII
// (punycode) form, so "Bücher.Example" and "xn--bcher-kva.example" compare
// equal. A name IDNA refuses is compared lower-cased as typed, which matches
// nothing a declaration could list.
func normalizeDomain(d string) string {
	d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
	if a, err := idna.Lookup.ToASCII(d); err == nil {
		return a
	}
	return d
}
