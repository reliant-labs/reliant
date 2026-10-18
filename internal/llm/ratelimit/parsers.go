// Copyright (c) 2025 Reliant Labs
package ratelimit

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Anthropic
// ---------------------------------------------------------------------------

// ParseAnthropic reads both Anthropic header families off one response:
//
//   - Subscription (Claude Pro/Max OAuth) `anthropic-ratelimit-unified-*`:
//     `-status` (allowed | allowed_warning | rejected), `-reset` (the
//     representative window's reset), `-representative-claim` (five_hour |
//     seven_day | ...), `-overage-disabled-reason`, and per window
//     `-5h-` / `-7d-` + `utilization` / `reset` / `status`. UNDOCUMENTED:
//     `-status`, `-reset`, `-representative-claim` and `-overage-disabled-reason`
//     are confirmed against live 429s (stream_retry.go, 2026-09); the per-window
//     `-5h-*` / `-7d-*` names are best-known from Claude Code's behavior and
//     community captures, so they are parsed defensively. Utilization is read as
//     a 0..1 fraction; a value above 1 is taken to be a percentage.
//   - API key `anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-
//     {limit,remaining,reset}` (reset is RFC 3339), documented at
//     https://docs.anthropic.com/en/api/rate-limits.
func ParseAnthropic(h http.Header, now time.Time) (Snapshot, bool) {
	s := Snapshot{Status: StatusUnknown, ObservedAt: now.UTC()}
	found := false

	const unified = "anthropic-ratelimit-unified-"
	if v := h.Get(unified + "status"); v != "" {
		s.Status = anthropicStatus(v)
		found = true
	}
	s.OverageDisabledReason = strings.TrimSpace(h.Get(unified + "overage-disabled-reason"))
	s.RepresentativeWindow = normalizeAnthropicWindow(h.Get(unified + "representative-claim"))
	for _, name := range []string{"5h", "7d"} {
		w := Window{Name: name, Length: windowLength(name)}
		got := false
		if u, ok := parseFloat(h.Get(unified + name + "-utilization")); ok && u >= 0 {
			if u > 1 {
				u /= 100
			}
			w.UsedFraction = &u
			got = true
		}
		if t, ok := ParseResetTime(h.Get(unified + name + "-reset")); ok {
			w.ResetsAt = t
			got = true
		}
		if got {
			s.Windows = append(s.Windows, w)
			found = true
		}
	}
	if reset, ok := ParseResetTime(h.Get(unified + "reset")); ok {
		found = true
		name := s.RepresentativeWindow
		if name == "" {
			name = "unified"
		}
		attached := false
		for i := range s.Windows {
			if s.Windows[i].Name == name {
				if s.Windows[i].ResetsAt.IsZero() {
					s.Windows[i].ResetsAt = reset
				}
				attached = true
			}
		}
		if !attached {
			s.Windows = append(s.Windows, Window{Name: name, ResetsAt: reset, Length: windowLength(name)})
		}
	}

	apiRejected := false
	for _, kind := range []string{"requests", "tokens", "input-tokens", "output-tokens"} {
		p := "anthropic-ratelimit-" + kind + "-"
		w := Window{Name: kind}
		got := false
		if n, ok := parseInt(h.Get(p + "limit")); ok {
			w.Limit = &n
			got = true
		}
		if n, ok := parseInt(h.Get(p + "remaining")); ok {
			w.Remaining = &n
			got = true
			if n <= 0 {
				apiRejected = true
			}
		}
		if t, ok := ParseResetTime(h.Get(p + "reset")); ok {
			w.ResetsAt = t
			got = true
		}
		if got {
			s.Windows = append(s.Windows, w)
			found = true
		}
	}
	if s.Status == StatusUnknown && found {
		switch {
		case apiRejected:
			s.Status = StatusRejected
		case hasCountWindow(s.Windows):
			s.Status = StatusAllowed
		}
	}
	return s, found
}

func hasCountWindow(ws []Window) bool {
	for _, w := range ws {
		if w.Limit != nil || w.Remaining != nil {
			return true
		}
	}
	return false
}

func anthropicStatus(v string) Status {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "allowed":
		return StatusAllowed
	case "allowed_warning":
		return StatusWarning
	case "rejected":
		return StatusRejected
	}
	return StatusUnknown
}

func normalizeAnthropicWindow(claim string) string {
	switch c := strings.TrimSpace(claim); c {
	case "five_hour":
		return "5h"
	case "seven_day":
		return "7d"
	default:
		return c
	}
}

