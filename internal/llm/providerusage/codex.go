// Copyright (c) 2025 Reliant Labs
package providerusage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reliant-labs/reliant/internal/llm/ratelimit"
)

// CodexUsageRaw performs GET /backend-api/wham/usage with the credential's
// bearer token and the ChatGPT-Account-Id header (omitted when accountID is
// empty), and returns the body.
func CodexUsageRaw(ctx context.Context, d Doer, accessToken, accountID string) ([]byte, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("providerusage: codex usage: empty access token")
	}
	h := map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(accessToken),
		"Accept":        "application/json",
		"User-Agent":    codexUserAgent,
	}
	if id := strings.TrimSpace(accountID); id != "" {
		h["ChatGPT-Account-Id"] = id
	}
	return get(ctx, d, ProviderCodex, "usage", codexUsageURL, h)
}

// CodexUsage fetches and parses the subscription's usage windows.
func CodexUsage(ctx context.Context, d Doer, accessToken, accountID string) (ratelimit.Snapshot, error) {
	body, err := CodexUsageRaw(ctx, d, accessToken, accountID)
	if err != nil {
		return ratelimit.Snapshot{}, err
	}
	return ParseCodexUsage(body, time.Now())
}

type codexWindowJSON struct {
	UsedPercent        flexFloat `json:"used_percent"`
	LimitWindowSeconds flexFloat `json:"limit_window_seconds"`
	ResetAfterSeconds  flexFloat `json:"reset_after_seconds"`
	ResetAt            flexTime  `json:"reset_at"`
}

type codexRateLimitJSON struct {
	Allowed       *bool            `json:"allowed"`
	LimitReached  bool             `json:"limit_reached"`
	PrimaryWindow *codexWindowJSON `json:"primary_window"`
	Secondary     *codexWindowJSON `json:"secondary_window"`
}

type codexPayloadJSON struct {
	PlanType         string              `json:"plan_type"`
	RateLimit        *codexRateLimitJSON `json:"rate_limit"`
	ReachedType      json.RawMessage     `json:"rate_limit_reached_type"`
	AdditionalLimits []struct {
		LimitName      string              `json:"limit_name"`
		MeteredFeature string              `json:"metered_feature"`
		RateLimit      *codexRateLimitJSON `json:"rate_limit"`
	} `json:"additional_rate_limits"`
	Credits *struct {
		HasCredits          bool `json:"has_credits"`
		Unlimited           bool `json:"unlimited"`
		OverageLimitReached bool `json:"overage_limit_reached"`
	} `json:"credits"`
	SpendControl *struct {
		Reached bool `json:"reached"`
	} `json:"spend_control"`
}

// creditsPay reports whether credits pay for requests past a spent window: the
// account has credits (or unlimited ones) and neither the overage limit nor a
// workspace spend control has been reached.
func (p codexPayloadJSON) creditsPay() bool {
	c := p.Credits
	if c == nil || (!c.HasCredits && !c.Unlimited) || c.OverageLimitReached {
		return false
	}
	return p.SpendControl == nil || !p.SpendControl.Reached
}

func (w *codexWindowJSON) window(name string, now time.Time) (ratelimit.Window, bool) {
	if w == nil {
		return ratelimit.Window{}, false
	}
	pct, ok := w.UsedPercent.get()
	if !ok {
		return ratelimit.Window{}, false
	}
	out := ratelimit.Window{Name: name, UsedFraction: ptr(fraction(pct))}
	if secs, ok := w.LimitWindowSeconds.get(); ok && secs > 0 {
		out.Length = time.Duration(secs * float64(time.Second))
	}
	if t := w.ResetAt.get(); !t.IsZero() {
		out.ResetsAt = t
	} else if secs, ok := w.ResetAfterSeconds.get(); ok && secs >= 0 {
		out.ResetsAt = now.Add(time.Duration(secs * float64(time.Second))).UTC()
	}
	return out, true
}

