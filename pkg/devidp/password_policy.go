package devidp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The issuer's password-complexity policy: what a password must contain, so a
// sign-up or reset form can say so up front instead of answering "that
// password does not meet the requirements" after the fact.
//
// ── Which endpoint, and why ─────────────────────────────────────────────
//
// `GET /v2/settings/password/complexity` (the Settings API). It is the call
// Zitadel's own login UI makes, it is readable with the login broker's
// credential (verified against v4.19.5: the broker token, an IAM_LOGIN_CLIENT
// service account, gets 200), and it returns the INSTANCE policy — which is
// what every instance forge provisions enforces, because forge declares no
// per-org override. The management API (`/management/v1/policies/password/
// complexity`) answers 200 for the same token and carries the same numbers
// plus an `isDefault` flag, but it is org-scoped and needs the org context a
// browser-facing broker has no reason to hold.
//
// The policy is GUIDANCE. The issuer enforces it when the password is set and
// stays the authority: if an org overrides the instance policy, the form
// shows the instance rules up front and the rejection (see PasswordPolicyError)
// still names what the issuer actually refused.

// PasswordRule names one clause of the complexity policy. The values are the
// wire spelling the scaffolded frontend reads, so they are part of the
// contract between a broker and its sign-up form.
type PasswordRule string

const (
	RuleMinLength PasswordRule = "min_length"
	RuleUppercase PasswordRule = "uppercase"
	RuleLowercase PasswordRule = "lowercase"
	RuleNumber    PasswordRule = "number"
	RuleSymbol    PasswordRule = "symbol"
)

// PasswordPolicy is the issuer's password-complexity policy. The JSON tags are
// the shape the scaffolded broker serves at GET /auth/password-policy.
type PasswordPolicy struct {
	MinLength    int  `json:"min_length"`
	HasUppercase bool `json:"has_uppercase"`
	HasLowercase bool `json:"has_lowercase"`
	HasNumber    bool `json:"has_number"`
	HasSymbol    bool `json:"has_symbol"`
}

// PasswordPolicy reads the instance's password-complexity policy.
func (c *Client) PasswordPolicy(ctx context.Context) (PasswordPolicy, error) {
	body, err := c.getJSON(ctx, "/v2/settings/password/complexity")
	if err != nil {
		return PasswordPolicy{}, fmt.Errorf("read password policy: %w", err)
	}
	// minLength is an int64, which the gateway renders as a JSON string.
	// json.Number accepts both spellings.
	var out struct {
		Settings struct {
			MinLength         json.Number `json:"minLength"`
			RequiresUppercase bool        `json:"requiresUppercase"`
			RequiresLowercase bool        `json:"requiresLowercase"`
			RequiresNumber    bool        `json:"requiresNumber"`
			RequiresSymbol    bool        `json:"requiresSymbol"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return PasswordPolicy{}, fmt.Errorf("decode password policy: %w", err)
	}
	s := out.Settings
	min := 0
	if s.MinLength != "" {
		n, err := s.MinLength.Int64()
		if err != nil {
			return PasswordPolicy{}, fmt.Errorf("decode password policy: minLength %q: %w", s.MinLength, err)
		}
		min = int(n)
	}
	return PasswordPolicy{
		MinLength:    min,
		HasUppercase: s.RequiresUppercase,
		HasLowercase: s.RequiresLowercase,
		HasNumber:    s.RequiresNumber,
		HasSymbol:    s.RequiresSymbol,
	}, nil
}

// Unmet returns every rule of the policy the password breaks, in a stable
// order (length, uppercase, lowercase, number, symbol).
//
// The issuer reports only the FIRST rule a password breaks — "Sh0rt!" is
// answered with "too short" alone, though it also lacks nothing else, and
// "short" is answered with "too short" though it also lacks an uppercase
// letter, a number and a symbol. Telling the person all of it at once saves
// them a round trip per rule, which is why the broker evaluates the policy
// itself on a rejection rather than relaying the one message.
//
// The classes mirror the issuer's: A-Z, a-z, 0-9, and a symbol is anything
// else (a space counts — verified against v4.19.5). This is guidance; the
// issuer decides.
func (p PasswordPolicy) Unmet(password string) []PasswordRule {
	var unmet []PasswordRule
	if p.MinLength > 0 && utf8.RuneCountInString(password) < p.MinLength {
		unmet = append(unmet, RuleMinLength)
	}
	var upper, lower, digit, symbol bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	if p.HasUppercase && !upper {
		unmet = append(unmet, RuleUppercase)
	}
	if p.HasLowercase && !lower {
		unmet = append(unmet, RuleLowercase)
	}
	if p.HasNumber && !digit {
		unmet = append(unmet, RuleNumber)
	}
	if p.HasSymbol && !symbol {
		unmet = append(unmet, RuleSymbol)
	}
	return unmet
}

// Requirement renders one rule as the noun phrase a sentence can list:
// "at least 8 characters", "an uppercase letter".
func (p PasswordPolicy) Requirement(rule PasswordRule) string {
	switch rule {
	case RuleMinLength:
		return fmt.Sprintf("at least %d characters", p.MinLength)
	case RuleUppercase:
		return "an uppercase letter"
	case RuleLowercase:
		return "a lowercase letter"
	case RuleNumber:
		return "a number"
	case RuleSymbol:
		return "a symbol"
	}
	return string(rule)
}

// Describe joins the requirements for rules into one sentence fragment:
// "at least 8 characters, an uppercase letter and a number".
func (p PasswordPolicy) Describe(rules []PasswordRule) string {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		parts = append(parts, p.Requirement(r))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// policyRuleByID maps the stable tail of Zitadel's complexity-rejection error
// ids to the rule they report. Matched on the tail because the prefix names
// the package that raised the error and differs by call: setting a password
// raises DOMAIN-HuJf6 (verified v4.19.4), creating a user with one raises
// COMMA-HuJf6 (verified v4.19.5). The tail is what is stable.
var policyRuleByID = map[string]PasswordRule{
	"HuJf6": RuleMinLength,
	"VoaRj": RuleUppercase,
	"co3Xw": RuleLowercase,
	"ZBv4H": RuleNumber,
	"ZDLwA": RuleSymbol,
}

// policyErrorFrom reports whether err is the issuer refusing a password for
// its complexity, and as which rule.
func policyErrorFrom(err *apiError) *PasswordPolicyError {
	id, message := err.errorID()
	if id == "" {
		return nil
	}
	tail := id
	if i := strings.IndexByte(id, '-'); i >= 0 {
		tail = id[i+1:]
	}
	if rule, ok := policyRuleByID[tail]; ok {
		return &PasswordPolicyError{Rule: rule, Reason: message}
	}
	// A rule this table does not know yet (a newer Zitadel adding one):
	// still a policy rejection, just not one with a name.
	if strings.HasPrefix(id, policyErrorPrefix) {
		return &PasswordPolicyError{Reason: message}
	}
	return nil
}