// AnthropicLimitInfo reads the usage-window facts off a refused Anthropic
// response. explicit is true when the subscription headers say the window is
// exhausted (`anthropic-ratelimit-unified-status: rejected`); the caller adds
// body-based detection.
func AnthropicLimitInfo(h http.Header) (info LimitInfo, explicit bool) {
	if h == nil {
		return LimitInfo{}, false
	}
	const unified = "anthropic-ratelimit-unified-"
	info.Window = strings.TrimSpace(h.Get(unified + "representative-claim"))
	info.OverageReason = strings.TrimSpace(h.Get(unified + "overage-disabled-reason"))
	if t, ok := ParseResetTime(h.Get(unified + "reset")); ok {
		info.ResetAt = t
	}
	return info, anthropicStatus(h.Get(unified+"status")) == StatusRejected
}

// AnthropicBodyIsUsageLimit reports whether a refused Anthropic response's body
// names a subscription usage window rather than a per-minute rate limit. The
// 429 body is {"type":"error","error":{"type":"rate_limit_error","message":...}};
// the type alone is shared with transient limits, so the message is what tells
// a window apart ("... usage limit ..."). Best-known wording, header signal first.
func AnthropicBodyIsUsageLimit(errorType, message string) bool {
	if strings.EqualFold(errorType, "usage_limit_error") || strings.EqualFold(errorType, "usage_limit_reached") {
		return true
	}
	m := strings.ToLower(message)
	return strings.Contains(m, "usage limit") || strings.Contains(m, "limit will reset")
}

// ---------------------------------------------------------------------------
// Codex (ChatGPT subscription)
// ---------------------------------------------------------------------------

// ParseCodex reads the Codex rate-limit header families. Verified against
// openai/codex codex-rs/codex-api/src/rate_limits.rs (parse_all_rate_limits):
// per limit family `x-<family>-{primary,secondary}-{used-percent,
// window-minutes,reset-at}` (reset-at is Unix seconds), family "codex" by
// default and others discovered from any `x-<family>-primary-used-percent`
// header; `x-codex-rate-limit-reached-type` names a refused request. Older
// backends sent `*-reset-after-seconds`, still accepted.
func ParseCodex(h http.Header, now time.Time) (Snapshot, bool) {
	s := Snapshot{Status: StatusUnknown, ObservedAt: now.UTC(), LimitID: "codex"}
	families := codexFamilies(h)
	for _, fam := range families {
		for _, slot := range []string{"primary", "secondary"} {
			w, ok := codexHeaderWindow(h, "x-"+fam+"-"+slot+"-", now)
			if !ok {
				continue
			}
			w.Name = codexWindowName(fam, slot)
			s.Windows = append(s.Windows, w)
		}
	}
	reached := strings.TrimSpace(h.Get("x-codex-rate-limit-reached-type"))
	if len(s.Windows) == 0 && reached == "" {
		return Snapshot{}, false
	}
	s.Status = codexStatus(s.Windows, reached != "")
	return s, true
}

