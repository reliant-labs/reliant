// Copyright (c) 2025 Reliant Labs
package providerusage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

func claudeHeaders(accessToken string) map[string]string {
	return map[string]string{
		"Authorization":  "Bearer " + strings.TrimSpace(accessToken),
		"Accept":         "application/json",
		"anthropic-beta": claudeBeta,
		"User-Agent":     claudeUserAgent,
	}
}

// ClaudeUsageRaw performs GET /api/oauth/usage with the credential's bearer
// token and returns the body. The token needs the user:profile scope.
func ClaudeUsageRaw(ctx context.Context, d Doer, accessToken string) ([]byte, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("providerusage: claude usage: empty access token")
	}
	return get(ctx, d, ProviderClaude, "usage", claudeUsageURL, claudeHeaders(accessToken))
}

// ClaudeUsage fetches and parses the subscription's usage windows.
func ClaudeUsage(ctx context.Context, d Doer, accessToken string) (ratelimit.Snapshot, error) {
	body, err := ClaudeUsageRaw(ctx, d, accessToken)
	if err != nil {
		return ratelimit.Snapshot{}, err
	}
	return ParseClaudeUsage(body, time.Now())
}

type claudeWindowJSON struct {
	Utilization flexFloat `json:"utilization"`
	ResetsAt    flexTime  `json:"resets_at"`
}

type claudeLimitJSON struct {
	Kind    string    `json:"kind"`
	Percent flexFloat `json:"percent"`
	// Some payloads may spell it utilization; accepted as a fallback.
	Utilization flexFloat `json:"utilization"`
	ResetsAt    flexTime  `json:"resets_at"`
	Scope       struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// claudeKnownKeys are the top-level keys that mark a body as a usage payload.
var claudeKnownKeys = []string{"five_hour", "seven_day", "limits", "extra_usage", "seven_day_opus", "seven_day_sonnet"}

// ParseClaudeUsage turns a /api/oauth/usage body into a Snapshot.
//
// Shape (Claude Code /usage; https://github.com/anthropics/claude-code/issues/91623
// and robinebers/openusage ClaudeUsageMapper.swift):
//
//	{"five_hour":{"utilization":42.0,"resets_at":"2026-…Z"},
//	 "seven_day":{…}, "seven_day_opus":{…}|null, "seven_day_sonnet":{…}|null,
//	 "limits":[{"kind":"weekly_scoped","scope":{"model":{"display_name":"Fable"}},
//	            "percent":99,"resets_at":"…"}],
//	 "extra_usage":{…}}
//
// utilization and percent are 0..100 percentages. Windows are named like the
// header parser's: "5h", "7d", and "7d.<model>" for a model-scoped weekly row
// (from the legacy seven_day_<model> key or a limits[] weekly_scoped row; the
// limits[] row wins when both name the same model). Unknown limits[] kinds and
// null windows are skipped.
func ParseClaudeUsage(body []byte, now time.Time) (ratelimit.Snapshot, error) {
	root, ok := jsonObject(body)
	if !ok {
		return ratelimit.Snapshot{}, unexpected(ProviderClaude, "usage", "body is not a JSON object")
	}
	known := false
	for _, k := range claudeKnownKeys {
		if _, has := root[k]; has {
			known = true
			break
		}
	}
	if !known {
		return ratelimit.Snapshot{}, unexpected(ProviderClaude, "usage", "no usage windows in body")
	}

	var windows []ratelimit.Window
	byName := map[string]int{}
	add := func(w ratelimit.Window, override bool) {
		if i, dup := byName[w.Name]; dup {
			if override {
				windows[i] = w
			}
			return
		}
		byName[w.Name] = len(windows)
		windows = append(windows, w)
	}

	legacy := func(key, name string, length time.Duration) {
		raw, has := root[key]
		if !has {
			return
		}
		var w claudeWindowJSON
		if json.Unmarshal(raw, &w) != nil {
			return
		}
		pct, ok := w.Utilization.get()
		if !ok {
			return
		}
		add(ratelimit.Window{Name: name, UsedFraction: ptr(fraction(pct)), ResetsAt: w.ResetsAt.get(), Length: length}, false)
	}
	legacy("five_hour", "5h", 5*time.Hour)
	legacy("seven_day", "7d", 7*24*time.Hour)

	// Model-scoped weekly rows under their legacy top-level keys, in a stable order.
	var scopedKeys []string
	for k := range root {
		if strings.HasPrefix(k, "seven_day_") && len(k) > len("seven_day_") {
			scopedKeys = append(scopedKeys, k)
		}
	}
	sort.Strings(scopedKeys)
	for _, k := range scopedKeys {
		if m := slug(strings.TrimPrefix(k, "seven_day_")); m != "" {
			legacy(k, "7d."+m, 7*24*time.Hour)
		}
	}

	if raw, has := root["limits"]; has {
		var rows []json.RawMessage
		if json.Unmarshal(raw, &rows) == nil {
			for _, r := range rows {
				var l claudeLimitJSON
				if json.Unmarshal(r, &l) != nil || l.Kind != "weekly_scoped" {
					continue
				}
				m := slug(l.Scope.Model.DisplayName)
				if m == "" {
					continue
				}
				pct, ok := l.Percent.get()
				if !ok {
					if pct, ok = l.Utilization.get(); !ok {
						continue
					}
				}
				add(ratelimit.Window{Name: "7d." + m, UsedFraction: ptr(fraction(pct)), ResetsAt: l.ResetsAt.get(), Length: 7 * 24 * time.Hour}, true)
			}
		}
	}

	s := ratelimit.Snapshot{Provider: ProviderClaude, Status: ratelimit.StatusUnknown, ObservedAt: now.UTC(), Windows: windows}
	if len(windows) > 0 {
		s.Status = ratelimit.StatusAllowed
		if anyExhausted(windows) {
			s.Status = ratelimit.StatusRejected
		}
	}
	return s, nil
}

// ClaudeAccountProfile is the account behind a Claude OAuth credential, with its
// CURRENT plan (the plan stored at sign-in goes stale on an upgrade).
type ClaudeAccountProfile struct {
	AccountUUID      string
	Email            string
	OrganizationUUID string
	OrganizationName string
	// OrganizationType is the raw organization.organization_type ("claude_max").
	OrganizationType string
	// RateLimitTier is the raw organization.rate_limit_tier ("default_claude_max_20x").
	RateLimitTier      string
	SubscriptionStatus string
	// Plan is the normalized plan (see NormalizePlan); "" when unknown.
	Plan string
}

// ClaudeProfileRaw performs GET /api/oauth/profile and returns the body.
func ClaudeProfileRaw(ctx context.Context, d Doer, accessToken string) ([]byte, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("providerusage: claude profile: empty access token")
	}
	return get(ctx, d, ProviderClaude, "profile", claudeProfileURL, claudeHeaders(accessToken))
}