// ParseCodexUsage turns a wham/usage body into a Snapshot.
//
// Shape (codex-rs backend-client RateLimitStatusPayload; the generated model is
// https://raw.githubusercontent.com/openai/codex/main/codex-rs/codex-backend-openapi-models/src/models/rate_limit_status_payload.rs):
//
//	{"plan_type":"plus",
//	 "rate_limit":{"allowed":true,"limit_reached":false,
//	   "primary_window":{"used_percent":12,"limit_window_seconds":18000,"reset_after_seconds":900,"reset_at":1760000000},
//	   "secondary_window":{…}},
//	 "additional_rate_limits":[{"limit_name":"GPT-5.3-Codex-Spark","metered_feature":"codex_bengalfox","rate_limit":{…}}],
//	 "credits":{…}, "rate_limit_reached_type":{"type":"rate_limit_reached"}}
//
// The payload is accepted at the top level or wrapped under "rate_limits"
// (RateLimitStatusWithResetCredits). Windows keep the header parser's slot names
// ("primary", "secondary") with the true length in Window.Length, because the
// backend can move a sole weekly window into the primary slot; additional
// model-scoped limits are "<metered_feature>.primary" / ".secondary". The
// Snapshot status reflects the ordinary limit only: a model-specific cap does
// not stop the subscription. A spent ordinary limit is StatusOverage, not
// StatusRejected, while "credits" (and "spend_control") say credits pay for
// the requests past it and no additional family is spent too.
func ParseCodexUsage(body []byte, now time.Time) (ratelimit.Snapshot, error) {
	root, ok := jsonObject(body)
	if !ok {
		return ratelimit.Snapshot{}, unexpected(ProviderCodex, "usage", "body is not a JSON object")
	}
	payloadRaw := json.RawMessage(body)
	for _, k := range []string{"rate_limits", "rate_limit_status"} {
		if inner, has := root[k]; has {
			if m, ok := jsonObject(inner); ok {
				if _, hasRL := m["rate_limit"]; hasRL {
					payloadRaw = inner
					break
				}
				if _, hasPlan := m["plan_type"]; hasPlan {
					payloadRaw = inner
					break
				}
			}
		}
	}
	var p codexPayloadJSON
	if err := json.Unmarshal(payloadRaw, &p); err != nil {
		return ratelimit.Snapshot{}, unexpected(ProviderCodex, "usage", "payload does not decode")
	}
	if p.RateLimit == nil && strings.TrimSpace(p.PlanType) == "" && len(p.AdditionalLimits) == 0 {
		return ratelimit.Snapshot{}, unexpected(ProviderCodex, "usage", "no rate limit or plan in body")
	}

	s := ratelimit.Snapshot{
		Provider:   ProviderCodex,
		Status:     ratelimit.StatusUnknown,
		ObservedAt: now.UTC(),
		PlanType:   strings.TrimSpace(p.PlanType),
		LimitID:    "codex",
	}
	var base []ratelimit.Window
	if rl := p.RateLimit; rl != nil {
		if w, ok := rl.PrimaryWindow.window("primary", now); ok {
			base = append(base, w)
		}
		if w, ok := rl.Secondary.window("secondary", now); ok {
			base = append(base, w)
		}
	}
	s.Windows = append(s.Windows, base...)

	// familySpent: an additional limit family is exhausted. Alone it does not
	// stop the subscription, but credits are not known to pay for it, so it
	// keeps a spent plan window from reading as overage.
	familySpent := false
	for _, a := range p.AdditionalLimits {
		if a.RateLimit == nil {
			continue
		}
		if rl := a.RateLimit; rl.LimitReached || (rl.Allowed != nil && !*rl.Allowed) {
			familySpent = true
		}
		fam := slug(a.MeteredFeature)
		if fam == "" {
			fam = slug(a.LimitName)
		}
		if fam == "" {
			continue
		}
		var famWindows []ratelimit.Window
		if w, ok := a.RateLimit.PrimaryWindow.window(fam+".primary", now); ok {
			famWindows = append(famWindows, w)
		}
		if w, ok := a.RateLimit.Secondary.window(fam+".secondary", now); ok {
			famWindows = append(famWindows, w)
		}
		familySpent = familySpent || anyExhausted(famWindows)
		s.Windows = append(s.Windows, famWindows...)
	}

	switch rl := p.RateLimit; {
	case hasReachedType(p.ReachedType):
		s.Status = ratelimit.StatusRejected
	case rl != nil && (rl.LimitReached || (rl.Allowed != nil && !*rl.Allowed)), anyExhausted(base):
		// allowed / limit_reached judge the plan's windows alone: a workspace
		// with credits reads allowed:false, limit_reached:true and is still
		// served (verified live 2026-11-08, a Team workspace past its weekly
		// window, while the Codex CLI kept answering). Overage claims every
		// request is served, so a spent additional family, which credits are
		// not known to cover, leaves it rejected (as the header parser does).
		s.Status = ratelimit.StatusRejected
		if p.creditsPay() && !familySpent {
			s.Status = ratelimit.StatusOverage
		}
	case len(base) > 0 || (rl != nil && rl.Allowed != nil):
		s.Status = ratelimit.StatusAllowed
	}
	return s, nil
}

// hasReachedType reports whether rate_limit_reached_type names a real kind
// (not null / absent / "unknown").
func hasReachedType(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var obj struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Type != "" {
		return obj.Type != "unknown"
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str != "" && str != "unknown"
	}
	return false
}