func codexFamilies(h http.Header) []string {
	seen := map[string]bool{"codex": true}
	const suffix = "-primary-used-percent"
	for k := range h {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-") && strings.HasSuffix(lk, suffix) && len(lk) > 2+len(suffix) {
			seen[strings.TrimSuffix(strings.TrimPrefix(lk, "x-"), suffix)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { // "codex" first, then alphabetical
		if (out[i] == "codex") != (out[j] == "codex") {
			return out[i] == "codex"
		}
		return out[i] < out[j]
	})
	return out
}

func codexWindowName(family, slot string) string {
	if family == "codex" {
		return slot
	}
	return strings.ReplaceAll(family, "-", "_") + "." + slot
}

func codexHeaderWindow(h http.Header, prefix string, now time.Time) (Window, bool) {
	pct, ok := parseFloat(h.Get(prefix + "used-percent"))
	if !ok {
		return Window{}, false
	}
	var w Window
	frac := pct / 100
	w.UsedFraction = &frac
	if m, ok := parseInt(h.Get(prefix + "window-minutes")); ok && m > 0 {
		w.Length = time.Duration(m) * time.Minute
	}
	if t, ok := ParseResetTime(h.Get(prefix + "reset-at")); ok {
		w.ResetsAt = t
	} else if d, ok := parseResetDuration(h.Get(prefix + "reset-after-seconds")); ok {
		w.ResetsAt = now.Add(d).UTC()
	}
	// codex-rs drops an all-empty window (0%, no length, no reset).
	if pct == 0 && w.Length == 0 && w.ResetsAt.IsZero() {
		return Window{}, false
	}
	return w, true
}

func codexStatus(ws []Window, reached bool) Status {
	if reached {
		return StatusRejected
	}
	st := StatusAllowed
	for _, w := range ws {
		if u, ok := w.Used(); ok && u >= 1 {
			return StatusRejected
		}
	}
	return st
}

// ParseCodexEvent reads the `codex.rate_limits` server-sent event the Codex
// backend streams (codex-rs parse_rate_limit_event):
// {"type":"codex.rate_limits","plan_type":"plus","rate_limits":{"primary":
// {"used_percent":12.5,"window_minutes":300,"reset_at":1704069000},"secondary":
// {...}},"metered_limit_name":"codex"}.
func ParseCodexEvent(payload []byte, now time.Time) (Snapshot, bool) {
	var ev struct {
		Type       string `json:"type"`
		PlanType   string `json:"plan_type"`
		Metered    string `json:"metered_limit_name"`
		LimitName  string `json:"limit_name"`
		RateLimits *struct {
			Primary   *codexEventWindow `json:"primary"`
			Secondary *codexEventWindow `json:"secondary"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(payload, &ev) != nil || ev.Type != "codex.rate_limits" {
		return Snapshot{}, false
	}
	fam := strings.ToLower(strings.TrimSpace(ev.Metered))
	if fam == "" {
		fam = strings.ToLower(strings.TrimSpace(ev.LimitName))
	}
	fam = strings.ReplaceAll(fam, "_", "-")
	if fam == "" {
		fam = "codex"
	}
	s := Snapshot{Status: StatusUnknown, ObservedAt: now.UTC(), LimitID: strings.ReplaceAll(fam, "-", "_"), PlanType: ev.PlanType}
	if ev.RateLimits != nil {
		for slot, w := range map[string]*codexEventWindow{"primary": ev.RateLimits.Primary, "secondary": ev.RateLimits.Secondary} {
			if w != nil {
				win := w.window()
				win.Name = codexWindowName(fam, slot)
				s.Windows = append(s.Windows, win)
			}
		}
	}
	sort.Slice(s.Windows, func(i, j int) bool { return s.Windows[i].Name < s.Windows[j].Name })
	if len(s.Windows) == 0 {
		return Snapshot{}, false
	}
	s.Status = codexStatus(s.Windows, false)
	return s, true
}

type codexEventWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes *int64  `json:"window_minutes"`
	ResetAt       *int64  `json:"reset_at"`
}

func (e *codexEventWindow) window() Window {
	frac := e.UsedPercent / 100
	w := Window{UsedFraction: &frac}
	if e.WindowMinutes != nil && *e.WindowMinutes > 0 {
		w.Length = time.Duration(*e.WindowMinutes) * time.Minute
	}
	if e.ResetAt != nil && *e.ResetAt > 0 {
		w.ResetsAt = time.Unix(*e.ResetAt, 0).UTC()
	}
	return w
}

// CodexUsageLimit recognizes the Codex 429 that means a subscription window is
// exhausted. Verified against codex-rs codex-api/src/api_bridge.rs (map_api_error,
// UsageErrorBody): HTTP 429 whose body is
//
//	{"error":{"type":"usage_limit_reached","plan_type":"plus",
//	          "resets_at":1704069000,"limit_window_minutes":300}}
//
// resets_at is Unix seconds. `resets_in_seconds` appears in older codex builds
// and is accepted as a fallback; when neither is in the body the reset comes from
// the exhausted window in the response's x-codex-* headers. body may be the full
// response body or just the inner error object (the openai SDK unwraps "error").
func CodexUsageLimit(body []byte, h http.Header, now time.Time) (LimitInfo, bool) {
	var raw struct {
		Error *codexErrorBody `json:"error"`
		codexErrorBody
	}
	if json.Unmarshal(body, &raw) != nil {
		return LimitInfo{}, false
	}
	e := raw.Error
	if e == nil {
		e = &raw.codexErrorBody
	}
	if e.Type != "usage_limit_reached" {
		return LimitInfo{}, false
	}
	var info LimitInfo
	switch {
	case e.ResetsAt != nil && *e.ResetsAt > 0:
		info.ResetAt = time.Unix(*e.ResetsAt, 0).UTC()
	case e.ResetsInSeconds != nil && *e.ResetsInSeconds > 0:
		info.ResetAt = now.Add(time.Duration(*e.ResetsInSeconds) * time.Second).UTC()
	}
	if e.LimitWindowMinutes != nil {
		info.Window = codexWindowFromMinutes(*e.LimitWindowMinutes)
	}
	fillFromCodexHeaders(&info, h, now)
	return info, true
}

// CodexHeaderLimit reads a refusal off the x-codex-* headers alone: the 429 is
// a usage window when `x-codex-rate-limit-reached-type` is set or a window's
// used-percent has reached 100. Used when the body does not carry the typed
// usage_limit_reached error.
func CodexHeaderLimit(h http.Header, now time.Time) (LimitInfo, bool) {
	snap, ok := ParseCodex(h, now)
	if !ok || snap.Status != StatusRejected {
		return LimitInfo{}, false
	}
	var info LimitInfo
	fillFromCodexHeaders(&info, h, now)
	return info, true
}

// fillFromCodexHeaders fills the reset and window still missing from info with
// those of the exhausted (>=100% used) window that resets last.
func fillFromCodexHeaders(info *LimitInfo, h http.Header, now time.Time) {
	snap, ok := ParseCodex(h, now)
	if !ok {
		return
	}
	var exhausted *Window
	for i := range snap.Windows {
		if u, ok := snap.Windows[i].Used(); ok && u >= 1 && !snap.Windows[i].ResetsAt.IsZero() {
			if exhausted == nil || snap.Windows[i].ResetsAt.After(exhausted.ResetsAt) {
				exhausted = &snap.Windows[i]
			}
		}
	}
	if exhausted == nil {
		return
	}
	if info.ResetAt.IsZero() {
		info.ResetAt = exhausted.ResetsAt
	}
	if info.Window == "" && exhausted.Length > 0 {
		info.Window = codexWindowFromMinutes(int64(exhausted.Length / time.Minute))
	}
}

type codexErrorBody struct {
	Type               string `json:"type"`
	Code               string `json:"code"`
	PlanType           string `json:"plan_type"`
	ResetsAt           *int64 `json:"resets_at"`
	ResetsInSeconds    *int64 `json:"resets_in_seconds"`
	LimitWindowMinutes *int64 `json:"limit_window_minutes"`
}

// CodexQuotaExhausted reports a 429 body codex-rs treats as non-retryable
// quota exhaustion rather than a window (api_bridge.rs: usage_not_included,
// insufficient_quota, and the spend-limit codes).
func CodexQuotaExhausted(body []byte) bool {
	var raw struct {
		Error *codexErrorBody `json:"error"`
		codexErrorBody
	}
	if json.Unmarshal(body, &raw) != nil {
		return false
	}
	e := raw.Error
	if e == nil {
		e = &raw.codexErrorBody
	}
	switch e.Type {
	case "usage_not_included", "insufficient_quota":
		return true
	}
	switch e.Code {
	case "insufficient_quota", "credit_balance_exhausted", "organization_spend_limit_exceeded",
		"project_spend_limit_exceeded", "organization_usage_limit_exceeded":
		return true
	}
	return false
}

// codexWindowFromMinutes names a Codex window in the vocabulary
// UsageLimitError.Error renders: the 5-hour and weekly windows by their
// Anthropic-style names, anything else by its length.
func codexWindowFromMinutes(m int64) string {
	switch {
	case m == 300:
		return "five_hour"
	case m == 10080:
		return "seven_day"
	case m > 0 && m%1440 == 0:
		return fmt.Sprintf("%d_day", m/1440)
	case m > 0 && m%60 == 0:
		return fmt.Sprintf("%d_hour", m/60)
	case m > 0:
		return fmt.Sprintf("%d_minute", m)
	}
	return ""
}

// ---------------------------------------------------------------------------
// OpenAI API
// ---------------------------------------------------------------------------

// ParseOpenAI reads `x-ratelimit-{limit,remaining,reset}-{requests,tokens}`
// (https://platform.openai.com/docs/guides/rate-limits). Reset is a relative
// duration like "6m0s" or "20ms".
func ParseOpenAI(h http.Header, now time.Time) (Snapshot, bool) {
	s := Snapshot{Status: StatusUnknown, ObservedAt: now.UTC()}
	exhausted := false
	for _, kind := range []string{"requests", "tokens"} {
		w := Window{Name: kind}
		got := false
		if n, ok := parseInt(h.Get("x-ratelimit-limit-" + kind)); ok {
			w.Limit = &n
			got = true
		}
		if n, ok := parseInt(h.Get("x-ratelimit-remaining-" + kind)); ok {
			w.Remaining = &n
			got = true
			if n <= 0 {
				exhausted = true
			}
		}
		if d, ok := parseResetDuration(h.Get("x-ratelimit-reset-" + kind)); ok {
			w.ResetsAt = now.Add(d).UTC()
			got = true
		}
		if got {
			s.Windows = append(s.Windows, w)
		}
	}
	if len(s.Windows) == 0 {
		return Snapshot{}, false
	}
	s.Status = StatusAllowed
	if exhausted {
		s.Status = StatusRejected
	}
	return s, true
}