// ClaudeProfile fetches and parses the profile.
func ClaudeProfile(ctx context.Context, d Doer, accessToken string) (ClaudeAccountProfile, error) {
	body, err := ClaudeProfileRaw(ctx, d, accessToken)
	if err != nil {
		return ClaudeAccountProfile{}, err
	}
	return ParseClaudeProfile(body)
}

// ParseClaudeProfile reads a /api/oauth/profile body:
//
//	{"account":{"uuid","email"|"email_address","has_claude_max",…},
//	 "organization":{"uuid","name","organization_type":"claude_max",
//	                 "rate_limit_tier":"default_claude_max_20x","subscription_status":"active"}}
//
// (robinebers/openusage ClaudeUsageClient.swift ClaudeAccountProfile; issue
// robinebers/openusage#394.) A body with neither account nor organization is an
// error.
func ParseClaudeProfile(body []byte) (ClaudeAccountProfile, error) {
	var raw struct {
		Account *struct {
			UUID         string `json:"uuid"`
			Email        string `json:"email"`
			EmailAddress string `json:"email_address"`
			HasMax       bool   `json:"has_claude_max"`
			HasPro       bool   `json:"has_claude_pro"`
		} `json:"account"`
		Organization *struct {
			UUID               string `json:"uuid"`
			Name               string `json:"name"`
			OrganizationType   string `json:"organization_type"`
			RateLimitTier      string `json:"rate_limit_tier"`
			SubscriptionStatus string `json:"subscription_status"`
		} `json:"organization"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return ClaudeAccountProfile{}, unexpected(ProviderClaude, "profile", "body is not a JSON object")
	}
	if raw.Account == nil && raw.Organization == nil {
		return ClaudeAccountProfile{}, unexpected(ProviderClaude, "profile", "no account or organization")
	}
	var p ClaudeAccountProfile
	if a := raw.Account; a != nil {
		p.AccountUUID = strings.TrimSpace(a.UUID)
		p.Email = strings.TrimSpace(a.Email)
		if p.Email == "" {
			p.Email = strings.TrimSpace(a.EmailAddress)
		}
	}
	if o := raw.Organization; o != nil {
		p.OrganizationUUID = strings.TrimSpace(o.UUID)
		p.OrganizationName = strings.TrimSpace(o.Name)
		p.OrganizationType = strings.TrimSpace(o.OrganizationType)
		p.RateLimitTier = strings.TrimSpace(o.RateLimitTier)
		p.SubscriptionStatus = strings.TrimSpace(o.SubscriptionStatus)
	}
	p.Plan = NormalizePlan(ProviderClaude, p.OrganizationType, p.RateLimitTier)
	if p.Plan == "" && raw.Account != nil {
		switch {
		case raw.Account.HasMax:
			p.Plan = "max"
		case raw.Account.HasPro:
			p.Plan = "pro"
		}
	}
	return p, nil
}
