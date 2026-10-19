// Copyright (c) 2025 Reliant Labs
package providerusage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ClaudeIdentityKey is the dedupe key of a Claude subscription account:
// "acct:<account uuid>/org:<organization uuid>" (DESIGN §1.2). The organization
// is part of the key because limits and prompt cache are per organization: one
// login in two organizations is two accounts. Returns "" when either uuid is
// missing, so the caller falls back to its "legacy:" key until a refresh
// supplies both.
func ClaudeIdentityKey(accountUUID, organizationUUID string) string {
	a := strings.ToLower(strings.TrimSpace(accountUUID))
	o := strings.ToLower(strings.TrimSpace(organizationUUID))
	if a == "" || o == "" {
		return ""
	}
	return "acct:" + a + "/org:" + o
}

// APIKeyFingerprint is the 32-hex-char salted fingerprint of an API key:
// hex(sha256(userID ‖ 0 ‖ provider ‖ 0 ‖ key))[:32]. The user salt makes the
// value useless for correlating the same key across users. It is deterministic,
// so connecting the same key twice finds the same account.
func APIKeyFingerprint(userID, provider, key string) string {
	h := sha256.New()
	h.Write([]byte(userID))
	h.Write([]byte{0})
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// APIKeyIdentityKey is "key:" + APIKeyFingerprint, the stored identity key.
func APIKeyIdentityKey(userID, provider, key string) string {
	return "key:" + APIKeyFingerprint(userID, provider, key)
}

// CodexIdentityInfo is what a Codex token pair says about its account.
type CodexIdentityInfo struct {
	// Email is the sign-in email: id_token top-level "email", else the
	// "https://api.openai.com/profile" claim's email. "" when neither.
	Email string
	// AccountID is chatgpt_account_id: the workspace the credential acts in,
	// also the ChatGPT-Account-Id request header.
	AccountID string
	// UserID is chatgpt_account_user_id, else chatgpt_user_id, else user_id.
	UserID string
	// PlanRaw is chatgpt_plan_type as sent; Plan is NormalizePlan of it.
	PlanRaw string
	Plan    string
}

// IdentityKey is "acct:<chatgpt_account_id>/user:<user id>" (DESIGN §1.2), ""
// when either part is unknown.
func (i CodexIdentityInfo) IdentityKey() string {
	if i.AccountID == "" || i.UserID == "" {
		return ""
	}
	return "acct:" + i.AccountID + "/user:" + i.UserID
}

type codexClaims struct {
	Email   string `json:"email"`
	Profile *struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
	Auth *struct {
		PlanType         string `json:"chatgpt_plan_type"`
		ChatGPTUserID    string `json:"chatgpt_user_id"`
		UserID           string `json:"user_id"`
		AccountID        string `json:"chatgpt_account_id"`
		AccountUserID    string `json:"chatgpt_account_user_id"`
		AccountIsFedramp bool   `json:"chatgpt_account_is_fedramp"`
	} `json:"https://api.openai.com/auth"`
}

// CodexIdentity reads the account identity out of a Codex id_token, filling any
// gap from the access token (which carries the same auth claims). Either may be
// empty. Claims are read WITHOUT verifying the signature: both tokens came
// straight from OpenAI's token endpoint over TLS and the result only labels and
// dedupes the user's own account; it authorizes nothing. An error is returned
// only when a supplied token is not a decodable JWT; a token with no claims we
// know yields empty fields.
func CodexIdentity(idToken, accessToken string) (CodexIdentityInfo, error) {
	var info CodexIdentityInfo
	for _, tok := range []string{idToken, accessToken} {
		if strings.TrimSpace(tok) == "" {
			continue
		}
		c, err := decodeCodexClaims(tok)
		if err != nil {
			return CodexIdentityInfo{}, err
		}
		if info.Email == "" {
			info.Email = strings.TrimSpace(c.Email)
			if info.Email == "" && c.Profile != nil {
				info.Email = strings.TrimSpace(c.Profile.Email)
			}
		}
		if a := c.Auth; a != nil {
			if info.AccountID == "" {
				info.AccountID = strings.TrimSpace(a.AccountID)
			}
			if info.UserID == "" {
				for _, v := range []string{a.AccountUserID, a.ChatGPTUserID, a.UserID} {
					if v = strings.TrimSpace(v); v != "" {
						info.UserID = v
						break
					}
				}
			}
			if info.PlanRaw == "" {
				info.PlanRaw = strings.TrimSpace(a.PlanType)
			}
		}
	}
	info.Plan = NormalizePlan(ProviderCodex, info.PlanRaw, "")
	return info, nil
}

func decodeCodexClaims(token string) (codexClaims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[1] == "" {
		return codexClaims{}, fmt.Errorf("providerusage: codex token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return codexClaims{}, fmt.Errorf("providerusage: codex token payload: %w", err)
	}
	var c codexClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return codexClaims{}, fmt.Errorf("providerusage: codex token claims: %w", err)
	}
	return c, nil
}

var multiplierRE = regexp.MustCompile(`(\d+)x`)

// NormalizePlan maps a provider's plan strings onto the stored vocabulary
// (max_20x, max_5x, max, pro, plus, team, business, enterprise, edu, free, …).
// "" means unknown. Unrecognised non-empty values pass through lowercased so a
// new plan is shown rather than lost.
//
//	claude: a = organization.organization_type ("claude_max"),
//	        b = organization.rate_limit_tier ("default_claude_max_20x");
//	        either may be empty, and the tier alone is enough.
//	codex:  a = chatgpt_plan_type / wham plan_type; b is ignored.
//
// Other providers have no plans and return "".
func NormalizePlan(provider, a, b string) string {
	switch provider {
	case ProviderClaude:
		return normalizeClaudePlan(a, b)
	case ProviderCodex:
		return normalizeCodexPlan(a)
	}
	return ""
}

func claudeBase(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "default_")
	s = strings.TrimPrefix(s, "claude_")
	return s
}

func normalizeClaudePlan(orgType, tier string) string {
	t, r := claudeBase(orgType), claudeBase(tier)
	mult := ""
	for _, s := range []string{r, t} {
		if m := multiplierRE.FindStringSubmatch(s); m != nil {
			mult = m[1] + "x"
			break
		}
	}
	for _, s := range []string{t, r} {
		switch {
		case strings.HasPrefix(s, "max"):
			if mult != "" {
				return "max_" + mult
			}
			return "max"
		case strings.HasPrefix(s, "pro"):
			return "pro"
		case strings.HasPrefix(s, "team"):
			return "team"
		case strings.HasPrefix(s, "enterprise"):
			return "enterprise"
		case strings.HasPrefix(s, "free"):
			return "free"
		}
	}
	if t != "" {
		return t
	}
	return r
}

func normalizeCodexPlan(raw string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	switch p {
	case "":
		return ""
	case "free", "guest", "free_workspace":
		return "free"
	case "go":
		return "go"
	case "plus":
		return "plus"
	case "pro":
		return "pro"
	case "prolite":
		return "pro_lite"
	case "promax":
		return "pro_max"
	case "team":
		return "team"
	case "business", "self_serve_business_prolite", "self_serve_business_usage_based":
		return "business"
	case "education", "edu", "edu_plus", "edu_pro", "k12", "quorum":
		return "edu"
	}
	if strings.HasPrefix(p, "enterprise") || strings.HasPrefix(p, "ent") && len(p) > 3 && p[3] >= '0' && p[3] <= '9' {
		return "enterprise"
	}
	return p
}
